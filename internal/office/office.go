// Package office writes small Office files from the AI's content: .docx
// from a Markdown subset (headings, paragraphs, bullet and numbered
// lists, simple tables, bold and italic) and .xlsx from sheets of typed
// cells. Plain WordprocessingML/SpreadsheetML in a zip, standard library
// only: no macros, no templates, nothing fetched, never a formula (text
// that starts with "=" stays text).
package office

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits: what a person would write by hand, not more.
const (
	MaxSheets = 20
	MaxRows   = 10000
	MaxCols   = 200
	MaxCell   = 32767 // Excel's own limit for a cell's text
	MaxText   = 2 << 20
)

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(clean(s)))
	return b.String()
}

// clean: only characters XML 1.0 (and so Word and Excel) accept.
func clean(s string) string {
	s = strings.ToValidUTF8(s, "")
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' || (r >= 0x20 && r <= 0xD7FF) || (r >= 0xE000 && r <= 0xFFFD) || r >= 0x10000 {
			return r
		}
		return -1
	}, s)
}

type file struct{ name, body string }

func zipOf(files []file) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.name, Method: zip.Deflate, Modified: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(f.body)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

const xmlHead = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"

// ---- .docx

var (
	reHeading = regexp.MustCompile(`^(#{1,3})\s+(.*)$`)
	reBullet  = regexp.MustCompile(`^\s*[-*+]\s+(.*)$`)
	reNumber  = regexp.MustCompile(`^\s*\d{1,4}[.)]\s+(.*)$`)
	reTableSp = regexp.MustCompile(`^\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$`)
	reInline  = regexp.MustCompile(`\*\*(.+?)\*\*|\*(.+?)\*|__(.+?)__|_(.+?)_`)
)

// runs: a line's text as Word runs, **bold** and *italic* marked.
func runs(s string) string {
	var b strings.Builder
	run := func(t string, bold, italic bool) {
		if t == "" {
			return
		}
		b.WriteString("<w:r>")
		if bold || italic {
			b.WriteString("<w:rPr>")
			if bold {
				b.WriteString("<w:b/>")
			}
			if italic {
				b.WriteString("<w:i/>")
			}
			b.WriteString("</w:rPr>")
		}
		b.WriteString(`<w:t xml:space="preserve">` + esc(t) + "</w:t></w:r>")
	}
	last := 0
	for _, m := range reInline.FindAllStringSubmatchIndex(s, -1) {
		run(s[last:m[0]], false, false)
		switch {
		case m[2] >= 0:
			run(s[m[2]:m[3]], true, false)
		case m[4] >= 0:
			run(s[m[4]:m[5]], false, true)
		case m[6] >= 0:
			run(s[m[6]:m[7]], true, false)
		case m[8] >= 0:
			run(s[m[8]:m[9]], false, true)
		}
		last = m[1]
	}
	run(s[last:], false, false)
	return b.String()
}

func para(style, text string, num int) string {
	var b strings.Builder
	b.WriteString("<w:p>")
	if style != "" || num > 0 {
		b.WriteString("<w:pPr>")
		if style != "" {
			b.WriteString(`<w:pStyle w:val="` + style + `"/>`)
		}
		if num > 0 {
			b.WriteString(`<w:numPr><w:ilvl w:val="0"/><w:numId w:val="` + strconv.Itoa(num) + `"/></w:numPr>`)
		}
		b.WriteString("</w:pPr>")
	}
	b.WriteString(runs(text) + "</w:p>")
	return b.String()
}

