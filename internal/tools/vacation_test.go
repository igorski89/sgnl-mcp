package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/igorski89/sgnl-mcp/internal/jmap"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeVac struct {
	mu   sync.Mutex
	sets []map[string]any
}

func (f *fakeVac) session(t *testing.T, h elicitFn) *mcp.ClientSession {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			json.NewEncoder(w).Encode(map[string]any{"apiUrl": srv.URL + "/api", "primaryAccounts": map[string]string{jmap.Mail: "A", jmap.Vacation: "A"}})
			return
		}
		var req struct{ MethodCalls [][]json.RawMessage }
		json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		defer f.mu.Unlock()
		var out [][]any
		for _, mc := range req.MethodCalls {
			var name, id string
			var args map[string]any
			json.Unmarshal(mc[0], &name)
			json.Unmarshal(mc[1], &args)
			json.Unmarshal(mc[2], &id)
			res := map[string]any{"updated": map[string]any{"singleton": nil}}
			if name == "VacationResponse/get" {
				res = map[string]any{"list": []any{map[string]any{"id": "singleton", "isEnabled": true, "fromDate": "2026-12-01T00:00:00Z", "toDate": "2026-12-05T22:59:59Z", "textBody": "away"}}}
			} else {
				f.sets = append(f.sets, args)
			}
			out = append(out, []any{name, res, id})
		}
		json.NewEncoder(w).Encode(map[string]any{"methodResponses": out})
	}))
	t.Cleanup(srv.Close)
	s := mcp.NewServer(&mcp.Implementation{Name: "t"}, nil)
	(&Tools{Auth: fakeTokens{}, JMAP: jmap.New(srv.URL + "/session"), Access: Access{All: true}, TZ: "Europe/Berlin"}).Register(s)
	ct, st := mcp.NewInMemoryTransports()
	if _, err := s.Connect(context.Background(), st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c"}, &mcp.ClientOptions{ElicitationHandler: h}).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func TestOutOfOffice(t *testing.T) {
	f := &fakeVac{}
	var asked []string
	act := "decline"
	cs := f.session(t, func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = append(asked, r.Params.Message)
		return &mcp.ElicitResult{Action: act}, nil
	})
	call := func(name string, args map[string]any) *mcp.CallToolResult {
		r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := call("out_of_office_get", map[string]any{})
	if b, _ := json.Marshal(r.StructuredContent); r.IsError || !strings.Contains(string(b), `"to":"2026-12-05"`) {
		t.Fatalf("get: %s", b)
	}
	on := map[string]any{"on": true, "from": "2099-12-01", "to": "2099-12-05", "subject": "Away", "text": "I'm away until Monday.\nAnna covers for me."}
	if r := call("out_of_office_set", on); !r.IsError || len(f.sets) != 0 {
		t.Fatalf("declined, yet set: %v", f.sets)
	}
	first, rest, _ := strings.Cut(asked[0], "\n")
	if !strings.Contains(first, "from Tue 1 Dec 2099 to Sat 5 Dec 2099") || !strings.Contains(first, "OUTSIDE") || !strings.Contains(rest, "Anna covers for me.") {
		t.Errorf("question: %q", asked[0])
	}
	act = "accept"
	if r := call("out_of_office_set", on); r.IsError || len(f.sets) != 1 {
		t.Fatalf("accepted: %q", resultText(r))
	}
	p := f.sets[0]["update"].(map[string]any)["singleton"].(map[string]any)
	if p["isEnabled"] != true || p["htmlBody"] != nil || p["toDate"] != "2099-12-05T22:59:59Z" || p["fromDate"] != "2099-11-30T23:00:00Z" {
		t.Fatalf("patch: %v", p)
	}
	// No end date is said on the first line.
	delete(on, "to")
	call("out_of_office_set", on)
	if first, _, _ := strings.Cut(asked[len(asked)-1], "\n"); !strings.Contains(first, "NO end date") {
		t.Errorf("no end date not said: %q", first)
	}
	// Off: no question.
	n := len(asked)
	if r := call("out_of_office_set", map[string]any{"on": false}); r.IsError || len(asked) != n {
		t.Fatalf("off: %q, asked %d", resultText(r), len(asked)-n)
	}
	for name, args := range map[string]map[string]any{
		"no text":   {"on": true},
		"too long":  {"on": true, "text": strings.Repeat("x", maxVacationText+1)},
		"past":      {"on": true, "from": "2001-01-01", "to": "2001-01-02", "text": "x"},
		"backwards": {"on": true, "from": "2099-12-05", "to": "2099-12-01", "text": "x"},
	} {
		if r := call("out_of_office_set", args); !r.IsError {
			t.Errorf("%s: not refused", name)
		}
	}
}
