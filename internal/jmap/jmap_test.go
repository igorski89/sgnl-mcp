package jmap

import (
	"context"
	"strings"
	"testing"
)

func refused(t *testing.T, call Call, want string) {
	t.Helper()
	c := New("http://127.0.0.1:1/never-called")
	_, err := c.Invoke(context.Background(), "t", &Session{APIURL: "http://127.0.0.1:1/"}, nil, call)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("%s: got %v, want %q", call.Method, err, want)
	}
}

func TestNeverSends(t *testing.T) {
	for _, m := range []string{"EmailSubmission/set", "EmailSubmission/get", "Mailbox/set", "Email/import", "VacationResponse/set",
		"SieveScript/set", "Identity/set", "ContactCard/set", "AddressBook/set", "Calendar/set", "Principal/set"} {
		refused(t, Call{Method: m}, "not allowed")
	}
}

func TestNoDestroy(t *testing.T) {
	refused(t, Call{Method: "Email/set", Args: map[string]any{"destroy": []string{"x"}}}, "destroy")
	refused(t, Call{Method: "CalendarEvent/set", Args: map[string]any{"destroy": []string{"x"}}}, "destroy")
	refused(t, Call{Method: "CalendarEvent/set", Args: map[string]any{"destroy": []string{"x"}}, DestroyDraft: true}, "destroy")
}

func TestNoInvitations(t *testing.T) {
	ev := map[string]any{"e": map[string]any{"title": "x", "participants": map[string]any{}}}
	refused(t, Call{Method: "CalendarEvent/set", Args: map[string]any{"create": ev}}, "participants")
	up := map[string]any{"e": map[string]any{"participants/abc": map[string]any{}}}
	refused(t, Call{Method: "CalendarEvent/set", Args: map[string]any{"update": up}}, "participants")
}

func TestDraftReplaceAllowed(t *testing.T) {
	if err := check(Call{Method: "Email/set", Args: map[string]any{"destroy": []string{"x"}}, DestroyDraft: true}); err != nil {
		t.Errorf("draft replace: %v", err)
	}
}

func sub(extra map[string]any) map[string]any {
	m := map[string]any{"emailId": "e", "identityId": "i"}
	for k, v := range extra {
		m[k] = v
	}
	return map[string]any{"accountId": "a", "create": map[string]any{"s": m}}
}

// Sending: only as mail_send does it, and only when the call says so.
func TestSendOnlyAsMailSend(t *testing.T) {
	ok := sub(nil)
	ok["onSuccessUpdateEmail"] = map[string]any{"#s": map[string]any{"keywords/$draft": nil, "mailboxIds/d": nil, "mailboxIds/s": true}}
	if err := check(Call{Method: "EmailSubmission/set", Args: ok, Send: true}); err != nil {
		t.Errorf("mail_send's call refused: %v", err)
	}
	refused(t, Call{Method: "EmailSubmission/set", Args: ok}, "not allowed")
	for name, args := range map[string]map[string]any{
		"envelope (other recipients)": sub(map[string]any{"envelope": map[string]any{}}),
		"update":                      {"accountId": "a", "update": map[string]any{"x": map[string]any{"undoStatus": "canceled"}}},
		"destroy":                     {"accountId": "a", "destroy": []string{"x"}},
		"destroy on success":          func() map[string]any { a := sub(nil); a["onSuccessDestroyEmail"] = []string{"#s"}; return a }(),
		"other change on success": func() map[string]any {
			a := sub(nil)
			a["onSuccessUpdateEmail"] = map[string]any{"#s": map[string]any{"keywords/$flagged": true}}
			return a
		}(),
		"two mails":   {"accountId": "a", "create": map[string]any{"a": map[string]any{"emailId": "e", "identityId": "i"}, "b": map[string]any{"emailId": "f", "identityId": "i"}}},
		"no identity": {"accountId": "a", "create": map[string]any{"s": map[string]any{"emailId": "e"}}},
	} {
		if err := check(Call{Method: "EmailSubmission/set", Args: args, Send: true}); err == nil || !strings.Contains(err.Error(), "send") {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

// Events with participants (which send invitations) only as event_invite.
func TestInviteOnlyAsEventInvite(t *testing.T) {
	att := func(mod func(p map[string]any)) map[string]any {
		p := map[string]any{"@type": "Participant", "email": "h@co.example", "sendTo": map[string]string{"imip": "mailto:h@co.example"},
			"roles": map[string]bool{"attendee": true}, "participationStatus": "needs-action", "expectReply": true}
		if mod != nil {
			mod(p)
		}
		return map[string]any{"accountId": "A", "sendSchedulingMessages": true,
			"create": map[string]any{"e": map[string]any{"title": "x", "participants": map[string]any{"a0": p}}}}
	}
	if err := check(Call{Method: "CalendarEvent/set", Invite: true, Args: att(nil)}); err != nil {
		t.Fatalf("a valid invite refused: %v", err)
	}
	if check(Call{Method: "CalendarEvent/set", Args: att(nil)}) == nil {
		t.Error("participants without Invite allowed")
	}
	if check(Call{Method: "CalendarEvent/set", Args: map[string]any{"accountId": "A", "sendSchedulingMessages": true,
		"create": map[string]any{"e": map[string]any{"title": "x"}}}}) == nil {
		t.Error("scheduling messages without Invite allowed")
	}
	for name, mod := range map[string]func(p map[string]any){
		"organizer role": func(p map[string]any) { p["roles"] = map[string]bool{"owner": true} },
		"other inbox":    func(p map[string]any) { p["sendTo"] = map[string]string{"imip": "mailto:evil@x.example"} },
		"other method":   func(p map[string]any) { p["sendTo"] = map[string]string{"web": "https://x"} },
		"unknown field":  func(p map[string]any) { p["delegatedTo"] = map[string]bool{"x": true} },
	} {
		if check(Call{Method: "CalendarEvent/set", Invite: true, Args: att(mod)}) == nil {
			t.Errorf("%s allowed", name)
		}
	}
	two := att(nil)
	two["create"].(map[string]any)["f"] = two["create"].(map[string]any)["e"]
	if check(Call{Method: "CalendarEvent/set", Invite: true, Args: two}) == nil {
		t.Error("two events in one invite allowed")
	}
	up := att(nil)
	up["update"] = map[string]any{"old": map[string]any{"title": "y"}}
	if check(Call{Method: "CalendarEvent/set", Invite: true, Args: up}) == nil {
		t.Error("an update alongside allowed")
	}
	noSched := att(nil)
	delete(noSched, "sendSchedulingMessages")
	if check(Call{Method: "CalendarEvent/set", Invite: true, Args: noSched}) == nil {
		t.Error("an invite without scheduling allowed")
	}
	rt := att(nil)
	rt["create"].(map[string]any)["e"].(map[string]any)["replyTo"] = map[string]string{"imip": "mailto:evil@x.example"}
	if check(Call{Method: "CalendarEvent/set", Invite: true, Args: rt}) == nil {
		t.Error("a replyTo from the gateway allowed")
	}
}
