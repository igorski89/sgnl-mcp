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

type fakeCal struct {
	mu          sync.Mutex
	sets        []map[string]any
	unscheduled bool // the mail server made no one organizer (no invitations)
}

func (f *fakeCal) serve(t *testing.T) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			_ = json.NewEncoder(w).Encode(map[string]any{"apiUrl": srv.URL + "/api", "primaryAccounts": map[string]string{jmap.Calendars: "A"}})
			return
		}
		var req struct{ MethodCalls [][]json.RawMessage }
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		defer f.mu.Unlock()
		var out [][]any
		for _, mc := range req.MethodCalls {
			var name, id string
			var args map[string]any
			_ = json.Unmarshal(mc[0], &name)
			_ = json.Unmarshal(mc[1], &args)
			_ = json.Unmarshal(mc[2], &id)
			var res any
			switch name {
			case "Calendar/get":
				res = map[string]any{"list": []map[string]any{{"id": "c1", "isDefault": true}}}
			case "CalendarEvent/set":
				f.sets = append(f.sets, args)
				e := map[string]any{"id": "ev1"}
				if !f.unscheduled {
					e["participants"] = map[string]any{"o": map[string]any{"roles": map[string]bool{"owner": true}}}
				}
				res = map[string]any{"created": map[string]any{"e": e}}
			}
			out = append(out, []any{name, res, id})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"methodResponses": out})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func inviteSession(t *testing.T, f *fakeCal, h elicitFn) *mcp.ClientSession {
	t.Helper()
	srv := f.serve(t)
	s := mcp.NewServer(&mcp.Implementation{Name: "t"}, nil)
	(&Tools{Auth: fakeTokens{}, JMAP: jmap.New(srv.URL + "/session"), Access: Access{All: true}, TZ: "Europe/Berlin",
		InternalDomains: []string{"co.example"}}).Register(s)
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

func invite(t *testing.T, cs *mcp.ClientSession, attendees ...string) *mcp.CallToolResult {
	t.Helper()
	r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "event_invite", Arguments: map[string]any{
		"title": "Q4 review", "start": "2026-12-03T10:00", "minutes": 30, "attendees": attendees}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestInviteOnlyWithThePersonsYes(t *testing.T) {
	f := &fakeCal{}
	var asked string
	cs := inviteSession(t, f, func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = r.Params.Message
		return &mcp.ElicitResult{Action: "decline"}, nil
	})
	if r := invite(t, cs, "hugo@co.example", "Partner <p@other.example>"); !r.IsError || len(f.sets) != 0 {
		t.Fatalf("declined, yet: %q, %d sets", resultText(r), len(f.sets))
	}
	first, _, _ := strings.Cut(asked, "\n")
	for _, want := range []string{"hugo@co.example", "p@other.example", `"Q4 review"`, "Thu 3 Dec 2026, 10:00", "1 OUTSIDE"} {
		if !strings.Contains(first, want) {
			t.Errorf("first line lacks %q: %q", want, first)
		}
	}

	f = &fakeCal{}
	cs = inviteSession(t, f, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "accept"}, nil
	})
	if r := invite(t, cs, "hugo@co.example"); r.IsError || len(f.sets) != 1 {
		t.Fatalf("accepted: %q, %d sets", resultText(r), len(f.sets))
	}
	a := f.sets[0]
	if a["sendSchedulingMessages"] != true {
		t.Errorf("invitations not requested: %v", a)
	}
	ev := a["create"].(map[string]any)["e"].(map[string]any)
	ps := ev["participants"].(map[string]any)
	if p := ps["a0"].(map[string]any); len(ps) != 1 || p["email"] != "hugo@co.example" ||
		p["calendarAddress"] != "mailto:hugo@co.example" || ev["calendarIds"] == nil {
		t.Errorf("event: %v", ev)
	}
}

// Live test 2026-11-25: Stalwart created the event but scheduled nothing,
// and the tool said "handed to the mail server". Now no organizer in the
// answer means a failure the person hears about.
func TestInviteNotScheduledIsAnError(t *testing.T) {
	f := &fakeCal{unscheduled: true}
	cs := inviteSession(t, f, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "accept"}, nil
	})
	r := invite(t, cs, "hugo@co.example")
	if !r.IsError || !strings.Contains(resultText(r), "did not schedule") {
		t.Fatalf("unscheduled invite reported as: %q", resultText(r))
	}
}

func TestInviteRefusals(t *testing.T) {
	f := &fakeCal{}
	cs := inviteSession(t, f, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		t.Error("asked, though it should have been refused first")
		return &mcp.ElicitResult{Action: "accept"}, nil
	})
	for name, att := range map[string][]string{"none": nil, "not an address": {"hugo"}} {
		if r := invite(t, cs, att...); !r.IsError {
			t.Errorf("%s: not refused", name)
		}
	}
	many := make([]string, maxAttendees+1)
	for i := range many {
		many[i] = strings.Repeat("a", i+1) + "@co.example"
	}
	if r := invite(t, cs, many...); !r.IsError || len(f.sets) != 0 {
		t.Error("too many: not refused")
	}
}
