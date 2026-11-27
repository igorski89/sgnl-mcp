package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/igorski89/sgnl-mcp/internal/admin"
	"github.com/igorski89/sgnl-mcp/internal/audit"
	"github.com/igorski89/sgnl-mcp/internal/jmap"
	"github.com/igorski89/sgnl-mcp/internal/listmonk"
	"github.com/igorski89/sgnl-mcp/internal/opencloud"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func auditStore(t *testing.T, user audit.Level) *audit.Store {
	t.Helper()
	s, err := audit.OpenForTest(audit.Config{Path: filepath.Join(t.TempDir(), "audit.db"), Admin: audit.Full, User: user,
		Clear: map[string]map[string]bool{audit.User: AuditClear, audit.Admin: admin.AuditClear}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// argsFor: arguments valid for a tool's input schema (required fields).
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

func connect(t *testing.T, s *mcp.Server) *mcp.ClientSession {
	ct, st := mcp.NewInMemoryTransports()
	if _, err := s.Connect(context.Background(), st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// Every registered user tool leaves called and done rows, whatever happens
// in it (here: every backend answers 500). (The admin connector: its own
// test, as an authenticated admin.)
func TestEveryUserToolIsAudited(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer down.Close()
	st := auditStore(t, audit.Meta)
	ac := &admin.Client{Base: down.URL, TokenURL: down.URL + "/token", ID: "c", Secret: "s", HTTP: down.Client()}
	mk := func() *mcp.ClientSession {
		s := mcp.NewServer(&mcp.Implementation{Name: "u"}, nil)
		(&Tools{Auth: fakeTokens{}, JMAP: jmap.New(down.URL + "/session"), Files: opencloud.New(down.URL), News: listmonk.New(down.URL), Access: Access{All: true},
			Audit: st, Self: admin.SelfForTest(ac, ac, nil)}).Register(s)
		return connect(t, s)
	}
	r, _ := mk().ListTools(context.Background(), nil)
	if len(r.Tools) < 40 {
		t.Fatalf("%d tools", len(r.Tools))
	}
	for _, tool := range r.Tools {
		cs := mk() // a fresh rate limit for each
		if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool.Name, Arguments: argsFor(tool.InputSchema)}); err != nil {
			t.Errorf("%s: %v", tool.Name, err)
		}
		evs, _ := st.Search(context.Background(), audit.Filter{Tool: tool.Name, Connector: audit.User})
		seen := map[string]bool{}
		for _, e := range evs {
			seen[e.Event] = true
		}
		if !seen["called"] || !seen["done"] {
			t.Errorf("%s: events %v", tool.Name, seen)
		}
	}
}

// Every argument field of every user tool is either stored in clear or
// deliberately hashed at the meta level.
func TestAuditFieldsAllConsidered(t *testing.T) {
	s := mcp.NewServer(&mcp.Implementation{Name: "u"}, nil)
	(&Tools{Auth: fakeTokens{}, Files: opencloud.New("http://x"), News: listmonk.New("http://x"), Access: Access{All: true}, Experiments: map[string]bool{"aud_check": true, "confirm_test": true},
		Self: admin.SelfForTest(&admin.Client{}, &admin.Client{}, nil)}).Register(s)
	r, _ := connect(t, s).ListTools(context.Background(), nil)
	for _, tool := range r.Tools {
		b, _ := json.Marshal(tool.InputSchema)
		var sc struct{ Properties map[string]any }
		json.Unmarshal(b, &sc)
		for k := range sc.Properties {
			if !AuditClear[k] && !AuditClear[tool.Name+"."+k] && !AuditHashed[k] && !AuditHashed[tool.Name+"."+k] {
				t.Errorf("%s.%s: neither stored in clear nor hashed (audit_fields.go)", tool.Name, k)
			}
		}
	}
}

// A whole draft at the meta level: nothing of the mail is in the file.
func TestAuditMetaStoresNoContent(t *testing.T) {
	asPerson(t, "igor@co.example")
	st := auditStore(t, audit.Meta)
	f := newFakeMail()
	cs := sendSessionWith(t, f, nil, false, func(tl *Tools) { tl.Audit = st })
	if txt, isErr := callTool(t, cs, "mail_draft_create", map[string]any{"to": []string{"MARK-RCPT@x.example"}, "subject": "MARK-SUBJECT", "body": "MARK-BODY"}); isErr {
		t.Fatal(txt)
	}
	evs, _ := st.Search(context.Background(), audit.Filter{Tool: "mail_draft_create"})
	if len(evs) < 2 {
		t.Fatalf("events: %v", evs)
	}
	dir := filepath.Dir(audit.PathForTest(st))
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		for _, m := range []string{"MARK-RCPT", "MARK-SUBJECT", "MARK-BODY", "Bearer"} {
			if strings.Contains(string(b), m) {
				t.Errorf("%s found in %s", m, e.Name())
			}
		}
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

// A send: the question and the answer, linked across the two invocations;
// at the meta level the question (which quotes the mail) only as a hash.
func TestAuditDialogLinked(t *testing.T) {
	st := auditStore(t, audit.Meta)
	f := newFakeMail()
	cs := sendSessionWith(t, f, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{}}, nil
	}, false, func(tl *Tools) { tl.Audit = st })
	if r := send(t, cs); r.IsError {
		t.Fatal(resultText(r))
	}
	evs, _ := st.Search(context.Background(), audit.Filter{Tool: "mail_send"})
	var asked, answered *audit.Event
	for i, e := range evs {
		switch e.Event {
		case "asked":
			asked = &evs[i]
		case "answered":
			answered = &evs[i]
		}
	}
	if asked == nil || answered == nil || asked.Dialog == "" || asked.Dialog != answered.Dialog {
		t.Fatalf("asked %+v answered %+v", asked, answered)
	}
	b, _ := json.Marshal(asked.Data)
	if strings.Contains(string(b), "Offer") || !strings.Contains(string(b), "sha256") {
		t.Errorf("question stored: %s", b)
	}
	if b, _ := json.Marshal(answered.Data); !strings.Contains(string(b), `"outcome":"accept"`) {
		t.Errorf("answer: %s", b)
	}
}
