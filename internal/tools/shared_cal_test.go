package tools

import (
	"context"
	"encoding/json"
	"github.com/igorski89/sgnl-mcp/internal/jmap"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSharedCalendars(t *testing.T) {
	asPerson(t, "me@co.example")
	ev := func(title string) map[string]any {
		return map[string]any{"id": "x", "title": title, "start": "2026-12-03T10:00:00", "timeZone": "Europe/Berlin", "duration": "PT1H"}
	}
	f := &fakeCal{events: map[string]map[string]any{}, shared: map[string]map[string]map[string]any{
		"S": {"a1": ev("Anna's dentist")}, "R": {"b1": ev("Board meeting")}}}
	var asked []string
	answer := "accept"
	cs := inviteSession(t, f, func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = append(asked, r.Params.Message)
		return &mcp.ElicitResult{Action: answer, Content: map[string]any{}}, nil
	})
	txt, isErr := callTool(t, cs, "calendar_list", nil)
	if isErr || !strings.Contains(txt, `{"account":"me@co.example","can_add":true,"id":"c1","name":"Mine","owner":"me@co.example","yours":true}`) ||
		!strings.Contains(txt, `{"account":"anna@co.example","can_add":true,"id":"s1","name":"Anna","owner":"anna@co.example"}`) ||
		!strings.Contains(txt, `{"account":"board@co.example","id":"r1","name":"Board","owner":"board@co.example","read_only":true}`) ||
		strings.Contains(txt, "Anna private") {
		t.Errorf("calendar_list: %s", txt)
	}
	txt, isErr = callTool(t, cs, "event_list", map[string]any{"account": "Board@co.example", "from": "2026-12-01", "to": "2026-12-05"})
	if isErr || !strings.Contains(txt, "Board meeting") || strings.Contains(txt, "dentist") {
		t.Errorf("event_list shared: %s", txt)
	}
	if txt, isErr := callTool(t, cs, "event_list", map[string]any{"account": "nobody@co.example"}); !isErr || !strings.Contains(txt, "anna@co.example, board@co.example") {
		t.Errorf("unknown account: %s", txt)
	}
	// Adding to anna's calendar: asked first, into her calendar the person may add to.
	txt, isErr = callTool(t, cs, "event_create", map[string]any{"account": "anna@co.example", "title": "Lunch", "start": "2026-12-04T12:00"})
	if isErr || len(asked) != 1 || !strings.HasPrefix(asked[0], `Add "Lunch" on 2026-12-04 12:00 to anna@co.example's calendar "Anna"? Everyone who can see that calendar will see it.`) {
		t.Fatalf("create shared: %s / %v", txt, asked)
	}
	b, _ := json.Marshal(f.sets[len(f.sets)-1])
	if !strings.Contains(string(b), `"accountId":"S"`) || !strings.Contains(string(b), `"calendarIds":{"s1":true}`) || strings.Contains(string(b), "participants") {
		t.Errorf("set: %s", b)
	}
	// Read-only: refused without asking. Declined: nothing added. Own calendar: no question.
	n := len(f.sets)
	if txt, isErr := callTool(t, cs, "event_create", map[string]any{"account": "board@co.example", "title": "X", "start": "2026-12-04"}); !isErr || !strings.Contains(txt, "only read") {
		t.Errorf("read-only: %s", txt)
	}
	answer = "decline"
	if txt, isErr := callTool(t, cs, "event_create", map[string]any{"account": "anna@co.example", "title": "X", "start": "2026-12-04"}); !isErr || !strings.Contains(txt, "declined") {
		t.Errorf("declined: %s", txt)
	}
	if len(f.sets) != n {
		t.Error("added without a yes")
	}
	na := len(asked)
	if _, isErr := callTool(t, cs, "event_create", map[string]any{"title": "Focus", "start": "2026-12-04T09:00"}); isErr || len(asked) != na {
		t.Error("own calendar asked or failed")
	}
}

func TestAvailability(t *testing.T) {
	f := &fakeCal{events: map[string]map[string]any{}}
	cs := inviteSession(t, f, nil)
	txt, isErr := callTool(t, cs, "availability_get", map[string]any{"people": []string{"hugo", "anna"}, "from": "2026-12-03T08:00", "to": "2026-12-03T12:30"})
	if isErr {
		t.Fatal(txt)
	}
	// Local times; hugo's busy block though his account isn't "shared" (a
	// free/busy-only share), asked in the person's own account; anna: not allowed.
	if !strings.Contains(txt, `"busy":[{"end":"2026-12-03T11:00","start":"2026-12-03T10:00","status":"confirmed"}]`) ||
		!strings.Contains(txt, `"times_in":"Europe/Berlin"`) || !strings.Contains(txt, `"error":"the mail server doesn't let you see their availability"`) {
		t.Errorf("availability: %s", txt)
	}
	if len(f.avail) != 1 || f.avail[0]["accountId"] != "A" || f.avail[0]["id"] != "hugo" ||
		f.avail[0]["utcStart"] != "2026-12-03T07:00:00Z" || f.avail[0]["utcEnd"] != "2026-12-03T11:30:00Z" {
		t.Errorf("asked: %v", f.avail)
	}
	// A whole last day.
	if _, isErr := callTool(t, cs, "availability_get", map[string]any{"people": []string{"hugo"}, "from": "2026-12-03", "to": "2026-12-03"}); isErr || f.avail[1]["utcEnd"] != "2026-12-03T23:00:00Z" {
		t.Errorf("day: %v", f.avail[1])
	}
	if txt, isErr := callTool(t, cs, "availability_get", map[string]any{"people": []string{"hugo"}, "from": "3 Dec"}); !isErr || !strings.Contains(txt, "YYYY-MM-DDTHH:MM") {
		t.Errorf("bad from: %s", txt)
	}
}

