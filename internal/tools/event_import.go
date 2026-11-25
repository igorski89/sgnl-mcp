package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/igorski89/sgnl-mcp/internal/jmap"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---- event_import: an invitation received by mail (its text/calendar
// part) into the person's calendar, after they confirm. The mail server
// doesn't add invitations from outside by itself (igor, 2026-11-25), so
// this is how they get in; event_respond then answers them. Nothing is
// mailed by the import.

type EventImportIn struct {
	ID string `json:"id" jsonschema:"message id of the mail with the invitation"`
}

// importFields: what is taken over from the invitation; anything else
// (alerts, links, the sender's own data) is left out.
var importFields = []string{"@type", "uid", "title", "description", "start", "timeZone", "duration", "showWithoutTime",
	"locations", "virtualLocations", "status", "sequence", "organizerCalendarAddress", "participants", "recurrenceRules", "excludedRecurrenceRules", "recurrenceOverrides"}

type bodyPart struct {
	PartID  string     `json:"partId"`
	BlobID  string     `json:"blobId"`
	Type    string     `json:"type"`
	Name    string     `json:"name"`
	SubPart []bodyPart `json:"subParts"`
}

func calendarPart(p bodyPart) *bodyPart {
	if strings.HasPrefix(strings.ToLower(p.Type), "text/calendar") || strings.HasPrefix(strings.ToLower(p.Type), "application/ics") ||
		strings.HasSuffix(strings.ToLower(p.Name), ".ics") {
		if p.BlobID != "" {
			return &p
		}
	}
	for _, s := range p.SubPart {
		if c := calendarPart(s); c != nil {
			return c
		}
	}
	return nil
}

