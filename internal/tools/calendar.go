package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/igorski89/sgnl-mcp/internal/jmap"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type CalendarListIn struct {
	From string `json:"from,omitempty" jsonschema:"first day, YYYY-MM-DD (default today)"`
	To   string `json:"to,omitempty" jsonschema:"last day, YYYY-MM-DD (default: 7 days after from)"`
}

type Event struct {
	Title    string `json:"title"`
	Start    string `json:"start"`
	TimeZone string `json:"time_zone,omitempty"`
	Duration string `json:"duration,omitempty"`
	AllDay   bool   `json:"all_day,omitempty"`
	Location string `json:"location,omitempty"`
	Calendar string `json:"calendar,omitempty"`
}

type CalendarListOut struct {
	From   string  `json:"from"`
	To     string  `json:"to"`
	Events []Event `json:"events"`
}

func (t *Tools) calendarList(ctx context.Context, _ *mcp.CallToolRequest, in CalendarListIn) (*mcp.CallToolResult, CalendarListOut, error) {
	from := time.Now().UTC().Truncate(24 * time.Hour)
	if in.From != "" {
		d, err := time.Parse("2006-01-02", in.From)
		if err != nil {
			return nil, CalendarListOut{}, fmt.Errorf("from: use YYYY-MM-DD")
		}
		from = d
	}
	to := from.AddDate(0, 0, 7)
	if in.To != "" {
		d, err := time.Parse("2006-01-02", in.To)
		if err != nil {
			return nil, CalendarListOut{}, fmt.Errorf("to: use YYYY-MM-DD")
		}
		to = d.AddDate(0, 0, 1) // inclusive
	}
	if !to.After(from) || to.Sub(from) > 92*24*time.Hour {
		return nil, CalendarListOut{}, fmt.Errorf("range: to must be after from, at most 92 days")
	}
	tok, err := t.Auth.AppToken(ctx)
	if err != nil {
		return nil, CalendarListOut{}, err
	}
	s, err := t.JMAP.Session(ctx, tok)
	if err != nil {
		return nil, CalendarListOut{}, err
	}
	acct := s.PrimaryAccounts[jmap.Calendars]
	if acct == "" {
		slog.Info("no calendar account", "capabilities", slices.Sorted(maps.Keys(s.Capabilities)))
		return nil, CalendarListOut{}, fmt.Errorf("the mail server offers no calendar for this account")
	}
	r, err := t.JMAP.Invoke(ctx, tok, s, []string{jmap.Core, jmap.Calendars},
		jmap.Call{Method: "Calendar/get", Args: map[string]any{"accountId": acct, "properties": []string{"id", "name"}}},
		jmap.Call{Method: "CalendarEvent/query", Args: map[string]any{"accountId": acct,
			"filter":            map[string]any{"after": from.Format(time.RFC3339), "before": to.Format(time.RFC3339)},
			"sort":              []any{map[string]any{"property": "start", "isAscending": true}},
			"expandRecurrences": true, "limit": 200}},
		jmap.Call{Method: "CalendarEvent/get", Args: map[string]any{"accountId": acct,
			"#ids":       jmap.Ref(1, "CalendarEvent/query", "/ids"),
			"properties": []string{"title", "start", "timeZone", "duration", "showWithoutTime", "locations", "calendarIds"}}})
	if err != nil {
		return nil, CalendarListOut{}, err
	}
	var cals struct {
		List []struct{ ID, Name string }
	}
	_ = json.Unmarshal(r[0], &cals)
	names := map[string]string{}
	for _, c := range cals.List {
		names[c.ID] = c.Name
	}
	var got struct {
		List []struct {
			Title           string                           `json:"title"`
			Start           string                           `json:"start"`
			TimeZone        string                           `json:"timeZone"`
			Duration        string                           `json:"duration"`
			ShowWithoutTime bool                             `json:"showWithoutTime"`
			Locations       map[string]struct{ Name string } `json:"locations"`
			CalendarIDs     map[string]bool                  `json:"calendarIds"`
		} `json:"list"`
	}
	if err := json.Unmarshal(r[2], &got); err != nil {
		return nil, CalendarListOut{}, err
	}
	out := CalendarListOut{From: from.Format("2006-01-02"), To: to.AddDate(0, 0, -1).Format("2006-01-02"), Events: []Event{}}
	for _, e := range got.List {
		ev := Event{Title: e.Title, Start: e.Start, TimeZone: e.TimeZone, Duration: e.Duration, AllDay: e.ShowWithoutTime}
		for _, l := range e.Locations {
			ev.Location = l.Name
			break
		}
		for id := range e.CalendarIDs {
			ev.Calendar = names[id]
			break
		}
		out.Events = append(out.Events, ev)
	}
	return nil, out, nil
}
