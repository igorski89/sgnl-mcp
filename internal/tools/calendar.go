package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/igorski89/sgnl-mcp/internal/jmap"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var calUsing = []string{jmap.Core, jmap.Calendars}

type EventListIn struct {
	From string `json:"from,omitempty" jsonschema:"first day, YYYY-MM-DD (default today)"`
	To   string `json:"to,omitempty" jsonschema:"last day, YYYY-MM-DD (default: 7 days after from)"`
}

type Event struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Start    string `json:"start"`
	TimeZone string `json:"time_zone,omitempty"`
	Duration string `json:"duration,omitempty"`
	AllDay   bool   `json:"all_day,omitempty"`
	Location string `json:"location,omitempty"`
	Calendar string `json:"calendar,omitempty"`
	Status   string `json:"status,omitempty" jsonschema:"cancelled: the organizer called it off; tentative"`
	Repeats  bool   `json:"repeats,omitempty" jsonschema:"one meeting of a repeating series (id is the series)"`
	// Occurrence: which meeting of the series (its original start); pass
	// it with the id to change or cancel just this one.
	Occurrence string `json:"occurrence,omitempty" jsonschema:"which meeting of the series (its original start), for changing just this one"`
	Note       string `json:"note,omitempty"`
}

type EventListOut struct {
	From   string  `json:"from"`
	To     string  `json:"to"`
	Events []Event `json:"events"`
}

func (t *Tools) calSession(ctx context.Context) (string, *jmap.Session, string, error) {
	tok, err := t.Auth.MailToken(ctx)
	if err != nil {
		return "", nil, "", err
	}
	s, err := t.JMAP.Session(ctx, tok)
	if err != nil {
		return "", nil, "", err
	}
	acct := s.PrimaryAccounts[jmap.Calendars]
	if acct == "" {
		slog.Info("no calendar account", "capabilities", slices.Sorted(maps.Keys(s.Capabilities)))
		return "", nil, "", fmt.Errorf("the mail server offers no calendar for this account")
	}
	return tok, s, acct, nil
}

func (t *Tools) loc() *time.Location {
	if l, err := time.LoadLocation(t.TZ); err == nil {
		return l
	}
	return time.UTC
}