func (t *Tools) eventImport(ctx context.Context, req *mcp.CallToolRequest, in EventImportIn) (*mcp.CallToolResult, EventCreateOut, error) {
	mtok, ms, macct, err := t.mailSession(ctx)
	if err != nil {
		return nil, EventCreateOut{}, err
	}
	r, err := t.JMAP.Invoke(ctx, mtok, ms, mailUsing, jmap.Call{Method: "Email/get", Args: map[string]any{"accountId": macct, "ids": []string{in.ID},
		"properties": []string{"id", "from", "subject", "bodyStructure"}}})
	if err != nil {
		return nil, EventCreateOut{}, err
	}
	var got struct {
		List []struct {
			From          []address `json:"from"`
			BodyStructure bodyPart  `json:"bodyStructure"`
		} `json:"list"`
	}
	if err := json.Unmarshal(r[0], &got); err != nil {
		return nil, EventCreateOut{}, err
	}
	if len(got.List) == 0 {
		return nil, EventCreateOut{}, fmt.Errorf("no message %q", oneLine(in.ID, 80))
	}
	part := calendarPart(got.List[0].BodyStructure)
	if part == nil {
		return nil, EventCreateOut{}, fmt.Errorf("this mail has no invitation (no calendar part)")
	}

	tok, s, acct, err := t.calSession(ctx)
	if err != nil {
		return nil, EventCreateOut{}, err
	}
	if acct != macct {
		return nil, EventCreateOut{}, fmt.Errorf("mail and calendar are different accounts here: can't import")
	}
	r, err = t.JMAP.Invoke(ctx, tok, s, calUsing, jmap.Call{Method: "CalendarEvent/parse", Args: map[string]any{"accountId": acct, "blobIds": []string{part.BlobID}}})
	if err != nil {
		return nil, EventCreateOut{}, err
	}
	var parsed struct {
		Parsed map[string]json.RawMessage `json:"parsed"`
	}
	_ = json.Unmarshal(r[0], &parsed)
	raw, ok := parsed.Parsed[part.BlobID]
	if !ok {
		return nil, EventCreateOut{}, fmt.Errorf("the invitation in this mail can't be read")
	}
	// One event (some servers wrap it in a list or a Group).
	var objs []map[string]any
	if json.Unmarshal(raw, &objs) != nil {
		var one map[string]any
		_ = json.Unmarshal(raw, &one)
		if entries, ok := one["entries"].([]any); ok {
			for _, x := range entries {
				if m, ok := x.(map[string]any); ok {
					objs = append(objs, m)
				}
			}
		} else if one != nil {
			objs = []map[string]any{one}
		}
	}
	if len(objs) != 1 {
		return nil, EventCreateOut{}, fmt.Errorf("the invitation holds %d events: only single events can be imported here", len(objs))
	}
	src := objs[0]
	e := map[string]any{}
	for _, k := range importFields {
		if v, ok := src[k]; ok && v != nil {
			e[k] = v
		}
	}
	var ev calEvent
	b, _ := json.Marshal(e)
	_ = json.Unmarshal(b, &ev)

	me, _ := personOf(ctx)
	role, mine := ev.role(me.Email)
	if role != "attendee" {
		return nil, EventCreateOut{}, fmt.Errorf("you aren't an attendee of this invitation (as far as it says), so it isn't imported")
	}
	org, _ := strings.CutPrefix(strings.ToLower(ev.Organizer), "mailto:")
	if org == "" {
		return nil, EventCreateOut{}, fmt.Errorf("the invitation has no organizer")
	}
	start, end, err := t.window(ev)
	if err != nil {
		return nil, EventCreateOut{}, err
	}
	if !end.After(time.Now()) && !ev.repeats() {
		return nil, EventCreateOut{}, fmt.Errorf("this event is over: not imported")
	}
	// Answers aren't taken over: the person hasn't answered yet.
	if ps, ok := e["participants"].(map[string]any); ok {
		if p, ok := ps[mine].(map[string]any); ok {
			p["participationStatus"] = "needs-action"
		}
	}

	orgShown := oneLine(org, 100)
	if !t.internal(ctx, org) {
		orgShown += " (OUTSIDE the company)"
	}
	from := ""
	if f := got.List[0].From; len(f) > 0 {
		from = f[0].Email
	}
	first := fmt.Sprintf("Add the invitation %q on %s from %s to your calendar? Nothing is sent; you answer it separately.", oneLine(ev.Title, 60), whenText(start, ev.ShowWithoutTime), orgShown)
	var q strings.Builder
	q.WriteString(first + "\n\n")
	if from != "" && !strings.EqualFold(from, org) {
		q.WriteString("Note: the mail came from " + oneLine(from, 100) + ", not from the organizer.\n")
	}
	who, outside := t.attendeesOf(ctx, ev, me.Email)
	fmt.Fprintf(&q, "Other attendees: %d", len(who))
	if outside > 0 {
		fmt.Fprintf(&q, " (%d OUTSIDE the company)", outside)
	}
	q.WriteString("\n")
	if ev.repeats() {
		q.WriteString("It repeats.\n")
	}
	res, ok, err := t.confirm(ctx, req, q.String(), in.ID+"|"+part.BlobID)
	if err != nil || res != nil {
		return res, EventCreateOut{}, err
	}
	if !ok {
		return nil, EventCreateOut{}, fmt.Errorf("the person declined: nothing was added")
	}
	cal, err := t.defaultCalendar(ctx, tok, s, acct)
	if err != nil {
		return nil, EventCreateOut{}, err
	}
	e["calendarIds"] = map[string]bool{cal: true}
	r, err = t.JMAP.Invoke(ctx, tok, s, calUsing, jmap.Call{Method: "CalendarEvent/set", Import: true, Args: map[string]any{"accountId": acct,
		"create": map[string]any{"e": e}, "sendSchedulingMessages": false}})
	if err != nil {
		return nil, EventCreateOut{}, err
	}
	if err := jmap.SetErrors(r[0]); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return nil, EventCreateOut{}, fmt.Errorf("this invitation is already in your calendar (nothing was added): find it with event_list")
		}
		return nil, EventCreateOut{}, err
	}
	var created struct {
		Created map[string]struct{ ID string } `json:"created"`
	}
	_ = json.Unmarshal(r[0], &created)
	id := created.Created["e"].ID
	if id == "" {
		return nil, EventCreateOut{}, fmt.Errorf("the calendar didn't add the event")
	}
	note := "Added to the person's calendar, not answered yet (nothing was sent). To answer: event_respond."
	if ev.repeats() {
		note = "Added to the person's calendar (a repeating event: answer it in the calendar app; nothing was sent)."
	}
	return nil, EventCreateOut{id, note}, nil
}
