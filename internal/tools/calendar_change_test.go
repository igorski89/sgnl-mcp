package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/igorski89/sgnl-mcp/internal/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func asPerson(t *testing.T, email string) {
	t.Helper()
	old := personOf
	personOf = func(context.Context) (auth.Claims, error) { return auth.Claims{Email: email, Username: "me"}, nil }
	t.Cleanup(func() { personOf = old })
}

func future(e map[string]any) map[string]any {
	c := map[string]any{}
	for k, v := range e {
		c[k] = v
	}
	delete(c, "recurrenceRules")
	c["start"] = "2099-12-03T10:00:00"
	return c
}

func calCall(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRespondOnlyAsAttendeeWithYes(t *testing.T) {
	asPerson(t, "me@co.example")
	private := map[string]any{"id": "p1", "title": "focus", "start": "2099-12-03T10:00:00", "duration": "PT60M"}
	past := future(meeting)
	past["start"] = "2001-01-01T10:00:00"
	f := &fakeCal{events: map[string]map[string]any{"e1": future(meeting), "r1": meeting, "p1": private, "old": past}}
	var asked string
	cs := inviteSession(t, f, func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = r.Params.Message
		return &mcp.ElicitResult{Action: "accept"}, nil
	})
	for id, why := range map[string]string{"p1": "not invited", "old": "over"} {
		if r := calCall(t, cs, "event_respond", map[string]any{"id": id, "answer": "decline"}); !r.IsError {
			t.Errorf("%s (%s): not refused", id, why)
		}
	}
	// One meeting of a series: the mail server doesn't allow it.
	if r := calCall(t, cs, "event_respond", map[string]any{"id": "r1", "answer": "decline", "occurrence": "2026-12-10T10:00:00"}); !r.IsError ||
		!strings.Contains(resultText(r), "single meetings of a series") {
		t.Errorf("occurrence answer: %q", resultText(r))
	}
	if r := calCall(t, cs, "event_respond", map[string]any{"id": "e1", "answer": "fine"}); !r.IsError {
		t.Error("bad answer not refused")
	}
	if len(f.sets) != 0 || asked != "" {
		t.Fatalf("refusals reached the server or the person: %v %q", f.sets, asked)
	}
	if r := calCall(t, cs, "event_respond", map[string]any{"id": "e1", "answer": "decline"}); r.IsError {
		t.Fatal(resultText(r))
	}
	first, _, _ := strings.Cut(asked, "\n")
	if !strings.Contains(first, `Answer DECLINE to "Q4 review"`) || !strings.Contains(first, "anna@co.example gets your answer") {
		t.Errorf("first line: %q", first)
	}
	a := f.sets[0]
	up := a["update"].(map[string]any)["e1"].(map[string]any)
	if a["sendSchedulingMessages"] != true || len(up) != 1 || up["participants/b/participationStatus"] != "declined" {
		t.Fatalf("set: %v", a)
	}
}

func TestUpdateAndCancel(t *testing.T) {
	asPerson(t, "anna@co.example") // the organizer of meeting
	private := map[string]any{"id": "p1", "title": "focus", "start": "2099-12-03T10:00:00", "timeZone": "Europe/Berlin", "duration": "PT60M"}
	f := &fakeCal{events: map[string]map[string]any{"e1": future(meeting), "p1": private}}
	var asked []string
	answer := "decline"
	cs := inviteSession(t, f, func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = append(asked, r.Params.Message)
		return &mcp.ElicitResult{Action: answer}, nil
	})
	// A private event changes without a question and without mail.
	if r := calCall(t, cs, "event_update", map[string]any{"id": "p1", "start": "2099-12-04T09:00"}); r.IsError || len(asked) != 0 {
		t.Fatalf("private update: %q, asked %v", resultText(r), asked)
	}
	if a := f.sets[0]; a["sendSchedulingMessages"] != nil || a["update"].(map[string]any)["p1"].(map[string]any)["start"] != "2099-12-04T09:00:00" {
		t.Fatalf("private update set: %v", a)
	}
	// A meeting: asked, and declined means nothing changes.
	if r := calCall(t, cs, "event_update", map[string]any{"id": "e1", "start": "2099-12-03T11:00"}); !r.IsError || len(f.sets) != 1 {
		t.Fatalf("declined update: %q", resultText(r))
	}
	if first, _, _ := strings.Cut(asked[0], "\n"); !strings.Contains(first, `Change the meeting "Q4 review"`) || !strings.Contains(first, "1 OUTSIDE") || !strings.Contains(first, "11:00") {
		t.Errorf("update first line: %q", first)
	}
	answer = "accept"
	if r := calCall(t, cs, "event_update", map[string]any{"id": "e1", "start": "2099-12-03T11:00"}); r.IsError {
		t.Fatal(resultText(r))
	}
	if a := f.sets[1]; a["sendSchedulingMessages"] != true {
		t.Fatalf("meeting update not sent: %v", a)
	}
	// Cancel: the meeting mails a cancellation, the private event doesn't.
	if r := calCall(t, cs, "event_cancel", map[string]any{"id": "e1"}); r.IsError {
		t.Fatal(resultText(r))
	}
	if a := f.sets[2]; a["sendSchedulingMessages"] != true || a["destroy"].([]any)[0] != "e1" {
		t.Fatalf("cancel: %v", a)
	}
	if first, _, _ := strings.Cut(asked[len(asked)-1], "\n"); !strings.Contains(first, "Cancel the meeting") || !strings.Contains(first, "2 attendee(s)") {
		t.Errorf("cancel first line: %q", first)
	}
	n := len(asked)
	if r := calCall(t, cs, "event_cancel", map[string]any{"id": "p1"}); r.IsError || len(asked) != n+1 {
		t.Fatalf("private delete must ask: %q", resultText(r))
	}
	if a := f.sets[3]; a["sendSchedulingMessages"] != false {
		t.Fatalf("private delete mailed: %v", a)
	}
}

func TestAttendeeCantCancelOrChange(t *testing.T) {
	asPerson(t, "me@co.example")
	f := &fakeCal{events: map[string]map[string]any{"e1": future(meeting)}}
	cs := inviteSession(t, f, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		t.Error("asked")
		return &mcp.ElicitResult{Action: "accept"}, nil
	})
	for tool, args := range map[string]map[string]any{"event_cancel": {"id": "e1"}, "event_update": {"id": "e1", "title": "mine now"}} {
		if r := calCall(t, cs, tool, args); !r.IsError || !strings.Contains(resultText(r), "event_respond") {
			t.Errorf("%s: %q", tool, resultText(r))
		}
	}
}

func TestDurText(t *testing.T) {
	for in, want := range map[string]string{"PT30M": "30 min", "PT1H30M": "1 h 30 min", "P1D": "1 day", "P2D": "2 days", "P1W": "1 week", "x": "x"} {
		if got := durText(in); got != want {
			t.Errorf("%s: %q", in, got)
		}
	}
}

// Live test: a cancelled meeting stayed in event_list without a sign, and
// could still be answered.
func TestCancelledIsShownAndNotAnswered(t *testing.T) {
	asPerson(t, "me@co.example")
	c := future(meeting)
	c["status"] = "cancelled"
	f := &fakeCal{events: map[string]map[string]any{"e1": c}}
	cs := inviteSession(t, f, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		t.Error("asked")
		return &mcp.ElicitResult{Action: "accept"}, nil
	})
	if r := calCall(t, cs, "event_respond", map[string]any{"id": "e1", "answer": "accept"}); !r.IsError || !strings.Contains(resultText(r), "cancelled") {
		t.Fatalf("%q", resultText(r))
	}
}
