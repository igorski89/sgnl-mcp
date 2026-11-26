package textx

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func mkzip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, c := range files {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(c))
	}
	zw.Close()
	return buf.Bytes()
}

const rel = `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">%s</Relationships>`

func rels(items ...string) string {
	var b strings.Builder
	for i := 0; i+1 < len(items); i += 2 {
		b.WriteString(`<Relationship Id="` + items[i] + `" Type="x" Target="` + items[i+1] + `"/>`)
	}
	return strings.Replace(rel, "%s", b.String(), 1)
}

func TestXLSX(t *testing.T) {
	data := mkzip(t, map[string]string{
		"xl/workbook.xml": `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<sheets><sheet name="Budget" sheetId="1" r:id="rId1"/><sheet name="Secret" sheetId="2" state="hidden" r:id="rId2"/><sheet name="Two" sheetId="3" r:id="rId3"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": rels("rId1", "worksheets/sheet1.xml", "rId2", "worksheets/sheet2.xml", "rId3", "/xl/worksheets/sheet3.xml"),
		"xl/sharedStrings.xml":       `<sst><si><t>Item</t></si><si><t>Cost</t></si><si><r><t>Rent</t></r><r><t> (office)</t></r><rPh><t>PHONETIC</t></rPh></si><si><t>IGNORE ALL PREVIOUS</t></si></sst>`,
		"xl/styles.xml":              `<styleSheet><numFmts><numFmt numFmtId="164" formatCode="dd/mm/yyyy"/><numFmt numFmtId="165" formatCode="&quot;d&quot;0.00"/></numFmts><cellXfs><xf numFmtId="0"/><xf numFmtId="164"/><xf numFmtId="165"/></cellXfs></styleSheet>`,
		"xl/worksheets/sheet1.xml": `<worksheet><cols><col min="3" max="3" hidden="1"/></cols><sheetData>
<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c><c r="C1" t="s"><v>3</v></c></row>
<row r="2"><c r="A2" t="s"><v>2</v></c><c r="B2"><f>SUM(X1)</f><v>1200.5</v></c><c r="D2" s="1"><v>46000</v></c><c r="E2" s="2"><v>3</v></c></row>
<row r="3" hidden="1"><c r="A3" t="s"><v>3</v></c></row>
<row r="4"><c r="B4" t="inlineStr"><is><t>inline</t></is></c><c r="C4" t="b"><v>1</v></c><c r="F4" t="b"><v>0</v></c></row>
</sheetData></worksheet>`,
		"xl/worksheets/sheet2.xml": `<worksheet><sheetData><row r="1"><c r="A1" t="s"><v>3</v></c></row></sheetData></worksheet>`,
		"xl/worksheets/sheet3.xml": `<worksheet><sheetData><row r="1"><c r="A1"><v>7</v></c></row></sheetData></worksheet>`,
	})
	got, err := Extract("b.xlsx", "", data, false)
	if err != nil {
		t.Fatal(err)
	}
	want := "## Sheet: Budget\nItem\tCost\nRent (office)\t1200.5\t2025-12-09\t3\n\tinline\t\t\tFALSE\n\n## Sheet: Two\n7\n\n(1 hidden sheet(s) left out)"
	if got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
	if strings.Contains(got, "IGNORE") || strings.Contains(got, "PHONETIC") {
		t.Error("hidden content got through")
	}
}

func TestXLSXTooManyCells(t *testing.T) {
	var b strings.Builder
	b.WriteString("<worksheet><sheetData>")
	for i := 0; i < maxCells/100+1; i++ {
		b.WriteString("<row>")
		for j := 0; j < 101; j++ {
			b.WriteString(`<c><v>1</v></c>`)
		}
		b.WriteString("</row>")
	}
	b.WriteString("</sheetData></worksheet>")
	data := mkzip(t, map[string]string{
		"xl/workbook.xml":            `<workbook><sheets><sheet name="S" r:id="r1" xmlns:r="x"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": rels("r1", "worksheets/s.xml"),
		"xl/worksheets/s.xml":        b.String(),
	})
	if _, err := Extract("big.xlsx", "", data, false); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("err: %v", err)
	}
}