func (t *Tools) events(ctx context.Context, from, to time.Time) ([]Event, error) {
	tok, s, acct, err := t.calSession(ctx)
	if err != nil {
		return nil, err
	}
	r, err := t.JMAP.Invoke(ctx, tok, s, calUsing,
		jmap.Call{Method: "Calendar/get", Args: map[string]any{"accountId": acct, "properties": []string{"id", "name"}}},
		jmap.Call{Method: "CalendarEvent/query", Args: map[string]any{"accountId": acct,
			"filter": map[string]any{"after": from.UTC().Format(time.RFC3339), "before": to.UTC().Format(time.RFC3339)},
			"sort":   []any{map[string]any{"property": "start", "isAscending": true}},
			"limit":  200}},
		jmap.Call{Method: "CalendarEvent/get", Args: map[string]any{"accountId": acct,
			"#ids":       jmap.Ref(1, "CalendarEvent/query", "/ids"),
			"properties": []string{"id", "title", "start", "timeZone", "duration", "showWithoutTime", "locations", "calendarIds", "status"}}})
	if err != nil {
		return nil, err
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
		State string     `json:"state"`
		List  []calEvent `json:"list"`
	}
	if err := json.Unmarshal(r[2], &got); err != nil {
		return nil, err
	}
	series, truncated, err := t.seriesOf(ctx, tok, s, acct, got.State)
	if err != nil {
		return nil, err
	}
	isSeries := map[string]bool{}
	for _, e := range series {
		isSeries[e.ID] = true
	}
	calName := func(ids map[string]bool) string {
		for _, id := range slices.Sorted(maps.Keys(ids)) {
			return names[id]
		}
		return ""
	}
	out := []Event{}
	for _, e := range got.List {
		if isSeries[e.ID] {
			continue // listed below, occurrence by occurrence
		}
		ev := Event{ID: e.ID, Title: e.Title, Start: e.Start, TimeZone: e.TimeZone, Duration: e.Duration, AllDay: e.ShowWithoutTime,
			Location: locNames(e.Locations), Calendar: calName(e.CalendarIDs)}
		if e.Status != "" && e.Status != "confirmed" {
			ev.Status = e.Status
		}
		out = append(out, ev)
	}
	for _, e := range series {
		occ, err := t.occurrences(e, from, to)
		if err != nil {
			// Not guessed: the series is listed once, as it starts, marked.
			if st, _, werr := t.window(e); werr == nil && st.Before(to) {
				out = append(out, Event{ID: e.ID, Title: e.Title, Start: e.Start, TimeZone: e.TimeZone, Duration: e.Duration, AllDay: e.ShowWithoutTime,
					Location: locNames(e.Locations), Calendar: calName(e.CalendarIDs), Repeats: true,
					Note: "repeats in a way not followed here: see your calendar app for its dates"})
			}
			continue
		}
		for _, o := range occ {
			ev := Event{ID: e.ID, Occurrence: o.RecurrenceID, Repeats: true, Title: o.Title, Start: o.Start.Format(localFmt), TimeZone: e.TimeZone,
				Duration: o.Duration, AllDay: e.ShowWithoutTime, Location: locNames(o.Locations), Calendar: calName(e.CalendarIDs)}
			if o.Status != "" && o.Status != "confirmed" {
				ev.Status = o.Status
			}
			if o.Moved {
				ev.Note = "moved from " + o.RecurrenceID
			}
			out = append(out, ev)
		}
	}
	slices.SortStableFunc(out, func(a, b Event) int { return strings.Compare(a.sortKey(t), b.sortKey(t)) })
	if truncated {
		slog.Warn("calendar: too many events to find every repeating one", "account", acct)
	}
	return out, nil
}

// sortKey: the start in UTC, so events in different zones sort right.
func (e Event) sortKey(t *Tools) string {
	st, _, err := t.window(calEvent{Start: e.Start, TimeZone: e.TimeZone})
	if err != nil {
		return e.Start
	}
	return st.UTC().Format(time.RFC3339)
}

// maxEvents: how many of a person's events are looked through for series.
const maxEvents = 3000

// seriesOf: the person's repeating events (whole objects), from the cache
// while the calendar state is unchanged.
func (t *Tools) seriesOf(ctx context.Context, tok string, s *jmap.Session, acct, state string) ([]calEvent, bool, error) {
	if c, ok := t.series.get(acct, state); ok {
		return c.series, c.truncated, nil
	}
	r, err := t.JMAP.Invoke(ctx, tok, s, calUsing, jmap.Call{Method: "CalendarEvent/query", Args: map[string]any{"accountId": acct, "limit": maxEvents}})
	if err != nil {
		return nil, false, err
	}
	var q struct {
		IDs   []string `json:"ids"`
		Total int      `json:"total"`
	}
	_ = json.Unmarshal(r[0], &q)
	entry := seriesEntry{state: state, truncated: len(q.IDs) >= maxEvents}
	for i := 0; i < len(q.IDs); i += 250 {
		batch := q.IDs[i:min(i+250, len(q.IDs))]
		// Whole objects: with a properties list the server leaves the rules out.
		r, err := t.JMAP.Invoke(ctx, tok, s, calUsing, jmap.Call{Method: "CalendarEvent/get", Args: map[string]any{"accountId": acct, "ids": batch}})
		if err != nil {
			return nil, false, err
		}
		var got struct {
			List []calEvent `json:"list"`
		}
		if err := json.Unmarshal(r[0], &got); err != nil {
			return nil, false, err
		}
		for _, e := range got.List {
			if e.repeats() {
				e.Description, e.Participants, e.Alerts, e.Links, e.Replies, e.VirtualLocs = "", nil, nil, nil, nil, nil
				entry.series = append(entry.series, e)
			}
		}
	}
	t.series.put(acct, entry)
	return entry.series, entry.truncated, nil
}

