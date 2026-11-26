package tools

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorski89/sgnl-mcp/internal/jmap"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeCal struct {
	mu          sync.Mutex
	sets        []map[string]any
	unscheduled bool // the mail server made no one organizer (no invitations)
	events      map[string]map[string]any
	mails       []any          // Email/get answer
	parsed      map[string]any // CalendarEvent/parse answer
	queries     int            // CalendarEvent/query calls
	// shared: other calendar accounts the session lists (S: anna's, may
	// add; R: board's, read only), with their events.
	shared map[string]map[string]map[string]any
	// avail: Principal/getAvailability calls (their args)
	avail []map[string]any
}

func (f *fakeCal) serve(t *testing.T) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			cal := map[string]any{jmap.Calendars: map[string]any{}}
			accts := map[string]any{"A": map[string]any{"name": "me@co.example", "isPersonal": true, "accountCapabilities": cal}}
			if f.shared != nil {
				accts["S"] = map[string]any{"name": "anna@co.example", "accountCapabilities": cal}
				accts["R"] = map[string]any{"name": "board@co.example", "accountCapabilities": cal}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"apiUrl": srv.URL + "/api", "primaryAccounts": map[string]string{jmap.Calendars: "A", jmap.Mail: "A"},
				"accounts": accts})
			return
		}
		var req struct{ MethodCalls [][]json.RawMessage }
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		defer f.mu.Unlock()
		var out [][]any
		var lastIDs []any
		for _, mc := range req.MethodCalls {
			var name, id string
			var args map[string]any
			_ = json.Unmarshal(mc[0], &name)
			_ = json.Unmarshal(mc[1], &args)
			_ = json.Unmarshal(mc[2], &id)
			var res any
			events := f.events
			if a, _ := args["accountId"].(string); a != "A" && f.shared != nil {
				events = f.shared[a]
			}
			switch name {
			case "CalendarEvent/query":
				// As Stalwart 1.1: a window finds an event only by its first
				// start; expandRecurrences is ignored.
				f.queries++
				ids := []any{}
				flt, _ := args["filter"].(map[string]any)
				for _, k := range slices.Sorted(maps.Keys(events)) {
					st, _ := events[k]["start"].(string)
					tz, _ := events[k]["timeZone"].(string)
					if tz == "" {
						tz = "Europe/Berlin"
					}
					loc, _ := time.LoadLocation(tz)
					at, _ := time.ParseInLocation("2006-01-02T15:04:05", st, loc)
					if a, ok := flt["after"].(string); ok {
						if t0, _ := time.Parse(time.RFC3339, a); at.Before(t0) {
							continue
						}
					}
					if b, ok := flt["before"].(string); ok {
						if t1, _ := time.Parse(time.RFC3339, b); !at.Before(t1) {
							continue
						}
					}
					ids = append(ids, k)
				}
				lastIDs = ids
				res = map[string]any{"ids": ids}
			case "CalendarEvent/get":
				list := []any{}
				ids, _ := args["ids"].([]any)
				if _, ok := args["#ids"]; ok {
					ids = lastIDs
				}
				for _, id := range ids {
					if e, ok := events[id.(string)]; ok {
						if _, ok := args["properties"]; ok {
							// As Stalwart: a properties list drops the rules.
							e = maps.Clone(e)
							delete(e, "recurrenceRules")
							delete(e, "recurrenceOverrides")
						}
						list = append(list, e)
					}
				}
				res = map[string]any{"list": list, "state": "s1"}
			case "Principal/query":
				q, _ := args["filter"].(map[string]any)["text"].(string)
				ids := []string{}
				for _, p := range []string{"hugo", "anna"} {
					if strings.Contains(p, strings.ToLower(q)) {
						ids = append(ids, p)
					}
				}
				lastIDs = nil
				for _, i := range ids {
					lastIDs = append(lastIDs, i)
				}
				res = map[string]any{"ids": ids}
			case "Principal/get":
				list := []any{}
				for _, i := range lastIDs {
					id := i.(string)
					// As Stalwart: no accountId for a free/busy-only share.
					cal := map[string]any{"accountId": nil, "mayGetAvailability": id != "anna"}
					list = append(list, map[string]any{"id": id, "name": id + "@co.example", "email": id + "@co.example", "type": "individual",
						"capabilities": map[string]any{jmap.Calendars: cal}})
				}
				res = map[string]any{"list": list}
			case "Principal/getAvailability":
				f.avail = append(f.avail, args)
				res = map[string]any{"list": []any{map[string]any{"utcStart": "2026-12-03T09:00:00Z", "utcEnd": "2026-12-03T10:00:00Z", "busyStatus": "confirmed"}}}
			case "Email/get":
				res = map[string]any{"list": f.mails}
			case "CalendarEvent/parse":
				res = map[string]any{"parsed": f.parsed}
			case "Calendar/get":
				res = map[string]any{"list": []map[string]any{{"id": "c1", "name": "Mine", "isDefault": true}}}
				switch args["accountId"] {
				case "S":
					res = map[string]any{"list": []map[string]any{
						{"id": "s1", "name": "Anna", "isDefault": true, "myRights": map[string]bool{"mayReadItems": true, "mayWriteOwn": true}},
						{"id": "s2", "name": "Anna private", "myRights": map[string]bool{"mayReadFreeBusy": true}},
						{"id": "s3", "name": "Anna team", "myRights": map[string]bool{"mayReadItems": true, "mayWriteAll": true}}}}
				case "R":
					res = map[string]any{"list": []map[string]any{{"id": "r1", "name": "Board", "myRights": map[string]bool{"mayReadItems": true}}}}
				}
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
