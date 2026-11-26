package office

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// RecalcNote: the line EditXlsx adds (not a change of its own) when
// formula results were dropped to be worked out again.
const RecalcNote = "Formula results are worked out again when the file is next opened in an office app (until then they show empty here)"

// XlsxEdit: one change to a workbook.
type XlsxEdit struct {
	Sheet      string         `json:"sheet,omitempty" jsonschema:"the sheet to change (its name); not with add_sheet"`
	AppendRows [][]any        `json:"append_rows,omitempty" jsonschema:"rows to add after the sheet's last used row: numbers, true/false, text, dates as YYYY-MM-DD, null for empty"`
	SetCells   map[string]any `json:"set_cells,omitempty" jsonschema:"cells to set, e.g. {\"B3\": 95, \"C3\": \"2026-12-01\"}; null clears a cell. Cells with formulas are never overwritten"`
	AddSheet   *Sheet         `json:"add_sheet,omitempty" jsonschema:"a new sheet at the end"`
}

const mainNS = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"

var reRef = regexp.MustCompile(`^([A-Z]{1,3})([1-9][0-9]{0,6})$`)

// cellRef: "B3" → column 1 (0-based), row 3.
func cellRef(s string) (col, row int, err error) {
	m := reRef.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(s)))
	if m == nil {
		return 0, 0, fmt.Errorf("%q isn't a cell like B3", s)
	}
	for _, ch := range m[1] {
		col = col*26 + int(ch-'A'+1)
	}
	col--
	row, _ = strconv.Atoi(m[2])
	if col >= 16384 || row > 1048576 {
		return 0, 0, fmt.Errorf("%q is outside a sheet", s)
	}
	return col, row, nil
}

type xCell struct {
	col, start, end int
	formula         bool
	style, typ, v   string
	inline          string
}

type xRow struct {
	r, start, tagEnd, end int
	selfClosing           bool
	cells                 []xCell
}

type xSheet struct {
	data             []byte
	sdStart, sdEnd   int // the whole <sheetData> element
	sdInner, sdClose int // after its start tag, and the start of </sheetData>
	sdSelfClosing    bool
	rows             []xRow
	merged           [][4]int // c1, r1, c2, r2
	dimStart, dimEnd int
	dimRef           string
	protected        bool
}

