package admin

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/igorski89/sgnl-mcp/internal/confirm"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeMailW: the management JMAP with state: reads as Stalwart 1.x answers
// them, x:Account/set (write key only) replacing whole properties.
type fakeMailW struct {
	mu     sync.Mutex
	accts  map[string]map[string]any
	sets   []string // every set sent, as JSON
	refuse bool     // answer sets with notUpdated
}

func newFakeMailW(t *testing.T) (*fakeMailW, *Stalwart) {
	f := &fakeMailW{accts: map[string]map[string]any{
		"g1": {"id": "g1", "@type": "Group", "name": "contact", "domainId": "d1"},
		"g2": {"id": "g2", "@type": "Group", "name": "alerts", "domainId": "d1"},
		"u1": {"id": "u1", "@type": "User", "name": "boss", "domainId": "d1", "memberGroupIds": map[string]any{"g1": true, "g2": true},
			"aliases": map[string]any{"0": map[string]any{"name": "postmaster", "domainId": "d1", "enable": true, "description": nil}, "1": map[string]any{"name": "old", "domainId": "d1", "enable": false, "description": "kept"}},
			"quotas":  map[string]any{"maxDiskQuota": float64(1 << 30), "other": float64(7)}, "usedDiskQuota": float64(300 << 20)},
		"u2": {"id": "u2", "@type": "User", "name": "hugo", "domainId": "d1", "memberGroupIds": map[string]any{"g1": true}},
		"u3": {"id": "u3", "@type": "User", "name": "mcp-gateway", "domainId": "d1"},
		"u4": {"id": "u4", "@type": "User", "name": "relay-bot", "domainId": "d1", "memberGroupIds": map[string]any{"g2": true}},
	}}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if key != "API_read" && key != "API_write" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/jmap/session" {
			json.NewEncoder(w).Encode(map[string]any{"apiUrl": srv.URL + "/jmap", "primaryAccounts": map[string]string{stalwartCap: "x"}})
			return
		}
		var req struct{ MethodCalls [][]json.RawMessage }
		json.NewDecoder(r.Body).Decode(&req)
		var out [][]any
		for _, mc := range req.MethodCalls {
			var name, id string
			var args map[string]any
			json.Unmarshal(mc[0], &name)
			json.Unmarshal(mc[1], &args)
			json.Unmarshal(mc[2], &id)
			var res any
			switch name {
			case "x:Account/query":
				var ids []string
				for k := range f.accts {
					ids = append(ids, k)
				}
				res = map[string]any{"ids": ids}
			case "x:Account/get":
				var l []any
				for _, a := range f.accts {
					c := map[string]any{}
					b, _ := json.Marshal(a)
					json.Unmarshal(b, &c)
					l = append(l, c)
				}
				res = map[string]any{"list": l}
			case "x:Domain/get":
				res = map[string]any{"list": []any{map[string]any{"id": "d1", "name": "co.example", "catchAllAddress": "contact@co.example"}, map[string]any{"id": "d2", "name": "elsewhere.example"}}}
			case "x:Account/set":
				if key != "API_write" {
					out = append(out, []any{"error", map[string]any{"type": "forbidden"}, id})
					continue
				}
				b, _ := json.Marshal(args)
				f.sets = append(f.sets, string(b))
				if cr, ok := args["create"].(map[string]any); ok {
					g := cr["g"].(map[string]any)
					g["id"] = "g9"
					f.accts["g9"] = g
					res = map[string]any{"created": map[string]any{"g": map[string]any{"id": "g9"}}}
					break
				}
				up, _ := args["update"].(map[string]any)
				done, not := map[string]any{}, map[string]any{}
				for aid, p := range up {
					if f.refuse {
						not[aid] = map[string]any{"type": "invalidProperties", "description": "nope"}
						continue
					}
					maps.Copy(f.accts[aid], p.(map[string]any))
					done[aid] = nil
				}
				res = map[string]any{"updated": done, "notUpdated": not}
			}
			out = append(out, []any{name, res, id})
		}
		json.NewEncoder(w).Encode(map[string]any{"methodResponses": out})
	}))
	t.Cleanup(srv.Close)
	return f, &Stalwart{Base: srv.URL, Key: "API_write", Write: true, HTTP: srv.Client()}
}