func cells(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func table(rows [][]string) string {
	n := 0
	for _, r := range rows {
		n = max(n, len(r))
	}
	var b strings.Builder
	b.WriteString(`<w:tbl><w:tblPr><w:tblStyle w:val="Table"/><w:tblW w:w="0" w:type="auto"/><w:tblBorders>`)
	for _, e := range []string{"top", "left", "bottom", "right", "insideH", "insideV"} {
		b.WriteString(`<w:` + e + ` w:val="single" w:sz="4" w:space="0" w:color="999999"/>`)
	}
	b.WriteString(`</w:tblBorders></w:tblPr><w:tblGrid>`)
	for range n {
		b.WriteString(`<w:gridCol/>`)
	}
	b.WriteString(`</w:tblGrid>`)
	for i, r := range rows {
		b.WriteString("<w:tr>")
		for j := range n {
			t := ""
			if j < len(r) {
				t = r[j]
			}
			if i == 0 && t != "" {
				t = "**" + strings.Trim(t, "*") + "**"
			}
			b.WriteString(`<w:tc><w:tcPr><w:tcW w:w="0" w:type="auto"/></w:tcPr>` + para("", t, 0) + "</w:tc>")
		}
		b.WriteString("</w:tr>")
	}
	b.WriteString("</w:tbl>")
	return b.String()
}

// Docx: a Word document from Markdown (# to ### headings, paragraphs
// separated by blank lines, "- " and "1. " lists, | tables |, **bold**,
// *italic*). title, if given, becomes the document's title property.
func Docx(md, title string) ([]byte, error) {
	if len(md) > MaxText {
		return nil, fmt.Errorf("content: at most %d MB", MaxText>>20)
	}
	if !utf8.ValidString(md) {
		return nil, fmt.Errorf("content isn't UTF-8 text")
	}
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	var body strings.Builder
	var paraBuf []string
	nums := []string{}
	curNum, listKind := 0, ""
	flush := func() {
		if len(paraBuf) > 0 {
			body.WriteString(para("", strings.Join(paraBuf, " "), 0))
			paraBuf = nil
		}
	}
	endList := func() { listKind = "" }
	for i := 0; i < len(lines); i++ {
		l := strings.TrimRight(lines[i], " \t")
		switch {
		case strings.TrimSpace(l) == "":
			flush()
			endList()
		case reHeading.MatchString(l):
			flush()
			endList()
			m := reHeading.FindStringSubmatch(l)
			body.WriteString(para("Heading"+strconv.Itoa(len(m[1])), m[2], 0))
		case strings.HasPrefix(strings.TrimSpace(l), "|"):
			flush()
			endList()
			var rows [][]string
			for ; i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "|"); i++ {
				if reTableSp.MatchString(strings.TrimSpace(lines[i])) {
					continue
				}
				if len(rows) < 1000 {
					rows = append(rows, cells(lines[i]))
				}
			}
			i--
			if len(rows) > 0 {
				body.WriteString(table(rows))
			}
		case reBullet.MatchString(l), reNumber.MatchString(l):
			flush()
			kind, text := "bullet", ""
			if m := reBullet.FindStringSubmatch(l); m != nil {
				text = m[1]
			} else {
				kind, text = "number", reNumber.FindStringSubmatch(l)[1]
			}
			if kind != listKind {
				// A new list: numbered lists start again at 1.
				abs := map[string]string{"bullet": "0", "number": "1"}[kind]
				nums = append(nums, abs)
				curNum, listKind = len(nums), kind
			}
			body.WriteString(para("ListParagraph", text, curNum))
		default:
			endList()
			paraBuf = append(paraBuf, strings.TrimSpace(l))
		}
	}
	flush()
	doc := xmlHead + `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` + body.String() +
		`<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1417" w:right="1417" w:bottom="1134" w:left="1417" w:header="708" w:footer="708" w:gutter="0"/></w:sectPr></w:body></w:document>`
	var nb strings.Builder
	nb.WriteString(xmlHead + `<w:numbering xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">`)
	nb.WriteString(`<w:abstractNum w:abstractNumId="0"><w:lvl w:ilvl="0"><w:start w:val="1"/><w:numFmt w:val="bullet"/><w:lvlText w:val="•"/><w:lvlJc w:val="left"/><w:pPr><w:ind w:left="720" w:hanging="360"/></w:pPr></w:lvl></w:abstractNum>`)
	nb.WriteString(`<w:abstractNum w:abstractNumId="1"><w:lvl w:ilvl="0"><w:start w:val="1"/><w:numFmt w:val="decimal"/><w:lvlText w:val="%1."/><w:lvlJc w:val="left"/><w:pPr><w:ind w:left="720" w:hanging="360"/></w:pPr></w:lvl></w:abstractNum>`)
	for i, abs := range nums {
		nb.WriteString(`<w:num w:numId="` + strconv.Itoa(i+1) + `"><w:abstractNumId w:val="` + abs + `"/><w:lvlOverride w:ilvl="0"><w:startOverride w:val="1"/></w:lvlOverride></w:num>`)
	}
	nb.WriteString(`</w:numbering>`)
	styles := xmlHead + `<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
		`<w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:ascii="Calibri" w:hAnsi="Calibri" w:cs="Calibri"/><w:sz w:val="22"/></w:rPr></w:rPrDefault><w:pPrDefault><w:pPr><w:spacing w:after="160" w:line="259" w:lineRule="auto"/></w:pPr></w:pPrDefault></w:docDefaults>` +
		`<w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/></w:style>` +
		`<w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:pPr><w:keepNext/><w:spacing w:before="240"/><w:outlineLvl w:val="0"/></w:pPr><w:rPr><w:b/><w:sz w:val="36"/></w:rPr></w:style>` +
		`<w:style w:type="paragraph" w:styleId="Heading2"><w:name w:val="heading 2"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:pPr><w:keepNext/><w:spacing w:before="200"/><w:outlineLvl w:val="1"/></w:pPr><w:rPr><w:b/><w:sz w:val="30"/></w:rPr></w:style>` +
		`<w:style w:type="paragraph" w:styleId="Heading3"><w:name w:val="heading 3"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:pPr><w:keepNext/><w:spacing w:before="160"/><w:outlineLvl w:val="2"/></w:pPr><w:rPr><w:b/><w:sz w:val="26"/></w:rPr></w:style>` +
		`<w:style w:type="paragraph" w:styleId="ListParagraph"><w:name w:val="List Paragraph"/><w:basedOn w:val="Normal"/><w:pPr><w:spacing w:after="60"/><w:ind w:left="720"/></w:pPr></w:style>` +
		`<w:style w:type="table" w:styleId="Table"><w:name w:val="Table"/><w:tblPr><w:tblCellMar><w:left w:w="108" w:type="dxa"/><w:right w:w="108" w:type="dxa"/></w:tblCellMar></w:tblPr></w:style>` +
		`</w:styles>`
	files := []file{
		{"[Content_Types].xml", xmlHead + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/>` +
			`<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>` +
			`<Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/>` +
			`<Override PartName="/word/numbering.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.numbering+xml"/>` +
			`<Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/></Types>`},
		{"_rels/.rels", xmlHead + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/></Relationships>`},
		{"docProps/core.xml", xmlHead + `<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>` + esc(title) + `</dc:title></cp:coreProperties>`},
		{"word/_rels/document.xml.rels", xmlHead + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/numbering" Target="numbering.xml"/></Relationships>`},
		{"word/document.xml", doc},
		{"word/styles.xml", styles},
		{"word/numbering.xml", nb.String()},
	}
	return zipOf(files)
}

