package textx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Spreadsheets (.xlsx, .ods), presentations (.pptx, .odp) and .odt as
// text. What their apps don't show is left out, as for HTML: hidden
// sheets, rows and columns, hidden slides and shapes, hidden text. Each
// part is read with a size cap, and the whole result with a cell cap, so
// a crafted file (a zip bomb, a sheet of a million empty cells) can't
// exhaust memory.

const (
	maxPart  = 32 << 20 // one XML part, unpacked
	maxCells = 200000   // cells over all sheets
	maxCols  = 1024     // repeated columns (ODS) shown at most
)

type ozip struct {
	files map[string]*zip.File
	total int64
}

func openZip(kind string, data []byte) (*ozip, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("not a valid %s: %v", kind, err)
	}
	z := &ozip{files: map[string]*zip.File{}}
	for _, f := range zr.File {
		z.files[f.Name] = f
	}
	return z, nil
}

// read: one part, or nil if it isn't there.
func (z *ozip) read(name string) ([]byte, error) {
	f := z.files[name]
	if f == nil {
		return nil, nil
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, maxPart+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxPart {
		return nil, fmt.Errorf("a part of this file is too large to read")
	}
	z.total += int64(len(b))
	if z.total > 4*maxPart {
		return nil, fmt.Errorf("this file is too large to read")
	}
	return b, nil
}

func attr(se xml.StartElement, local string) string {
	for _, a := range se.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// relID: an element's r:id (a relationship), not its plain id.
func relID(se xml.StartElement) string {
	for _, a := range se.Attr {
		if a.Name.Local == "id" && a.Name.Space != "" {
			return a.Value
		}
	}
	return ""
}

// rels: a part's relationships, id → target path (resolved against dir).
func (z *ozip) rels(part string) (map[string]string, error) {
	dir, file := path.Split(part)
	b, err := z.read(dir + "_rels/" + file + ".rels")
	if err != nil || b == nil {
		return map[string]string{}, err
	}
	out := map[string]string{}
	d := xml.NewDecoder(bytes.NewReader(b))
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "Relationship" {
			t := attr(se, "Target")
			if strings.HasPrefix(t, "/") {
				t = strings.TrimPrefix(t, "/")
			} else {
				t = path.Join(dir, t)
			}
			out[attr(se, "Id")] = t
		}
	}
	return out, nil
}

// ---- .xlsx

type xlsxSheet struct{ name, part string }

// colIndex: "BC12" → 54 (0-based column), -1 if not a cell reference.
func colIndex(ref string) int {
	n := 0
	i := 0
	for ; i < len(ref) && ref[i] >= 'A' && ref[i] <= 'Z'; i++ {
		n = n*26 + int(ref[i]-'A'+1)
	}
	if i == 0 {
		return -1
	}
	return n - 1
}

var (
	reDateFmt  = regexp.MustCompile(`(?i)(^|[^\\"])[dmy]`)
	reFmtQuote = regexp.MustCompile(`\[[^\]]*\]|"[^"]*"`)
)

func xlsx(data []byte) (string, error) {
	z, err := openZip(".xlsx", data)
	if err != nil {
		return "", err
	}
	wb, err := z.read("xl/workbook.xml")
	if err != nil || wb == nil {
		return "", fmt.Errorf("not a valid .xlsx: no workbook")
	}
	rels, err := z.rels("xl/workbook.xml")
	if err != nil {
		return "", err
	}
	var sheets []xlsxSheet
	hidden := 0
	date1904 := false
	d := xml.NewDecoder(bytes.NewReader(wb))
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "workbookPr":
			v := attr(se, "date1904")
			date1904 = v == "1" || v == "true"
		case "sheet":
			if st := attr(se, "state"); st == "hidden" || st == "veryHidden" {
				hidden++
				continue
			}
			if p := rels[relID(se)]; p != "" {
				sheets = append(sheets, xlsxSheet{attr(se, "name"), p})
			}
		}
	}
	shared, err := xlsxShared(z)
	if err != nil {
		return "", err
	}
	dates, err := xlsxDateStyles(z)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	cells := 0
	for _, sh := range sheets {
		fmt.Fprintf(&b, "## Sheet: %s\n", sh.name)
		x, err := z.read(sh.part)
		if err != nil {
			return "", err
		}
		if x == nil {
			continue
		}
		if err := xlsxRows(&b, x, shared, dates, date1904, &cells); err != nil {
			return "", err
		}
		b.WriteString("\n")
	}
	if hidden > 0 {
		fmt.Fprintf(&b, "(%d hidden sheet(s) left out)\n", hidden)
	}
	return strings.TrimSpace(b.String()), nil
}