func (f *fakeMailW) get(id, k string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := json.Marshal(f.accts[id][k])
	return string(b)
}

func (f *fakeMailW) setCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sets)
}

func mailWriteTools(t *testing.T, a answer) (*fakeMailW, *mcp.ClientSession, *[]*mcp.ElicitParams) {
	_, read, _ := newFake(t)
	f, w := newFakeMailW(t)
	r := &Stalwart{Base: w.Base, Key: "API_read", HTTP: w.HTTP}
	cs, asked := wsession(t, &Tools{API: read, Mail: r, MailWrite: w, Ask: &confirm.Asker{}, UserDomain: "co.example", MailGroupCreate: true}, a)
	return f, cs, asked
}

func TestMailWriteToolsOnlyWithWriteKey(t *testing.T) {
	_, read, write := newFake(t)
	_, w := newFakeMailW(t)
	r := &Stalwart{Base: w.Base, Key: "API_read", HTTP: w.HTTP}
	for _, tc := range []struct {
		at   *Tools
		want []string
	}{
		{&Tools{API: read, Mail: r}, nil},
		{&Tools{API: read, Mail: r, Write: write, Ask: &confirm.Asker{}}, nil},
		{&Tools{API: read, Mail: r, MailWrite: w, Ask: &confirm.Asker{}}, []string{"mail_group_member_add", "mail_group_member_remove", "mail_quota_set"}},
		// Without the switch, no group create even with a domain.
		{&Tools{API: read, Mail: r, MailWrite: w, Ask: &confirm.Asker{}, UserDomain: "co.example"}, []string{"mail_group_member_add", "mail_group_member_remove", "mail_quota_set"}},
		{&Tools{API: read, Mail: r, MailWrite: w, Ask: &confirm.Asker{}, UserDomain: "co.example", MailGroupCreate: true}, []string{"mail_group_create", "mail_group_member_add", "mail_group_member_remove", "mail_quota_set"}},
	} {
		cs, _ := wsession(t, tc.at, yes)
		res, _ := cs.ListTools(context.Background(), nil)
		var got []string
		for _, x := range res.Tools {
			if strings.HasPrefix(x.Name, "mail_") && !x.Annotations.ReadOnlyHint {
				got = append(got, x.Name)
			}
		}
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("mail write tools: %v, want %v", got, tc.want)
		}
		for _, x := range res.Tools {
			if tc.at.Write == nil && strings.HasPrefix(x.Name, "admin_") && !x.Annotations.ReadOnlyHint {
				t.Errorf("%s without the login write client", x.Name)
			}
		}
	}
}

func TestMailSetGuard(t *testing.T) {
	up := func(p map[string]any) mcall {
		return mcall{"x:Account/set", map[string]any{"update": map[string]any{"u1": p}}}
	}
	ok := []mcall{
		up(map[string]any{"memberGroupIds": map[string]any{"g1": true}}),
		up(map[string]any{"quotas": map[string]any{"maxDiskQuota": int64(5)}}),
		up(map[string]any{"memberGroupIds": map[string]any{}}),
	}
	for _, c := range ok {
		if err := checkSet(c); err != nil {
			t.Errorf("%v refused: %v", c.args, err)
		}
	}
	bad := []mcall{
		{"x:Account/set", map[string]any{"create": map[string]any{"n": map[string]any{"name": "x"}}}},
		{"x:Account/set", map[string]any{"destroy": []string{"u1"}}},
		{"x:Account/set", map[string]any{"update": map[string]any{"u1": map[string]any{"quotas": map[string]any{}}, "u2": map[string]any{"quotas": map[string]any{}}}}},
		{"x:Account/set", map[string]any{"update": map[string]any{"u1": map[string]any{"quotas": map[string]any{}}}, "destroy": []string{"u2"}}},
		{"x:Domain/set", map[string]any{"update": map[string]any{"d1": map[string]any{"aliases": map[string]any{}}}}},
		{"x:ApiKey/set", map[string]any{}},
		up(map[string]any{}),
		up(map[string]any{"secrets": map[string]any{}}),
		up(map[string]any{"roles": map[string]any{"admin": true}}),
		up(map[string]any{"permissions": map[string]any{}}),
		up(map[string]any{"name": "x"}),
		up(map[string]any{"aliases/0": map[string]any{"name": "x"}}),
		up(map[string]any{"memberGroupIds/g1": true}),
		up(map[string]any{"memberGroupIds": map[string]any{"g1": "yes"}}),
		up(map[string]any{"quotas": map[string]any{"maxDiskQuota": "big"}}),
		// Never aliases: they decide whose account a login lands in.
		up(map[string]any{"aliases": map[string]any{"0": map[string]any{"name": "a", "domainId": "d1", "enable": true}}}),
		up(map[string]any{"aliases": map[string]any{}}),
		up(map[string]any{"memberGroupIds": map[string]any{"g1": true}, "aliases": map[string]any{}}),
	}
	for _, c := range bad {
		if checkSet(c) == nil {
			t.Errorf("%s %v allowed", c.method, c.args)
		}
	}
	// The read key never sends a set, whatever it is.
	if checkMail(ok[0]) == nil {
		t.Error("a set allowed with the read key")
	}
	_, w := newFakeMailW(t)
	r := &Stalwart{Base: w.Base, Key: "API_read", HTTP: w.HTTP}
	if _, err := r.call(context.Background(), ok[0]); err == nil || !strings.Contains(err.Error(), "doesn't call") {
		t.Errorf("read key set: %v", err)
	}
}

