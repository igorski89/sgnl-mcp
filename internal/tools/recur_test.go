package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func occ(t *testing.T, e map[string]any, from, to string) []string {
	t.Helper()
	b, _ := json.Marshal(e)
	var ev calEvent
	_ = json.Unmarshal(b, &ev)
	loc, _ := time.LoadLocation("Europe/Berlin")
	f, _ := time.ParseInLocation("2006-01-02", from, loc)
	u, _ := time.ParseInLocation("2006-01-02", to, loc)
	tl := &Tools{TZ: "Europe/Berlin"}
	os, err := tl.occurrences(ev, f, u)
	if err != nil {
		return []string{"ERR " + err.Error()}
	}
	var out []string
	for _, o := range os {
		s := o.Start.Format("2006-01-02T15:04")
		if o.Status == "cancelled" {
			s += " cancelled"
		}
		if o.Title != ev.Title {
			s += " " + o.Title
		}
		out = append(out, s)
	}
	return out
}

func rule(kv ...any) []any {
	m := map[string]any{"@type": "RecurrenceRule"}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return []any{m}
}

func TestOccurrences(t *testing.T) {
	weekly := map[string]any{"title": "Weekly", "start": "2026-12-01T10:00:00", "timeZone": "Europe/Berlin", "duration": "PT30M",
		"recurrenceRules": rule("frequency", "weekly", "count", 4.0)}
	for name, c := range map[string]struct {
		e        map[string]any
		from, to string
		want     string
	}{
		"later week only": {weekly, "2026-12-07", "2026-12-10", "2026-12-08T10:00"},
		"count ends":      {weekly, "2026-12-01", "2027-01-31", "2026-12-01T10:00 2026-12-08T10:00 2026-12-15T10:00 2026-12-22T10:00"},
		"overrides": {func() map[string]any {
			e := clone(weekly)
			e["recurrenceOverrides"] = map[string]any{"2026-12-08T10:00:00": map[string]any{"start": "2026-12-08T11:00:00", "title": "Moved"},
				"2026-12-15T10:00:00": map[string]any{"excluded": true}, "2026-12-22T10:00:00": map[string]any{"status": "cancelled"}}
			return e
		}(), "2026-12-01", "2027-01-31", "2026-12-01T10:00 2026-12-08T11:00 Moved 2026-12-22T10:00 cancelled"},
		"until, interval, byDay": {map[string]any{"title": "x", "start": "2026-11-30T09:00:00", "timeZone": "Europe/Berlin", "duration": "PT1H",
			"recurrenceRules": rule("frequency", "weekly", "interval", 2.0, "until", "2026-12-31T23:59:59",
				"byDay", []any{map[string]any{"day": "mo"}, map[string]any{"day": "th"}})}, "2026-11-30", "2027-01-31",
			"2026-11-30T09:00 2026-12-03T09:00 2026-12-14T09:00 2026-12-17T09:00 2026-12-28T09:00 2026-12-31T09:00"},
		"monthly 2nd tuesday": {map[string]any{"title": "x", "start": "2026-12-08T09:00:00", "timeZone": "Europe/Berlin", "duration": "PT1H",
			"recurrenceRules": rule("frequency", "monthly", "byDay", []any{map[string]any{"day": "tu", "nthOfPeriod": 2.0}})}, "2027-01-01", "2027-03-01",
			"2027-01-12T09:00 2027-02-09T09:00"},
		// Same wall-clock time across the change to summer time.
		"dst": {map[string]any{"title": "x", "start": "2027-03-22T09:00:00", "timeZone": "Europe/Berlin", "duration": "PT1H",
			"recurrenceRules": rule("frequency", "weekly")}, "2027-03-22", "2027-04-02", "2027-03-22T09:00 2027-03-29T09:00"},
		"unknown part": {map[string]any{"title": "x", "start": "2026-12-01T09:00:00", "recurrenceRules": rule("frequency", "weekly", "rscale", "hebrew")},
			"2026-12-01", "2026-12-31", "ERR repeat pattern not followed here: rscale hebrew"},
	} {
		if got := strings.Join(occ(t, c.e, c.from, c.to), " "); got != c.want {
			t.Errorf("%s:\n got  %s\n want %s", name, got, c.want)
		}
	}
}

func clone(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

// A weekly meeting that started before the window is listed on each of its
// days (the mail server's query only finds it by its first day).
func TestEventListShowsLaterOccurrences(t *testing.T) {
	f := &fakeCal{events: map[string]map[string]any{
		"w": {"id": "w", "title": "Weekly", "start": "2026-12-01T10:00:00", "timeZone": "Europe/Berlin", "duration": "PT30M",
			"calendarIds": map[string]bool{"c1": true}, "recurrenceRules": rule("frequency", "weekly", "count", 4.0),
			"recurrenceOverrides": map[string]any{"2026-12-15T10:00:00": map[string]any{"excluded": true}}},
		"s": {"id": "s", "title": "Single", "start": "2026-12-09T15:00:00", "timeZone": "Europe/Berlin", "duration": "PT1H", "calendarIds": map[string]bool{"c1": true}},
	}}
	cs := inviteSession(t, f, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) { return nil, nil })
	r := calCall(t, cs, "event_list", map[string]any{"from": "2026-12-07", "to": "2026-12-20"})
	b, _ := json.Marshal(r.StructuredContent)
	got := string(b)
	if r.IsError || !strings.Contains(got, `"occurrence":"2026-12-08T10:00:00"`) || !strings.Contains(got, `"id":"s"`) ||
		strings.Contains(got, "2026-12-15T10") || strings.Index(got, `"2026-12-08T10:00:00"`) > strings.Index(got, `"id":"s"`) {
		t.Fatalf("list: %s", got)
	}
	// The second look reuses the series (same calendar state).
	n := f.queries
	calCall(t, cs, "event_list", map[string]any{"from": "2026-12-21", "to": "2026-12-27"})
	if f.queries != n+1 {
		t.Errorf("series re-read without a change: %d queries", f.queries-n)
	}
}
