package tools

import "testing"

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
