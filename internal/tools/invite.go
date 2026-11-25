package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/mail"
	"slices"
	"strings"
	"time"

	"github.com/igorski89/sgnl-mcp/internal/auth"
	"github.com/igorski89/sgnl-mcp/internal/jmap"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---- event_invite: an event with attendees; the mail server sends them
// invitations, only after the person confirms.

const maxAttendees = 20

type EventInviteIn struct {
	EventCreateIn
	Attendees []string `json:"attendees" jsonschema:"email addresses of the people to invite (at most 20)"`
}

type EventInviteOut struct {
	ID        string   `json:"id"`
	Attendees []string `json:"attendees"`
	Note      string   `json:"note"`
}

func (t *Tools) eventInvite(ctx context.Context, req *mcp.CallToolRequest, in EventInviteIn) (*mcp.CallToolResult, EventInviteOut, error) {
	e, err := t.buildEvent(in.EventCreateIn)
	if err != nil {
		return nil, EventInviteOut{}, err
	}
	me, _ := auth.Person(ctx)
	var who []string
	for _, a := range in.Attendees {
		addr, err := mail.ParseAddress(strings.TrimSpace(a))
		if err != nil {
			return nil, EventInviteOut{}, fmt.Errorf("attendee %q: not an email address", oneLine(a, 80))
		}
		k := strings.ToLower(addr.Address)
		if k == strings.ToLower(me.Email) || slices.Contains(who, k) {
			continue // the person is the organizer anyway
		}
		who = append(who, k)
	}
	if len(who) == 0 {
		return nil, EventInviteOut{}, fmt.Errorf("no one to invite: give the attendees' email addresses (for a private entry use event_create)")
	}
	if len(who) > maxAttendees {
		return nil, EventInviteOut{}, fmt.Errorf("at most %d attendees per event (got %d)", maxAttendees, len(who))
	}
	ps := map[string]any{}
	for i, a := range who {
		ps[fmt.Sprintf("a%d", i)] = map[string]any{"@type": "Participant", "email": a,
			"calendarAddress": "mailto:" + a, "roles": map[string]bool{"attendee": true},
			"participationStatus": "needs-action", "expectReply": true}
	}
	e["participants"] = ps

	// The question: everything on the first line (clients fold the rest).
	outside := 0
	tags := make([]string, len(who))
	for i, a := range who {
		if !t.internal(ctx, a) {
			outside++
			tags[i] = "  (OUTSIDE the company)"
		}
	}
	when := in.Start
	if d, err := time.Parse("2006-01-02T15:04", in.Start); err == nil {
		when = d.Format("Mon 2 Jan 2006, 15:04")
	} else if d, err := time.Parse("2006-01-02", in.Start); err == nil {
		when = d.Format("Mon 2 Jan 2006") + ", all day"
	}
	shown := make([]string, 0, 3)
	for _, a := range who[:min(3, len(who))] {
		shown = append(shown, oneLine(a, 80))
	}
	first := fmt.Sprintf("Invite %s to %q on %s", strings.Join(shown, ", "), oneLine(in.Title, 60), when)
	if len(who) > 3 {
		first += fmt.Sprintf(" and %d more", len(who)-3)
	}
	if outside > 0 {
		first += fmt.Sprintf(" (%d OUTSIDE the company)", outside)
	}
	var b strings.Builder
	b.WriteString(first + "? They get an invitation by email.\n\n")
	b.WriteString("Event: " + oneLine(in.Title, 200) + "\nWhen: " + when + " (" + fmt.Sprint(e["timeZone"]) + "), " + fmt.Sprint(e["duration"]) + "\n")
	if in.Location != "" {
		b.WriteString("Where: " + oneLine(in.Location, 200) + "\n")
	}
	for i, a := range who {
		b.WriteString("Attendee: " + oneLine(a, 200) + tags[i] + "\n")
	}
	if in.Description != "" {
		b.WriteString("Description begins: " + oneLine(in.Description, 300) + "\n")
	}
	var warn []string
	if outside > 0 {
		warn = append(warn, fmt.Sprintf("%d attendee(s) outside the company", outside))
	}
	if t.outside.recent(me.Username) {
		warn = append(warn, "your assistant read email from outside the company in the last 30 minutes; make sure this is what you asked for")
	}
	if len(warn) > 0 {
		b.WriteString("\nWARNING: " + strings.Join(warn, "; ") + ".\n")
	}
	bind, _ := json.Marshal(e)
	res, ok, err := t.confirm(ctx, req, b.String(), string(bind))
	if err != nil || res != nil {
		return res, EventInviteOut{}, err
	}
	if !ok {
		return nil, EventInviteOut{}, fmt.Errorf("the person declined: no event was created and no one was invited")
	}

	tok, s, acct, err := t.calSession(ctx)
	if err != nil {
		return nil, EventInviteOut{}, err
	}
	cal, err := t.defaultCalendar(ctx, tok, s, acct)
	if err != nil {
		return nil, EventInviteOut{}, err
	}
	e["calendarIds"] = map[string]bool{cal: true}
	r, err := t.JMAP.Invoke(ctx, tok, s, calUsing, jmap.Call{Method: "CalendarEvent/set", Invite: true,
		Args: map[string]any{"accountId": acct, "create": map[string]any{"e": e}, "sendSchedulingMessages": true}})
	if err != nil {
		return nil, EventInviteOut{}, err
	}
	if err := jmap.SetErrors(r[0]); err != nil {
		return nil, EventInviteOut{}, err
	}
	var got struct {
		Created map[string]struct {
			ID           string         `json:"id"`
			Participants map[string]any `json:"participants"`
		} `json:"created"`
	}
	_ = json.Unmarshal(r[0], &got)
	id := got.Created["e"].ID
	if id == "" {
		return nil, EventInviteOut{}, fmt.Errorf("the calendar didn't create the event")
	}
	// The mail server schedules only once it has made the person the
	// organizer, and then it returns the participants it changed. Without
	// them no invitation went out: say so rather than claim success.
	if len(got.Created["e"].Participants) == 0 {
		slog.Warn("invitations not scheduled", "user", me.Username, "event", id)
		return nil, EventInviteOut{}, fmt.Errorf("the event %s is in the calendar, but the mail server did not schedule the invitations: no one was invited. Tell the person, and don't retry", id)
	}
	l := len(who)
	slogInvite(me.Username, l, outside)
	return nil, EventInviteOut{id, who, "Event added to the person's calendar, and handed to the mail server to send the invitations. Replies will show in the calendar."}, nil
}

func slogInvite(user string, n, outside int) {
	slog.Info("invitations requested", "user", user, "attendees", n, "outside", outside)
}