// parseSheet: the sheet's rows and cells with their byte ranges.
func parseSheet(b []byte) (*xSheet, error) {
	s := &xSheet{data: b, dimStart: -1}
	d := xml.NewDecoder(bytes.NewReader(b))
	depth := 0
	var inData bool
	var row *xRow
	var cell *xCell
	var text *strings.Builder
	var textKind string
	for {
		off := int(d.InputOffset())
		tok, err := d.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errNotUnderstood
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			name := t.Name.Local
			if depth == 1 && (name != "worksheet" || t.Name.Space != "") {
				return nil, fmt.Errorf("this sheet uses an XML form the editor doesn't handle")
			}
			if depth == 1 {
				ok := false
				for _, a := range t.Attr {
					if a.Name.Space == "" && a.Name.Local == "xmlns" && a.Value == mainNS {
						ok = true
					}
				}
				if !ok {
					return nil, fmt.Errorf("this workbook uses a format (strict OOXML?) the editor doesn't handle")
				}
			}
			if t.Name.Space != "" && inData {
				return nil, errNotUnderstood
			}
			switch {
			case depth == 2 && name == "sheetData":
				inData = true
				s.sdStart, s.sdInner = off, int(d.InputOffset())
			case depth == 2 && name == "dimension":
				s.dimStart, s.dimRef = off, attr(t, "ref")
			case depth == 2 && name == "sheetProtection":
				if v := attr(t, "sheet"); v == "1" || v == "true" {
					s.protected = true
				}
			case name == "mergeCell":
				ref := attr(t, "ref")
				a, b2, ok := strings.Cut(ref, ":")
				c1, r1, e1 := cellRef(a)
				c2, r2, e2 := cellRef(b2)
				if !ok || e1 != nil || e2 != nil {
					return nil, errNotUnderstood
				}
				s.merged = append(s.merged, [4]int{c1, r1, c2, r2})
			case inData && depth == 3 && name == "row":
				r, err := strconv.Atoi(attr(t, "r"))
				if err != nil || r < 1 {
					return nil, fmt.Errorf("a row without a number: the editor doesn't handle this sheet")
				}
				s.rows = append(s.rows, xRow{r: r, start: off, tagEnd: int(d.InputOffset())})
				row = &s.rows[len(s.rows)-1]
			case inData && depth == 4 && name == "c" && row != nil:
				col, r, err := cellRef(attr(t, "r"))
				if err != nil || r != row.r {
					return nil, fmt.Errorf("a cell without a proper reference: the editor doesn't handle this sheet")
				}
				row.cells = append(row.cells, xCell{col: col, start: off, style: attr(t, "s"), typ: attr(t, "t")})
				cell = &row.cells[len(row.cells)-1]
			case inData && depth == 4:
				return nil, fmt.Errorf("this sheet's rows hold more than cells: the editor doesn't handle it")
			case inData && depth >= 5 && cell != nil:
				switch name {
				case "f":
					cell.formula = true
				case "v", "t":
					text, textKind = &strings.Builder{}, name
				}
			}
		case xml.CharData:
			if text != nil {
				text.Write(t)
			}
		case xml.EndElement:
			end := int(d.InputOffset())
			name := t.Name.Local
			switch {
			case depth == 2 && name == "sheetData":
				s.sdEnd, s.sdClose = end, off
				s.sdSelfClosing = off == s.sdInner && end == off
				inData = false
			case depth == 2 && name == "dimension":
				s.dimEnd = end
			case inData && depth == 3 && name == "row" && row != nil:
				row.end = end
				row.selfClosing = off == row.tagEnd && end == off
				row = nil
			case inData && depth == 4 && name == "c" && cell != nil:
				cell.end = end
				cell = nil
			case text != nil && (name == "v" || name == "t"):
				if textKind == "v" {
					cell.v = text.String()
				} else {
					cell.inline += text.String()
				}
				text = nil
			}
			depth--
		}
	}
	if s.sdEnd == 0 {
		return nil, fmt.Errorf("a sheet without its data part: the editor doesn't handle it")
	}
	for i := 1; i < len(s.rows); i++ {
		if s.rows[i].r <= s.rows[i-1].r {
			return nil, fmt.Errorf("the sheet's rows are out of order: the editor doesn't handle it")
		}
	}
	return s, nil
}

func (s *xSheet) inMerge(col, row int) bool {
	for _, m := range s.merged {
		if col >= m[0] && col <= m[2] && row >= m[1] && row <= m[3] && (col != m[0] || row != m[1]) {
			return true
		}
	}
	return false
}

// lastRow: the last row with a value or formula (formatted empty cells
// don't count, so a template's styled rows don't push data down).
func (s *xSheet) lastRow() int {
	last := 0
	for _, r := range s.rows {
		for _, c := range r.cells {
			if c.formula || c.v != "" || c.inline != "" {
				last = r.r
			}
		}
	}
	return last
}

// cellXML: one cell. style: the s attribute to keep ("" none); a date
// gets dateStyle.
func cellXML(ref string, v any, style, dateStyle string) (string, error) {
	st := ""
	if style != "" {
		st = ` s="` + style + `"`
	}
	switch x := v.(type) {
	case nil:
		return `<c r="` + ref + `"` + st + `/>`, nil
	case bool:
		b := "0"
		if x {
			b = "1"
		}
		return `<c r="` + ref + `" t="b"` + st + `><v>` + b + `</v></c>`, nil
	case float64:
		if x != x || x > 1e308 || x < -1e308 {
			return "", fmt.Errorf("%s: not a number", ref)
		}
		return `<c r="` + ref + `"` + st + `><v>` + strconv.FormatFloat(x, 'g', -1, 64) + `</v></c>`, nil
	case int:
		return `<c r="` + ref + `"` + st + `><v>` + strconv.Itoa(x) + `</v></c>`, nil
	case string:
		if d, ok := dateSerial(x); ok && reDate.MatchString(x) {
			return `<c r="` + ref + `" s="` + dateStyle + `"><v>` + strconv.FormatFloat(d, 'f', -1, 64) + `</v></c>`, nil
		}
		if len(x) > MaxCell {
			return "", fmt.Errorf("%s: at most %d characters", ref, MaxCell)
		}
		// Always text: never a formula, whatever it starts with.
		return `<c r="` + ref + `" t="inlineStr"` + st + `><is><t xml:space="preserve">` + esc(x) + `</t></is></c>`, nil
	}
	return "", fmt.Errorf("%s: a cell is a number, true/false, text or null", ref)
}