// ---- .xlsx

// Sheet: one sheet's rows; a cell is a number, true/false, text, a date
// as text YYYY-MM-DD (stored as a date), or nil (empty).
type Sheet struct {
	Name string  `json:"name" jsonschema:"the sheet's name (at most 31 characters)"`
	Rows [][]any `json:"rows" jsonschema:"rows of cells: numbers, true/false, text, dates as YYYY-MM-DD, null for empty; the first row is usually the header"`
}

var (
	reDate      = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	badSheetChr = regexp.MustCompile(`[\[\]:*?/\\]`)
)

func colName(i int) string {
	s := ""
	for i++; i > 0; i = (i - 1) / 26 {
		s = string(rune('A'+(i-1)%26)) + s
	}
	return s
}

func dateSerial(s string) (float64, bool) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return 0, false
	}
	return t.Sub(time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)).Hours() / 24, true
}

// Xlsx: an Excel workbook from sheets. The first row of each sheet is
// written bold (a header).
func Xlsx(sheets []Sheet) ([]byte, error) {
	if len(sheets) == 0 {
		return nil, fmt.Errorf("sheets: at least one")
	}
	if len(sheets) > MaxSheets {
		return nil, fmt.Errorf("sheets: at most %d", MaxSheets)
	}
	seen := map[string]bool{}
	total := 0
	var wbSheets, wbRels, ctypes strings.Builder
	var files []file
	for i, sh := range sheets {
		name := strings.TrimSpace(badSheetChr.ReplaceAllString(clean(sh.Name), " "))
		name = strings.Trim(name, "'")
		if name == "" {
			name = "Sheet" + strconv.Itoa(i+1)
		}
		if r := []rune(name); len(r) > 31 {
			name = string(r[:31])
		}
		if seen[strings.ToLower(name)] {
			return nil, fmt.Errorf("sheets: two are called %q", name)
		}
		seen[strings.ToLower(name)] = true
		if len(sh.Rows) > MaxRows {
			return nil, fmt.Errorf("sheet %q: at most %d rows", name, MaxRows)
		}
		var sd strings.Builder
		for r, row := range sh.Rows {
			if len(row) > MaxCols {
				return nil, fmt.Errorf("sheet %q: at most %d columns", name, MaxCols)
			}
			sd.WriteString(`<row r="` + strconv.Itoa(r+1) + `">`)
			for c, v := range row {
				ref := colName(c) + strconv.Itoa(r+1)
				style := ""
				if r == 0 {
					style = ` s="2"`
				}
				switch x := v.(type) {
				case nil:
					continue
				case bool:
					b := "0"
					if x {
						b = "1"
					}
					sd.WriteString(`<c r="` + ref + `" t="b"` + style + `><v>` + b + `</v></c>`)
				case float64:
					if math.IsNaN(x) || math.IsInf(x, 0) {
						return nil, fmt.Errorf("sheet %q, %s: not a number", name, ref)
					}
					sd.WriteString(`<c r="` + ref + `"` + style + `><v>` + strconv.FormatFloat(x, 'g', -1, 64) + `</v></c>`)
				case int:
					sd.WriteString(`<c r="` + ref + `"` + style + `><v>` + strconv.Itoa(x) + `</v></c>`)
				case string:
					if d, ok := dateSerial(x); ok && reDate.MatchString(x) && r > 0 {
						sd.WriteString(`<c r="` + ref + `" s="1"><v>` + strconv.FormatFloat(d, 'f', -1, 64) + `</v></c>`)
						break
					}
					if len(x) > MaxCell {
						return nil, fmt.Errorf("sheet %q, %s: at most %d characters", name, ref, MaxCell)
					}
					// Always text: never a formula, whatever it starts with.
					sd.WriteString(`<c r="` + ref + `" t="inlineStr"` + style + `><is><t xml:space="preserve">` + esc(x) + `</t></is></c>`)
				default:
					return nil, fmt.Errorf("sheet %q, %s: a cell is a number, true/false, text or null", name, ref)
				}
				total++
			}
			sd.WriteString(`</row>`)
		}
		if total > 200000 {
			return nil, fmt.Errorf("at most 200000 cells in all")
		}
		n := strconv.Itoa(i + 1)
		files = append(files, file{"xl/worksheets/sheet" + n + ".xml", xmlHead +
			`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>` + sd.String() + `</sheetData></worksheet>`})
		wbSheets.WriteString(`<sheet name="` + esc(name) + `" sheetId="` + n + `" r:id="rId` + n + `"/>`)
		wbRels.WriteString(`<Relationship Id="rId` + n + `" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet` + n + `.xml"/>`)
		ctypes.WriteString(`<Override PartName="/xl/worksheets/sheet` + n + `.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`)
	}
	nst := strconv.Itoa(len(sheets) + 1)
	wbRels.WriteString(`<Relationship Id="rId` + nst + `" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>`)
	files = append([]file{
		{"[Content_Types].xml", xmlHead + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/>` +
			`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` +
			`<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>` + ctypes.String() + `</Types>`},
		{"_rels/.rels", xmlHead + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`},
		{"xl/workbook.xml", xmlHead + `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>` + wbSheets.String() + `</sheets></workbook>`},
		{"xl/_rels/workbook.xml.rels", xmlHead + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` + wbRels.String() + `</Relationships>`},
		{"xl/styles.xml", xmlHead + `<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` +
			`<fonts count="2"><font><sz val="11"/><name val="Calibri"/></font><font><b/><sz val="11"/><name val="Calibri"/></font></fonts>` +
			`<fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills>` +
			`<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>` +
			`<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>` +
			`<cellXfs count="3"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="14" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/><xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/></cellXfs>` +
			`</styleSheet>`},
	}, files...)
	return zipOf(files)
}
