package tools

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/igorski89/sgnl-mcp/internal/auth"
	"github.com/igorski89/sgnl-mcp/internal/jmap"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Answering, changing and cancelling events. Anything that mails other
// people (an answer to the organizer, an update or a cancellation to the
// attendees) is asked of the person first, with the event as it is on the
// server. For repeating events see below. Nothing is mailed about an
// event that is over (the mail server would silently send nothing).

var reDur = regexp.MustCompile(`^P(?:(\d+)W)?(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?)?$`)

// window: when an event starts and ends, in its own time zone.
func (t *Tools) window(e calEvent) (start, end time.Time, err error) {
	loc := t.loc()
	if e.TimeZone != "" {
		if l, lerr := time.LoadLocation(e.TimeZone); lerr == nil {
			loc = l
		}
	}
	start, err = time.ParseInLocation("2006-01-02T15:04:05", e.Start, loc)
	if err != nil {
		return start, start, fmt.Errorf("the event's start %q can't be read", e.Start)
	}
	end = start
	if m := reDur.FindStringSubmatch(e.Duration); m != nil {
		n := func(i int) int { v, _ := strconv.Atoi(m[i]); return v }
		end = start.AddDate(0, 0, 7*n(1)+n(2)).Add(time.Duration(n(3))*time.Hour + time.Duration(n(4))*time.Minute + time.Duration(n(5))*time.Second)
	}
	return start, end, nil
}

func whenText(start time.Time, allDay bool) string {
	if allDay {
		return start.Format("Mon 2 Jan 2006") + ", all day"
	}
	return start.Format("Mon 2 Jan 2006, 15:04")
}

// attendeesOf: the other people on the event (not the organizer, not the
// person), each with an OUTSIDE mark.
func (t *Tools) attendeesOf(ctx context.Context, e calEvent, me string) (list []string, outside int) {
	org, _ := strings.CutPrefix(strings.ToLower(e.Organizer), "mailto:")
	for _, p := range e.Participants {
		a := p.address()
		if a == "" || a == org || strings.EqualFold(a, me) {
			continue
		}
		if !t.internal(ctx, a) {
			outside++
			a += " (OUTSIDE the company)"
		}
		list = append(list, oneLine(a, 100))
	}
	return
}

// ---- repeating events: a series, or one meeting of it (occurrence: its
// original local start, as event_list gives it). The mail server lets an
// attendee answer only the whole series; the organizer (or owner) can
// change or cancel one meeting through recurrenceOverrides, always sent as
// the whole object with the existing overrides kept (probed 2026-11-26).

// ruleText: how a series repeats, as people say it.
func ruleText(e calEvent) string {
	if len(e.RecurrenceRules) != 1 {
		return "repeating"
	}
	r, _ := e.RecurrenceRules[0].(map[string]any)
	f := fmt.Sprint(r["frequency"])
	if n, _ := r["interval"].(float64); n > 1 {
		f = fmt.Sprintf("every %d %s", int(n), map[string]string{"daily": "days", "weekly": "weeks", "monthly": "months", "yearly": "years"}[f])
	}
	switch {
	case r["count"] != nil:
		n, _ := r["count"].(float64)
		return fmt.Sprintf("%s, %d times", f, int(n))
	case r["until"] != nil:
		u := fmt.Sprint(r["until"])
		return fmt.Sprintf("%s until %s", f, u[:min(len(u), 10)])
	}
	return f + ", no end date"
}

// occurrenceOf: the meeting of series e that originally started at occ.
func (t *Tools) occurrenceOf(e calEvent, occ string) (occurrence, error) {
	occ = strings.TrimSpace(occ)
	if len(occ) == 16 {
		occ += ":00"
	}
	st, _, err := t.window(calEvent{Start: occ, TimeZone: e.TimeZone})
	if err != nil {
		return occurrence{}, fmt.Errorf("occurrence: use the value event_list gives (YYYY-MM-DDTHH:MM:SS)")
	}
	os, err := t.occurrences(e, st.AddDate(0, 0, -2), st.AddDate(0, 0, 2))
	if err != nil {
		return occurrence{}, fmt.Errorf("this series %s: change it in your calendar app", err)
	}
	for _, o := range os {
		if o.RecurrenceID == occ {
			return o, nil
		}
	}
	if p, ok := e.RecurrenceOver[occ]; ok && p["excluded"] == true {
		return occurrence{}, fmt.Errorf("the meeting of %s was already cancelled", occ)
	}
	return occurrence{}, fmt.Errorf("this series has no meeting at %s (see event_list)", occ)
}