func needsDate(vals ...any) bool {
	for _, v := range vals {
		if s, ok := v.(string); ok && reDate.MatchString(s) {
			if _, ok := dateSerial(s); ok {
				return true
			}
		}
	}
	return false
}

// workbook: the sheets as the workbook lists them.
type wbSheet struct {
	name, rid, part, state string
	id                     int
}

type workbook struct {
	locked    bool // structure protected: no sheets added
	p         *pkg
	xml       []byte
	sheets    []wbSheet
	rels      map[string][2]string
	relsXML   []byte
	relsName  string
	rPrefix   string
	dateStyle string
	sst       []string
}

func openWorkbook(p *pkg) (*workbook, error) {
	b, err := p.read("xl/workbook.xml")
	if err != nil || b == nil {
		return nil, fmt.Errorf("not a valid .xlsx: no workbook")
	}
	w := &workbook{p: p, xml: b}
	if w.rels, w.relsXML, w.relsName, err = p.rels("xl/workbook.xml"); err != nil {
		return nil, fmt.Errorf("not a valid .xlsx: %v", err)
	}
	d := xml.NewDecoder(bytes.NewReader(b))
	for {
		tok, err := d.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errNotUnderstood
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if se.Name.Local == "workbook" {
			if se.Name.Space != "" {
				return nil, fmt.Errorf("this workbook uses an XML form the editor doesn't handle")
			}
			for _, a := range se.Attr {
				if a.Name.Space == "xmlns" && a.Value == "http://schemas.openxmlformats.org/officeDocument/2006/relationships" {
					w.rPrefix = a.Name.Local
				}
			}
		}
		if se.Name.Local == "workbookProtection" && se.Name.Space == "" {
			if v := attr(se, "lockStructure"); v == "1" || v == "true" {
				w.locked = true
			}
		}
		if se.Name.Local == "sheet" && se.Name.Space == "" {
			id, _ := strconv.Atoi(attr(se, "sheetId"))
			rid := ""
			for _, a := range se.Attr {
				if a.Name.Space == w.rPrefix && a.Name.Local == "id" {
					rid = a.Value
				}
			}
			w.sheets = append(w.sheets, wbSheet{name: attr(se, "name"), rid: rid, part: w.rels[rid][0], state: attr(se, "state"), id: id})
		}
	}
	if w.rPrefix == "" {
		return nil, fmt.Errorf("this workbook uses an XML form the editor doesn't handle")
	}
	// Shared strings, for showing old values.
	if sb, _ := p.read("xl/sharedStrings.xml"); sb != nil {
		d := xml.NewDecoder(bytes.NewReader(sb))
		var cur *strings.Builder
		inRPh := 0
		for {
			tok, err := d.Token()
			if err != nil {
				break
			}
			switch t := tok.(type) {
			case xml.StartElement:
				switch t.Name.Local {
				case "si":
					cur = &strings.Builder{}
				case "rPh":
					inRPh++
				}
			case xml.EndElement:
				switch t.Name.Local {
				case "si":
					w.sst = append(w.sst, cur.String())
					cur = nil
				case "rPh":
					inRPh--
				}
			case xml.CharData:
				if cur != nil && inRPh == 0 {
					cur.Write(t)
				}
			}
		}
	}
	return w, nil
}

