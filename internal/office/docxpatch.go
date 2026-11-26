package office

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// TextReplace: one exact text to replace in a Word document.
type TextReplace struct {
	Old string `json:"old" jsonschema:"the exact text now in the document (it must occur exactly once and within one formatting run)"`
	New string `json:"new" jsonschema:"what it becomes (one line)"`
}

const wordNS = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"

type wText struct {
	start, end int // the whole <w:t …>…</w:t>
	text       string
	field      bool // a field's result (updated by the app: a change wouldn't last)
}

type wDoc struct {
	data     []byte
	insertAt int // before the body's own section properties, or </w:body>
	texts    []wText
}

func parseDocument(b []byte) (*wDoc, error) {
	doc := &wDoc{data: b, insertAt: -1}
	d := xml.NewDecoder(bytes.NewReader(b))
	depth := 0
	wPrefix := ""
	var cur *wText
	var buf strings.Builder
	bodyDepth := -1
	sectStart, sectEnd := -1, -1
	fldSimple, fieldDepth, inResult := 0, 0, 0
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
			if depth == 1 {
				if t.Name.Local != "document" {
					return nil, errNotUnderstood
				}
				for _, a := range t.Attr {
					if a.Name.Space == "xmlns" && a.Value == wordNS {
						wPrefix = a.Name.Local
					}
				}
				if wPrefix == "" || t.Name.Space != wPrefix {
					return nil, fmt.Errorf("this document uses a format (strict OOXML?) the editor doesn't handle")
				}
				if wPrefix != "w" {
					return nil, fmt.Errorf("this document uses an XML form the editor doesn't handle")
				}
			}
			if t.Name.Space == wPrefix {
				switch {
				case t.Name.Local == "body" && depth == 2:
					bodyDepth = depth
				case t.Name.Local == "sectPr" && depth == bodyDepth+1:
					sectStart = off // the last one wins: the body's own
				case t.Name.Local == "fldSimple":
					fldSimple++
				case t.Name.Local == "fldChar":
					switch attr(t, "fldCharType") {
					case "begin":
						fieldDepth++
					case "separate":
						if fieldDepth > 0 {
							inResult++
						}
					case "end":
						if inResult > 0 {
							inResult--
						}
						if fieldDepth > 0 {
							fieldDepth--
						}
					}
				case t.Name.Local == "t":
					doc.texts = append(doc.texts, wText{start: off, field: fldSimple > 0 || inResult > 0})
					cur = &doc.texts[len(doc.texts)-1]
					buf.Reset()
				}
			}
		case xml.CharData:
			if cur != nil {
				buf.Write(t)
			}
		case xml.EndElement:
			if t.Name.Space == wPrefix {
				switch {
				case t.Name.Local == "fldSimple" && fldSimple > 0:
					fldSimple--
				case t.Name.Local == "t" && cur != nil:
					cur.end = int(d.InputOffset())
					cur.text = buf.String()
					cur = nil
				case t.Name.Local == "sectPr" && depth == bodyDepth+1:
					sectEnd = int(d.InputOffset())
				case t.Name.Local == "body" && depth == bodyDepth:
					// New content goes before the body's own section
					// properties (its last child), else before </w:body>.
					doc.insertAt = off
					if sectStart >= 0 && sectEnd >= sectStart && strings.TrimSpace(string(b[sectEnd:off])) == "" {
						doc.insertAt = sectStart
					}
				}
			}
			depth--
		}
	}
	if bodyDepth < 0 || doc.insertAt < 0 {
		return nil, errNotUnderstood
	}
	return doc, nil
}

// docStyles: which heading levels (1-3) the document has styles for.
func docStyles(p *pkg, docPart string) map[int]bool {
	have := map[int]bool{}
	rels, _, _, err := p.rels(docPart)
	if err != nil {
		return have
	}
	for _, r := range rels {
		if strings.HasSuffix(r[1], "/styles") {
			b, _ := p.read(r[0])
			for lvl := 1; lvl <= 3; lvl++ {
				if bytes.Contains(b, []byte(`styleId="Heading`+strconv.Itoa(lvl)+`"`)) {
					have[lvl] = true
				}
			}
		}
	}
	return have
}

