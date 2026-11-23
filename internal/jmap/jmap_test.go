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
	for _, m := range []string{"EmailSubmission/set", "Mailbox/set", "Email/import", "VacationResponse/set",
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