// ensureDateStyle: the index of a cell style that shows a date, added to
// styles.xml if there is none.
func (w *workbook) ensureDateStyle() (string, error) {
	if w.dateStyle != "" {
		return w.dateStyle, nil
	}
	b, err := w.p.read("xl/styles.xml")
	if err != nil || b == nil {
		return "", fmt.Errorf("this workbook has no styles part: dates can't be written into it")
	}
	d := xml.NewDecoder(bytes.NewReader(b))
	depth, in, idx := 0, false, 0
	closeAt, countStart, countEnd := -1, -1, -1
	for {
		off := int(d.InputOffset())
		tok, err := d.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", errNotUnderstood
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if depth == 1 && (t.Name.Local != "styleSheet" || t.Name.Space != "") {
				return "", fmt.Errorf("this workbook's styles use an XML form the editor doesn't handle")
			}
			if depth == 2 && t.Name.Local == "cellXfs" {
				in = true
				tag := string(b[off:d.InputOffset()])
				if i := strings.Index(tag, `count="`); i >= 0 {
					j := strings.Index(tag[i+7:], `"`)
					countStart, countEnd = off+i+7, off+i+7+j
				}
			}
			if in && depth == 3 && t.Name.Local == "xf" {
				n, _ := strconv.Atoi(attr(t, "numFmtId"))
				if (n >= 14 && n <= 17) || n == 22 {
					w.dateStyle = strconv.Itoa(idx)
					return w.dateStyle, nil
				}
				idx++
			}
		case xml.EndElement:
			if in && depth == 2 && t.Name.Local == "cellXfs" {
				closeAt = off
				in = false
			}
			depth--
		}
	}
	if closeAt < 0 {
		return "", fmt.Errorf("this workbook's styles have no cell formats: dates can't be written into it")
	}
	edits := []edit{{closeAt, closeAt, `<xf numFmtId="14" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>`}}
	if countStart >= 0 {
		edits = append(edits, edit{countStart, countEnd, strconv.Itoa(idx + 1)})
	}
	nb, err := splice(b, edits)
	if err != nil {
		return "", err
	}
	w.p.set("xl/styles.xml", nb)
	w.dateStyle = strconv.Itoa(idx)
	return w.dateStyle, nil
}

func (w *workbook) shown(c xCell) string {
	switch {
	case c.formula:
		return "a formula"
	case c.typ == "s":
		if i, err := strconv.Atoi(c.v); err == nil && i >= 0 && i < len(w.sst) {
			return strconv.Quote(snip(w.sst[i], 60))
		}
	case c.typ == "inlineStr":
		return strconv.Quote(snip(c.inline, 60))
	case c.typ == "b":
		return map[string]string{"1": "TRUE", "0": "FALSE"}[c.v]
	}
	if c.v == "" {
		return "empty"
	}
	return c.v
}

func snip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func shownValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "empty"
	case string:
		return strconv.Quote(snip(x, 60))
	case bool:
		return strings.ToUpper(strconv.FormatBool(x))
	}
	return fmt.Sprint(v)
}