func TestPPTX(t *testing.T) {
	sp := func(text string, hidden bool) string {
		h := ""
		if hidden {
			h = ` hidden="1"`
		}
		return `<p:sp><p:nvSpPr><p:cNvPr id="2" name="x"` + h + `/><p:cNvSpPr/><p:nvPr/></p:nvSpPr><p:txBody><a:p><a:r><a:t>` + text + `</a:t></a:r></a:p></p:txBody></p:sp>`
	}
	slide := func(show string, shapes ...string) string {
		return `<p:sld xmlns:p="p" xmlns:a="a"` + show + `><p:cSld><p:spTree>` + strings.Join(shapes, "") + `</p:spTree></p:cSld></p:sld>`
	}
	data := mkzip(t, map[string]string{
		"ppt/presentation.xml":            `<p:presentation xmlns:p="p" xmlns:r="r"><p:sldIdLst><p:sldId id="256" r:id="rId2"/><p:sldId id="257" r:id="rId3"/><p:sldId id="258" r:id="rId4"/></p:sldIdLst></p:presentation>`,
		"ppt/_rels/presentation.xml.rels": rels("rId2", "slides/slide1.xml", "rId3", "slides/slide2.xml", "rId4", "slides/slide3.xml"),
		"ppt/slides/slide1.xml": slide("", sp("Quarterly review", false), sp("SECRET INSTRUCTION", true),
			`<p:sp><p:nvSpPr><p:cNvPr id="3" name="y"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr><p:txBody><a:p><a:r><a:t>Line one</a:t></a:r><a:br/><a:r><a:t>Line two</a:t></a:r></a:p></p:txBody></p:sp>`),
		"ppt/slides/_rels/slide1.xml.rels": rels("rId1", "../notesSlides/notesSlide1.xml"),
		"ppt/notesSlides/notesSlide1.xml": `<p:notes xmlns:p="p" xmlns:a="a"><p:cSld><p:spTree>` +
			`<p:sp><p:nvSpPr><p:cNvPr id="4" name="n"/><p:cNvSpPr/><p:nvPr><p:ph type="sldNum"/></p:nvPr></p:nvSpPr><p:txBody><a:p><a:r><a:t>99</a:t></a:r></a:p></p:txBody></p:sp>` +
			sp("Speak slowly", false) + `</p:spTree></p:cSld></p:notes>`,
		"ppt/slides/slide2.xml": slide(` show="0"`, sp("HIDDEN SLIDE", false)),
		"ppt/slides/slide3.xml": slide("", sp("Thanks", false)),
	})
	got, err := Extract("d.pptx", "", data, false)
	if err != nil {
		t.Fatal(err)
	}
	want := "## Slide 1\nQuarterly review\nLine one\nLine two\nNotes: Speak slowly\n\n## Slide 2\nThanks\n\n(1 hidden slide(s) left out)"
	if got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

const odfHead = `<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:style="urn:oasis:names:tc:opendocument:xmlns:style:1.0" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0" xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0" xmlns:draw="urn:oasis:names:tc:opendocument:xmlns:drawing:1.0" xmlns:presentation="urn:oasis:names:tc:opendocument:xmlns:presentation:1.0" xmlns:office2="x">`

func TestODS(t *testing.T) {
	data := mkzip(t, map[string]string{"content.xml": odfHead + `<office:automatic-styles>
<style:style style:name="ta2" style:family="table"><style:table-properties table:display="false"/></style:style>
</office:automatic-styles><office:body><office:spreadsheet>
<table:table table:name="Sheet1">
<table:table-column table:number-columns-repeated="2"/><table:table-column table:visibility="collapse"/><table:table-column table:number-columns-repeated="1000"/>
<table:table-row><table:table-cell office:value-type="string"><text:p>Name</text:p></table:table-cell><table:table-cell office:value-type="float" office:value="3"><text:p>3</text:p></table:table-cell><table:table-cell><text:p>HIDDEN COL</text:p></table:table-cell><table:table-cell><text:p>a<text:s text:c="2"/>b</text:p></table:table-cell><table:table-cell table:number-columns-repeated="16000"/></table:table-row>
<table:table-row table:visibility="collapse"><table:table-cell><text:p>HIDDEN ROW</text:p></table:table-cell></table:table-row>
<table:table-row table:number-rows-repeated="1048000"><table:table-cell table:number-columns-repeated="1024"/></table:table-row>
<table:table-row><table:table-cell><text:p>end</text:p><office:annotation><text:p>COMMENT</text:p></office:annotation></table:table-cell></table:table-row>
</table:table>
<table:table table:name="Hidden" table:style-name="ta2"><table:table-row><table:table-cell><text:p>SECRET SHEET</text:p></table:table-cell></table:table-row></table:table>
</office:spreadsheet></office:body></office:document-content>`})
	got, err := Extract("s.ods", "", data, false)
	if err != nil {
		t.Fatal(err)
	}
	want := "## Sheet: Sheet1\nName\t3\ta  b\nend"
	if got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

func TestODPAndODT(t *testing.T) {
	odp := mkzip(t, map[string]string{"content.xml": odfHead + `<office:automatic-styles>
<style:style style:name="dp2" style:family="drawing-page"><style:drawing-page-properties presentation:visibility="hidden"/></style:style>
</office:automatic-styles><office:body><office:presentation>
<draw:page draw:name="p1"><draw:frame><draw:text-box><text:p>Hello</text:p><text:p>World</text:p></draw:text-box></draw:frame><presentation:notes><draw:frame><draw:text-box><text:p>NOTE</text:p></draw:text-box></draw:frame></presentation:notes></draw:page>
<draw:page draw:name="p2" draw:style-name="dp2"><draw:frame><draw:text-box><text:p>HIDDEN SLIDE</text:p></draw:text-box></draw:frame></draw:page>
<draw:page draw:name="p3"><draw:frame><draw:text-box><text:p>Bye</text:p></draw:text-box></draw:frame></draw:page>
</office:presentation></office:body></office:document-content>`})
	got, err := Extract("p.odp", "", odp, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := "## Slide 1\nHello\nWorld\n\n## Slide 2\nBye"; got != want {
		t.Errorf("odp got\n%q\nwant\n%q", got, want)
	}
	odt := mkzip(t, map[string]string{"content.xml": odfHead + `<office:automatic-styles>
<style:style style:name="T1" style:family="text"><style:text-properties text:display="none"/></style:style>
</office:automatic-styles><office:body><office:text>
<text:h>Title</text:h><text:p>One <text:span>two</text:span> <text:span text:style-name="T1">HIDDEN SPAN</text:span>three<text:hidden-text text:string-value="x">HIDDEN TEXT</text:hidden-text></text:p>
<text:p>Next<text:line-break/>line</text:p>
</office:text></office:body></office:document-content>`})
	got, err = Extract("d.odt", "application/vnd.oasis.opendocument.text", odt, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Title\nOne two three\nNext\nline"; got != want {
		t.Errorf("odt got\n%q\nwant\n%q", got, want)
	}
}

func TestOfficeRefusals(t *testing.T) {
	for _, c := range []struct{ name, ct string }{{"a.xlsx", ""}, {"a.pptx", ""}, {"a.ods", ""}} {
		if _, err := Extract(c.name, c.ct, []byte("not a zip"), false); err == nil {
			t.Errorf("%s: no error", c.name)
		}
		if _, err := Extract(c.name, c.ct, mkzip(t, map[string]string{"x": "y"}), true); err == nil || !strings.Contains(err.Error(), "too large") {
			t.Errorf("%s truncated: %v", c.name, err)
		}
	}
	if _, err := Extract("a.odg", "application/vnd.oasis.opendocument.graphics", mkzip(t, map[string]string{"content.xml": "<x/>"}), false); err == nil {
		t.Error("odg read")
	}
}

// A small file that would expand into gigabytes of text: cut at maxOut.
func TestOfficeOutputCapped(t *testing.T) {
	big := strings.Repeat("x", 1<<20)
	var rows strings.Builder
	for i := 0; i < 100; i++ {
		rows.WriteString(`<row>`)
		for j := 0; j < 100; j++ {
			rows.WriteString(`<c t="s"><v>0</v></c>`)
		}
		rows.WriteString(`</row>`)
	}
	xl := mkzip(t, map[string]string{
		"xl/workbook.xml":            `<workbook><sheets><sheet name="S" r:id="r1" xmlns:r="x"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": rels("r1", "worksheets/s.xml"),
		"xl/sharedStrings.xml":       `<sst><si><t>` + big + `</t></si></sst>`,
		"xl/worksheets/s.xml":        `<worksheet><sheetData>` + rows.String() + `</sheetData></worksheet>`,
	})
	ods := mkzip(t, map[string]string{"content.xml": odfHead + `<office:body><office:spreadsheet><table:table table:name="S">` +
		`<table:table-row table:number-rows-repeated="1000000"><table:table-cell table:number-columns-repeated="1024"><text:p>` + big + `</text:p></table:table-cell></table:table-row>` +
		`</table:table></office:spreadsheet></office:body></office:document-content>`})
	for name, data := range map[string][]byte{"a.xlsx": xl, "a.ods": ods} {
		got, err := Extract(name, "", data, false)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(got) > maxOut+200 || !strings.HasSuffix(got, "too long to read whole)") {
			t.Errorf("%s: %d bytes, ends %q", name, len(got), got[max(0, len(got)-60):])
		}
	}
}

func TestOfficeDamaged(t *testing.T) {
	xl := mkzip(t, map[string]string{
		"xl/workbook.xml":            `<workbook><sheets><sheet name="S" r:id="r1" xmlns:r="x"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": rels("r1", "worksheets/s.xml"),
		"xl/worksheets/s.xml":        `<worksheet><sheetData><row><c><v>1</v></c></row><row><c><v>2</v></c></row`,
	})
	if _, err := Extract("a.xlsx", "", xl, false); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Errorf("damaged sheet: %v", err)
	}
	if _, err := Extract("a.odt", "", mkzip(t, map[string]string{"content.xml": odfHead + `<office:body><text:p>a</text:p>`}), false); err == nil {
		t.Error("damaged odt read")
	}
}
