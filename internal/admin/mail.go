package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/igorski89/sgnl-mcp/internal/audit"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Mail admin, read-only: shared mailboxes and their members, people's
// addresses and quotas, and the outbound queue. Through the mail server's
// management JMAP (Stalwart 1.x, capability urn:stalwart:jmap) with the
// gateway's own machine account's API key, read permissions only. Only the
// methods and properties below are ever asked for: never a message's
// blobId (with it the queued mail itself could be downloaded), never
// credentials; blobId is also stripped from any answer.

const stalwartCap = "urn:stalwart:jmap"

// Stalwart: the mail server's management JMAP.
type Stalwart struct {
	Base string // https://mail.<apps domain>
	Key  string // API_…
	HTTP *http.Client
	// Write: a key that may also change accounts (mail_write.go); only
	// then is x:Account/set ever sent, and only as checkSet allows.
	Write bool

	mu   sync.Mutex
	acct string
	api  string
}

// accountProps: what the mail tools read of an account; offboarding also
// reads permissions (whether logins are allowed), never credentials.
var accountProps = []string{"id", "@type", "name", "domainId", "emailAddress", "description", "memberGroupIds", "aliases", "quotas", "usedDiskQuota"}

// The only reads, each with the only properties it may ask for.
var mailReads = map[string][]string{
	"x:Account/query":       nil,
	"x:Account/get":         append(slices.Clone(accountProps), "permissions"),
	"x:Domain/get":          {"id", "name", "catchAllAddress"},
	"x:QueuedMessage/query": nil,
	"x:QueuedMessage/get":   {"id", "createdAt", "nextRetry", "nextNotify", "returnPath", "recipients", "size", "envId", "priority", "flags"},
}

type mcall struct {
	method string
	args   map[string]any
}

// checkMail: only mailReads, a /get only with an explicit allowed list.
func checkMail(c mcall) error {
	props, ok := mailReads[c.method]
	if !ok {
		return fmt.Errorf("the gateway doesn't call %s", c.method)
	}
	if strings.HasSuffix(c.method, "/get") {
		ps, _ := c.args["properties"].([]string)
		if len(ps) == 0 {
			return fmt.Errorf("%s without a properties list", c.method)
		}
		for _, p := range ps {
			if !slices.Contains(props, p) {
				return fmt.Errorf("%s: %s is never asked for", c.method, p)
			}
		}
	}
	return nil
}

// stripBlobs: any blobId removed from an answer, wherever it is.
func stripBlobs(v any) any {
	switch x := v.(type) {
	case map[string]any:
		delete(x, "blobId")
		for k, e := range x {
			x[k] = stripBlobs(e)
		}
	case []any:
		for i, e := range x {
			x[i] = stripBlobs(e)
		}
	}
	return v
}

func (m *Stalwart) do(ctx context.Context, method, url string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(ctx, method, url, rd)
	req.Header.Set("Authorization", "Bearer "+m.Key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("the mail server didn't answer: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return fmt.Errorf("the mail server refused the gateway's key (%d): ask whoever runs the instance", resp.StatusCode)
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("the mail server answered %d", resp.StatusCode)
	}
	return json.Unmarshal(data, out)
}

// session: the API URL and the account to address, once.
func (m *Stalwart) session(ctx context.Context) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.api != "" {
		return m.api, m.acct, nil
	}
	var s struct {
		APIURL          string            `json:"apiUrl"`
		PrimaryAccounts map[string]string `json:"primaryAccounts"`
	}
	if err := m.do(ctx, "GET", strings.TrimRight(m.Base, "/")+"/jmap/session", nil, &s); err != nil {
		return "", "", err
	}
	if s.APIURL == "" {
		return "", "", fmt.Errorf("the mail server's session has no API URL")
	}
	m.api, m.acct = s.APIURL, s.PrimaryAccounts[stalwartCap]
	return m.api, m.acct, nil
}