// nextOccurrence: the series' next meeting that isn't over, if any.
func (t *Tools) nextOccurrence(e calEvent) (occurrence, bool) {
	now := time.Now()
	st, _, werr := t.window(e)
	if werr != nil {
		return occurrence{}, false
	}
	until := now
	if st.After(now) {
		until = st
	}
	os, err := t.occurrences(e, now, until.AddDate(2, 0, 0))
	if err != nil {
		return occurrence{Start: st, Title: e.Title}, true
	}
	for _, o := range os {
		if o.Status != "cancelled" {
			return o, true
		}
	}
	return occurrence{}, false
}

// withOverride: the series' overrides with occ's replaced by o (the whole
// object, as the mail server needs it).
func withOverride(e calEvent, occ string, o map[string]any) map[string]any {
	all := map[string]any{}
	for k, v := range e.RecurrenceOver {
		all[k] = v
	}
	all[occ] = o
	return all
}

// ---- event_respond

type EventRespondIn struct {
	ID         string `json:"id" jsonschema:"event id from event_list"`
	Answer     string `json:"answer" jsonschema:"accept, decline or maybe"`
	Occurrence string `json:"occurrence,omitempty" jsonschema:"not supported: a repeating series is answered as a whole"`
}

type EventChangeOut struct {
	ID   string `json:"id"`
	Note string `json:"note"`
}

func (t *Tools) eventRespond(ctx context.Context, req *mcp.CallToolRequest, in EventRespondIn) (*mcp.CallToolResult, EventChangeOut, error) {
	status, word := map[string]string{"accept": "accepted", "decline": "declined", "maybe": "tentative"}[strings.ToLower(strings.TrimSpace(in.Answer))], strings.ToUpper(strings.TrimSpace(in.Answer))
	if status == "" {
		return nil, EventChangeOut{}, fmt.Errorf("answer: accept, decline or maybe")
	}
	tok, s, acct, e, err := t.getEvent(ctx, in.ID)
	if err != nil {
		return nil, EventChangeOut{}, err
	}
	me, _ := personOf(ctx)
	role, mine := e.role(me.Email)
	if role != "attendee" {
		return nil, EventChangeOut{}, fmt.Errorf("you aren't invited to this event as an attendee (you are: %s), so there's nothing to answer", role)
	}
	if e.Status == "cancelled" {
		return nil, EventChangeOut{}, fmt.Errorf("the organizer cancelled this event: there's nothing to answer")
	}
	series := e.repeats()
	if strings.TrimSpace(in.Occurrence) != "" {
		if !series {
			return nil, EventChangeOut{}, fmt.Errorf("this isn't a repeating event: answer it without occurrence")
		}
		return nil, EventChangeOut{}, fmt.Errorf("single meetings of a series can't be answered here (the mail server doesn't allow it): answer the whole series, or that one meeting in your calendar app")
	}
	var start time.Time
	if series {
		o, ok := t.nextOccurrence(e)
		if !ok {
			return nil, EventChangeOut{}, fmt.Errorf("every meeting of this series is over: no answer would be sent")
		}
		start = o.Start
	} else {
		st, end, err := t.window(e)
		if err != nil {
			return nil, EventChangeOut{}, err
		}
		if !end.After(time.Now()) {
			return nil, EventChangeOut{}, fmt.Errorf("this event is over: no answer would be sent")
		}
		start = st
	}
	org, _ := strings.CutPrefix(e.Organizer, "mailto:")
	if org == "" {
		return nil, EventChangeOut{}, fmt.Errorf("this event has no organizer to answer")
	}
	orgShown := oneLine(org, 100)
	if !t.internal(ctx, org) {
		orgShown += " (OUTSIDE the company)"
	}
	title := oneLine(e.Title, 60)
	first := fmt.Sprintf("Answer %s to %q on %s? %s gets your answer by email.", word, title, whenText(start, e.ShowWithoutTime), orgShown)
	if series {
		first = fmt.Sprintf("Answer %s to ALL meetings of %q (%s, next %s)? %s gets your answer by email.", word, title, ruleText(e), whenText(start, e.ShowWithoutTime), orgShown)
	}
	body := first + "\n\nWas: " + answer(e.Participants[mine].ParticipationStatus) + "\n"
	if t.outside.recent(me.Username) {
		body += "\nWARNING: your assistant read email from outside the company in the last 30 minutes; make sure this is what you asked for.\n"
	}
	res, ok, err := t.confirm(ctx, req, body, e.ID+"|"+mine+"|"+status+"|"+e.Start+"|"+fmt.Sprint(series))
	if err != nil || res != nil {
		return res, EventChangeOut{}, err
	}
	if !ok {
		return nil, EventChangeOut{}, fmt.Errorf("the person declined to answer: nothing was sent")
	}
	r, err := t.JMAP.Invoke(ctx, tok, s, calUsing, jmap.Call{Method: "CalendarEvent/set", Reply: true, Args: map[string]any{"accountId": acct,
		"update": map[string]any{e.ID: map[string]any{"participants/" + mine + "/participationStatus": status}}, "sendSchedulingMessages": true}})
	if err != nil {
		return nil, EventChangeOut{}, err
	}
	if err := jmap.SetErrors(r[0]); err != nil {
		return nil, EventChangeOut{}, err
	}
	note := "Answered " + status + "; the mail server sends the answer to the organizer."
	if series {
		note = "Answered " + status + " for the whole series; the mail server sends the answer to the organizer."
	}
	return nil, EventChangeOut{e.ID, note}, nil
}

