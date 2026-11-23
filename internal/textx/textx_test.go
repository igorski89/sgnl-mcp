package textx

import "testing"

func TestHTML(t *testing.T) {
	in := `<html><head><style>p{}</style></head><body><p>Hello &amp; welcome</p><script>x()</script><div>Line<br>two</div></body></html>`
	if got, want := HTML(in), "Hello & welcome\nLine\ntwo"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestRefusesBinary(t *testing.T) {
	if _, err := Extract("a.zip", "application/zip", []byte{1, 2}, false); err == nil {
		t.Error("zip accepted")
	}
}
