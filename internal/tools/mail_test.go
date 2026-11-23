package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAddrs(t *testing.T) {
	if got := addrs([]address{{"", "a@x"}, {"B", "b@x"}}); got != "a@x, B <b@x>" {
		t.Errorf("got %q", got)
	}
}

func TestToAddrs(t *testing.T) {
	got := addrs(toAddrs([]string{"a@x", " Bea <b@x> ", ""}))
	if got != "a@x, Bea <b@x>" {
		t.Errorf("got %q", got)
	}
}

// A draft read back from the server has cc (or to) null when empty; Stalwart
// refuses null there, so the re-created draft must leave them out.
func TestDraftEmailLeavesOutEmptyFields(t *testing.T) {
	e := draftEmail(draft{subject: "s", body: "b"}, "d", address{Email: "me@x"})
	for _, k := range []string{"to", "cc", "inReplyTo", "references"} {
		if _, ok := e[k]; ok {
			t.Errorf("%s set on an empty draft: %v", k, e[k])
		}
	}
	e = draftEmail(draft{to: []address{{Email: "a@x"}}, inReplyTo: []string{"<m@x>"}, references: []string{"<m@x>"}}, "d", address{Email: "me@x"})
	if _, ok := e["cc"]; ok {
		t.Error("cc set though empty")
	}
	for _, k := range []string{"to", "inReplyTo", "references"} {
		if _, ok := e[k]; !ok {
			t.Errorf("%s missing", k)
		}
	}
	b, _ := json.Marshal(e)
	if strings.Contains(string(b), "null") {
		t.Errorf("null in draft JSON: %s", b)
	}
}