func TestMailMembers(t *testing.T) {
	f, cs, asked := mailWriteTools(t, yes)
	txt, isErr := call(t, cs, "mail_group_member_add", map[string]any{"mailbox": "alerts@co.example", "person": "hugo"})
	if isErr || !strings.Contains(txt, `"members":["boss","hugo","relay-bot"]`) {
		t.Fatalf("add: %q", txt)
	}
	if q := (*asked)[0].Message; !strings.HasPrefix(q, "Add hugo to alerts@co.example: they read and can send as alerts@co.example?") {
		t.Errorf("question: %q", q)
	}
	if got := f.get("u2", "memberGroupIds"); got != `{"g1":true,"g2":true}` {
		t.Errorf("hugo's groups: %s", got)
	}
	txt, isErr = call(t, cs, "mail_group_member_remove", map[string]any{"mailbox": "contact", "person": "boss@co.example"})
	if isErr || !strings.Contains(txt, `"members":["hugo"]`) {
		t.Fatalf("remove: %q", txt)
	}
	if q := (*asked)[1].Message; !strings.HasPrefix(q, "Remove boss from contact@co.example: they lose access to contact@co.example and its mail?") {
		t.Errorf("question: %q", q)
	}
	if got := f.get("u1", "memberGroupIds"); got != `{"g2":true}` {
		t.Errorf("boss's groups: %s", got)
	}
	n := f.setCount()
	for _, c := range []struct{ tool, mailbox, person, want string }{
		{"mail_group_member_remove", "contact", "hugo", "last member"},
		{"mail_group_member_add", "alerts", "hugo", "already"},
		{"mail_group_member_add", "alerts", "mcp-gateway", "machine account"},
		{"mail_group_member_remove", "alerts", "relay-bot", "no login account"},
		{"mail_group_member_add", "hugo", "boss", "not a shared one"},
		{"mail_group_member_add", "alerts", "contact", "not a person"},
		{"mail_group_member_add", "nope", "hugo", "no mailbox"},
	} {
		txt, isErr := call(t, cs, c.tool, map[string]any{"mailbox": c.mailbox, "person": c.person})
		if !isErr || !strings.Contains(txt, c.want) {
			t.Errorf("%s %s %s: %q, want %q", c.tool, c.mailbox, c.person, txt, c.want)
		}
	}
	if f.setCount() != n || len(*asked) != 2 {
		t.Errorf("a refused change was asked or sent")
	}
}