// call: checked reads; the answers (blobIds stripped) in order.
func (m *Stalwart) call(ctx context.Context, calls ...mcall) ([]map[string]any, error) {
	for _, c := range calls {
		check := checkMail
		if c.method == "x:Account/set" && m.Write {
			check = checkSet
		}
		if err := check(c); err != nil {
			return nil, err
		}
	}
	api, acct, err := m.session(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(calls))
	for i, c := range calls {
		names[i] = c.method
	}
	audit.Methods(ctx, "stalwart-admin", names)
	var mc [][]any
	for i, c := range calls {
		a := map[string]any{}
		for k, v := range c.args {
			a[k] = v
		}
		if acct != "" {
			a["accountId"] = acct
		}
		mc = append(mc, []any{c.method, a, fmt.Sprint(i)})
	}
	var r struct {
		MethodResponses [][]json.RawMessage `json:"methodResponses"`
	}
	if err := m.do(ctx, "POST", api, map[string]any{"using": []string{"urn:ietf:params:jmap:core", stalwartCap}, "methodCalls": mc}, &r); err != nil {
		return nil, err
	}
	var out []map[string]any
	for _, x := range r.MethodResponses {
		var name string
		var res map[string]any
		if len(x) < 2 || json.Unmarshal(x[0], &name) != nil || json.Unmarshal(x[1], &res) != nil {
			return nil, fmt.Errorf("the mail server's answer can't be read")
		}
		if name == "error" {
			return nil, fmt.Errorf("the mail server refused: %s %s", short(fmt.Sprint(res["type"])), short(fmt.Sprint(res["description"])))
		}
		out = append(out, stripBlobs(res).(map[string]any))
	}
	return out, nil
}

func list(r map[string]any) []map[string]any {
	var out []map[string]any
	ls, _ := r["list"].([]any)
	for _, x := range ls {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return short(s)
}

// directory: every account and the domain names, for one answer; the
// accounts with these extra properties (from mailReads).
func directory(ctx context.Context, m *Stalwart, extra ...string) ([]map[string]any, map[string]string, error) {
	r, err := m.call(ctx,
		mcall{"x:Account/query", map[string]any{"limit": 2000}},
		mcall{"x:Account/get", map[string]any{"#ids": map[string]any{"resultOf": "0", "name": "x:Account/query", "path": "/ids"},
			"properties": append(slices.Clone(accountProps), extra...)}},
		mcall{"x:Domain/get", map[string]any{"ids": nil, "properties": []string{"id", "name"}}})
	if err != nil {
		return nil, nil, err
	}
	domains := map[string]string{}
	for _, d := range list(r[2]) {
		domains[str(d, "id")] = str(d, "name")
	}
	return list(r[1]), domains, nil
}

// MailAccount: a person's or a shared mailbox's addresses, groups, quota.
type MailAccount struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind" jsonschema:"person or shared (a group mailbox)"`
	Address   string   `json:"address,omitempty"`
	Aliases   []string `json:"aliases,omitempty"`
	Members   []string `json:"members,omitempty" jsonschema:"shared mailboxes: who reads and sends from it"`
	MemberOf  []string `json:"member_of,omitempty" jsonschema:"people: the shared mailboxes they're in"`
	QuotaMiB  *float64 `json:"quota_mib,omitempty"`
	Quota     string   `json:"quota,omitempty"`
	UsedMiB   *float64 `json:"used_mib,omitempty"`
	Disabled  bool     `json:"disabled,omitempty"`
	About     string   `json:"description,omitempty"`
	accountID string
}

func mib(v any) *float64 {
	f, ok := v.(float64)
	if !ok {
		return nil
	}
	x := round(f/(1<<20), 1)
	return &x
}

func (t *Tools) mailAccounts(ctx context.Context) ([]MailAccount, error) {
	accts, domains, err := directory(ctx, t.Mail)
	if err != nil {
		return nil, err
	}
	return accountsFrom(accts, domains), nil
}