// EditXlsx: the workbook with the edits, and one line per change for the
// person's confirmation.
func EditXlsx(data []byte, edits []XlsxEdit) ([]byte, []string, error) {
	p, err := openPkg(data, ".xlsx")
	if err != nil {
		return nil, nil, err
	}
	w, err := openWorkbook(p)
	if err != nil {
		return nil, nil, err
	}
	var lines []string
	bySheet := map[string][]XlsxEdit{}
	var order []string
	for _, e := range edits {
		if e.AddSheet != nil {
			if e.Sheet != "" || e.AppendRows != nil || e.SetCells != nil {
				return nil, nil, fmt.Errorf("add_sheet comes alone in its change")
			}
			continue
		}
		if e.AppendRows == nil && e.SetCells == nil {
			return nil, nil, fmt.Errorf("a change needs append_rows, set_cells or add_sheet")
		}
		if _, ok := bySheet[e.Sheet]; !ok {
			order = append(order, e.Sheet)
		}
		bySheet[e.Sheet] = append(bySheet[e.Sheet], e)
	}
	for _, name := range order {
		var ws *wbSheet
		for i := range w.sheets {
			if strings.EqualFold(w.sheets[i].name, name) {
				ws = &w.sheets[i]
			}
		}
		if ws == nil {
			return nil, nil, fmt.Errorf("no sheet %q in this workbook", name)
		}
		if ws.state == "hidden" || ws.state == "veryHidden" {
			return nil, nil, fmt.Errorf("sheet %q is hidden: it isn't edited here", ws.name)
		}
		if ws.part == "" || !strings.HasPrefix(ws.part, "xl/worksheets/") {
			return nil, nil, fmt.Errorf("sheet %q isn't a worksheet (a chart sheet?): it isn't edited here", ws.name)
		}
		sb, err := p.read(ws.part)
		if err != nil || sb == nil {
			return nil, nil, fmt.Errorf("sheet %q is missing from the file", ws.name)
		}
		nb, ls, err := w.editSheet(ws.name, sb, bySheet[name])
		if err != nil {
			if err == errNotUnderstood {
				return nil, nil, fmt.Errorf("sheet %q has a structure the editor doesn't fully understand: not changing it", ws.name)
			}
			return nil, nil, err
		}
		p.set(ws.part, nb)
		lines = append(lines, ls...)
	}
	// Cells changed: formula results that may depend on them are computed
	// afresh when the file is next opened (in every sheet: formulas may
	// refer across sheets).
	if len(order) > 0 {
		anyF := false
		for _, ws := range w.sheets {
			if !strings.HasPrefix(ws.part, "xl/worksheets/") {
				continue
			}
			sb, err := p.read(ws.part)
			if err != nil || sb == nil {
				continue
			}
			nb, had, err := dropFormulaValues(sb)
			if err != nil {
				return nil, nil, fmt.Errorf("sheet %q has a structure the editor doesn't fully understand: not changing it", ws.name)
			}
			if had {
				anyF = true
				p.set(ws.part, nb)
			}
		}
		if anyF {
			if err := w.fullCalcOnLoad(); err != nil {
				return nil, nil, fmt.Errorf("this workbook's settings have a form the editor doesn't handle")
			}
			lines = append(lines, RecalcNote)
		}
	}
	for _, e := range edits {
		if e.AddSheet != nil {
			l, err := w.addSheet(*e.AddSheet)
			if err != nil {
				return nil, nil, err
			}
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return nil, nil, fmt.Errorf("nothing to change")
	}
	out, err := p.bytes()
	return out, lines, err
}

func (w *workbook) editSheet(name string, b []byte, es []XlsxEdit) ([]byte, []string, error) {
	s, err := parseSheet(b)
	if err != nil {
		return nil, nil, err
	}
	if s.protected {
		return nil, nil, fmt.Errorf("sheet %q is protected: change it in your office app", name)
	}
	type target struct {
		col, row int
		v        any
		appended bool
	}
	var sets []target
	var lines []string
	next := s.lastRow() + 1
	for _, e := range es {
		for ref, v := range e.SetCells {
			c, r, err := cellRef(ref)
			if err != nil {
				return nil, nil, err
			}
			sets = append(sets, target{c, r, v, false})
		}
		if len(e.AppendRows) > MaxRows {
			return nil, nil, fmt.Errorf("at most %d rows at once", MaxRows)
		}
		first := next
		for _, row := range e.AppendRows {
			if len(row) > MaxCols {
				return nil, nil, fmt.Errorf("at most %d columns", MaxCols)
			}
			for c, v := range row {
				if v != nil {
					sets = append(sets, target{c, next, v, true})
				}
			}
			next++
		}
		if n := len(e.AppendRows); n > 0 {
			l := fmt.Sprintf("Sheet %q: add %d row(s) after the last one (rows %d–%d)", name, n, first, next-1)
			if len(e.AppendRows[0]) > 0 {
				var cs []string
				for _, v := range e.AppendRows[0][:min(4, len(e.AppendRows[0]))] {
					cs = append(cs, shownValue(v))
				}
				l += ", the first: " + strings.Join(cs, ", ")
			}
			lines = append(lines, l)
		}
	}
	if len(sets) > 200000 {
		return nil, nil, fmt.Errorf("at most 200000 cells at once")
	}
	slices.SortStableFunc(sets, func(a, b target) int {
		if a.row != b.row {
			return a.row - b.row
		}
		return a.col - b.col
	})
	for i := 1; i < len(sets); i++ {
		if sets[i].row == sets[i-1].row && sets[i].col == sets[i-1].col {
			return nil, nil, fmt.Errorf("cell %s%d is set twice", colName(sets[i].col), sets[i].row)
		}
	}
	dateStyle := ""
	for _, t := range sets {
		if needsDate(t.v) {
			if dateStyle, err = w.ensureDateStyle(); err != nil {
				return nil, nil, err
			}
			break
		}
	}
	rowIdx := map[int]int{}
	for i, r := range s.rows {
		rowIdx[r.r] = i
	}
	var out []edit
	maxRow, maxCol := 0, 0
	for i := 0; i < len(sets); {
		j := i
		for j < len(sets) && sets[j].row == sets[i].row {
			j++
		}
		rowNum, group := sets[i].row, sets[i:j]
		i = j
		news := map[int]string{}
		for _, t := range group {
			ref := colName(t.col) + strconv.Itoa(t.row)
			if s.inMerge(t.col, t.row) {
				return nil, nil, fmt.Errorf("sheet %q, %s: inside merged cells (only their top-left cell can be set)", name, ref)
			}
			style := ""
			var old *xCell
			if ri, ok := rowIdx[rowNum]; ok {
				for k := range s.rows[ri].cells {
					if s.rows[ri].cells[k].col == t.col {
						old = &s.rows[ri].cells[k]
					}
				}
			}
			if old != nil {
				if old.formula {
					return nil, nil, fmt.Errorf("sheet %q, %s holds a formula: it isn't overwritten", name, ref)
				}
				style = old.style
			}
			x, err := cellXML(ref, t.v, style, dateStyle)
			if err != nil {
				return nil, nil, fmt.Errorf("sheet %q: %v", name, err)
			}
			news[t.col] = x
			maxRow, maxCol = max(maxRow, t.row), max(maxCol, t.col)
			if t.appended {
				continue // shown with the appended rows
			}
			was := "empty"
			if old != nil {
				was = w.shown(*old)
			}
			lines = append(lines, fmt.Sprintf("Sheet %q, %s: %s → %s", name, ref, was, shownValue(t.v)))
		}
		cols := make([]int, 0, len(news))
		for c := range news {
			cols = append(cols, c)
		}
		slices.Sort(cols)
		if ri, ok := rowIdx[rowNum]; ok {
			r := s.rows[ri]
			var inner strings.Builder
			k := 0
			for _, c := range r.cells {
				for k < len(cols) && cols[k] < c.col {
					inner.WriteString(news[cols[k]])
					k++
				}
				if k < len(cols) && cols[k] == c.col {
					inner.WriteString(news[cols[k]])
					k++
					continue
				}
				inner.Write(b[c.start:c.end])
			}
			for ; k < len(cols); k++ {
				inner.WriteString(news[cols[k]])
			}
			tag := string(b[r.start:r.tagEnd])
			if r.selfClosing {
				tag = strings.TrimSuffix(strings.TrimSpace(strings.TrimSuffix(tag, "/>")), "/") + ">"
			}
			out = append(out, edit{r.start, r.end, tag + inner.String() + "</row>"})
			continue
		}
		var row strings.Builder
		row.WriteString(`<row r="` + strconv.Itoa(rowNum) + `">`)
		for _, c := range cols {
			row.WriteString(news[c])
		}
		row.WriteString("</row>")
		at := s.sdClose
		for _, r := range s.rows {
			if r.r > rowNum {
				at = r.start
				break
			}
		}
		out = append(out, edit{at, at, row.String()})
	}
	if s.sdSelfClosing {
		var all strings.Builder
		for _, e := range out {
			all.WriteString(e.with)
		}
		out = []edit{{s.sdStart, s.sdEnd, "<sheetData>" + all.String() + "</sheetData>"}}
	}
	// The used range grows to cover the new cells.
	if s.dimStart >= 0 && s.dimRef != "" {
		a, bRef, _ := strings.Cut(s.dimRef, ":")
		if bRef == "" {
			bRef = a
		}
		c2, r2, err := cellRef(bRef)
		if err == nil {
			c2, r2 = max(c2, maxCol), max(r2, maxRow)
			out = append(out, edit{s.dimStart, s.dimEnd, `<dimension ref="` + a + `:` + colName(c2) + strconv.Itoa(r2) + `"/>`})
		}
	}
	nb, err := splice(b, sortEdits(out))
	return nb, lines, err
}

// sortEdits: by start, an insertion before a replacement at the same place.
func sortEdits(es []edit) []edit {
	slices.SortStableFunc(es, func(a, b edit) int {
		if a.start != b.start {
			return a.start - b.start
		}
		return a.end - b.end
	})
	return es
}

func (w *workbook) addSheet(sh Sheet) (string, error) {
	if w.locked {
		return "", fmt.Errorf("this workbook's structure is protected: sheets can't be added here")
	}
	name := strings.TrimSpace(badSheetChr.ReplaceAllString(clean(sh.Name), " "))
	if r := []rune(name); len(r) > 31 {
		name = string(r[:31])
	}
	if name == "" {
		return "", fmt.Errorf("add_sheet: give the sheet a name")
	}
	maxID := 0
	for _, s := range w.sheets {
		if strings.EqualFold(s.name, name) {
			return "", fmt.Errorf("there is already a sheet %q", s.name)
		}
		maxID = max(maxID, s.id)
	}
	if len(sh.Rows) > MaxRows {
		return "", fmt.Errorf("add_sheet: at most %d rows", MaxRows)
	}
	dateStyle := ""
	var body strings.Builder
	for r, row := range sh.Rows {
		if len(row) > MaxCols {
			return "", fmt.Errorf("add_sheet: at most %d columns", MaxCols)
		}
		body.WriteString(`<row r="` + strconv.Itoa(r+1) + `">`)
		for c, v := range row {
			if v == nil {
				continue
			}
			if dateStyle == "" && needsDate(v) {
				ds, err := w.ensureDateStyle()
				if err != nil {
					return "", err
				}
				dateStyle = ds
			}
			x, err := cellXML(colName(c)+strconv.Itoa(r+1), v, "", dateStyle)
			if err != nil {
				return "", fmt.Errorf("add_sheet: %v", err)
			}
			body.WriteString(x)
		}
		body.WriteString("</row>")
	}
	n := 1
	for {
		if _, ok := w.p.files["xl/worksheets/sheet"+strconv.Itoa(n)+".xml"]; !ok {
			if _, ok := w.p.changed["xl/worksheets/sheet"+strconv.Itoa(n)+".xml"]; !ok {
				break
			}
		}
		n++
	}
	part := "xl/worksheets/sheet" + strconv.Itoa(n) + ".xml"
	rid := 1
	for {
		if _, ok := w.rels["rId"+strconv.Itoa(rid)]; !ok {
			break
		}
		rid++
	}
	ridS := "rId" + strconv.Itoa(rid)
	w.p.set(part, []byte(xmlHead+`<worksheet xmlns="`+mainNS+`"><sheetData>`+body.String()+`</sheetData></worksheet>`))
	// workbook.xml: the sheet at the end of <sheets>.
	wb := string(w.xml)
	if strings.Count(wb, "</sheets>") != 1 {
		return "", fmt.Errorf("this workbook's sheet list has a form the editor doesn't handle")
	}
	wb = strings.Replace(wb, "</sheets>", `<sheet name="`+esc(name)+`" sheetId="`+strconv.Itoa(maxID+1)+`" `+w.rPrefix+`:id="`+ridS+`"/></sheets>`, 1)
	w.xml = []byte(wb)
	w.p.set("xl/workbook.xml", w.xml)
	rels := string(w.relsXML)
	if strings.Count(rels, "</Relationships>") != 1 {
		return "", errNotUnderstood
	}
	rels = strings.Replace(rels, "</Relationships>", `<Relationship Id="`+ridS+`" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet`+strconv.Itoa(n)+`.xml"/></Relationships>`, 1)
	w.relsXML = []byte(rels)
	w.rels[ridS] = [2]string{part, "worksheet"}
	w.p.set(w.relsName, w.relsXML)
	ct, _ := w.p.read("[Content_Types].xml")
	cts := string(ct)
	if strings.Count(cts, "</Types>") != 1 {
		return "", errNotUnderstood
	}
	cts = strings.Replace(cts, "</Types>", `<Override PartName="/`+part+`" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>`, 1)
	w.p.set("[Content_Types].xml", []byte(cts))
	w.sheets = append(w.sheets, wbSheet{name: name, rid: ridS, part: part, id: maxID + 1})
	return fmt.Sprintf("New sheet %q at the end (%d row(s))", name, len(sh.Rows)), nil
}

// dropFormulaValues: the sheet with every formula cell's cached result
// removed (the formula itself kept), so office apps compute it afresh on
// opening instead of showing a result from before the change (LibreOffice
// keeps cached results even with fullCalcOnLoad); whether it had any.
func dropFormulaValues(b []byte) ([]byte, bool, error) {
	d := xml.NewDecoder(bytes.NewReader(b))
	depth, cellDepth := 0, -1
	hasF := false
	var vStart int
	var pending []edit
	var cellVs []edit
	for {
		off := int(d.InputOffset())
		tok, err := d.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false, errNotUnderstood
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			switch {
			case t.Name.Local == "c" && t.Name.Space == "":
				cellDepth, hasF, cellVs = depth, false, nil
			case cellDepth > 0 && depth == cellDepth+1 && t.Name.Local == "f":
				hasF = true
			case cellDepth > 0 && depth == cellDepth+1 && t.Name.Local == "v":
				vStart = off
			}
		case xml.EndElement:
			switch {
			case cellDepth > 0 && depth == cellDepth+1 && t.Name.Local == "v":
				cellVs = append(cellVs, edit{vStart, int(d.InputOffset()), ""})
			case depth == cellDepth && t.Name.Local == "c":
				if hasF {
					pending = append(pending, cellVs...)
				}
				cellDepth = -1
			}
			depth--
		}
	}
	if len(pending) == 0 {
		return b, false, nil
	}
	nb, err := splice(b, pending)
	return nb, true, err
}