// xlsxShared: the shared strings, phonetic hints (rPh) left out.
func xlsxShared(z *ozip) ([]string, error) {
	x, err := z.read("xl/sharedStrings.xml")
	if err != nil || x == nil {
		return nil, err
	}
	var out []string
	var cur strings.Builder
	in, skip := false, 0
	d := xml.NewDecoder(bytes.NewReader(x))
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				cur.Reset()
				in = true
			case "rPh":
				skip++
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "si":
				out = append(out, cur.String())
				in = false
			case "rPh":
				skip--
			}
		case xml.CharData:
			if in && skip == 0 {
				cur.Write(t)
			}
		}
	}
	return out, nil
}

// xlsxDateStyles: which cell styles (s="n") format numbers as dates.
func xlsxDateStyles(z *ozip) (map[int]bool, error) {
	x, err := z.read("xl/styles.xml")
	if err != nil || x == nil {
		return nil, err
	}
	custom := map[int]bool{}
	out := map[int]bool{}
	inXfs, xf := false, 0
	d := xml.NewDecoder(bytes.NewReader(x))
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "numFmt":
				id, _ := strconv.Atoi(attr(t, "numFmtId"))
				code := reFmtQuote.ReplaceAllString(attr(t, "formatCode"), "")
				custom[id] = reDateFmt.MatchString(code)
			case "cellXfs":
				inXfs = true
			case "xf":
				if inXfs {
					id, _ := strconv.Atoi(attr(t, "numFmtId"))
					if (id >= 14 && id <= 22) || (id >= 45 && id <= 47) || custom[id] {
						out[xf] = true
					}
					xf++
				}
			}
		case xml.EndElement:
			if t.Name.Local == "cellXfs" {
				inXfs = false
			}
		}
	}
	return out, nil
}

func serialDate(v string, date1904 bool) string {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 || f > 2958465 {
		return v
	}
	base := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	if date1904 {
		base = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	days := math.Floor(f)
	secs := math.Round((f - days) * 86400)
	t := base.AddDate(0, 0, int(days)).Add(time.Duration(secs) * time.Second)
	if secs == 0 {
		return t.Format("2006-01-02")
	}
	if days == 0 && !date1904 {
		return t.Format("15:04:05")
	}
	return t.Format("2006-01-02 15:04:05")
}

func xlsxRows(b *strings.Builder, x []byte, shared []string, dates map[int]bool, date1904 bool, cells *int) error {
	hiddenCols := map[int]bool{}
	var row []string
	rowHidden := false
	var cellType, cellStyle, val, inline string
	col := 0
	inV, inIs, skip := false, false, 0
	d := xml.NewDecoder(bytes.NewReader(x))
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "col":
				if h := attr(t, "hidden"); h == "1" || h == "true" {
					lo, _ := strconv.Atoi(attr(t, "min"))
					hi, _ := strconv.Atoi(attr(t, "max"))
					for i := max(lo, 1); i <= hi && i <= 16384; i++ {
						hiddenCols[i-1] = true
					}
				}
			case "row":
				row = row[:0]
				col = 0
				h := attr(t, "hidden")
				rowHidden = h == "1" || h == "true"
			case "c":
				if c := colIndex(attr(t, "r")); c >= 0 {
					col = c
				}
				cellType, cellStyle, val, inline = attr(t, "t"), attr(t, "s"), "", ""
			case "v":
				inV = true
			case "is":
				inIs = true
			case "rPh":
				skip++
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inV = false
			case "is":
				inIs = false
			case "rPh":
				skip--
			case "c":
				s := val
				switch cellType {
				case "s":
					if i, err := strconv.Atoi(val); err == nil && i >= 0 && i < len(shared) {
						s = shared[i]
					}
				case "inlineStr":
					s = inline
				case "b":
					s = map[string]string{"1": "TRUE", "0": "FALSE"}[val]
				case "", "n":
					if st, err := strconv.Atoi(cellStyle); err == nil && dates[st] && val != "" {
						s = serialDate(val, date1904)
					}
				}
				if !hiddenCols[col] && s != "" && col < 16384 {
					*cells++
					if *cells > maxCells {
						return fmt.Errorf("this spreadsheet has more than %d cells: too large to read whole", maxCells)
					}
					for len(row) <= col {
						row = append(row, "")
					}
					row[col] = strings.NewReplacer("\t", " ", "\r\n", " ", "\n", " ").Replace(s)
				}
				col++
			case "row":
				if !rowHidden {
					writeRow(b, row, hiddenCols)
				}
			}
		case xml.CharData:
			if inV {
				val += string(t)
			} else if inIs && skip == 0 {
				inline += string(t)
			}
		}
	}
	return nil
}

