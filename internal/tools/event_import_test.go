package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func inviteMail(blob string) []any {
	return []any{map[string]any{"id": "m1", "from": []map[string]any{{"email": "bob@other.example"}}, "subject": "Invitation",
		"bodyStructure": map[string]any{"type": "multipart/mixed", "subParts": []any{
			map[string]any{"type": "multipart/alternative", "subParts": []any{
				map[string]any{"partId": "1", "type": "text/plain", "blobId": "t1"},
				map[string]any{"partId": "2", "type": "text/calendar", "blobId": blob}}}}}}}
}

func outsideInvite(attendee string) map[string]any {
	return map[string]any{"@type": "Event", "uid": "u1", "title": "Partner sync", "start": "2099-12-03T10:00:00", "timeZone": "Europe/Berlin", "duration": "PT30M",
		"organizerCalendarAddress": "mailto:bob@other.example",
		"alerts":                   map[string]any{"a": map[string]any{"action": "email"}},
		"participants": map[string]any{
			"o": map[string]any{"calendarAddress": "mailto:bob@other.example", "roles": map[string]any{"owner": true}},
			"x": map[string]any{"calendarAddress": "mailto:" + attendee, "roles": map[string]any{"attendee": true}, "participationStatus": "accepted"}}}
}

func TestImportInvitation(t *testing.T) {
	asPerson(t, "me@co.example")
	f := &fakeCal{mails: inviteMail("B2"), parsed: map[string]any{"B2": outsideInvite("me@co.example")}}
	var asked string
	act := "decline"
	cs := inviteSession(t, f, func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = r.Params.Message
		return &mcp.ElicitResult{Action: act}, nil
	})
	if r := calCall(t, cs, "event_import", map[string]any{"id": "m1"}); !r.IsError || len(f.sets) != 0 {
		t.Fatalf("declined: %q, %v", resultText(r), f.sets)
	}
	first, _, _ := strings.Cut(asked, "\n")
	if !strings.Contains(first, `"Partner sync"`) || !strings.Contains(first, "bob@other.example (OUTSIDE the company)") || !strings.Contains(first, "Nothing is sent") {
		t.Errorf("first line: %q", first)
	}
	act = "accept"
	if r := calCall(t, cs, "event_import", map[string]any{"id": "m1"}); r.IsError {
		t.Fatal(resultText(r))
	}
	a := f.sets[0]
	e := a["create"].(map[string]any)["e"].(map[string]any)
	if a["sendSchedulingMessages"] != false || e["alerts"] != nil || e["calendarIds"] == nil ||
		e["participants"].(map[string]any)["x"].(map[string]any)["participationStatus"] != "needs-action" {
		t.Fatalf("set: %v", a)
	}
}

func TestImportRefusals(t *testing.T) {
	asPerson(t, "me@co.example")
	cs := func(f *fakeCal) *mcp.ClientSession {
		return inviteSession(t, f, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			t.Error("asked")
			return &mcp.ElicitResult{Action: "accept"}, nil
		})
	}
	past := outsideInvite("me@co.example")
	past["start"] = "2001-01-01T10:00:00"
	for name, f := range map[string]*fakeCal{
		"not an attendee": {mails: inviteMail("B2"), parsed: map[string]any{"B2": outsideInvite("someone@co.example")}},
		"no calendar":     {mails: []any{map[string]any{"id": "m1", "bodyStructure": map[string]any{"type": "text/plain", "blobId": "t"}}}},
		"over":            {mails: inviteMail("B2"), parsed: map[string]any{"B2": past}},
		"unreadable":      {mails: inviteMail("B2"), parsed: map[string]any{}},
	} {
		if r := calCall(t, cs(f), "event_import", map[string]any{"id": "m1"}); !r.IsError || len(f.sets) != 0 {
			t.Errorf("%s: not refused (%q)", name, resultText(r))
		}
	}
}