func TestMailQuota(t *testing.T) {
	f, cs, asked := mailWriteTools(t, yes)
	txt, isErr := call(t, cs, "mail_quota_set", map[string]any{"mailbox": "boss", "limit": "5 GB"})
	if isErr || !strings.Contains(txt, `"quota_mib":5120`) {
		t.Fatalf("set: %q", txt)
	}
	if q := (*asked)[0].Message; !strings.HasPrefix(q, "Set the size limit of boss@co.example to 5 GB (now 1 GB)?") {
		t.Errorf("question: %q", q)
	}
	if got := f.get("u1", "quotas"); got != `{"maxDiskQuota":5368709120,"other":7}` {
		t.Errorf("quotas (other keys kept): %s", got)
	}
	if txt, isErr := call(t, cs, "mail_quota_set", map[string]any{"mailbox": "boss", "limit": "no limit"}); isErr || !strings.Contains(txt, `"quota":"no limit set"`) {
		t.Errorf("no limit: %q", txt)
	}
	if got := f.get("u1", "quotas"); got != `{"other":7}` {
		t.Errorf("no limit: the key must go, others stay: %s", got)
	}
	if txt, isErr := call(t, cs, "mail_quota_set", map[string]any{"mailbox": "contact@co.example", "limit": "no limit"}); !isErr || !strings.Contains(txt, "already no limit") {
		t.Errorf("same: %q", txt)
	}
	for _, c := range []struct{ mailbox, limit, want string }{
		{"boss", "100 MB", "already uses 300 MB"},
		{"boss", "lots", "give a size"},
		{"boss", "1 MB", "between"},
		{"mcp-gateway", "1 GB", "machine account"},
	} {
		txt, isErr := call(t, cs, "mail_quota_set", map[string]any{"mailbox": c.mailbox, "limit": c.limit})
		if !isErr || !strings.Contains(txt, c.want) {
			t.Errorf("%s %s: %q, want %q", c.mailbox, c.limit, txt, c.want)
		}
	}
	if f.setCount() != 2 {
		t.Errorf("sets: %d", f.setCount())
	}
}

func TestMailChangeDeclinedOrRefused(t *testing.T) {
	f, cs, _ := mailWriteTools(t, no)
	if txt, isErr := call(t, cs, "mail_group_member_add", map[string]any{"mailbox": "alerts", "person": "hugo"}); !isErr || !strings.Contains(txt, "declined") {
		t.Errorf("declined: %q", txt)
	}
	if f.setCount() != 0 {
		t.Error("sent after a decline")
	}
	f, cs, _ = mailWriteTools(t, yes)
	f.refuse = true
	if txt, isErr := call(t, cs, "mail_group_member_add", map[string]any{"mailbox": "alerts", "person": "hugo"}); !isErr || !strings.Contains(txt, "refused the change: invalidProperties nope") {
		t.Errorf("notUpdated: %q", txt)
	}
}

// The answer counts only for what was shown: a change in between refuses it.
func TestMailChangeBoundToWhatWasShown(t *testing.T) {
	var f *fakeMailW
	f, cs, _ := mailWriteTools(t, func(*mcp.ElicitRequest) *mcp.ElicitResult {
		f.mu.Lock()
		f.accts["u2"]["memberGroupIds"] = map[string]any{"g1": true, "g9": true}
		f.mu.Unlock()
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{}}
	})
	txt, isErr := call(t, cs, "mail_group_member_add", map[string]any{"mailbox": "alerts", "person": "hugo"})
	if !isErr || !strings.Contains(txt, "changed since") {
		t.Errorf("stale: %q", txt)
	}
	if f.setCount() != 0 {
		t.Error("sent after a change")
	}
}

func TestParseLimit(t *testing.T) {
	for in, want := range map[string]int64{"no limit": 0, "500 MB": 500 << 20, "5GB": 5 << 30, "1.5 GiB": 3 << 29, "2 tb": 2 << 40} {
		if got, err := parseLimit(in); err != nil || got != want {
			t.Errorf("%q: %d %v", in, got, err)
		}
	}
	for _, in := range []string{"", "5", "-1 GB", "5 kb", "20 TB", "9 MB"} {
		if _, err := parseLimit(in); err == nil {
			t.Errorf("%q allowed", in)
		}
	}
}