// writeRow: one line, cells separated by tabs, hidden columns and trailing
// empty cells left out.
func writeRow(b *strings.Builder, row []string, hiddenCols map[int]bool) {
	var out []string
	for i, c := range row {
		if !hiddenCols[i] {
			out = append(out, c)
		}
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	if len(out) > 0 {
		b.WriteString(strings.Join(out, "\t") + "\n")
	}
}

// ---- .pptx

func pptx(data []byte) (string, error) {
	z, err := openZip(".pptx", data)
	if err != nil {
		return "", err
	}
	pres, err := z.read("ppt/presentation.xml")
	if err != nil || pres == nil {
		return "", fmt.Errorf("not a valid .pptx: no presentation")
	}
	rels, err := z.rels("ppt/presentation.xml")
	if err != nil {
		return "", err
	}
	var slides []string
	d := xml.NewDecoder(bytes.NewReader(pres))
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "sldId" {
			if p := rels[relID(se)]; p != "" {
				slides = append(slides, p)
			}
		}
	}
	var b strings.Builder
	hidden, n := 0, 0
	for _, p := range slides {
		x, err := z.read(p)
		if err != nil {
			return "", err
		}
		if x == nil {
			continue
		}
		text, shown := drawingText(x)
		if !shown {
			hidden++
			continue
		}
		n++
		fmt.Fprintf(&b, "## Slide %d\n%s\n", n, text)
		srels, err := z.rels(p)
		if err != nil {
			return "", err
		}
		for _, t := range srels {
			if strings.Contains(t, "notesSlides/") {
				if nx, err := z.read(t); err == nil && nx != nil {
					if notes, _ := drawingText(nx); notes != "" {
						fmt.Fprintf(&b, "Notes: %s\n", notes)
					}
				}
			}
		}
		b.WriteString("\n")
	}
	if hidden > 0 {
		fmt.Fprintf(&b, "(%d hidden slide(s) left out)\n", hidden)
	}
	return strings.TrimSpace(b.String()), nil
}

