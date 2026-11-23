package jmap

import (
	"context"
	"strings"
	"testing"
)

func TestWritesAreRefused(t *testing.T) {
	c := New("http://127.0.0.1:1/never-called")
	for _, m := range []string{"Email/set", "EmailSubmission/set", "Mailbox/set", "CalendarEvent/set", "Email/import"} {
		_, err := c.Invoke(context.Background(), "t", &Session{APIURL: "http://127.0.0.1:1/"}, nil, Call{Method: m})
		if err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Errorf("%s: got %v, want read-only refusal", m, err)
		}
	}
}