// ---- event_update

type EventUpdateIn struct {
	InCalendar
	ID          string  `json:"id" jsonschema:"event id from event_list"`
	Occurrence  string  `json:"occurrence,omitempty" jsonschema:"for a repeating series: change only this meeting (the occurrence event_list gives); without it, title, place and description of ALL meetings"`
	Title       *string `json:"title,omitempty"`
	Start       string  `json:"start,omitempty" jsonschema:"new local start, YYYY-MM-DDTHH:MM (or YYYY-MM-DD for all day)"`
	Minutes     int     `json:"minutes,omitempty" jsonschema:"new length in minutes"`
	Location    *string `json:"location,omitempty"`
	Description *string `json:"description,omitempty"`
}

func (t *Tools) eventUpdate(ctx context.Context, req *mcp.CallToolRequest, in EventUpdateIn) (*mcp.CallToolResult, EventChangeOut, error) {
	tok, s, acct, e, err := t.getEvent(ctx, in.ID)
	if err != nil {
		return nil, EventChangeOut{}, err
	}
	me, _ := personOf(ctx)
	owner, calName, err := t.sharedCal(ctx, tok, s, acct, e)
	if err != nil {
		return nil, EventChangeOut{}, err
	}
	self := me.Email // in a shared calendar: as its owner
	if owner != "" {
		self = owner
	}
	role, _ := e.role(self)
	if role != "owner" && role != "organizer" {
		if owner != "" {
			return nil, EventChangeOut{}, fmt.Errorf("%s doesn't organize this meeting, so it can't be changed in their calendar here", oneLine(owner, 80))
		}
		return nil, EventChangeOut{}, fmt.Errorf("only the organizer can change this event (you are: %s); to say you can't come, use event_respond", role)
	}
	series, occ := e.repeats(), strings.TrimSpace(in.Occurrence)
	if occ != "" && !series {
		return nil, EventChangeOut{}, fmt.Errorf("this isn't a repeating event: change it without occurrence")
	}
	if series && occ == "" && (in.Start != "" || in.Minutes > 0) {
		return nil, EventChangeOut{}, fmt.Errorf("moving every meeting of a series isn't offered here (use your calendar app); to move one meeting, give its occurrence")
	}
	// What is changed: the event, or one meeting of the series.
	cur := calEvent{Title: e.Title, Start: e.Start, TimeZone: e.TimeZone, Duration: e.Duration, ShowWithoutTime: e.ShowWithoutTime,
		Locations: e.Locations, Description: e.Description}
	var o occurrence
	if occ != "" {
		if o, err = t.occurrenceOf(e, occ); err != nil {
			return nil, EventChangeOut{}, err
		}
		occ = o.RecurrenceID
		cur.Title, cur.Start, cur.Duration, cur.Locations = o.Title, o.Start.Format(localFmt), o.Duration, o.Locations
		if d, ok := e.RecurrenceOver[occ]["description"].(string); ok {
			cur.Description = d
		}
	}
	oldStart, oldEnd, err := t.window(cur)
	if err != nil {
		return nil, EventChangeOut{}, err
	}
	patch := map[string]any{}
	var changes []string
	if in.Title != nil && *in.Title != cur.Title {
		patch["title"] = *in.Title
		changes = append(changes, fmt.Sprintf("title → %q", oneLine(*in.Title, 80)))
	}
	newStart, allDay := oldStart, cur.ShowWithoutTime
	if in.Start != "" {
		if d, err := time.ParseInLocation("2006-01-02T15:04", in.Start, oldStart.Location()); err == nil {
			newStart, allDay = d, false
		} else if d, err := time.ParseInLocation("2006-01-02", in.Start, oldStart.Location()); err == nil {
			newStart, allDay = d, true
		} else {
			return nil, EventChangeOut{}, fmt.Errorf("start: use YYYY-MM-DDTHH:MM or YYYY-MM-DD")
		}
		if occ != "" && allDay != e.ShowWithoutTime {
			return nil, EventChangeOut{}, fmt.Errorf("one meeting of a series can't switch between all-day and timed here")
		}
	}
	dur := cur.Duration
	if in.Minutes > 0 {
		if in.Minutes > 14*24*60 {
			return nil, EventChangeOut{}, fmt.Errorf("minutes: at most 14 days")
		}
		if allDay {
			dur = fmt.Sprintf("P%dD", max(1, in.Minutes/(24*60)))
		} else {
			dur = fmt.Sprintf("PT%dM", in.Minutes)
		}
	} else if allDay != cur.ShowWithoutTime {
		if allDay {
			dur = "P1D"
		} else {
			dur = "PT60M"
		}
	}
	if !newStart.Equal(oldStart) || allDay != cur.ShowWithoutTime || dur != cur.Duration {
		patch["start"] = newStart.Format("2006-01-02T15:04:05")
		if occ == "" {
			patch["showWithoutTime"] = allDay
		}
		patch["duration"] = dur
		changes = append(changes, fmt.Sprintf("time %s → %s (%s)", whenText(oldStart, cur.ShowWithoutTime), whenText(newStart, allDay), durText(dur)))
	}
	if in.Location != nil {
		patch["locations"] = map[string]any{"l": map[string]string{"@type": "Location", "name": *in.Location}}
		if *in.Location == "" {
			patch["locations"] = nil
			if occ != "" {
				patch["locations"] = map[string]any{}
			}
		}
		changes = append(changes, fmt.Sprintf("place → %q", oneLine(*in.Location, 80)))
	}
	if in.Description != nil && *in.Description != cur.Description {
		patch["description"] = *in.Description
		changes = append(changes, "description changed")
	}
	if len(patch) == 0 {
		return nil, EventChangeOut{}, fmt.Errorf("nothing to change")
	}
	if occ != "" {
		ov := map[string]any{}
		for k, v := range e.RecurrenceOver[occ] {
			ov[k] = v
		}
		for k, v := range patch {
			ov[k] = v
		}
		patch = map[string]any{"recurrenceOverrides": withOverride(e, occ, ov)}
	}

	if role == "owner" {
		// A private event: no one else is told; in the person's own calendar
		// no question, in a shared one always.
		if owner != "" {
			q := fmt.Sprintf("Change %q in %s: %s? %s", oneLine(cur.Title, 60), where(owner, calName), strings.Join(changes, "; "), seenByAll)
			if occ != "" {
				q = fmt.Sprintf("Change only the meeting %q on %s in %s: %s? %s", oneLine(cur.Title, 60), whenText(oldStart, cur.ShowWithoutTime), where(owner, calName),
					strings.Join(changes, "; "), seenByAll)
			}
			res, ok, err := t.confirm(ctx, req, q+"\n\nNo one is mailed.", acct+"|"+e.ID+"|"+e.Start+"|"+e.Title+"|"+occ+"|"+fmt.Sprint(e.RecurrenceOver)+"|"+fmt.Sprint(patch))
			if err != nil || res != nil {
				return res, EventChangeOut{}, err
			}
			if !ok {
				return nil, EventChangeOut{}, fmt.Errorf("the person declined: nothing was changed")
			}
		}
		r, err := t.JMAP.Invoke(ctx, tok, s, calUsing, jmap.Call{Method: "CalendarEvent/set", Args: map[string]any{"accountId": acct, "update": map[string]any{e.ID: patch}}})
		if err != nil {
			return nil, EventChangeOut{}, err
		}
		if err := jmap.SetErrors(r[0]); err != nil {
			return nil, EventChangeOut{}, err
		}
		if owner != "" {
			return nil, EventChangeOut{e.ID, "Changed in " + where(owner, calName) + " with the person's confirmation; no one was mailed."}, nil
		}
		what := "Changed in the person's calendar (private: no one else was told)."
		if occ != "" {
			what = "Changed only the meeting of " + occ + " in the person's calendar (private: no one else was told)."
		} else if series {
			what = "Changed every meeting of the series in the person's calendar (private: no one else was told)."
		}
		return nil, EventChangeOut{e.ID, what}, nil
	}

	if !oldEnd.After(time.Now()) {
		return nil, EventChangeOut{}, fmt.Errorf("this meeting is over: change it in your calendar app if needed (no update would be sent)")
	}
	who, outside := t.attendeesOf(ctx, e, self)
	var first string
	switch {
	case occ != "":
		first = fmt.Sprintf("Change only the meeting %q on %s: %s? %d attendee(s) get the updated series by email", oneLine(cur.Title, 60), whenText(oldStart, cur.ShowWithoutTime),
			strings.Join(changes, "; "), len(who))
	case series:
		first = fmt.Sprintf("Change ALL meetings of %q (%s): %s? %d attendee(s) get the update by email", oneLine(e.Title, 60), ruleText(e), strings.Join(changes, "; "), len(who))
	default:
		first = fmt.Sprintf("Change the meeting %q: %s? %d attendee(s) get the update by email", oneLine(e.Title, 60), strings.Join(changes, "; "), len(who))
	}
	if outside > 0 {
		first += fmt.Sprintf(" (%d OUTSIDE the company)", outside)
	}
	if owner != "" {
		first = fmt.Sprintf("In %s, as %s: %s", where(owner, calName), oneLine(owner, 80), first)
	}
	var b strings.Builder
	b.WriteString(first + ".\n\nWas: " + whenText(oldStart, cur.ShowWithoutTime) + "\n")
	if owner != "" {
		b.WriteString("The update goes out in " + oneLine(owner, 80) + "'s name. " + seenByAll + "\n")
	}
	for _, a := range who {
		b.WriteString("Attendee: " + a + "\n")
	}
	if t.outside.recent(me.Username) {
		b.WriteString("\nWARNING: your assistant read email from outside the company in the last 30 minutes; make sure this is what you asked for.\n")
	}
	res, ok, err := t.confirm(ctx, req, b.String(), acct+"|"+e.ID+"|"+e.Start+"|"+e.Duration+"|"+e.Title+"|"+occ+"|"+fmt.Sprint(e.RecurrenceOver)+"|"+fmt.Sprint(patch))
	if err != nil || res != nil {
		return res, EventChangeOut{}, err
	}
	if !ok {
		return nil, EventChangeOut{}, fmt.Errorf("the person declined: the meeting wasn't changed")
	}
	r, err := t.JMAP.Invoke(ctx, tok, s, calUsing, jmap.Call{Method: "CalendarEvent/set", Reschedule: true, Args: map[string]any{"accountId": acct,
		"update": map[string]any{e.ID: patch}, "sendSchedulingMessages": true}})
	if err != nil {
		return nil, EventChangeOut{}, err
	}
	if err := jmap.SetErrors(r[0]); err != nil {
		return nil, EventChangeOut{}, err
	}
	return nil, EventChangeOut{e.ID, "Changed; the mail server sends the update to the attendees."}, nil
}

