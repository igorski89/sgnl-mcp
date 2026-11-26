package office

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/igorski89/sgnl-mcp/internal/textx"
)

const md = `# Q4 plan

Intro with **bold** and *italic* & <angle> text,
on two lines.

## Goals

- First goal
- Second goal

1. Step one
2. Step two

| Item | Cost |
|------|------|
| Rent | 1200 |
| Power | 80 |

Closing words.`

func TestDocxRoundTrip(t *testing.T) {
	b, err := Docx(md, "Q4 plan")
	if err != nil {
		t.Fatal(err)
	}
	got, err := textx.Extract("x.docx", "", b, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"Q4 plan", "Intro with bold and italic & <angle> text, on two lines.", "First goal", "Step two", "Rent", "1200", "Closing words."} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
	doc := part(t, b, "word/document.xml")
	if !strings.Contains(doc, `<w:pStyle w:val="Heading1"/>`) || !strings.Contains(doc, `<w:numId w:val="2"/>`) || !strings.Contains(doc, "<w:b/>") || !strings.Contains(doc, "<w:tbl>") {
		t.Errorf("structure: %s", doc)
	}
	if strings.Contains(strings.ToLower(string(b)), "vbaproject") {
		t.Error("macros?")
	}
	save(t, "t.docx", b)
}

func TestXlsxRoundTrip(t *testing.T) {
	b, err := Xlsx([]Sheet{
		{Name: "Budget", Rows: [][]any{{"Item", "Cost", "When", "Paid"}, {"Rent", 1200.5, "2026-01-15", true}, {"=HYPERLINK(\"http://evil\")", 80.0, nil, false}}},
		{Name: "Notes/Q4", Rows: [][]any{{"x"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := textx.Extract("x.xlsx", "", b, false)
	if err != nil {
		t.Fatal(err)
	}
	want := "## Sheet: Budget\nItem\tCost\tWhen\tPaid\nRent\t1200.5\t2026-01-15\tTRUE\n=HYPERLINK(\"http://evil\")\t80\t\tFALSE\n\n## Sheet: Notes Q4\nx"
	if got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
	if s := part(t, b, "xl/worksheets/sheet1.xml"); strings.Contains(s, "<f>") {
		t.Error("a formula was written")
	}
	save(t, "t.xlsx", b)
}

func TestXlsxRefusals(t *testing.T) {
	for _, sh := range [][]Sheet{
		nil,
		{{Name: "a"}, {Name: "A"}},
		{{Rows: [][]any{{map[string]any{"x": 1}}}}},
		{{Rows: make([][]any, MaxRows+1)}},
	} {
		if _, err := Xlsx(sh); err == nil {
			t.Errorf("%v allowed", sh)
		}
	}
	if _, err := Docx(strings.Repeat("x", MaxText+1), ""); err == nil {
		t.Error("too long allowed")
	}
}

func part(t *testing.T, b []byte, name string) string {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if f.Name == name {
			rc, _ := f.Open()
			x, _ := io.ReadAll(rc)
			return string(x)
		}
	}
	t.Fatalf("no %s", name)
	return ""
}

// save: with OFFICE_OUT set, the files are written there (to open them
// in LibreOffice or Word by hand).
func save(t *testing.T, name string, b []byte) {
	if d := os.Getenv("OFFICE_OUT"); d != "" {
		_ = os.WriteFile(filepath.Join(d, name), b, 0o644)
	}
}
