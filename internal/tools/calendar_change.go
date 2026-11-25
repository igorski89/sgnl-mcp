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
// server. Repeating events aren't changed yet (which occurrences a change
// would hit isn't settled), and nothing is mailed about an event that is
// over (the mail server would silently send nothing).

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

// ---- event_respond

type EventRespondIn struct {
	ID     string `json:"id" jsonschema:"event id from event_list"`
	Answer string `json:"answer" jsonschema:"accept, decline or maybe"`
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
	if e.repeats() {
		return nil, EventChangeOut{}, fmt.Errorf("this is a repeating event: answer it in your calendar app (not offered here yet)")
	}
	if e.Status == "cancelled" {
		return nil, EventChangeOut{}, fmt.Errorf("the organizer cancelled this event: there's nothing to answer")
	}
	start, end, err := t.window(e)
	if err != nil {
		return nil, EventChangeOut{}, err
	}
	if !end.After(time.Now()) {
		return nil, EventChangeOut{}, fmt.Errorf("this event is over: no answer would be sent")
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
	body := first + "\n\nWas: " + answer(e.Participants[mine].ParticipationStatus) + "\n"
	if t.outside.recent(me.Username) {
		body += "\nWARNING: your assistant read email from outside the company in the last 30 minutes; make sure this is what you asked for.\n"
	}
	res, ok, err := t.confirm(ctx, req, body, e.ID+"|"+mine+"|"+status+"|"+e.Start)
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
	return nil, EventChangeOut{e.ID, "Answered " + status + "; the mail server sends the answer to the organizer."}, nil
}

// ---- event_update

type EventUpdateIn struct {
	ID          string  `json:"id" jsonschema:"event id from event_list"`
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
	role, _ := e.role(me.Email)
	if role != "owner" && role != "organizer" {
		return nil, EventChangeOut{}, fmt.Errorf("only the organizer can change this event (you are: %s); to say you can't come, use event_respond", role)
	}
	if e.repeats() {
		return nil, EventChangeOut{}, fmt.Errorf("this is a repeating event: change it in your calendar app (not offered here yet)")
	}
	oldStart, oldEnd, err := t.window(e)
	if err != nil {
		return nil, EventChangeOut{}, err
	}
	patch := map[string]any{}
	var changes []string
	if in.Title != nil && *in.Title != e.Title {
		patch["title"] = *in.Title
		changes = append(changes, fmt.Sprintf("title → %q", oneLine(*in.Title, 80)))
	}
	newStart, allDay := oldStart, e.ShowWithoutTime
	if in.Start != "" {
		if d, err := time.ParseInLocation("2006-01-02T15:04", in.Start, oldStart.Location()); err == nil {
			newStart, allDay = d, false
		} else if d, err := time.ParseInLocation("2006-01-02", in.Start, oldStart.Location()); err == nil {
			newStart, allDay = d, true
		} else {
			return nil, EventChangeOut{}, fmt.Errorf("start: use YYYY-MM-DDTHH:MM or YYYY-MM-DD")
		}
	}
	dur := e.Duration
	if in.Minutes > 0 {
		if in.Minutes > 14*24*60 {
			return nil, EventChangeOut{}, fmt.Errorf("minutes: at most 14 days")
		}
		if allDay {
			dur = fmt.Sprintf("P%dD", max(1, in.Minutes/(24*60)))
		} else {
			dur = fmt.Sprintf("PT%dM", in.Minutes)
		}
	} else if allDay != e.ShowWithoutTime {
		if allDay {
			dur = "P1D"
		} else {
			dur = "PT60M"
		}
	}
	if !newStart.Equal(oldStart) || allDay != e.ShowWithoutTime || dur != e.Duration {
		patch["start"] = newStart.Format("2006-01-02T15:04:05")
		patch["showWithoutTime"] = allDay
		patch["duration"] = dur
		changes = append(changes, fmt.Sprintf("time %s → %s (%s)", whenText(oldStart, e.ShowWithoutTime), whenText(newStart, allDay), durText(dur)))
	}
	if in.Location != nil {
		patch["locations"] = map[string]any{"l": map[string]string{"@type": "Location", "name": *in.Location}}
		if *in.Location == "" {
			patch["locations"] = nil
		}
		changes = append(changes, fmt.Sprintf("place → %q", oneLine(*in.Location, 80)))
	}
	if in.Description != nil && *in.Description != e.Description {
		patch["description"] = *in.Description
		changes = append(changes, "description changed")
	}
	if len(patch) == 0 {
		return nil, EventChangeOut{}, fmt.Errorf("nothing to change")
	}

	if role == "owner" {
		// A private event: no one else is told, no question.
		r, err := t.JMAP.Invoke(ctx, tok, s, calUsing, jmap.Call{Method: "CalendarEvent/set", Args: map[string]any{"accountId": acct, "update": map[string]any{e.ID: patch}}})
		if err != nil {
			return nil, EventChangeOut{}, err
		}
		if err := jmap.SetErrors(r[0]); err != nil {
			return nil, EventChangeOut{}, err
		}
		return nil, EventChangeOut{e.ID, "Changed in the person's calendar (private: no one else was told)."}, nil
	}

	if !oldEnd.After(time.Now()) {
		return nil, EventChangeOut{}, fmt.Errorf("this meeting is over: change it in your calendar app if needed (no update would be sent)")
	}
	who, outside := t.attendeesOf(ctx, e, me.Email)
	first := fmt.Sprintf("Change the meeting %q: %s? %d attendee(s) get the update by email", oneLine(e.Title, 60), strings.Join(changes, "; "), len(who))
	if outside > 0 {
		first += fmt.Sprintf(" (%d OUTSIDE the company)", outside)
	}
	var b strings.Builder
	b.WriteString(first + ".\n\nWas: " + whenText(oldStart, e.ShowWithoutTime) + "\n")
	for _, a := range who {
		b.WriteString("Attendee: " + a + "\n")
	}
	if t.outside.recent(me.Username) {
		b.WriteString("\nWARNING: your assistant read email from outside the company in the last 30 minutes; make sure this is what you asked for.\n")
	}
	res, ok, err := t.confirm(ctx, req, b.String(), e.ID+"|"+e.Start+"|"+e.Duration+"|"+e.Title+"|"+fmt.Sprint(patch))
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

// ---- event_cancel

type EventCancelIn struct {
	ID string `json:"id" jsonschema:"event id from event_list"`
}

func (t *Tools) eventCancel(ctx context.Context, req *mcp.CallToolRequest, in EventCancelIn) (*mcp.CallToolResult, EventChangeOut, error) {
	tok, s, acct, e, err := t.getEvent(ctx, in.ID)
	if err != nil {
		return nil, EventChangeOut{}, err
	}
	me, _ := personOf(ctx)
	role, _ := e.role(me.Email)
	if role == "attendee" {
		return nil, EventChangeOut{}, fmt.Errorf("you're an attendee, not the organizer: to say you can't come, use event_respond with decline")
	}
	if role != "owner" && role != "organizer" {
		return nil, EventChangeOut{}, fmt.Errorf("only the organizer can cancel this event (you are: %s)", role)
	}
	if e.repeats() {
		return nil, EventChangeOut{}, fmt.Errorf("this is a repeating event: cancel it in your calendar app (not offered here yet)")
	}
	start, end, err := t.window(e)
	if err != nil {
		return nil, EventChangeOut{}, err
	}
	send := role == "organizer"
	var b strings.Builder
	if send {
		if !end.After(time.Now()) {
			return nil, EventChangeOut{}, fmt.Errorf("this meeting is over: remove it in your calendar app if needed (no cancellation would be sent)")
		}
		who, outside := t.attendeesOf(ctx, e, me.Email)
		first := fmt.Sprintf("Cancel the meeting %q on %s? %d attendee(s) get a cancellation by email", oneLine(e.Title, 60), whenText(start, e.ShowWithoutTime), len(who))
		if outside > 0 {
			first += fmt.Sprintf(" (%d OUTSIDE the company)", outside)
		}
		b.WriteString(first + ", and it is removed from your calendar.\n\n")
		for _, a := range who {
			b.WriteString("Attendee: " + a + "\n")
		}
	} else {
		fmt.Fprintf(&b, "Delete your private event %q on %s from your calendar? It can't be restored.\n", oneLine(e.Title, 60), whenText(start, e.ShowWithoutTime))
	}
	if t.outside.recent(me.Username) {
		b.WriteString("\nWARNING: your assistant read email from outside the company in the last 30 minutes; make sure this is what you asked for.\n")
	}
	res, ok, err := t.confirm(ctx, req, b.String(), e.ID+"|"+e.Start+"|"+e.Title+"|"+fmt.Sprint(send))
	if err != nil || res != nil {
		return res, EventChangeOut{}, err
	}
	if !ok {
		return nil, EventChangeOut{}, fmt.Errorf("the person declined: nothing was cancelled")
	}
	r, err := t.JMAP.Invoke(ctx, tok, s, calUsing, jmap.Call{Method: "CalendarEvent/set", Cancel: true, Args: map[string]any{"accountId": acct,
		"destroy": []string{e.ID}, "sendSchedulingMessages": send}})
	if err != nil {
		return nil, EventChangeOut{}, err
	}
	if err := jmap.SetErrors(r[0]); err != nil {
		return nil, EventChangeOut{}, err
	}
	if send {
		return nil, EventChangeOut{e.ID, "Cancelled; the mail server sends the cancellation to the attendees."}, nil
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