// sharedCal: for an event in a calendar someone shared with the person,
// whose calendar it is and its name; the change is refused unless the
// share lets the person change any event there (mayWriteAll). "" owner:
// the person's own calendar.
func (t *Tools) sharedCal(ctx context.Context, tok string, s *jmap.Session, acct string, e calEvent) (owner, name string, err error) {
	if acct == s.PrimaryAccounts[jmap.Calendars] {
		return "", "", nil
	}
	owner = s.Accounts[acct].Name
	cs, err := t.calendarsOf(ctx, tok, s, acct)
	if err != nil {
		return "", "", err
	}
	for _, c := range cs {
		if e.CalendarIDs[c.ID] && c.writeAll {
			return owner, c.Name, nil
		}
	}
	return "", "", fmt.Errorf("%s's calendar is shared with you for reading or adding only: its events can't be changed or cancelled here", oneLine(owner, 80))
}

// where: the place of a change, for a question.
func where(owner, cal string) string {
	if owner == "" {
		return "your calendar"
	}
	return fmt.Sprintf("%s's calendar %q", oneLine(owner, 80), oneLine(cal, 60))
}

const seenByAll = "Everyone who can see that calendar will see it."

// ---- event_cancel

type EventCancelIn struct {
	InCalendar
	ID         string `json:"id" jsonschema:"event id from event_list"`
	Occurrence string `json:"occurrence,omitempty" jsonschema:"for a repeating series: cancel only this meeting (the occurrence event_list gives); without it the WHOLE series"`
}