func (t *Tools) eventList(ctx context.Context, _ *mcp.CallToolRequest, in EventListIn) (*mcp.CallToolResult, EventListOut, error) {
	now := time.Now().In(t.loc())
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, t.loc())
	if in.From != "" {
		d, err := time.ParseInLocation("2006-01-02", in.From, t.loc())
		if err != nil {
			return nil, EventListOut{}, fmt.Errorf("from: use YYYY-MM-DD")
		}
		from = d
	}
	to := from.AddDate(0, 0, 7)
	if in.To != "" {
		d, err := time.ParseInLocation("2006-01-02", in.To, t.loc())
		if err != nil {
			return nil, EventListOut{}, fmt.Errorf("to: use YYYY-MM-DD")
		}
		to = d.AddDate(0, 0, 1) // inclusive
	}
	if !to.After(from) || to.Sub(from) > 92*24*time.Hour {
		return nil, EventListOut{}, fmt.Errorf("range: to must be after from, at most 92 days")
	}
	evs, err := t.events(ctx, from, to)
	if err != nil {
		return nil, EventListOut{}, err
	}
	return nil, EventListOut{From: from.Format("2006-01-02"), To: to.AddDate(0, 0, -1).Format("2006-01-02"), Events: evs}, nil
}

// ---- event_create: a private event (no participants: no invitations)

type EventCreateIn struct {
	Title       string `json:"title"`
	Start       string `json:"start" jsonschema:"local start, YYYY-MM-DDTHH:MM (or YYYY-MM-DD for all day)"`
	Minutes     int    `json:"minutes,omitempty" jsonschema:"length in minutes (default 60; all-day events: whole days)"`
	TimeZone    string `json:"time_zone,omitempty" jsonschema:"IANA zone, e.g. Europe/Berlin (default: the instance's)"`
	Location    string `json:"location,omitempty"`
	Description string `json:"description,omitempty"`
}

type EventCreateOut struct {
	ID   string `json:"id"`
	Note string `json:"note"`
}

// buildEvent: the JSCalendar event for in (no calendar, no participants).
func (t *Tools) buildEvent(in EventCreateIn) (map[string]any, error) {
	tz := in.TimeZone
	if tz == "" {
		tz = t.TZ
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return nil, fmt.Errorf("time_zone: unknown zone %q", tz)
	}
	e := map[string]any{"@type": "Event", "title": in.Title, "timeZone": tz}
	if d, err := time.Parse("2006-01-02T15:04", in.Start); err == nil {
		m := in.Minutes
		if m <= 0 {
			m = 60
		}
		if m > 14*24*60 {
			return nil, fmt.Errorf("minutes: at most 14 days")
		}
		e["start"] = d.Format("2006-01-02T15:04:05")
		e["duration"] = fmt.Sprintf("PT%dM", m)
	} else if d, err := time.Parse("2006-01-02", in.Start); err == nil {
		days := in.Minutes / (24 * 60)
		if days < 1 {
			days = 1
		}
		e["start"] = d.Format("2006-01-02T15:04:05")
		e["showWithoutTime"] = true
		e["duration"] = fmt.Sprintf("P%dD", days)
	} else {
		return nil, fmt.Errorf("start: use YYYY-MM-DDTHH:MM or YYYY-MM-DD")
	}
	if in.Location != "" {
		e["locations"] = map[string]any{"l": map[string]string{"@type": "Location", "name": in.Location}}
	}
	if in.Description != "" {
		e["description"] = in.Description
	}
	return e, nil
}

