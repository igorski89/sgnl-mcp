package admin

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGrafana: answers the two paths the gateway may call, as the admin
// (X-JWT-Assertion), and records every request.
func fakeGrafana(t *testing.T) (*Grafana, *[]string) {
	var mu sync.Mutex
	var seen []string
	now := float64(time.Now().Unix())
	vec := func(items ...map[string]any) map[string]any {
		return map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": items}}
	}
	val := func(labels map[string]string, v float64) map[string]any {
		return map[string]any{"metric": labels, "value": []any{now, fmt.Sprint(v)}}
	}
	mcp := map[string]string{"name": "sgnl-mcp-web-f31f7ed", "container_service": "sgnl-mcp", "container_role": "web"}
	old := map[string]string{"name": "sgnl-mcp-web-238e9a9", "container_service": "sgnl-mcp", "container_role": "web"}
	results := map[string]map[string]any{
		diskUsed:                                 vec(val(nil, 0.4567)),
		`node_memory_MemTotal_bytes{job="node"}`: vec(val(nil, 8<<30)),
		`container_start_time_seconds{job="cadvisor",name!=""}`:                       vec(val(mcp, now-3600), val(old, now-7200)),
		`tlast_over_time(container_start_time_seconds{job="cadvisor",name!=""}[10m])`: vec(val(mcp, now-20), val(old, now-200)),
		`changes(container_start_time_seconds{job="cadvisor",name!=""}[24h])`:         vec(val(mcp, 2)),
		`sgnl_backup_last_success_timestamp_seconds`:                                  vec(val(map[string]string{"job": "node", "exported_job": "mail"}, now-7200), val(map[string]string{"job": "files"}, now-90000)),
		`sgnl_backup_last_run_timestamp_seconds`:                                      vec(val(map[string]string{"job": "node", "exported_job": "mail"}, now-7200), val(map[string]string{"job": "files"}, now-3600)),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+" "+r.URL.Query().Get("query"))
		mu.Unlock()
		if r.Header.Get("X-JWT-Assertion") != "admin-grafana-token" || r.Header.Get("Authorization") != "" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case pathQuery:
			res, ok := results[r.URL.Query().Get("query")]
			if !ok {
				res = vec()
			}
			json.NewEncoder(w).Encode(res)
		case pathAlerts:
			json.NewEncoder(w).Encode([]map[string]any{
				{"labels": map[string]string{"alertname": "HostDiskFilling", "severity": "warning"}, "annotations": map[string]string{"summary": "Disk / is 81% full"},
					"startsAt": "2026-11-26T00:00:00Z", "status": map[string]string{"state": "active"}},
				{"labels": map[string]string{"alertname": "TargetDown", "severity": "critical", "service": "sgnl-x‮"}, "annotations": map[string]string{},
					"startsAt": "2026-11-26T01:00:00Z", "status": map[string]string{"state": "active"}},
				{"labels": map[string]string{"alertname": "VectorErrors", "severity": "warning"}, "startsAt": "2026-11-25T00:00:00Z", "status": map[string]string{"state": "suppressed"}},
			})
		default:
			w.WriteHeader(404)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &Grafana{Base: srv.URL, HTTP: srv.Client(), Token: func(context.Context) (string, error) { return "admin-grafana-token", nil }}, &seen
}

func TestOps(t *testing.T) {
	_, read, _ := newFake(t)
	g, seen := fakeGrafana(t)
	cs, _ := wsession(t, &Tools{API: read, Grafana: g}, nil)

	txt, isErr := call(t, cs, "ops_alerts", nil)
	if isErr || !strings.Contains(txt, `"suppressed":1`) || strings.Index(txt, "TargetDown") > strings.Index(txt, "HostDiskFilling") || strings.Contains(txt, "‮") {
		t.Errorf("alerts: %q", txt)
	}
	txt, isErr = call(t, cs, "ops_host", nil)
	if isErr || !strings.Contains(txt, `"disk_used_pct":45.7`) || !strings.Contains(txt, `"memory_total_gib":8`) || !strings.Contains(txt, `"cpu_busy_pct":null`) {
		t.Errorf("host: %q", txt)
	}
	txt, isErr = call(t, cs, "ops_apps", nil)
	if isErr || !strings.Contains(txt, `"deploy":"f31f7ed"`) || strings.Contains(txt, "238e9a9") || !strings.Contains(txt, `"restarts_24h":2`) {
		t.Errorf("apps: %q", txt)
	}
	txt, isErr = call(t, cs, "ops_backups", nil)
	if isErr || !strings.Contains(txt, `{"hours_since_success":25,"job":"files"`) || !strings.Contains(txt, `"job":"mail"`) || strings.Contains(txt, `"job":"node"`) ||
		!strings.Contains(txt, `"last_run_unfinished":true`) || !strings.Contains(txt, "never recorded") {
		t.Errorf("backups: %q", txt)
	}
	for _, s := range *seen {
		if !strings.HasPrefix(s, "GET "+pathQuery+" ") && !strings.HasPrefix(s, "GET "+pathAlerts+" ") {
			t.Errorf("unexpected request: %s", s)
		}
	}
	if err := g.get(context.Background(), "/api/admin/users", nil, nil); err == nil {
		t.Error("another Grafana path was called")
	}
}

func TestOpsOnlyForAdmins(t *testing.T) {
	_, read, _ := newFake(t)
	g, seen := fakeGrafana(t)
	cs, _ := wsessionAs(t, &Tools{API: read, Grafana: g}, nil, "sgnl_users")
	if txt, isErr := call(t, cs, "ops_host", nil); !isErr || !strings.Contains(txt, "admins only") {
		t.Fatalf("non-admin: %q", txt)
	}
	if len(*seen) != 0 {
		t.Fatal("monitoring was called for a non-admin")
	}
}

func TestOpsToolsOnlyWhenConfigured(t *testing.T) {
	_, read, _ := newFake(t)
	cs, _ := wsession(t, &Tools{API: read}, nil)
	r, _ := cs.ListTools(context.Background(), nil)
	for _, x := range r.Tools {
		if strings.HasPrefix(x.Name, "ops_") {
			t.Errorf("%s without monitoring", x.Name)
		}
	}
}

func TestCerts(t *testing.T) {
	saved := dialTLS
	t.Cleanup(func() { dialTLS = saved })
	dialTLS = func(_ context.Context, host string) (*tls.ConnectionState, error) {
		switch host {
		case "bad.example":
			return nil, &tls.CertificateVerificationError{Err: errors.New("x509: certificate has expired")}
		case "down.example":
			return nil, errors.New("dial tcp: connection refused")
		}
		days := map[string]int{"a.example": 60, "b.example": 10}[host]
		c := &x509.Certificate{NotAfter: time.Now().Add(time.Duration(days)*24*time.Hour + time.Hour), Issuer: pkix.Name{Organization: []string{"Let's Encrypt"}, CommonName: "R11"}}
		return &tls.ConnectionState{PeerCertificates: []*x509.Certificate{c}}, nil
	}
	_, read, _ := newFake(t)
	cs, _ := wsession(t, &Tools{API: read, CertHosts: []string{"a.example", "b.example", "bad.example", "down.example"}}, nil)
	txt, isErr := call(t, cs, "ops_certs", nil)
	if isErr || !strings.Contains(txt, "certificate has expired") || !strings.Contains(txt, `"issuer":"Let's Encrypt R11"`) ||
		strings.Index(txt, "b.example") > strings.Index(txt, "a.example") || strings.Index(txt, "bad.example") > strings.Index(txt, "b.example") {
		t.Errorf("certs: %q", txt)
	}
}
