package tools

import "testing"

func TestHTMLToText(t *testing.T) {
	in := `<html><head><style>p{}</style></head><body><p>Hello &amp; welcome</p><script>x()</script><div>Line<br>two</div></body></html>`
	if got, want := HTMLToText(in), "Hello & welcome\nLine\ntwo"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestAddrs(t *testing.T) {
	if got := addrs([]address{{"", "a@x"}, {"B", "b@x"}}); got != "a@x, B <b@x>" {
		t.Errorf("got %q", got)
	}
}