// defaultCalendar: the person's default calendar (else their first).
func (t *Tools) defaultCalendar(ctx context.Context, tok string, s *jmap.Session, acct string) (string, error) {
	r, err := t.JMAP.Invoke(ctx, tok, s, calUsing, jmap.Call{Method: "Calendar/get", Args: map[string]any{"accountId": acct, "properties": []string{"id", "isDefault"}}})
	if err != nil {
		return "", err
	}
	var cals struct {
		List []struct {
			ID        string `json:"id"`
			IsDefault bool   `json:"isDefault"`
		}
	}
	_ = json.Unmarshal(r[0], &cals)
	if len(cals.List) == 0 {
		return "", fmt.Errorf("no calendar")
	}
	cal := cals.List[0].ID
	for _, c := range cals.List {
		if c.IsDefault {
			cal = c.ID
		}
	}
	return cal, nil
}

func (t *Tools) eventCreate(ctx context.Context, _ *mcp.CallToolRequest, in EventCreateIn) (*mcp.CallToolResult, EventCreateOut, error) {
	if strings.TrimSpace(in.Title) == "" {
		return nil, EventCreateOut{}, fmt.Errorf("title is required")
	}
	e, err := t.buildEvent(in)
	if err != nil {
		return nil, EventCreateOut{}, err
	}
	tok, s, acct, err := t.calSession(ctx)
	if err != nil {
		return nil, EventCreateOut{}, err
	}
	cal, err := t.defaultCalendar(ctx, tok, s, acct)
	if err != nil {
		return nil, EventCreateOut{}, err
	}
	e["calendarIds"] = map[string]bool{cal: true}
	r, err := t.JMAP.Invoke(ctx, tok, s, calUsing, jmap.Call{Method: "CalendarEvent/set", Args: map[string]any{"accountId": acct, "create": map[string]any{"e": e}}})
	if err != nil {
		return nil, EventCreateOut{}, err
	}
	if err := jmap.SetErrors(r[0]); err != nil {
		return nil, EventCreateOut{}, err
	}
	var got struct {
		Created map[string]struct{ ID string } `json:"created"`
	}
	_ = json.Unmarshal(r[0], &got)
	return nil, EventCreateOut{got.Created["e"].ID, "Added to the person's calendar. Private: no one was invited."}, nil
}

// ---- availability_get: colleagues' free/busy (when, never what)

type AvailabilityIn struct {
	People []string `json:"people" jsonschema:"colleagues: names or email addresses (max 10)"`
	From   string   `json:"from,omitempty" jsonschema:"first day YYYY-MM-DD (default today)"`
	To     string   `json:"to,omitempty" jsonschema:"last day YYYY-MM-DD (default from + 7 days)"`
}

type Busy struct {
	Start  string `json:"start"`
	End    string `json:"end"`
	Status string `json:"status,omitempty"`
}

type PersonAvailability struct {
	Person string `json:"person"`
	Email  string `json:"email,omitempty"`
	Busy   []Busy `json:"busy"`
	Error  string `json:"error,omitempty"`
}

type AvailabilityOut struct {
	From   string               `json:"from"`
	To     string               `json:"to"`
	Zone   string               `json:"times_in"`
	People []PersonAvailability `json:"people"`
}

var principalUsing = []string{jmap.Core, jmap.Principals, jmap.Availability, jmap.Calendars}

type principal struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Email        string `json:"email"`
	Type         string `json:"type"`
	Capabilities map[string]struct {
		AccountID string `json:"accountId"`
	} `json:"capabilities"`
}

