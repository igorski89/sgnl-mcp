package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/igorski89/sgnl-mcp/internal/audit"
	"github.com/igorski89/sgnl-mcp/internal/confirm"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func auditStore(t *testing.T) *audit.Store {
	s, err := audit.OpenForTest(audit.Config{Path: filepath.Join(t.TempDir(), "audit.db"), Admin: audit.Full, User: audit.Meta,
		Clear: map[string]map[string]bool{audit.Admin: AuditClear}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func argsFor(schema any) map[string]any {
	b, _ := json.Marshal(schema)
	var sc struct {
		Required   []string
		Properties map[string]struct{ Type any }
	}
	json.Unmarshal(b, &sc)
	out := map[string]any{}
	for _, k := range sc.Required {
		out[k] = valueFor(sc.Properties[k].Type)
	}
	return out
}

// Every registered admin tool leaves called and done rows (backends here
// answer 500, the login server is the fake).
func TestEveryAdminToolIsAudited(t *testing.T) {
	_, read, write := newFake(t)
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer down.Close()
	st := auditStore(t)
	at := writeTools(read, write)
	at.Audit = st
	at.Grafana = &Grafana{Base: down.URL, HTTP: down.Client(), Token: func(context.Context) (string, error) { return "t", nil }}
	at.Mail = &Stalwart{Base: down.URL, Key: "k", HTTP: down.Client()}
	at.MailWrite = &Stalwart{Base: down.URL, Key: "k", HTTP: down.Client(), Write: true}
	at.MailGroupCreate = true
	at.Files = &Files{Base: down.URL, HTTP: down.Client(), Token: func(context.Context) (string, error) { return "t", nil }}
	at.CertHosts = []string{"127.0.0.1:1"}
	cs, _ := wsession(t, at, no)
	r, err := cs.ListTools(context.Background(), nil)
	if err != nil || len(r.Tools) < 30 {
		t.Fatalf("%d tools, %v", len(r.Tools), err)
	}
	for _, tool := range r.Tools {
		if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool.Name, Arguments: argsFor(tool.InputSchema)}); err != nil {
			t.Errorf("%s: %v", tool.Name, err)
		}
		evs, _ := st.Search(context.Background(), audit.Filter{Tool: tool.Name, Connector: audit.Admin})
		seen := map[string]bool{}
		for _, e := range evs {
			seen[e.Event] = true
			if e.Username != "boss" {
				t.Errorf("%s: who %q", tool.Name, e.Username)
			}
		}
		if !seen["called"] || !seen["done"] {
			t.Errorf("%s: events %v", tool.Name, seen)
		}
	}
}

// An admin change whose record can't be written isn't made: neither when
// the call can't be recorded nor when the person's yes can't.
func TestAdminChangeFailsClosed(t *testing.T) {
	for _, failing := range []string{"called", "answered"} {
		f, read, write := newFake(t)
		st := auditStore(t)
		at := writeTools(read, write)
		at.Audit = st
		at.Ask = &confirm.Asker{}
		cs, asked := wsession(t, at, yes)
		audit.FailForTest(st, failing)
		txt, isErr := call(t, cs, "admin_group_add", map[string]any{"username": "hugo", "groups": []string{"developers"}})
		if !isErr || len(f.changed()) != 0 {
			t.Errorf("%s failing: %q, changes %v", failing, txt, f.changed())
		}
		if failing == "called" && len(*asked) != 0 {
			t.Error("asked though the call couldn't be recorded")
		}
	}
	// A read still works.
	_, read, _ := newFake(t)
	st := auditStore(t)
	audit.FailForTest(st, "called")
	cs, _ := wsession(t, &Tools{API: read, Audit: st}, nil)
	if txt, isErr := call(t, cs, "admin_user", map[string]any{"username": "hugo"}); isErr {
		t.Errorf("read refused: %s", txt)
	}
}

// valueFor: a value of a schema type ("array", or ["null","array"]).
func valueFor(typ any) any {
	t, _ := typ.(string)
	if l, ok := typ.([]any); ok {
		for _, x := range l {
			if x != "null" {
				t, _ = x.(string)
			}
		}
	}
	switch t {
	case "string":
		return "x"
	case "integer", "number":
		return 1
	case "boolean":
		return false
	case "array":
		return []any{}
	}
	return map[string]any{}
}