// fullCalcOnLoad: the workbook asks Excel to recalculate everything when
// opened.
func (w *workbook) fullCalcOnLoad() error {
	wb := string(w.xml)
	if i := strings.Index(wb, "<calcPr"); i >= 0 {
		j := strings.Index(wb[i:], ">")
		if j < 0 {
			return errNotUnderstood
		}
		tag := wb[i : i+j+1]
		if strings.Contains(tag, "fullCalcOnLoad=") {
			tag2 := regexp.MustCompile(`fullCalcOnLoad="[^"]*"`).ReplaceAllString(tag, `fullCalcOnLoad="1"`)
			wb = wb[:i] + tag2 + wb[i+j+1:]
		} else {
			wb = wb[:i] + "<calcPr fullCalcOnLoad=\"1\"" + tag[len("<calcPr"):] + wb[i+j+1:]
		}
	} else {
		// Its place in the workbook: before these, else at the end.
		at := strings.Index(wb, "</workbook>")
		for _, next := range []string{"<oleSize", "<customWorkbookViews", "<pivotCaches", "<smartTagPr", "<smartTagTypes", "<webPublishing", "<fileRecoveryPr", "<webPublishObjects", "<extLst"} {
			if k := strings.Index(wb, next); k >= 0 && k < at {
				at = k
			}
		}
		if at < 0 {
			return errNotUnderstood
		}
		wb = wb[:at] + `<calcPr fullCalcOnLoad="1"/>` + wb[at:]
	}
	w.xml = []byte(wb)
	w.p.set("xl/workbook.xml", w.xml)
	return nil
}