// drawingText: the text of a slide or notes part (DrawingML a:t in a:p),
// hidden shapes left out; shown is false for a hidden slide (show="0").
// Slide-number and date placeholders in notes are left out too.
func drawingText(x []byte) (string, bool) {
	var b strings.Builder
	shown := true
	// depth of the shape element we're skipping, 0 when not skipping
	skipDepth, depth := 0, 0
	inT := false
	shapeOpen := []string{} // element names of open shapes
	d := xml.NewDecoder(bytes.NewReader(x))
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			switch t.Name.Local {
			case "sld":
				if s := attr(t, "show"); s == "0" || s == "false" {
					shown = false
				}
			case "sp", "grpSp", "graphicFrame", "pic", "cxnSp":
				shapeOpen = append(shapeOpen, t.Name.Local)
			case "cNvPr":
				if h := attr(t, "hidden"); (h == "1" || h == "true") && skipDepth == 0 && len(shapeOpen) > 0 {
					skipDepth = depth - 2 // the shape: cNvPr sits in nvSpPr inside it
				}
			case "ph":
				if ty := attr(t, "type"); (ty == "sldNum" || ty == "dt" || ty == "hdr" || ty == "ftr" || ty == "sldImg") && skipDepth == 0 {
					skipDepth = depth - 3 // ph sits in nvPr in nvSpPr in the shape
				}
			case "t":
				inT = true
			case "br":
				if skipDepth == 0 {
					b.WriteString("\n")
				}
			case "tab":
				if skipDepth == 0 {
					b.WriteString("\t")
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "p":
				if skipDepth == 0 {
					b.WriteString("\n")
				}
			case "sp", "grpSp", "graphicFrame", "pic", "cxnSp":
				if len(shapeOpen) > 0 {
					shapeOpen = shapeOpen[:len(shapeOpen)-1]
				}
			}
			if skipDepth > 0 && depth == skipDepth {
				skipDepth = 0
			}
			depth--
		case xml.CharData:
			if inT && skipDepth == 0 {
				b.Write(t)
			}
		}
	}
	return Visible(b.String()), shown
}

// ---- OpenDocument (.odt, .ods, .odp)