// accountsFrom: the server's accounts as people read them.
func accountsFrom(accts []map[string]any, domains map[string]string) []MailAccount {
	byID := map[string]*MailAccount{}
	var out []*MailAccount
	for _, a := range accts {
		m := &MailAccount{Name: str(a, "name"), Kind: "person", About: str(a, "description"), accountID: str(a, "id")}
		if strings.EqualFold(str(a, "@type"), "group") {
			m.Kind = "shared"
		}
		d := domains[str(a, "domainId")]
		m.Address = str(a, "emailAddress")
		if m.Address == "" && d != "" {
			m.Address = m.Name + "@" + d
		}
		// Aliases: a list or (as the server keeps them) a map of objects.
		var als []any
		switch x := a["aliases"].(type) {
		case []any:
			als = x
		case map[string]any:
			for _, k := range slices.Sorted(maps.Keys(x)) {
				als = append(als, x[k])
			}
		}
		for _, x := range als {
			al, _ := x.(map[string]any)
			if en, ok := al["enable"].(bool); ok && !en {
				continue
			}
			n := str(al, "name")
			if n == "" {
				continue
			}
			if dn := domains[str(al, "domainId")]; !strings.Contains(n, "@") {
				if dn == "" {
					dn = d
				}
				n += "@" + dn
			}
			m.Aliases = append(m.Aliases, n)
		}
		if q, ok := a["quotas"].(map[string]any); ok {
			m.QuotaMiB = mib(q["maxDiskQuota"])
		}
		if m.QuotaMiB == nil || *m.QuotaMiB == 0 {
			m.QuotaMiB, m.Quota = nil, "no limit set"
		}
		m.UsedMiB = mib(a["usedDiskQuota"])
		byID[m.accountID] = m
		out = append(out, m)
	}
	for _, a := range accts {
		gs, _ := a["memberGroupIds"].(map[string]any)
		me := byID[str(a, "id")]
		for gid := range gs {
			if g := byID[gid]; g != nil && me != nil {
				g.Members = append(g.Members, me.Name)
				me.MemberOf = append(me.MemberOf, g.Address)
			}
		}
	}
	res := []MailAccount{}
	for _, m := range out {
		slices.Sort(m.Members)
		slices.Sort(m.MemberOf)
		slices.Sort(m.Aliases)
		res = append(res, *m)
	}
	slices.SortFunc(res, func(a, b MailAccount) int { return strings.Compare(a.Kind+a.Name, b.Kind+b.Name) })
	return res
}

// ---- mail_mailboxes

type MailboxesOut struct {
	Shared []MailAccount `json:"shared" jsonschema:"shared (group) mailboxes with their members"`
	People []MailAccount `json:"people"`
}

func (t *Tools) mailboxes(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, MailboxesOut, error) {
	all, err := t.mailAccounts(ctx)
	if err != nil {
		return nil, MailboxesOut{}, err
	}
	out := MailboxesOut{Shared: []MailAccount{}, People: []MailAccount{}}
	for _, a := range all {
		if a.Kind == "shared" {
			out.Shared = append(out.Shared, a)
		} else {
			out.People = append(out.People, a)
		}
	}
	return nil, out, nil
}

// ---- mail_mailbox

type MailboxIn struct {
	Name string `json:"name" jsonschema:"a person's or shared mailbox's name or address"`
}

func (t *Tools) mailbox(ctx context.Context, _ *mcp.CallToolRequest, in MailboxIn) (*mcp.CallToolResult, MailAccount, error) {
	want := strings.ToLower(strings.TrimSpace(in.Name))
	if want == "" {
		return nil, MailAccount{}, fmt.Errorf("name is required")
	}
	all, err := t.mailAccounts(ctx)
	if err != nil {
		return nil, MailAccount{}, err
	}
	for _, a := range all {
		if strings.ToLower(a.Name) == want || strings.ToLower(a.Address) == want || slices.ContainsFunc(a.Aliases, func(x string) bool { return strings.ToLower(x) == want }) {
			return nil, a, nil
		}
	}
	return nil, MailAccount{}, fmt.Errorf("no mailbox %q (see mail_mailboxes)", short(in.Name))
}

// ---- mail_queue / mail_delivery

type QueueIn struct {
	To   string `json:"to,omitempty" jsonschema:"only mail to this address"`
	From string `json:"from,omitempty" jsonschema:"only mail from this address (its envelope sender)"`
}

type Queued struct {
	Created    string      `json:"created,omitempty"`
	From       string      `json:"from,omitempty"`
	SizeKiB    *float64    `json:"size_kib,omitempty"`
	NextRetry  string      `json:"next_retry,omitempty"`
	Recipients []Recipient `json:"recipients"`
}

