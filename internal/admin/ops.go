package admin

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Operations, read-only: alerts, the host, the apps, backups (through
// Grafana, with the admin's own token: Grafana's JWT auth) and
// certificates (a TLS handshake, as any visitor). Only fixed queries:
// nothing here takes a query from the model, and nothing reads logs.

// Grafana: the instance's monitoring, called as the admin.
type Grafana struct {
	Base  string                                    // https://monitor.<apps domain>
	Token func(ctx context.Context) (string, error) // the admin's token for Grafana
	HTTP  *http.Client
}

// The only paths the gateway calls at Grafana (all GET): the metrics
// datasource's instant query and Alertmanager's alert list.
const (
	pathQuery  = "/api/datasources/proxy/uid/victoriametrics/api/v1/query"
	pathAlerts = "/api/alertmanager/alertmanager/api/v2/alerts"
)

func (g *Grafana) get(ctx context.Context, path string, q url.Values, out any) error {
	if path != pathQuery && path != pathAlerts {
		return fmt.Errorf("the gateway doesn't call %s", path)
	}
	tok, err := g.Token(ctx)
	if err != nil {
		return err
	}
	u := strings.TrimRight(g.Base, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	req.Header.Set("X-JWT-Assertion", tok)
	req.Header.Set("Accept", "application/json")
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("monitoring didn't answer: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return fmt.Errorf("monitoring refused your login (%d): open it once in the browser, or ask whoever runs the instance", resp.StatusCode)
	case resp.StatusCode != 200:
		return fmt.Errorf("monitoring answered %d", resp.StatusCode)
	}
	return json.Unmarshal(body, out)
}

type sample struct {
	Labels map[string]string
	Value  float64
	At     float64
}

// query: one fixed PromQL expression, now.
func (g *Grafana) query(ctx context.Context, expr string) ([]sample, error) {
	var r struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]any            `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := g.get(ctx, pathQuery, url.Values{"query": {expr}}, &r); err != nil {
		return nil, err
	}
	if r.Status != "success" {
		return nil, fmt.Errorf("metrics query failed: %s", r.Error)
	}
	var out []sample
	for _, x := range r.Data.Result {
		at, _ := x.Value[0].(float64)
		s, _ := x.Value[1].(string)
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			continue
		}
		out = append(out, sample{x.Metric, v, at})
	}
	return out, nil
}

// one: the value of an expression with one result; nil: no data.
func (g *Grafana) one(ctx context.Context, expr string) (*float64, error) {
	s, err := g.query(ctx, expr)
	if err != nil || len(s) == 0 {
		return nil, err
	}
	v := s[0].Value
	return &v, nil
}

func round(v float64, places int) float64 {
	p, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'f', places, 64), 64)
	return p
}

func pct(v *float64) *float64 {
	if v == nil {
		return nil
	}
	x := round(*v*100, 1)
	return &x
}

func gib(v *float64) *float64 {
	if v == nil {
		return nil
	}
	x := round(*v/(1<<30), 1)
	return &x
}

func when(unix float64) string {
	if unix <= 0 {
		return ""
	}
	return time.Unix(int64(unix), 0).UTC().Format(time.RFC3339)
}

// ---- ops_alerts

type Alert struct {
	Name     string            `json:"name"`
	Severity string            `json:"severity,omitempty"`
	State    string            `json:"state" jsonschema:"active, or suppressed (silenced or inhibited)"`
	Since    string            `json:"since"`
	Summary  string            `json:"summary,omitempty"`
	Labels   map[string]string `json:"labels,omitempty" jsonschema:"what it's about (container, service, …)"`
}

type AlertsOut struct {
	Firing     []Alert `json:"firing"`
	Suppressed int     `json:"suppressed" jsonschema:"silenced or inhibited alerts, listed after the firing ones"`
	Note       string  `json:"note"`
}

func (t *Tools) alerts(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, AlertsOut, error) {
	var r []struct {
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
		StartsAt    string            `json:"startsAt"`
		Status      struct {
			State string `json:"state"`
		} `json:"status"`
	}
	if err := t.Grafana.get(ctx, pathAlerts, url.Values{"active": {"true"}, "silenced": {"true"}, "inhibited": {"true"}}, &r); err != nil {
		return nil, AlertsOut{}, err
	}
	out := AlertsOut{Firing: []Alert{}, Note: "Rule names and summaries are the instance's alert rules; label values come from the servers."}
	var sup []Alert
	for _, a := range r {
		l := map[string]string{}
		for k, v := range a.Labels {
			if k != "alertname" && k != "severity" && k != "tenant" {
				l[k] = short(v)
			}
		}
		x := Alert{Name: short(a.Labels["alertname"]), Severity: a.Labels["severity"], State: "active", Since: a.StartsAt,
			Summary: short(a.Annotations["summary"]), Labels: l}
		if len(l) == 0 {
			x.Labels = nil
		}
		if a.Status.State == "suppressed" {
			x.State = "suppressed"
			sup = append(sup, x)
			continue
		}
		out.Firing = append(out.Firing, x)
	}
	rank := map[string]int{"critical": 0, "warning": 1, "info": 2}
	sort.SliceStable(out.Firing, func(i, j int) bool {
		ri, ok := rank[out.Firing[i].Severity]
		if !ok {
			ri = 3
		}
		rj, ok := rank[out.Firing[j].Severity]
		if !ok {
			rj = 3
		}
		return ri < rj
	})
	out.Suppressed = len(sup)
	out.Firing = append(out.Firing, sup...)
	if len(out.Firing) == 0 {
		out.Note = "No alerts firing. " + out.Note
	}
	return nil, out, nil
}

// ---- ops_host

const (
	rootFS   = `job="node",mountpoint="/",fstype!~"tmpfs|overlay"`
	diskUsed = `1 - node_filesystem_avail_bytes{` + rootFS + `} / node_filesystem_size_bytes{` + rootFS + `}`
	memAvail = `node_memory_MemAvailable_bytes{job="node"} / node_memory_MemTotal_bytes{job="node"}`
	cpuBusy  = `1 - avg(rate(node_cpu_seconds_total{job="node",mode="idle"}[5m]))`
)

type HostOut struct {
	DiskUsedPct      *float64 `json:"disk_used_pct" jsonschema:"disk / used, percent"`
	DiskFreeGiB      *float64 `json:"disk_free_gib"`
	DiskSizeGiB      *float64 `json:"disk_size_gib"`
	DiskUsedMax24h   *float64 `json:"disk_used_max_24h_pct"`
	DiskFreeIn24hGiB *float64 `json:"disk_free_in_24h_gib" jsonschema:"forecast from the last 6 hours; below 0: full within a day"`
	MemAvailPct      *float64 `json:"memory_available_pct"`
	MemAvailMin24h   *float64 `json:"memory_available_min_24h_pct"`
	MemTotalGiB      *float64 `json:"memory_total_gib"`
	CPUBusyPct       *float64 `json:"cpu_busy_pct" jsonschema:"last 5 minutes"`
	CPUBusyMax24h    *float64 `json:"cpu_busy_max_24h_pct"`
	Cores            *float64 `json:"cores"`
	Load1            *float64 `json:"load_1m"`
	OOMKills24h      *float64 `json:"oom_kills_24h"`
	UpSince          string   `json:"up_since,omitempty"`
	Note             string   `json:"note,omitempty"`
}

func (t *Tools) host(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, HostOut, error) {
	g := t.Grafana
	q := map[string]string{
		"used": diskUsed, "free": `node_filesystem_avail_bytes{` + rootFS + `}`, "size": `node_filesystem_size_bytes{` + rootFS + `}`,
		"usedmax": `max_over_time((` + diskUsed + `)[24h:5m])`,
		"in24h":   `predict_linear(node_filesystem_avail_bytes{` + rootFS + `}[6h], 86400)`,
		"mem":     memAvail, "memmin": `min_over_time((` + memAvail + `)[24h:5m])`, "memtotal": `node_memory_MemTotal_bytes{job="node"}`,
		"cpu": cpuBusy, "cpumax": `max_over_time((` + cpuBusy + `)[24h:5m])`,
		"cores": `count(node_cpu_seconds_total{job="node",mode="idle"})`, "load": `node_load1{job="node"}`,
		"oom": `increase(node_vmstat_oom_kill{job="node"}[24h])`, "boot": `node_boot_time_seconds{job="node"}`,
	}
	v, err := t.many(ctx, g, q)
	if err != nil {
		return nil, HostOut{}, err
	}
	out := HostOut{DiskUsedPct: pct(v["used"]), DiskFreeGiB: gib(v["free"]), DiskSizeGiB: gib(v["size"]), DiskUsedMax24h: pct(v["usedmax"]),
		DiskFreeIn24hGiB: gib(v["in24h"]), MemAvailPct: pct(v["mem"]), MemAvailMin24h: pct(v["memmin"]), MemTotalGiB: gib(v["memtotal"]),
		CPUBusyPct: pct(v["cpu"]), CPUBusyMax24h: pct(v["cpumax"]), Cores: v["cores"], OOMKills24h: v["oom"]}
	if l := v["load"]; l != nil {
		x := round(*l, 2)
		out.Load1 = &x
	}
	if b := v["boot"]; b != nil {
		out.UpSince = when(*b)
	}
	if out.DiskUsedPct == nil && out.MemAvailPct == nil {
		out.Note = "No host metrics: the node exporter may be down (see ops_alerts)."
	}
	return nil, out, nil
}

// many: several one-value queries at once.
func (t *Tools) many(ctx context.Context, g *Grafana, q map[string]string) (map[string]*float64, error) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	out := map[string]*float64{}
	var first error
	for k, expr := range q {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := g.one(ctx, expr)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && first == nil {
				first = err
			}
			out[k] = v
		}()
	}
	wg.Wait()
	return out, first
}

// ---- ops_apps

type App struct {
	Container   string   `json:"container"`
	Service     string   `json:"service,omitempty"`
	Role        string   `json:"role,omitempty"`
	Version     string   `json:"version,omitempty" jsonschema:"from the container name (what was deployed)"`
	Running     bool     `json:"running" jsonschema:"seen in the last 2 minutes"`
	StartedAt   string   `json:"started_at,omitempty" jsonschema:"≈ its last deploy or restart"`
	Restarts24h *float64 `json:"restarts_24h,omitempty"`
	MemoryMiB   *float64 `json:"memory_mib,omitempty"`
	MemoryPct   *float64 `json:"memory_of_limit_pct,omitempty"`
}

type AppsOut struct {
	Apps []App  `json:"apps"`
	Note string `json:"note"`
}

func (t *Tools) appsStatus(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, AppsOut, error) {
	g := t.Grafana
	type res struct {
		s   []sample
		err error
	}
	qs := []string{
		`container_start_time_seconds{job="cadvisor",name!=""}`,
		`changes(container_start_time_seconds{job="cadvisor",name!=""}[24h])`,
		`container_memory_working_set_bytes{job="cadvisor",name!=""}`,
		`container_memory_working_set_bytes{job="cadvisor",name!=""} / (container_spec_memory_limit_bytes{job="cadvisor",name!=""} > 0)`,
	}
	rs := make([]res, len(qs))
	var wg sync.WaitGroup
	for i, q := range qs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := g.query(ctx, q)
			rs[i] = res{s, err}
		}()
	}
	wg.Wait()
	for _, r := range rs {
		if r.err != nil {
			return nil, AppsOut{}, r.err
		}
	}
	now := float64(time.Now().Unix())
	apps := map[string]*App{}
	for _, s := range rs[0].s {
		n := s.Labels["name"]
		a := &App{Container: n, Service: s.Labels["container_service"], Role: s.Labels["container_role"],
			Running: now-s.At < 120, StartedAt: when(s.Value)}
		if a.Service != "" && a.Role != "" {
			if v, ok := strings.CutPrefix(n, a.Service+"-"+a.Role+"-"); ok {
				a.Version = v
			}
		}
		apps[n] = a
	}
	for _, s := range rs[1].s {
		if a := apps[s.Labels["name"]]; a != nil {
			v := s.Value
			a.Restarts24h = &v
		}
	}
	for _, s := range rs[2].s {
		if a := apps[s.Labels["name"]]; a != nil {
			v := round(s.Value/(1<<20), 0)
			a.MemoryMiB = &v
		}
	}
	for _, s := range rs[3].s {
		if a := apps[s.Labels["name"]]; a != nil {
			a.MemoryPct = pct(&s.Value)
		}
	}
	out := AppsOut{Apps: []App{}, Note: "Every container on the server (apps and monitoring). Restarts count starts of the same container in 24 h; a deploy makes a new container."}
	for _, a := range apps {
		out.Apps = append(out.Apps, *a)
	}
	slices.SortFunc(out.Apps, func(a, b App) int { return strings.Compare(a.Container, b.Container) })
	return nil, out, nil
}

// ---- ops_backups (the backup job's and the restore drill's own metrics)

type Backup struct {
	Job         string   `json:"job"`
	LastSuccess string   `json:"last_success,omitempty"`
	AgeHours    *float64 `json:"hours_since_success,omitempty"`
	LastRun     string   `json:"last_run,omitempty"`
	Unfinished  bool     `json:"last_run_unfinished" jsonschema:"the last run started after the last success: it failed, or is running now"`
}

type BackupsOut struct {
	Backups      []Backup `json:"backups"`
	DrillSuccess string   `json:"last_restore_drill_success,omitempty"`
	DrillAgeDays *float64 `json:"days_since_restore_drill,omitempty"`
	Note         string   `json:"note"`
}

func (t *Tools) backups(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, BackupsOut, error) {
	g := t.Grafana
	ok, err := g.query(ctx, `sgnl_backup_last_success_timestamp_seconds`)
	if err != nil {
		return nil, BackupsOut{}, err
	}
	runs, err := g.query(ctx, `sgnl_backup_last_run_timestamp_seconds`)
	if err != nil {
		return nil, BackupsOut{}, err
	}
	drill, err := g.query(ctx, `max(sgnl_restore_drill_last_success_timestamp_seconds)`)
	if err != nil {
		return nil, BackupsOut{}, err
	}
	now := float64(time.Now().Unix())
	jobs := map[string]*Backup{}
	get := func(j string) *Backup {
		if jobs[j] == nil {
			jobs[j] = &Backup{Job: short(j)}
		}
		return jobs[j]
	}
	// The job's own name: the textfile's "job" label, which the scrape
	// renames exported_job (its own job label is the exporter's).
	jobOf := func(l map[string]string) string { return firstOf(l["exported_job"], l["job"]) }
	lastOK := map[string]float64{}
	for _, s := range ok {
		b := get(jobOf(s.Labels))
		b.LastSuccess = when(s.Value)
		h := round((now-s.Value)/3600, 1)
		b.AgeHours = &h
		lastOK[jobOf(s.Labels)] = s.Value
	}
	for _, s := range runs {
		b := get(jobOf(s.Labels))
		b.LastRun = when(s.Value)
		b.Unfinished = s.Value > lastOK[jobOf(s.Labels)]
	}
	out := BackupsOut{Backups: []Backup{}}
	for _, b := range jobs {
		out.Backups = append(out.Backups, *b)
	}
	slices.SortFunc(out.Backups, func(a, b Backup) int { return strings.Compare(a.Job, b.Job) })
	if len(drill) > 0 && drill[0].Value > 0 {
		out.DrillSuccess = when(drill[0].Value)
		d := round((now-drill[0].Value)/86400, 1)
		out.DrillAgeDays = &d
	}
	switch {
	case len(out.Backups) == 0:
		out.Note = "No backup has been recorded yet (or the backup metrics aren't set up): unknown, not OK."
	case out.DrillSuccess == "":
		out.Note = "Restore drill: never recorded."
	default:
		out.Note = "From the backup job's own report; a job that never ran is caught by the BackupMissing alert (ops_alerts)."
	}
	return nil, out, nil
}

// ---- ops_certs

type Cert struct {
	Host     string  `json:"host"`
	NotAfter string  `json:"expires,omitempty"`
	DaysLeft float64 `json:"days_left,omitempty"`
	Issuer   string  `json:"issuer,omitempty"`
	Error    string  `json:"error,omitempty" jsonschema:"the handshake failed or the certificate isn't valid"`
}

type CertsOut struct {
	Certs []Cert `json:"certs"`
}

// dialTLS: a variable for tests.
var dialTLS = func(ctx context.Context, host string) (*tls.ConnectionState, error) {
	d := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: &tls.Config{ServerName: host}}
	c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, "443"))
	if err != nil {
		return nil, err
	}
	defer c.Close()
	st := c.(*tls.Conn).ConnectionState()
	return &st, nil
}

func (t *Tools) certs(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, CertsOut, error) {
	out := CertsOut{Certs: make([]Cert, len(t.CertHosts))}
	var wg sync.WaitGroup
	for i, h := range t.CertHosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := Cert{Host: h}
			st, err := dialTLS(ctx, h)
			switch {
			case err != nil:
				var v *tls.CertificateVerificationError
				if errors.As(err, &v) {
					c.Error = "not valid: " + short(v.Err.Error())
				} else {
					c.Error = short(err.Error())
				}
			case len(st.PeerCertificates) == 0:
				c.Error = "no certificate"
			default:
				leaf := st.PeerCertificates[0]
				c.NotAfter = leaf.NotAfter.UTC().Format(time.RFC3339)
				c.DaysLeft = round(time.Until(leaf.NotAfter).Hours()/24, 1)
				c.Issuer = short(strings.TrimSpace(strings.Join(leaf.Issuer.Organization, " ") + " " + leaf.Issuer.CommonName))
			}
			out.Certs[i] = c
		}()
	}
	wg.Wait()
	slices.SortStableFunc(out.Certs, func(a, b Cert) int {
		switch {
		case a.Error != "" && b.Error == "":
			return -1
		case a.Error == "" && b.Error != "":
			return 1
		}
		return int(a.DaysLeft - b.DaysLeft)
	})
	return nil, out, nil
}