// EditDocx: the document with the replacements and the appended
// Markdown, and one line per change for the person's confirmation.
func EditDocx(data []byte, appendMD string, reps []TextReplace) ([]byte, []string, error) {
	p, err := openPkg(data, ".docx")
	if err != nil {
		return nil, nil, err
	}
	rels, _, _, err := p.rels("")
	if err != nil {
		return nil, nil, fmt.Errorf("not a valid .docx: no package relationships")
	}
	docPart := ""
	for _, r := range rels {
		if strings.HasSuffix(r[1], "/officeDocument") {
			docPart = r[0]
		}
	}
	b, err := p.read(docPart)
	if err != nil || b == nil || docPart == "" {
		return nil, nil, fmt.Errorf("not a valid .docx: no document part")
	}
	doc, err := parseDocument(b)
	if err != nil {
		if err == errNotUnderstood {
			return nil, nil, fmt.Errorf("this document has a structure the editor doesn't fully understand: not changing it")
		}
		return nil, nil, err
	}
	if err := docSettings(p, docPart); err != nil {
		return nil, nil, err
	}
	var all strings.Builder
	at := make([]int, len(doc.texts))
	for i, t := range doc.texts {
		at[i] = all.Len()
		all.WriteString(t.text)
	}
	full := all.String()
	var lines []string
	var edits []edit
	used := map[int]bool{}
	for _, r := range reps {
		if r.Old == "" {
			return nil, nil, fmt.Errorf("replace: give the text to replace")
		}
		if strings.ContainsAny(r.New, "\n\r") {
			return nil, nil, fmt.Errorf("replace: the new text is one line (to add paragraphs, append)")
		}
		switch n := strings.Count(full, r.Old); {
		case n == 0:
			return nil, nil, fmt.Errorf("%q isn't in the document (as written, within its text)", snip(r.Old, 60))
		case n > 1:
			return nil, nil, fmt.Errorf("%q is in the document %d times: give more of the text around it so it occurs once", snip(r.Old, 60), n)
		}
		pos := strings.Index(full, r.Old)
		idx := -1
		for i := range doc.texts {
			if pos >= at[i] && pos+len(r.Old) <= at[i]+len(doc.texts[i].text) {
				idx = i
			}
		}
		if idx < 0 {
			return nil, nil, fmt.Errorf("%q spans text with different formatting: it can't be replaced exactly here; replace a part within one formatting (e.g. one word or sentence)", snip(r.Old, 60))
		}
		if doc.texts[idx].field {
			return nil, nil, fmt.Errorf("%q is part of a field (a date, page number, reference…) the office app fills in: it isn't replaced here", snip(r.Old, 60))
		}
		if used[idx] {
			return nil, nil, fmt.Errorf("two replacements in the same piece of text: give them as one")
		}
		used[idx] = true
		t := doc.texts[idx]
		nt := strings.Replace(t.text, r.Old, r.New, 1)
		edits = append(edits, edit{t.start, t.end, `<w:t xml:space="preserve">` + esc(nt) + `</w:t>`})
		lines = append(lines, fmt.Sprintf("Replace %q with %q", snip(r.Old, 80), snip(r.New, 80)))
	}
	if strings.TrimSpace(appendMD) != "" {
		if len(appendMD) > MaxText {
			return nil, nil, fmt.Errorf("append: at most %d MB", MaxText>>20)
		}
		body, _, err := mdBody(appendMD, bodyOpts{listsAsText: true, headings: docStyles(p, docPart)})
		if err != nil {
			return nil, nil, err
		}
		edits = append(edits, edit{doc.insertAt, doc.insertAt, body})
		paras := strings.Count(body, "<w:p>") - strings.Count(body, "<w:tc>")
		tables := strings.Count(body, "<w:tbl>")
		l := fmt.Sprintf("Append at the end: %d paragraph(s)", max(paras, 0))
		if tables > 0 {
			l += fmt.Sprintf(" and %d table(s)", tables)
		}
		lines = append(lines, l+", starting: "+strconv.Quote(snip(firstLine(appendMD), 80)))
	}
	if len(edits) == 0 {
		return nil, nil, fmt.Errorf("nothing to change: give append and/or replace")
	}
	nb, err := splice(b, sortEdits(edits))
	if err != nil {
		return nil, nil, fmt.Errorf("this document has a structure the editor doesn't fully understand: not changing it")
	}
	p.set(docPart, nb)
	out, err := p.bytes()
	return out, lines, err
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return strings.TrimLeft(l, "#-*| ")
		}
	}
	return ""
}

// docSettings: refuse a document that is protected against editing, or
// that tracks changes (a change made here wouldn't be tracked).
func docSettings(p *pkg, docPart string) error {
	rels, _, _, err := p.rels(docPart)
	if err != nil {
		return nil
	}
	for _, r := range rels {
		if !strings.HasSuffix(r[1], "/settings") {
			continue
		}
		b, err := p.read(r[0])
		if err != nil || b == nil {
			continue
		}
		d := xml.NewDecoder(bytes.NewReader(b))
		for {
			tok, err := d.Token()
			if err != nil {
				break
			}
			se, ok := tok.(xml.StartElement)
			if !ok || se.Name.Space != wordNS {
				continue
			}
			switch se.Name.Local {
			case "documentProtection":
				if v := attr(se, "enforcement"); v == "1" || v == "true" || v == "on" {
					return fmt.Errorf("this document is protected against editing: change it in your office app")
				}
			case "trackRevisions":
				if v := attr(se, "val"); v == "" || v == "1" || v == "true" || v == "on" {
					return fmt.Errorf("this document tracks changes: change it in your office app, so the change is tracked")
				}
			}
		}
	}
	return nil
}