func (t *Tools) eventCancel(ctx context.Context, req *mcp.CallToolRequest, in EventCancelIn) (*mcp.CallToolResult, EventChangeOut, error) {
	tok, s, acct, e, err := t.getEvent(ctx, in.ID)
	if err != nil {
		return nil, EventChangeOut{}, err
	}
	me, _ := personOf(ctx)
	owner, calName, err := t.sharedCal(ctx, tok, s, acct, e)
	if err != nil {
		return nil, EventChangeOut{}, err
	}
	self := me.Email
	if owner != "" {
		self = owner
	}
	role, _ := e.role(self)
	if owner != "" && role != "owner" && role != "organizer" {
		return nil, EventChangeOut{}, fmt.Errorf("%s doesn't organize this meeting, so it can't be cancelled in their calendar here", oneLine(owner, 80))
	}
	if role == "attendee" {
		return nil, EventChangeOut{}, fmt.Errorf("you're an attendee, not the organizer: to say you can't come, use event_respond with decline")
	}
	if role != "owner" && role != "organizer" {
		return nil, EventChangeOut{}, fmt.Errorf("only the organizer can cancel this event (you are: %s)", role)
	}
	series, occ := e.repeats(), strings.TrimSpace(in.Occurrence)
	if occ != "" && !series {
		return nil, EventChangeOut{}, fmt.Errorf("this isn't a repeating event: cancel it without occurrence")
	}
	start, end, err := t.window(e)
	if err != nil {
		return nil, EventChangeOut{}, err
	}
	if occ != "" {
		o, err := t.occurrenceOf(e, occ)
		if err != nil {
			return nil, EventChangeOut{}, err
		}
		occ = o.RecurrenceID
		_, oe, _ := t.window(calEvent{Start: o.Start.Format(localFmt), TimeZone: e.TimeZone, Duration: o.Duration})
		start, end = o.Start, oe
	} else if series {
		if o, ok := t.nextOccurrence(e); ok {
			start, end = o.Start, time.Now().AddDate(10, 0, 0)
		}
	}
	send := role == "organizer"
	var b strings.Builder
	what := fmt.Sprintf("the meeting %q on %s", oneLine(e.Title, 60), whenText(start, e.ShowWithoutTime))
	switch {
	case occ != "":
		what = fmt.Sprintf("only the meeting %q on %s (the rest of the series stays)", oneLine(e.Title, 60), whenText(start, e.ShowWithoutTime))
	case series:
		what = fmt.Sprintf("the WHOLE series %q (%s, every meeting)", oneLine(e.Title, 60), ruleText(e))
	}
	if send {
		if !end.After(time.Now()) {
			return nil, EventChangeOut{}, fmt.Errorf("this meeting is over: remove it in your calendar app if needed (no cancellation would be sent)")
		}
		who, outside := t.attendeesOf(ctx, e, self)
		first := fmt.Sprintf("Cancel %s? %d attendee(s) get a cancellation by email", what, len(who))
		if owner != "" {
			first = fmt.Sprintf("In %s, as %s: cancel %s? %d attendee(s) get a cancellation by email", where(owner, calName), oneLine(owner, 80), what, len(who))
		}
		if outside > 0 {
			first += fmt.Sprintf(" (%d OUTSIDE the company)", outside)
		}
		b.WriteString(first + ", and it is removed from " + where(owner, calName) + ".\n\n")
		if owner != "" {
			b.WriteString("The cancellation goes out in " + oneLine(owner, 80) + "'s name. " + seenByAll + "\n")
		}
		for _, a := range who {
			b.WriteString("Attendee: " + a + "\n")
		}
	} else if occ != "" {
		fmt.Fprintf(&b, "Remove %s from %s?\n", what, where(owner, calName))
	} else if owner != "" {
		fmt.Fprintf(&b, "Delete %s from %s? It can't be restored. %s\n", what, where(owner, calName), seenByAll)
	} else {
		fmt.Fprintf(&b, "Delete your private event: %s, from your calendar? It can't be restored.\n", what)
	}
	if t.outside.recent(me.Username) {
		b.WriteString("\nWARNING: your assistant read email from outside the company in the last 30 minutes; make sure this is what you asked for.\n")
	}
	res, ok, err := t.confirm(ctx, req, b.String(), acct+"|"+e.ID+"|"+e.Start+"|"+e.Title+"|"+fmt.Sprint(send)+"|"+occ+"|"+fmt.Sprint(e.RecurrenceOver))
	if err != nil || res != nil {
		return res, EventChangeOut{}, err
	}
	if !ok {
		return nil, EventChangeOut{}, fmt.Errorf("the person declined: nothing was cancelled")
	}
	var call jmap.Call
	if occ != "" {
		patch := map[string]any{"recurrenceOverrides": withOverride(e, occ, map[string]any{"excluded": true})}
		call = jmap.Call{Method: "CalendarEvent/set", Args: map[string]any{"accountId": acct, "update": map[string]any{e.ID: patch}}}
		if send {
			call.Reschedule = true
			call.Args["sendSchedulingMessages"] = true
		}
	} else {
		call = jmap.Call{Method: "CalendarEvent/set", Cancel: true, Args: map[string]any{"accountId": acct, "destroy": []string{e.ID}, "sendSchedulingMessages": send}}
	}
	r, err := t.JMAP.Invoke(ctx, tok, s, calUsing, call)
	if err != nil {
		return nil, EventChangeOut{}, err
	}
	if err := jmap.SetErrors(r[0]); err != nil {
		return nil, EventChangeOut{}, err
	}
	switch {
	case occ != "" && send:
		return nil, EventChangeOut{e.ID, "Cancelled the meeting of " + occ + "; the mail server sends the attendees a cancellation for that date. The rest of the series stays."}, nil
	case occ != "":
		return nil, EventChangeOut{e.ID, "Removed the meeting of " + occ + " from the person's calendar; the rest of the series stays."}, nil
	case send:
		return nil, EventChangeOut{e.ID, "Cancelled; the mail server sends the cancellation to the attendees."}, nil
	}
	if owner != "" {
		return nil, EventChangeOut{e.ID, "Deleted from " + where(owner, calName) + " with the person's confirmation."}, nil
	}
	return nil, EventChangeOut{e.ID, "Deleted from the person's calendar."}, nil
}

// personOf: who is asking (a variable so tests can be someone).
var personOf = auth.Person

// durText: an ISO duration as people say it ("30 min", "1 h 30 min", "2 days").
func durText(d string) string {
	m := reDur.FindStringSubmatch(d)
	if m == nil {
		return d
	}
	var parts []string
	for i, unit := range []string{"week", "day", "h", "min", "s"} {
		n, _ := strconv.Atoi(m[i+1])
		if n == 0 {
			continue
		}
		switch {
		case unit == "week" || unit == "day":
			if n > 1 {
				unit += "s"
			}
		}
		parts = append(parts, fmt.Sprintf("%d %s", n, unit))
	}
	if len(parts) == 0 {
		return "0 min"
	}
	return strings.Join(parts, " ")
}