func TestMailGroupCreateGuard(t *testing.T) {
	cr := func(g map[string]any, extra ...string) mcall {
		a := map[string]any{"create": map[string]any{"g": g}}
		for _, k := range extra {
			a[k] = map[string]any{}
		}
		return mcall{"x:Account/set", a}
	}
	if err := checkSet(cr(map[string]any{"@type": "Group", "name": "sales", "domainId": "d1", "description": "Sales"})); err != nil {
		t.Errorf("group create refused: %v", err)
	}
	for _, c := range []mcall{
		cr(map[string]any{"@type": "User", "name": "x", "domainId": "d1"}),
		cr(map[string]any{"@type": "Group", "name": "x", "domainId": "d1", "aliases": map[string]any{}}),
		cr(map[string]any{"@type": "Group", "name": "x", "domainId": "d1", "roles": "admin"}),
		cr(map[string]any{"@type": "Group", "name": "x", "domainId": "d1", "permissions": map[string]any{}}),
		cr(map[string]any{"@type": "Group", "name": "x", "domainId": "d1", "quotas": map[string]any{}}),
		cr(map[string]any{"@type": "Group", "name": "x", "domainId": "d1", "memberTenantId": "t"}),
		cr(map[string]any{"@type": "Group", "name": "x"}),
		cr(map[string]any{"@type": "Group", "name": "x", "domainId": "d1"}, "update"),
		cr(map[string]any{"@type": "Group", "name": "x", "domainId": "d1"}, "destroy"),
		{"x:Account/set", map[string]any{"create": map[string]any{"a": map[string]any{"@type": "Group", "name": "a", "domainId": "d1"}, "b": map[string]any{"@type": "Group", "name": "b", "domainId": "d1"}}}},
	} {
		if checkSet(c) == nil {
			t.Errorf("allowed: %v", c.args)
		}
	}
}

func TestMailGroupCreate(t *testing.T) {
	f, cs, asked := mailWriteTools(t, yes)
	txt, isErr := call(t, cs, "mail_group_create", map[string]any{"name": "Sales", "sender_name": "ACME Sales", "members": []string{"boss", "hugo@co.example"}})
	if isErr || !strings.Contains(txt, `"members":["boss","hugo"]`) || !strings.Contains(txt, `"address":"sales@co.example"`) {
		t.Fatalf("create: %s", txt)
	}
	q := (*asked)[0].Message
	if !strings.HasPrefix(q, `Create the shared mailbox sales@co.example ("ACME Sales") with members boss, hugo: they read it and can send as sales@co.example?`) ||
		!strings.Contains(q, "now lands in contact@co.example (the catch-all)") {
		t.Errorf("question: %q", q)
	}
	if len(f.sets) != 3 || !strings.Contains(f.sets[0], `"create":{"g":{"@type":"Group","description":"ACME Sales","domainId":"d1","name":"sales"}}`) {
		t.Errorf("sets: %v", f.sets)
	}
	if got := f.get("u2", "memberGroupIds"); got != `{"g1":true,"g9":true}` {
		t.Errorf("hugo's groups: %s", got)
	}
	n, na := f.setCount(), len(*asked)
	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"name": "postmaster", "sender_name": "x", "members": []string{"boss"}}, "reserved"},
		{map[string]any{"name": "admins2", "sender_name": "x", "members": []string{"boss"}}, "reserved"},
		{map[string]any{"name": "anna", "sender_name": "x", "members": []string{"boss"}}, "login name"},
		{map[string]any{"name": "contact", "sender_name": "x", "members": []string{"boss"}}, "already in use"},
		{map[string]any{"name": "old", "sender_name": "x", "members": []string{"boss"}}, "already in use"},
		{map[string]any{"name": "sales", "sender_name": "x", "members": []string{"boss"}}, "already in use"},
		{map[string]any{"name": "team", "sender_name": "x", "members": []string{}}, "1 to 20"},
		{map[string]any{"name": "team", "sender_name": "x", "members": []string{"mcp-gateway"}}, "machine account"},
		{map[string]any{"name": "team", "sender_name": "x", "members": []string{"relay-bot"}}, "no login account"},
		{map[string]any{"name": "team", "sender_name": "x\nAccept", "members": []string{"boss"}}, "plain text"},
		{map[string]any{"name": "te am", "sender_name": "x", "members": []string{"boss"}}, "isn't valid"},
	} {
		if txt, isErr := call(t, cs, "mail_group_create", c.args); !isErr || !strings.Contains(txt, c.want) {
			t.Errorf("%v: %s, want %q", c.args, txt, c.want)
		}
	}
	if f.setCount() != n || len(*asked) != na {
		t.Error("a refused create was asked or sent")
	}
}