type Recipient struct {
	Address string `json:"address"`
	Status  string `json:"status" jsonschema:"scheduled, completed, temporary failure or permanent failure"`
	Detail  string `json:"detail,omitempty" jsonschema:"the receiving server's reply or the error"`
	Retries *int   `json:"retries,omitempty"`
	Expires string `json:"gives_up,omitempty"`
}

type QueueOut struct {
	Messages []Queued `json:"messages"`
	Note     string   `json:"note"`
}

const queueNote = "Only mail still in the outbound queue is here (no message content). Delivered mail leaves the queue; there's no delivery history. Outbound mail is handed to the relay service, so 'completed' means the relay took it; a later bounce comes back by mail to the sender."

func recipientOf(v any) Recipient {
	m, _ := v.(map[string]any)
	r := Recipient{Address: firstOf(str(m, "address"), str(m, "email"))}
	st := m["status"]
	switch s := st.(type) {
	case string:
		r.Status = short(s)
	case map[string]any:
		r.Status = firstOf(str(s, "@type"), str(s, "type"))
		var parts []string
		for _, k := range []string{"remoteHost", "hostname", "response", "message", "errorType", "command"} {
			if v := s[k]; v != nil {
				b, _ := json.Marshal(v)
				parts = append(parts, short(strings.Trim(string(b), `"`)))
			}
		}
		r.Detail = strings.Join(parts, " · ")
	}
	if n, ok := map[string]string{"Scheduled": "scheduled", "Completed": "completed", "TemporaryFailure": "temporary failure", "PermanentFailure": "permanent failure"}[r.Status]; ok {
		r.Status = n
	}
	if n, ok := m["retryCount"].(float64); ok {
		i := int(n)
		r.Retries = &i
	}
	r.Expires = str(m, "expires")
	return r
}

func (t *Tools) queue(ctx context.Context, _ *mcp.CallToolRequest, in QueueIn) (*mcp.CallToolResult, QueueOut, error) {
	f := map[string]any{}
	if v := strings.TrimSpace(in.To); v != "" {
		f["to"] = v
	}
	if v := strings.TrimSpace(in.From); v != "" {
		f["returnPath"] = v
	}
	r, err := t.Mail.call(ctx,
		mcall{"x:QueuedMessage/query", map[string]any{"filter": f, "limit": 100}},
		mcall{"x:QueuedMessage/get", map[string]any{"#ids": map[string]any{"resultOf": "0", "name": "x:QueuedMessage/query", "path": "/ids"},
			"properties": mailReads["x:QueuedMessage/get"]}})
	if err != nil {
		return nil, QueueOut{}, err
	}
	out := QueueOut{Messages: []Queued{}, Note: queueNote}
	for _, q := range list(r[1]) {
		m := Queued{Created: str(q, "createdAt"), From: str(q, "returnPath"), NextRetry: str(q, "nextRetry")}
		if s, ok := q["size"].(float64); ok {
			k := round(s/1024, 1)
			m.SizeKiB = &k
		}
		switch rs := q["recipients"].(type) {
		case []any:
			for _, x := range rs {
				m.Recipients = append(m.Recipients, recipientOf(x))
			}
		case map[string]any:
			for _, x := range rs {
				m.Recipients = append(m.Recipients, recipientOf(x))
			}
		}
		out.Messages = append(out.Messages, m)
	}
	return nil, out, nil
}

type DeliveryIn struct {
	To string `json:"to" jsonschema:"the recipient's address"`
}

func (t *Tools) delivery(ctx context.Context, req *mcp.CallToolRequest, in DeliveryIn) (*mcp.CallToolResult, QueueOut, error) {
	if strings.TrimSpace(in.To) == "" {
		return nil, QueueOut{}, fmt.Errorf("to is required")
	}
	res, out, err := t.queue(ctx, req, QueueIn{To: in.To})
	if err != nil {
		return res, out, err
	}
	if len(out.Messages) == 0 {
		out.Note = "Nothing to " + short(in.To) + " is waiting in the outbound queue: it was handed on (or never sent). " + queueNote
	}
	return nil, out, nil
}
