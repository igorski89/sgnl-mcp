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
	for _, m := range []string{"EmailSubmission/set", "EmailSubmission/get", "Email/import", "VacationResponse/set",
		"SieveScript/set", "Identity/set", "AddressBook/set", "Calendar/set", "Principal/set"} {
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
		p := map[string]any{"@type": "Participant", "email": "h@co.example", "calendarAddress": "mailto:h@co.example",
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
		"other inbox":    func(p map[string]any) { p["calendarAddress"] = "mailto:evil@x.example" },
		"other method":   func(p map[string]any) { p["calendarAddress"] = "https://x" },
		"no address":     func(p map[string]any) { delete(p, "calendarAddress") },
		"sendTo":         func(p map[string]any) { p["sendTo"] = map[string]string{"imip": "mailto:h@co.example"} },
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

// Answer, reschedule and cancel each pass only their own narrow shape.
func TestCalendarChangeGuards(t *testing.T) {
	up := func(patch map[string]any, send any) map[string]any {
		a := map[string]any{"accountId": "A", "update": map[string]any{"e1": patch}}
		if send != nil {
			a["sendSchedulingMessages"] = send
		}
		return a
	}
	ok := []Call{
		{Method: "CalendarEvent/set", Reply: true, Args: up(map[string]any{"participants/b/participationStatus": "declined"}, true)},
		{Method: "CalendarEvent/set", Reschedule: true, Args: up(map[string]any{"start": "2026-12-03T11:00:00", "title": "x"}, true)},
		{Method: "CalendarEvent/set", Cancel: true, Args: map[string]any{"accountId": "A", "destroy": []string{"e1"}, "sendSchedulingMessages": true}},
		{Method: "CalendarEvent/set", Cancel: true, Args: map[string]any{"accountId": "A", "destroy": []string{"e1"}, "sendSchedulingMessages": false}},
	}
	for i, c := range ok {
		if err := check(c); err != nil {
			t.Errorf("ok %d: %v", i, err)
		}
	}
	bad := map[string]Call{
		"reply other field":    {Method: "CalendarEvent/set", Reply: true, Args: up(map[string]any{"participants/b/participationStatus": "declined", "title": "x"}, true)},
		"reply other part":     {Method: "CalendarEvent/set", Reply: true, Args: up(map[string]any{"participants/b/email": "evil@x.example"}, true)},
		"reply bad answer":     {Method: "CalendarEvent/set", Reply: true, Args: up(map[string]any{"participants/b/participationStatus": "delegated"}, true)},
		"reply no send":        {Method: "CalendarEvent/set", Reply: true, Args: up(map[string]any{"participants/b/participationStatus": "declined"}, nil)},
		"reply nested":         {Method: "CalendarEvent/set", Reply: true, Args: up(map[string]any{"participants/b/x/participationStatus": "declined"}, true)},
		"resched attendees":    {Method: "CalendarEvent/set", Reschedule: true, Args: up(map[string]any{"participants": map[string]any{}}, true)},
		"resched organizer":    {Method: "CalendarEvent/set", Reschedule: true, Args: up(map[string]any{"organizerCalendarAddress": "mailto:x@x"}, true)},
		"resched two events":   {Method: "CalendarEvent/set", Reschedule: true, Args: map[string]any{"accountId": "A", "sendSchedulingMessages": true, "update": map[string]any{"e1": map[string]any{"title": "x"}, "e2": map[string]any{"title": "y"}}}},
		"resched create":       {Method: "CalendarEvent/set", Reschedule: true, Args: map[string]any{"accountId": "A", "sendSchedulingMessages": true, "create": map[string]any{"e": map[string]any{}}}},
		"cancel two":           {Method: "CalendarEvent/set", Cancel: true, Args: map[string]any{"accountId": "A", "destroy": []string{"e1", "e2"}}},
		"cancel with update":   {Method: "CalendarEvent/set", Cancel: true, Args: map[string]any{"accountId": "A", "destroy": []string{"e1"}, "update": map[string]any{}}},
		"two flags":            {Method: "CalendarEvent/set", Cancel: true, Reply: true, Args: map[string]any{"accountId": "A", "destroy": []string{"e1"}}},
		"flag elsewhere":       {Method: "Email/set", Cancel: true, Args: map[string]any{"accountId": "A", "destroy": []string{"m1"}}},
		"destroy without flag": {Method: "CalendarEvent/set", Args: map[string]any{"accountId": "A", "destroy": []string{"e1"}}},
		"send without flag":    {Method: "CalendarEvent/set", Args: up(map[string]any{"title": "x"}, true)},
	}
	for name, c := range bad {
		if err := check(c); err == nil {
			t.Errorf("%s: allowed", name)
		}
	}
}

func TestVacationOnlyWithFlag(t *testing.T) {
	up := func(p map[string]any) map[string]any {
		return map[string]any{"accountId": "A", "update": map[string]any{"singleton": p}}
	}
	if err := check(Call{Method: "VacationResponse/set", Vacation: true, Args: up(map[string]any{"isEnabled": true, "textBody": "x", "htmlBody": nil})}); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]Call{
		"no flag":   {Method: "VacationResponse/set", Args: up(map[string]any{"isEnabled": false})},
		"html":      {Method: "VacationResponse/set", Vacation: true, Args: up(map[string]any{"htmlBody": "<img src=x>"})},
		"other key": {Method: "VacationResponse/set", Vacation: true, Args: up(map[string]any{"forwardTo": "x"})},
		"create":    {Method: "VacationResponse/set", Vacation: true, Args: map[string]any{"accountId": "A", "create": map[string]any{"x": map[string]any{}}}},
		"elsewhere": {Method: "Email/set", Vacation: true, Args: map[string]any{"accountId": "A"}},
	} {
		if err := check(c); err == nil {
			t.Errorf("%s: allowed", name)
		}
	}
}

func TestImportNeverMails(t *testing.T) {
	ev := func(mod func(e map[string]any)) Call {
		e := map[string]any{"title": "x", "organizerCalendarAddress": "mailto:b@x.example", "participants": map[string]any{}, "calendarIds": map[string]bool{"c": true}}
		if mod != nil {
			mod(e)
		}
		return Call{Method: "CalendarEvent/set", Import: true, Args: map[string]any{"accountId": "A", "sendSchedulingMessages": false, "create": map[string]any{"e": e}}}
	}
	if err := check(ev(nil)); err != nil {
		t.Fatal(err)
	}
	send := ev(nil)
	send.Args["sendSchedulingMessages"] = true
	for name, c := range map[string]Call{
		"send":         send,
		"no organizer": ev(func(e map[string]any) { delete(e, "organizerCalendarAddress") }),
		"alerts":       ev(func(e map[string]any) { e["alerts"] = map[string]any{} }),
		"replyTo":      ev(func(e map[string]any) { e["replyTo"] = map[string]any{} }),
		"with invite":  {Method: "CalendarEvent/set", Import: true, Invite: true, Args: ev(nil).Args},
	} {
		if err := check(c); err == nil {
			t.Errorf("%s: allowed", name)
		}
	}
}

// Folders: create or rename one, by name and parent only; never delete.
func TestMailboxOnlyCreateOrRename(t *testing.T) {
	for name, c := range map[string]struct {
		args map[string]any
		ok   bool
	}{
		"create":        {map[string]any{"accountId": "A", "create": map[string]any{"f": map[string]any{"name": "Clients", "parentId": "in"}}}, true},
		"rename":        {map[string]any{"accountId": "A", "update": map[string]any{"te": map[string]any{"name": "Old"}}}, true},
		"destroy":       {map[string]any{"accountId": "A", "destroy": []string{"te"}}, false},
		"remove emails": {map[string]any{"accountId": "A", "onDestroyRemoveEmails": true, "update": map[string]any{"te": map[string]any{"name": "x"}}}, false},
		"role":          {map[string]any{"accountId": "A", "update": map[string]any{"te": map[string]any{"role": "inbox"}}}, false},
		"rights":        {map[string]any{"accountId": "A", "update": map[string]any{"te": map[string]any{"shareWith": map[string]any{}}}}, false},
		"two":           {map[string]any{"accountId": "A", "create": map[string]any{"a": map[string]any{"name": "a"}, "b": map[string]any{"name": "b"}}}, false},
		"empty":         {map[string]any{"accountId": "A", "update": map[string]any{"te": map[string]any{}}}, false},
	} {
		if err := check(Call{Method: "Mailbox/set", Args: c.args}); (err == nil) != c.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestContactCardGuard(t *testing.T) {
	ok := func(c Call) {
		t.Helper()
		if err := check(c); err != nil {
			t.Errorf("%v: %v", c.Args, err)
		}
	}
	ok(Call{Method: "ContactCard/set", Args: map[string]any{"accountId": "A", "create": map[string]any{"c": map[string]any{"@type": "Card", "uid": "u", "addressBookIds": map[string]bool{"a": true}, "name": map[string]any{}}}}})
	ok(Call{Method: "ContactCard/set", Args: map[string]any{"accountId": "A", "update": map[string]any{"c1": map[string]any{"emails": map[string]any{}}}}})
	ok(Call{Method: "ContactCard/set", DeleteContact: true, Args: map[string]any{"accountId": "A", "destroy": []string{"c1"}}})
	refused(t, Call{Method: "ContactCard/set", Args: map[string]any{"destroy": []string{"c1"}}}, "destroy")
	refused(t, Call{Method: "ContactCard/set", DeleteContact: true, Args: map[string]any{"destroy": []string{"c1", "c2"}}}, "destroy")
	refused(t, Call{Method: "ContactCard/set", Args: map[string]any{"update": map[string]any{"c1": map[string]any{"addressBookIds": map[string]bool{}}}}}, "addressBookIds")
	refused(t, Call{Method: "ContactCard/set", Args: map[string]any{"update": map[string]any{"c1": map[string]any{"media": map[string]any{}}}}}, "media")
	refused(t, Call{Method: "ContactCard/set", Args: map[string]any{"create": map[string]any{"a": map[string]any{"name": 1}, "b": map[string]any{"name": 2}}}}, "exactly one")
	ok(Call{Method: "ContactCard/set", Args: map[string]any{"accountId": "A", "create": map[string]any{"g": map[string]any{"kind": "group", "members": map[string]any{"urn:uuid:1": true}}}}})
	ok(Call{Method: "ContactCard/set", Args: map[string]any{"accountId": "A", "update": map[string]any{"g": map[string]any{"members": map[string]any{}}}}})
	refused(t, Call{Method: "ContactCard/set", Args: map[string]any{"update": map[string]any{"g": map[string]any{"members/urn:uuid:1": true}}}}, "members/")
	refused(t, Call{Method: "ContactCard/set", Args: map[string]any{"update": map[string]any{"g": map[string]any{"members": map[string]any{"u": false}}}}}, "member must be true")
	refused(t, Call{Method: "ContactCard/set", Args: map[string]any{"update": map[string]any{"g": map[string]any{"members": []any{"u"}}}}}, "whole object")
	refused(t, Call{Method: "ContactCard/set", Args: map[string]any{"create": map[string]any{"g": map[string]any{"kind": "org"}}}}, "kind")
	refused(t, Call{Method: "ContactCard/set", Args: map[string]any{"update": map[string]any{"g": map[string]any{"kind": "group"}}}}, "kind")
	refused(t, Call{Method: "Email/set", DeleteContact: true, Args: map[string]any{"destroy": []string{"x"}}}, "contact delete")
	refused(t, Call{Method: "AddressBook/set"}, "not allowed")
}

// One meeting of a series: overrides only whole, only time/title/place/text
// or excluded; never participants, on both the confirmed and private paths.
func TestOverridesGuard(t *testing.T) {
	ov := func(o map[string]any) map[string]any {
		return map[string]any{"accountId": "A", "sendSchedulingMessages": true, "update": map[string]any{"w": map[string]any{"recurrenceOverrides": o}}}
	}
	priv := func(p map[string]any) map[string]any {
		return map[string]any{"accountId": "A", "update": map[string]any{"w": p}}
	}
	if err := check(Call{Method: "CalendarEvent/set", Reschedule: true, Args: ov(map[string]any{"2099-01-20T10:00:00": map[string]any{"start": "2099-01-20T14:00:00"},
		"2099-01-27T10:00:00": map[string]any{"excluded": true}})}); err != nil {
		t.Errorf("move+cancel: %v", err)
	}
	if err := check(Call{Method: "CalendarEvent/set", Args: priv(map[string]any{"recurrenceOverrides": map[string]any{"2099-01-27T10:00:00": map[string]any{"excluded": true}}})}); err != nil {
		t.Errorf("private: %v", err)
	}
	for name, c := range map[string]Call{
		"participants in override": {Method: "CalendarEvent/set", Reschedule: true, Args: ov(map[string]any{"2099-01-20T10:00:00": map[string]any{"participants/x": map[string]any{}}})},
		"excluded false":           {Method: "CalendarEvent/set", Reschedule: true, Args: ov(map[string]any{"2099-01-20T10:00:00": map[string]any{"excluded": false}})},
		"bad key":                  {Method: "CalendarEvent/set", Reschedule: true, Args: ov(map[string]any{"next week": map[string]any{"title": "x"}})},
		"private pointer":          {Method: "CalendarEvent/set", Args: priv(map[string]any{"recurrenceOverrides/2099-01-20T10:00:00": map[string]any{"title": "x"}})},
		"private participants":     {Method: "CalendarEvent/set", Args: priv(map[string]any{"recurrenceOverrides": map[string]any{"2099-01-20T10:00:00": map[string]any{"participants": map[string]any{}}}})},
		"rule change":              {Method: "CalendarEvent/set", Reschedule: true, Args: map[string]any{"accountId": "A", "sendSchedulingMessages": true, "update": map[string]any{"w": map[string]any{"recurrenceRules": []any{}}}}},
	} {
		if err := check(c); err == nil {
			t.Errorf("%s: allowed", name)
		}
	}
}
