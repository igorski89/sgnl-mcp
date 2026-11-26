package office

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/igorski89/sgnl-mcp/internal/textx"
)

// rawParts: each part's compressed bytes and CRC, by name.
func rawParts(t *testing.T, b []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		r, err := f.OpenRaw()
		if err != nil {
			t.Fatal(err)
		}
		x, _ := io.ReadAll(r)
		out[f.Name] = string(x) + "|" + string(rune(f.CRC32))
	}
	return out
}

// unchangedExcept: every part not named is byte-identical (compressed
// data and CRC); the named ones may differ; none disappear.
func unchangedExcept(t *testing.T, before, after []byte, changed ...string) {
	t.Helper()
	a, b := rawParts(t, before), rawParts(t, after)
	for name, v := range a {
		w, ok := b[name]
		switch {
		case !ok:
			t.Errorf("part %s is gone", name)
		case v != w && !containsStr(changed, name):
			t.Errorf("part %s changed", name)
		}
	}
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func testFiles(t *testing.T, ext string) []string {
	fs, _ := filepath.Glob(filepath.Join("testdata", "*"+ext))
	if len(fs) == 0 {
		t.Fatal("no test files")
	}
	return fs
}

func TestXlsxEditRealFiles(t *testing.T) {
	for _, f := range testFiles(t, ".xlsx") {
		t.Run(filepath.Base(f), func(t *testing.T) {
			data, _ := os.ReadFile(f)
			p, err := openPkg(data, ".xlsx")
			if err != nil {
				t.Fatal(err)
			}
			w, err := openWorkbook(p)
			if strings.Contains(f, "Strict") {
				if _, _, err := EditXlsx(data, []XlsxEdit{{Sheet: "Sheet1", SetCells: map[string]any{"A1": 1.0}}}); err == nil {
					t.Error("strict file edited")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			sh := w.sheets[0]
			orig, _ := p.read(sh.part)
			s, err := parseSheet(orig)
			if err != nil {
				t.Fatal(err)
			}
			edits := []XlsxEdit{{Sheet: sh.name, SetCells: map[string]any{"AZ200": "set by test"}, AppendRows: [][]any{{"appended", 42.0, "2026-03-01", true}}}}
			out, lines, err := EditXlsx(data, edits)
			if err != nil {
				t.Fatal(err)
			}
			if len(lines) < 2 || len(lines) > 3 {
				t.Errorf("lines: %v", lines)
			}
			// Other sheets change only where formula results were dropped;
			// the workbook only gains fullCalcOnLoad.
			others := []string{sh.part, "xl/styles.xml", "xl/workbook.xml"}
			p2x, _ := openPkg(out, ".xlsx")
			for _, o := range w.sheets {
				if o.part == sh.part {
					continue
				}
				ob, _ := p.read(o.part)
				exp, had, _ := dropFormulaValues(ob)
				if had {
					others = append(others, o.part)
					if got, _ := p2x.read(o.part); !bytes.Equal(got, exp) {
						t.Errorf("sheet %s changed beyond the dropped results", o.name)
					}
				}
			}
			if wb, _ := p2x.read("xl/workbook.xml"); len(lines) == 3 && !bytes.Contains(wb, []byte(`fullCalcOnLoad="1"`)) {
				t.Error("no fullCalcOnLoad")
			}
			unchangedExcept(t, data, out, others...)
			// Every original row is still there, byte for byte.
			p2, _ := openPkg(out, ".xlsx")
			nb, _ := p2.read(sh.part)
			// (Not the rows written to: an existing empty row right after the
			// last used one takes the appended row, and row 200.)
			for _, r := range s.rows {
				if r.r == s.lastRow()+1 || r.r == 200 {
					continue
				}
				want, _, _ := dropFormulaValues(orig[r.start:r.end]) // only cached formula results go
				if !bytes.Contains(nb, want) {
					t.Errorf("row %d changed", r.r)
				}
			}
			text, err := textx.Extract("x.xlsx", "", out, false)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"set by test", "appended\t42\t2026-03-01\tTRUE"} {
				if !strings.Contains(text, want) {
					t.Errorf("%q missing from:\n%s", want, text)
				}
			}
			// A new sheet; the old ones stay as they were.
			out2, _, err := EditXlsx(data, []XlsxEdit{{AddSheet: &Sheet{Name: "Added", Rows: [][]any{{"a", 1.0}}}}})
			if err != nil {
				t.Fatal(err)
			}
			unchangedExcept(t, data, out2, "xl/workbook.xml", "xl/_rels/workbook.xml.rels", "[Content_Types].xml")
			if text, _ := textx.Extract("x.xlsx", "", out2, false); !strings.Contains(text, "## Sheet: Added\na\t1") {
				t.Errorf("added sheet missing:\n%s", text)
			}
			// Formulas and merged cells are refused.
			for _, r := range s.rows {
				for _, c := range r.cells {
					ref := colName(c.col) + itoa(r.r)
					if c.formula {
						if _, _, err := EditXlsx(data, []XlsxEdit{{Sheet: sh.name, SetCells: map[string]any{ref: 1.0}}}); err == nil || !strings.Contains(err.Error(), "formula") {
							t.Errorf("formula %s overwritten: %v", ref, err)
						}
					}
				}
			}
			for _, m := range s.merged {
				if m[2] > m[0] || m[3] > m[1] {
					ref := colName(m[2]) + itoa(m[3])
					if _, _, err := EditXlsx(data, []XlsxEdit{{Sheet: sh.name, SetCells: map[string]any{ref: 1.0}}}); err == nil || !strings.Contains(err.Error(), "merged") {
						t.Errorf("merged %s set: %v", ref, err)
					}
				}
			}
			if d := os.Getenv("OFFICE_OUT"); d != "" {
				os.WriteFile(filepath.Join(d, "edited-"+filepath.Base(f)), out, 0o644)
				os.WriteFile(filepath.Join(d, "added-"+filepath.Base(f)), out2, 0o644)
			}
		})
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

func TestDocxEditRealFiles(t *testing.T) {
	for _, f := range testFiles(t, ".docx") {
		t.Run(filepath.Base(f), func(t *testing.T) {
			data, _ := os.ReadFile(f)
			p, _ := openPkg(data, ".docx")
			rels, _, _, _ := p.rels("")
			part := ""
			for _, r := range rels {
				if strings.HasSuffix(r[1], "/officeDocument") {
					part = r[0]
				}
			}
			orig, _ := p.read(part)
			doc, err := parseDocument(orig)
			if err != nil {
				t.Fatal(err)
			}
			// A word that occurs once in the whole text, inside one run.
			var full strings.Builder
			for _, x := range doc.texts {
				full.WriteString(x.text)
			}
			target := ""
			for _, x := range doc.texts {
				if x.field {
					continue
				}
				for _, w := range strings.Fields(x.text) {
					if len(w) > 4 && strings.Count(full.String(), w) == 1 {
						target = w
						break
					}
				}
				if target != "" {
					break
				}
			}
			reps := []TextReplace{}
			if target != "" {
				reps = append(reps, TextReplace{Old: target, New: "REPLACED-BY-TEST"})
			}
			if strings.Contains(f, "changeTracking_on") {
				if _, _, err := EditDocx(data, "x", nil); err == nil || !strings.Contains(err.Error(), "tracks changes") {
					t.Errorf("tracked document edited: %v", err)
				}
				return
			}
			// A field's text is never replaced.
			for _, x := range doc.texts {
				if x.field && strings.TrimSpace(x.text) != "" && strings.Count(full.String(), x.text) == 1 {
					if _, _, err := EditDocx(data, "", []TextReplace{{Old: x.text, New: "y"}}); err == nil || !strings.Contains(err.Error(), "field") {
						t.Errorf("field text %q replaced: %v", x.text, err)
					}
					break
				}
			}
			out, lines, err := EditDocx(data, "## Added by test\n\nA paragraph.\n\n- one\n- two\n\n| a | b |\n|---|---|\n| 1 | 2 |", reps)
			if err != nil {
				t.Fatal(err)
			}
			if len(lines) != len(reps)+1 {
				t.Errorf("lines: %v", lines)
			}
			unchangedExcept(t, data, out, part)
			p2, _ := openPkg(out, ".docx")
			nb, _ := p2.read(part)
			// The section properties stay the body's last child.
			if i := bytes.LastIndex(nb, []byte("Added by test")); i < 0 || bytes.Index(nb[i:], []byte("</w:body>")) < 0 {
				t.Error("appended text not before the end of the body")
			}
			if s := string(nb); strings.Contains(s, "<w:sectPr") && strings.LastIndex(s, "<w:sectPr") < strings.LastIndex(s, "Added by test") {
				t.Error("content after the section properties")
			}
			text, err := textx.Extract("x.docx", "", out, false)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range []string{"Added by test", "A paragraph.", "• one", "• two"} {
				if !strings.Contains(text, w) {
					t.Errorf("%q missing", w)
				}
			}
			if target != "" && (!strings.Contains(text, "REPLACED-BY-TEST") || strings.Contains(text, target)) {
				t.Errorf("replace of %q not done", target)
			}
			// Everything outside the edits is byte-identical.
			if len(doc.texts) > 0 && !bytes.HasPrefix(nb, orig[:doc.texts[0].start]) {
				t.Error("the document's start changed")
			}
			if d := os.Getenv("OFFICE_OUT"); d != "" {
				os.WriteFile(filepath.Join(d, "edited-"+filepath.Base(f)), out, 0o644)
			}
		})
	}
}

func TestDocxReplaceRefusals(t *testing.T) {
	b, _ := Docx("Hello **big** world, hello again.\n\nhello", "")
	for _, c := range []struct {
		r    TextReplace
		want string
	}{
		{TextReplace{"Hello big", "x"}, "spans"},
		{TextReplace{"ello", "x"}, "3 times"},
		{TextReplace{"absent", "x"}, "isn't in"},
		{TextReplace{"again", "a\nb"}, "one line"},
		{TextReplace{"", "x"}, "give the text"},
	} {
		if _, _, err := EditDocx(b, "", []TextReplace{c.r}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.r, err, c.want)
		}
	}
	if _, _, err := EditDocx(b, "", []TextReplace{{"world, hello again.", "everyone."}}); err != nil {
		t.Errorf("within one run: %v", err)
	}
}

func TestOfficeEditRefusals(t *testing.T) {
	x, _ := Xlsx([]Sheet{{Name: "S", Rows: [][]any{{"a"}}}})
	// A macro part.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	zr, _ := zip.NewReader(bytes.NewReader(x), int64(len(x)))
	for _, f := range zr.File {
		w, _ := zw.Create(f.Name)
		rc, _ := f.Open()
		io.Copy(w, rc)
	}
	w, _ := zw.Create("xl/vbaProject.bin")
	w.Write([]byte("x"))
	zw.Close()
	if _, _, err := EditXlsx(buf.Bytes(), []XlsxEdit{{Sheet: "S", SetCells: map[string]any{"A2": 1.0}}}); err == nil || !strings.Contains(err.Error(), "macro") {
		t.Errorf("macros: %v", err)
	}
	// Encrypted / old binary.
	if _, _, err := EditXlsx([]byte{0xD0, 0xCF, 0x11, 0xE0, 0, 0}, nil); err == nil || !strings.Contains(err.Error(), "encrypted") {
		t.Errorf("cfb: %v", err)
	}
	// Too large.
	if _, _, err := EditDocx(make([]byte, MaxEditFile+1), "x", nil); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("size: %v", err)
	}
	// Hidden sheet, unknown sheet, a cell set twice, a formula given as text.
	if _, _, err := EditXlsx(x, []XlsxEdit{{Sheet: "Nope", SetCells: map[string]any{"A1": 1.0}}}); err == nil {
		t.Error("unknown sheet")
	}
	out, _, err := EditXlsx(x, []XlsxEdit{{Sheet: "S", SetCells: map[string]any{"B1": "=SUM(A1:A9)"}}})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := openPkg(out, ".xlsx")
	sb, _ := p.read("xl/worksheets/sheet1.xml")
	if bytes.Contains(sb, []byte("<f>")) || !bytes.Contains(sb, []byte(`t="inlineStr"`)) {
		t.Errorf("formula text: %s", sb)
	}
}

// A formula that depends on a changed cell: its old result is dropped (the
// formula kept), and the workbook recalculates on load.
func TestXlsxFormulaResultsDropped(t *testing.T) {
	data, _ := os.ReadFile("testdata/ms-shared_formulas.xlsx")
	out, lines, err := EditXlsx(data, []XlsxEdit{{Sheet: "Label", SetCells: map[string]any{"B2": "CHANGED"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lines[len(lines)-1], "worked out again") {
		t.Errorf("lines: %v", lines)
	}
	p, _ := openPkg(out, ".xlsx")
	sb, _ := p.read("xl/worksheets/sheet1.xml")
	s, _ := parseSheet(sb)
	for _, r := range s.rows {
		for _, c := range r.cells {
			if c.formula && c.v != "" {
				t.Errorf("%s%d keeps its old result %q", colName(c.col), r.r, c.v)
			}
		}
	}
	if !bytes.Contains(sb, []byte(`<f t="shared" ref="A2:A41" si="0">B2</f>`)) {
		t.Error("the formula is gone")
	}
	// No formulas anywhere: nothing but the edited sheet changes.
	x, _ := Xlsx([]Sheet{{Name: "S", Rows: [][]any{{"a"}}}})
	out2, lines2, _ := EditXlsx(x, []XlsxEdit{{Sheet: "S", SetCells: map[string]any{"B1": 1.0}}})
	if len(lines2) != 1 {
		t.Errorf("lines: %v", lines2)
	}
	unchangedExcept(t, x, out2, "xl/worksheets/sheet1.xml")
}

func TestOfficeProtectedRefused(t *testing.T) {
	x, _ := Xlsx([]Sheet{{Name: "S", Rows: [][]any{{"a"}}}})
	p, _ := openPkg(x, ".xlsx")
	sb, _ := p.read("xl/worksheets/sheet1.xml")
	p.set("xl/worksheets/sheet1.xml", bytes.Replace(sb, []byte("</sheetData>"), []byte(`</sheetData><sheetProtection sheet="1"/>`), 1))
	wb, _ := p.read("xl/workbook.xml")
	p.set("xl/workbook.xml", bytes.Replace(wb, []byte("<sheets>"), []byte(`<workbookProtection lockStructure="1"/><sheets>`), 1))
	prot, _ := p.bytes()
	if _, _, err := EditXlsx(prot, []XlsxEdit{{Sheet: "S", SetCells: map[string]any{"B1": 1.0}}}); err == nil || !strings.Contains(err.Error(), "protected") {
		t.Errorf("protected sheet: %v", err)
	}
	if _, _, err := EditXlsx(prot, []XlsxEdit{{AddSheet: &Sheet{Name: "N"}}}); err == nil || !strings.Contains(err.Error(), "protected") {
		t.Errorf("locked workbook: %v", err)
	}
	// Formatted empty rows don't count as used.
	sb2 := bytes.Replace(sb, []byte("</sheetData>"), []byte(`<row r="5"><c r="A5" s="0"/></row></sheetData>`), 1)
	s, _ := parseSheet(sb2)
	if s.lastRow() != 1 {
		t.Errorf("last row: %d", s.lastRow())
	}
	// A document with another prefix for the Word namespace: refused.
	d, _ := Docx("Hello", "")
	pd, _ := openPkg(d, ".docx")
	db, _ := pd.read("word/document.xml")
	pd.set("word/document.xml", bytes.ReplaceAll(bytes.ReplaceAll(db, []byte("<w:"), []byte("<x:")), []byte("</w:"), []byte("</x:")))
	nd := pd.changed["word/document.xml"]
	pd.set("word/document.xml", bytes.Replace(nd, []byte("xmlns:w="), []byte("xmlns:x="), 1))
	other, _ := pd.bytes()
	if _, _, err := EditDocx(other, "more", nil); err == nil {
		t.Error("other prefix edited")
	}
}