func TestAvailabilityNote(t *testing.T) {
	for _, shared := range []bool{false, true} {
		f := &fakeCal{events: map[string]map[string]any{}}
		srv := f.serve(t)
		s := mcp.NewServer(&mcp.Implementation{Name: "t"}, nil)
		(&Tools{Auth: fakeTokens{}, JMAP: jmap.New(srv.URL + "/session"), Access: Access{All: true}, TZ: "Europe/Berlin", FreeBusyShared: shared}).Register(s)
		ct, st := mcp.NewInMemoryTransports()
		if _, err := s.Connect(context.Background(), st, nil); err != nil {
			t.Fatal(err)
		}
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "c"}, nil).Connect(context.Background(), ct, nil)
		if err != nil {
			t.Fatal(err)
		}
		txt, _ := callTool(t, cs, "availability_get", map[string]any{"people": []string{"hugo"}, "from": "2026-12-03"})
		want := map[bool]string{false: "or that they share no calendar with you", true: "An empty list means free: colleagues share"}[shared]
		if !strings.Contains(txt, want) {
			t.Errorf("shared=%v: %s", shared, txt)
		}
		cs.Close()
	}
}

func TestSharedCalendarChanges(t *testing.T) {
	asPerson(t, "me@co.example")
	mk := func(title, cal string, parts map[string]any) map[string]any {
		e := map[string]any{"id": "x", "title": title, "start": "2027-03-04T10:00:00", "timeZone": "Europe/Berlin", "duration": "PT1H",
			"calendarIds": map[string]any{cal: true}}
		if parts != nil {
			e["participants"] = parts
			for _, p := range parts {
				if p.(map[string]any)["roles"].(map[string]any)["owner"] == true {
					e["organizerCalendarAddress"] = p.(map[string]any)["calendarAddress"]
				}
			}
		}
		return e
	}
	person := func(addr string, owner bool) map[string]any {
		r := map[string]any{"attendee": true}
		if owner {
			r = map[string]any{"owner": true, "attendee": true}
		}
		return map[string]any{"calendarAddress": "mailto:" + addr, "roles": r}
	}
	f := &fakeCal{events: map[string]map[string]any{}, shared: map[string]map[string]map[string]any{"S": {
		"a1": mk("Dentist", "s1", nil),
		"p2": mk("Offsite prep", "s3", nil),
		"t1": mk("Team sync", "s3", map[string]any{"o": person("anna@co.example", true), "b": person("bob@co.example", false)}),
		"x1": mk("Bob's review", "s3", map[string]any{"o": person("bob@co.example", true), "a": person("anna@co.example", false)}),
	}}}
	for id, e := range f.shared["S"] {
		e["id"] = id
	}
	var asked []string
	cs := inviteSession(t, f, func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = append(asked, r.Params.Message)
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{}}, nil
	})
	// Only "add" rights on that calendar: refused, nothing asked.
	if txt, isErr := callTool(t, cs, "event_update", map[string]any{"account": "anna@co.example", "id": "a1", "title": "X"}); !isErr || !strings.Contains(txt, "reading or adding only") {
		t.Errorf("writeOwn calendar: %s", txt)
	}
	// A private entry in a calendar the person may change: asked, never mailed.
	if txt, isErr := callTool(t, cs, "event_update", map[string]any{"account": "anna@co.example", "id": "p2", "title": "Offsite"}); isErr {
		t.Fatalf("private shared: %s", txt)
	}
	if len(asked) != 1 || !strings.HasPrefix(asked[0], `Change "Offsite prep" in anna@co.example's calendar "Anna team": title → "Offsite"? Everyone who can see that calendar will see it.`) {
		t.Errorf("question: %v", asked)
	}
	b, _ := json.Marshal(f.sets[len(f.sets)-1])
	if !strings.Contains(string(b), `"accountId":"S"`) || strings.Contains(string(b), "sendSchedulingMessages") {
		t.Errorf("private set: %s", b)
	}
	// A meeting the owner organizes: as the owner, attendees get the cancellation.
	if txt, isErr := callTool(t, cs, "event_cancel", map[string]any{"account": "anna@co.example", "id": "t1"}); isErr {
		t.Fatalf("cancel: %s", txt)
	}
	if q := asked[len(asked)-1]; !strings.HasPrefix(q, `In anna@co.example's calendar "Anna team", as anna@co.example: cancel the meeting "Team sync"`) ||
		!strings.Contains(q, "Attendee: bob@co.example") || !strings.Contains(q, "goes out in anna@co.example's name") {
		t.Errorf("cancel question: %q", q)
	}
	b, _ = json.Marshal(f.sets[len(f.sets)-1])
	if !strings.Contains(string(b), `"accountId":"S"`) || !strings.Contains(string(b), `"destroy":["t1"]`) || !strings.Contains(string(b), `"sendSchedulingMessages":true`) {
		t.Errorf("cancel set: %s", b)
	}
	// The owner is only an attendee: refused.
	n := len(f.sets)
	if txt, isErr := callTool(t, cs, "event_cancel", map[string]any{"account": "anna@co.example", "id": "x1"}); !isErr || !strings.Contains(txt, "doesn't organize") {
		t.Errorf("attendee owner: %s", txt)
	}
	if len(f.sets) != n {
		t.Error("changed without being allowed")
	}
}