func (t *Tools) findPrincipals(ctx context.Context, tok string, s *jmap.Session, acct, text string, limit int) ([]principal, error) {
	r, err := t.JMAP.Invoke(ctx, tok, s, principalUsing,
		jmap.Call{Method: "Principal/query", Args: map[string]any{"accountId": acct, "filter": map[string]any{"text": text}, "limit": limit}},
		jmap.Call{Method: "Principal/get", Args: map[string]any{"accountId": acct, "#ids": jmap.Ref(0, "Principal/query", "/ids"),
			"properties": []string{"id", "name", "description", "email", "type", "capabilities"}}})
	if err != nil {
		return nil, err
	}
	var got struct{ List []principal }
	err = json.Unmarshal(r[1], &got)
	return got.List, err
}

func (t *Tools) availabilityGet(ctx context.Context, _ *mcp.CallToolRequest, in AvailabilityIn) (*mcp.CallToolResult, AvailabilityOut, error) {
	if len(in.People) == 0 || len(in.People) > 10 {
		return nil, AvailabilityOut{}, fmt.Errorf("give 1-10 people")
	}
	now := time.Now().In(t.loc())
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, t.loc())
	if in.From != "" {
		d, err := time.ParseInLocation("2006-01-02", in.From, t.loc())
		if err != nil {
			return nil, AvailabilityOut{}, fmt.Errorf("from: use YYYY-MM-DD")
		}
		from = d
	}
	to := from.AddDate(0, 0, 7)
	if in.To != "" {
		d, err := time.ParseInLocation("2006-01-02", in.To, t.loc())
		if err != nil {
			return nil, AvailabilityOut{}, fmt.Errorf("to: use YYYY-MM-DD")
		}
		to = d.AddDate(0, 0, 1)
	}
	if !to.After(from) || to.Sub(from) > 31*24*time.Hour {
		return nil, AvailabilityOut{}, fmt.Errorf("range: at most 31 days")
	}
	tok, err := t.Auth.MailToken(ctx)
	if err != nil {
		return nil, AvailabilityOut{}, err
	}
	s, err := t.JMAP.Session(ctx, tok)
	if err != nil {
		return nil, AvailabilityOut{}, err
	}
	acct := s.PrimaryAccounts[jmap.Principals]
	if acct == "" {
		acct = s.PrimaryAccounts[jmap.Mail]
	}
	out := AvailabilityOut{From: from.Format("2006-01-02"), To: to.AddDate(0, 0, -1).Format("2006-01-02"), Zone: "UTC", People: []PersonAvailability{}}
	for _, who := range in.People {
		pa := PersonAvailability{Person: who, Busy: []Busy{}}
		ps, err := t.findPrincipals(ctx, tok, s, acct, who, 2)
		switch {
		case err != nil:
			pa.Error = err.Error()
		case len(ps) == 0:
			pa.Error = "no one by that name or address in the company directory"
		case len(ps) > 1 && !strings.EqualFold(ps[0].Email, who):
			pa.Error = fmt.Sprintf("ambiguous: %s, %s, ...: give the email address", ps[0].Email, ps[1].Email)
		default:
			p := ps[0]
			pa.Email = p.Email
			calAcct := p.Capabilities[jmap.Calendars].AccountID
			if calAcct == "" {
				pa.Error = "no shared calendar"
				break
			}
			r, err := t.JMAP.Invoke(ctx, tok, s, principalUsing, jmap.Call{Method: "Principal/getAvailability", Args: map[string]any{
				"accountId": calAcct, "id": p.ID, "utcStart": from.UTC().Format("2006-01-02T15:04:05Z"), "utcEnd": to.UTC().Format("2006-01-02T15:04:05Z")}})
			if err != nil {
				pa.Error = err.Error()
				break
			}
			var got struct {
				List []struct {
					UTCStart   string `json:"utcStart"`
					UTCEnd     string `json:"utcEnd"`
					BusyStatus string `json:"busyStatus"`
				}
			}
			_ = json.Unmarshal(r[0], &got)
			for _, b := range got.List {
				pa.Busy = append(pa.Busy, Busy{b.UTCStart, b.UTCEnd, b.BusyStatus})
			}
		}
		out.People = append(out.People, pa)
	}
	return nil, out, nil
}