// odf: content.xml as text. Spreadsheets: a "## Sheet:" per table, cells
// tab-separated; presentations: a "## Slide" per page. Hidden: styles with
// table:display="false" (sheets), presentation:visibility="hidden"
// (slides), text:display="none" (text), collapsed or filtered rows and
// columns, text:hidden-text / hidden-paragraph, notes and annotations.
func odf(kind string, data []byte) (string, error) {
	z, err := openZip(kind, data)
	if err != nil {
		return "", err
	}
	x, err := z.read("content.xml")
	if err != nil || x == nil {
		return "", fmt.Errorf("not a valid %s: no content", kind)
	}
	hiddenStyles := map[string]bool{}
	// First pass: automatic styles that hide.
	d := xml.NewDecoder(bytes.NewReader(x))
	style := ""
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok {
			switch se.Name.Local {
			case "style":
				style = attr(se, "name")
			case "table-properties", "drawing-page-properties", "text-properties", "paragraph-properties", "table-column-properties", "table-row-properties":
				for _, a := range se.Attr {
					if (a.Name.Local == "display" && (a.Value == "false" || a.Value == "none")) ||
						(a.Name.Local == "visibility" && a.Value == "hidden") {
						hiddenStyles[style] = true
					}
				}
			}
		}
	}

	var b strings.Builder
	cells, slide := 0, 0
	var row []string
	var cell strings.Builder
	var colHidden []bool
	skip := 0       // inside something not shown
	inCell := false // collecting a cell's text
	rowRepeat, cellRepeat := 1, 1
	rowHidden := false
	colIdx := 0
	paraStart := true
	inPara := 0 // open text:p / text:h: only their text is document text
	d = xml.NewDecoder(bytes.NewReader(x))
	var stack []bool // per open element: did it start a skip
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			starts := false
			name := t.Name.Local
			hides := hiddenStyles[attr(t, "style-name")]
			switch name {
			case "hidden-text", "hidden-paragraph", "annotation", "notes", "note", "tracked-changes", "forms", "conditional-text":
				starts = true
			case "table":
				if t.Name.Space == "" || strings.Contains(t.Name.Space, "table") {
					if hides {
						starts = true
					} else if skip == 0 && kind == ".ods" {
						fmt.Fprintf(&b, "## Sheet: %s\n", attr(t, "name"))
						colHidden = colHidden[:0]
					}
				}
			case "table-column":
				n, _ := strconv.Atoi(attr(t, "number-columns-repeated"))
				n = min(max(n, 1), maxCols)
				v := attr(t, "visibility")
				for range n {
					colHidden = append(colHidden, (v == "collapse" || v == "filter") || hides)
				}
			case "table-row":
				rowRepeat, _ = strconv.Atoi(attr(t, "number-rows-repeated"))
				rowRepeat = max(rowRepeat, 1)
				v := attr(t, "visibility")
				rowHidden = v == "collapse" || v == "filter" || hides
				row = row[:0]
				colIdx = 0
			case "table-cell", "covered-table-cell":
				cellRepeat, _ = strconv.Atoi(attr(t, "number-columns-repeated"))
				cellRepeat = min(max(cellRepeat, 1), maxCols)
				cell.Reset()
				inCell = true // a number's or date's text, as the app shows it, is in its text:p
			case "page":
				if hides {
					starts = true
				} else if skip == 0 && kind == ".odp" {
					slide++
					fmt.Fprintf(&b, "## Slide %d\n", slide)
				}
			case "p", "h", "span", "section", "frame", "custom-shape", "list-item":
				starts = hides
				if name == "p" || name == "h" {
					inPara++
				}
				if (name == "p" || name == "h") && skip == 0 && !starts {
					if inCell {
						if cell.Len() > 0 {
							cell.WriteString(" ")
						}
					} else if !paraStart {
						b.WriteString("\n")
					}
					paraStart = false
				}
			case "s":
				if skip == 0 {
					n, _ := strconv.Atoi(attr(t, "c"))
					w := strings.Repeat(" ", min(max(n, 1), 100))
					if inCell {
						cell.WriteString(w)
					} else {
						b.WriteString(w)
					}
				}
			case "tab":
				if skip == 0 {
					if inCell {
						cell.WriteString(" ")
					} else {
						b.WriteString("\t")
					}
				}
			case "line-break":
				if skip == 0 {
					if inCell {
						cell.WriteString(" ")
					} else {
						b.WriteString("\n")
					}
				}
			}
			if starts {
				skip++
			}
			stack = append(stack, starts)
		case xml.EndElement:
			name := t.Name.Local
			switch name {
			case "p", "h":
				inPara--
			case "table-cell", "covered-table-cell":
				inCell = false
				if skip == 0 {
					s := strings.TrimSpace(cell.String())
					for range cellRepeat {
						if colIdx < len(colHidden) && colHidden[colIdx] {
							row = append(row, "\x00")
						} else {
							row = append(row, s)
						}
						colIdx++
					}
					if s != "" {
						cells += cellRepeat
						if cells > maxCells {
							return "", fmt.Errorf("this spreadsheet has more than %d cells: too large to read whole", maxCells)
						}
					}
				}
			case "table-row":
				if skip == 0 && !rowHidden {
					var out []string
					for _, c := range row {
						if c != "\x00" {
							out = append(out, c)
						}
					}
					for len(out) > 0 && out[len(out)-1] == "" {
						out = out[:len(out)-1]
					}
					if len(out) > 0 {
						line := strings.Join(out, "\t") + "\n"
						for range min(rowRepeat, 100) {
							b.WriteString(line)
						}
					}
					paraStart = true
				}
			case "table":
				if skip == 0 && kind == ".ods" {
					b.WriteString("\n")
					paraStart = true
				}
			case "page":
				if skip == 0 && kind == ".odp" {
					b.WriteString("\n\n")
					paraStart = true
				}
			}
			if n := len(stack); n > 0 {
				if stack[n-1] {
					skip--
				}
				stack = stack[:n-1]
			}
		case xml.CharData:
			if skip > 0 {
				continue
			}
			if inCell {
				cell.Write(t)
			} else if inPara > 0 {
				b.Write(t)
			}
		}
	}
	return strings.TrimSpace(b.String()), nil
}
