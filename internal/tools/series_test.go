package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A weekly series organized by anna (me@ attends), far in the future.
func weeklySeries() map[string]any {
	return map[string]any{"id": "w", "title": "Weekly sync", "start": "2099-01-06T10:00:00", "timeZone": "Europe/Berlin", "duration": "PT30M",
		"organizerCalendarAddress": "mailto:anna@co.example",
		"recurrenceRules":          rule("frequency", "weekly", "count", 10.0),
		"recurrenceOverrides":      map[string]any{"2099-01-13T10:00:00": map[string]any{"start": "2099-01-13T11:00:00"}},
		"participants": map[string]any{
			"o": map[string]any{"calendarAddress": "mailto:anna@co.example", "roles": map[string]any{"owner": true, "attendee": true}},
			"b": map[string]any{"calendarAddress": "mailto:me@co.example", "roles": map[string]any{"attendee": true}}}}
}

func seriesSession(t *testing.T, f *fakeCal) (*mcp.ClientSession, *[]string) {
	var asked []string
	cs := inviteSession(t, f, func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = append(asked, r.Params.Message)
		return &mcp.ElicitResult{Action: "accept"}, nil
	})
	return cs, &asked
}

func TestSeriesAnswerWhole(t *testing.T) {
	asPerson(t, "me@co.example")
	f := &fakeCal{events: map[string]map[string]any{"w": weeklySeries()}}
	cs, asked := seriesSession(t, f)
	if r := calCall(t, cs, "event_respond", map[string]any{"id": "w", "answer": "accept"}); r.IsError {
		t.Fatal(resultText(r))
	}
	if !strings.HasPrefix((*asked)[0], `Answer ACCEPT to ALL meetings of "Weekly sync" (weekly, 10 times, next Tue 6 Jan 2099, 10:00)?`) {
		t.Errorf("question: %q", (*asked)[0])
	}
	up := f.sets[0]["update"].(map[string]any)["w"].(map[string]any)
	if len(up) != 1 || up["participants/b/participationStatus"] != "accepted" {
		t.Errorf("set: %v", f.sets[0])
	}
}

func TestSeriesOrganizerOneMeeting(t *testing.T) {
	asPerson(t, "anna@co.example")
	f := &fakeCal{events: map[string]map[string]any{"w": weeklySeries()}}
	cs, asked := seriesSession(t, f)
	// Refusals: no such meeting; moving every meeting; attendee-only tools.
	for _, c := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"event_update", map[string]any{"id": "w", "occurrence": "2099-01-07T10:00:00", "title": "x"}, "no meeting at"},
		{"event_update", map[string]any{"id": "w", "start": "2099-01-06T12:00"}, "moving every meeting"},
		{"event_cancel", map[string]any{"id": "w", "occurrence": "2099-03-31T10:00:00"}, "no meeting at"},
	} {
		if r := calCall(t, cs, c.tool, c.args); !r.IsError || !strings.Contains(resultText(r), c.want) {
			t.Errorf("%s %v: %q", c.tool, c.args, resultText(r))
		}
	}
	if len(f.sets) != 0 || len(*asked) != 0 {
		t.Fatal("refusal reached the server or the person")
	}
	// Move the meeting of 20 Jan: overrides sent whole, the old one kept.
	if r := calCall(t, cs, "event_update", map[string]any{"id": "w", "occurrence": "2099-01-20T10:00:00", "start": "2099-01-20T14:00"}); r.IsError {
		t.Fatal(resultText(r))
	}
	if !strings.HasPrefix((*asked)[0], `Change only the meeting "Weekly sync" on Tue 20 Jan 2099, 10:00: time`) {
		t.Errorf("question: %q", (*asked)[0])
	}
	b, _ := json.Marshal(f.sets[0])
	if !strings.Contains(string(b), `"recurrenceOverrides":{"2099-01-13T10:00:00":{"start":"2099-01-13T11:00:00"},"2099-01-20T10:00:00":{"duration":"PT30M","start":"2099-01-20T14:00:00"}}`) ||
		!strings.Contains(string(b), `"sendSchedulingMessages":true`) {
		t.Errorf("set: %s", b)
	}
	// Cancel only the meeting of 27 Jan.
	if r := calCall(t, cs, "event_cancel", map[string]any{"id": "w", "occurrence": "2099-01-27T10:00:00"}); r.IsError {
		t.Fatal(resultText(r))
	}
	if !strings.HasPrefix((*asked)[1], `Cancel only the meeting "Weekly sync" on Tue 27 Jan 2099, 10:00 (the rest of the series stays)?`) {
		t.Errorf("question: %q", (*asked)[1])
	}
	b, _ = json.Marshal(f.sets[1])
	if !strings.Contains(string(b), `"2099-01-27T10:00:00":{"excluded":true}`) || strings.Contains(string(b), "destroy") {
		t.Errorf("set: %s", b)
	}
	// Title of the whole series; cancelling the whole series says so.
	if r := calCall(t, cs, "event_update", map[string]any{"id": "w", "title": "Weekly"}); r.IsError {
		t.Fatal(resultText(r))
	}
	if !strings.HasPrefix((*asked)[2], `Change ALL meetings of "Weekly sync" (weekly, 10 times): title`) {
		t.Errorf("question: %q", (*asked)[2])
	}
	calCall(t, cs, "event_cancel", map[string]any{"id": "w"})
	if !strings.HasPrefix((*asked)[3], `Cancel the WHOLE series "Weekly sync" (weekly, 10 times, every meeting)?`) {
		t.Errorf("question: %q", (*asked)[3])
	}
}
