// Package textx: plain text from documents (text formats, HTML, .docx,
// PDF), for the model to read. Other formats are refused clearly.
package textx

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	nethtml "golang.org/x/net/html"

	"github.com/ledongthuc/pdf"
)

var reBlank = regexp.MustCompile(`\n[ \t]*(\n[ \t]*)+`)

// Elements whose text a reader never sees.
var invisibleTags = map[string]bool{"script": true, "style": true, "head": true, "title": true,
	"template": true, "noscript": true, "svg": true, "object": true, "iframe": true}

// Elements that start a new line.
var blockTags = map[string]bool{"br": true, "p": true, "div": true, "tr": true, "li": true, "table": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "blockquote": true, "pre": true, "hr": true}

// Hidden text is the usual carrier for instructions aimed at an AI. Some
// styles hide a whole element (display:none, opacity 0, a zero-size box
// that clips); others (font size, visibility) are inherited and a child
// can undo them, so they are tracked down the tree like a browser does.

// styles: an inline style attribute as property -> value, lowercased.
func styles(n *nethtml.Node) map[string]string {
	out := map[string]string{}
	for _, a := range n.Attr {
		if !strings.EqualFold(a.Key, "style") {
			continue
		}
		for _, d := range strings.Split(a.Val, ";") {
			k, v, ok := strings.Cut(d, ":")
			if ok {
				v = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(strings.ToLower(v)), "!important"))
				out[strings.TrimSpace(strings.ToLower(k))] = strings.TrimSpace(v)
			}
		}
	}
	return out
}

var reZeroLen = regexp.MustCompile(`^-?0*(\.0+)?(px|pt|em|rem|%|vh|vw)?$`)
var reTinyFont = regexp.MustCompile(`^(0*(\.\d+)?|1)(px|pt)?$|^0*(\.0+)?(em|rem|%)$`)

func zeroLen(v string) bool { return v != "" && reZeroLen.MatchString(v) }

// hiddenBox: the element and everything in it is invisible, whatever the
// children say.
func hiddenBox(n *nethtml.Node, st map[string]string) bool {
	if invisibleTags[n.Data] {
		return true
	}
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, "hidden") {
			return true
		}
	}
	if st["display"] == "none" || zeroLen(st["opacity"]) {
		return true
	}
	clips := strings.Contains(st["overflow"], "hidden") || strings.Contains(st["overflow"], "clip")
	for _, k := range []string{"height", "max-height", "width", "max-width"} {
		if clips && zeroLen(st[k]) {
			return true
		}
	}
	if ti := st["text-indent"]; strings.HasPrefix(ti, "-") && len(strings.TrimLeft(ti, "-0123456789.")) < len(ti)-4 {
		return true // pushed far off-screen (-1000px and the like)
	}
	return false
}

func linkTarget(n *nethtml.Node) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, "href") {
			h := strings.TrimSpace(a.Val)
			l := strings.ToLower(h)
			if strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "mailto:") {
				return h
			}
		}
	}
	return ""
}

// HTML: the text a person would see, as plain text. Hidden elements,
// comments and invisible characters are dropped.
func HTML(s string) string {
	doc, err := nethtml.Parse(strings.NewReader(s))
	if err != nil {
		return ""
	}
	var b strings.Builder
	// tiny: inherited font size too small to read; unseen: inherited
	// visibility:hidden. Children can undo both.
	var walk func(n *nethtml.Node, tiny, unseen bool)
	walk = func(n *nethtml.Node, tiny, unseen bool) {
		switch n.Type {
		case nethtml.CommentNode:
			return
		case nethtml.TextNode:
			if !tiny && !unseen {
				b.WriteString(n.Data)
			}
			return
		case nethtml.ElementNode:
			st := styles(n)
			if hiddenBox(n, st) {
				return
			}
			if fs, ok := st["font-size"]; ok {
				tiny = reTinyFont.MatchString(fs)
			}
			switch st["visibility"] {
			case "hidden", "collapse":
				unseen = true
			case "visible":
				unseen = false
			}
			if blockTags[n.Data] {
				b.WriteString("\n")
			}
		}
		start := b.Len()
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, tiny, unseen)
		}
		if n.Type == nethtml.ElementNode {
			// A link's address, as a person sees it on hover: "text (address)".
			if n.Data == "a" && !tiny && !unseen && b.Len() > start {
				if h := linkTarget(n); h != "" && !strings.Contains(b.String()[start:], h) {
					b.WriteString(" (" + h + ")")
				}
			}
			if blockTags[n.Data] && n.Data != "br" {
				b.WriteString("\n")
			}
		}
	}
	walk(doc, false, false)
	return Visible(b.String())
}

// Visible: s without characters a reader can't see (zero-width, bidi
// controls, Unicode tag characters used for "ASCII smuggling", soft
// hyphens), with blank-line runs collapsed.
func Visible(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 0x200B && r <= 0x200F, r >= 0x202A && r <= 0x202E, r >= 0x2060 && r <= 0x2064,
			r >= 0x2066 && r <= 0x2069, r == 0xFEFF, r == 0x00AD, r == 0x180E, r >= 0xE0000 && r <= 0xE007F:
			return -1
		}
		return r
	}, s)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t\r")
	}
	return strings.TrimSpace(reBlank.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

var textExt = map[string]bool{".md": true, ".txt": true, ".csv": true, ".json": true, ".yaml": true, ".yml": true, ".log": true, ".ics": true, ".vcf": true}

// Extract: text of a document by name and content type. truncated: data
// is only the start of the file (binary formats then can't be read).
func Extract(name, ctype string, data []byte, truncated bool) (string, error) {
	s, err := extract(name, ctype, data, truncated)
	return Visible(s), err
}

func extract(name, ctype string, data []byte, truncated bool) (string, error) {
	ext := strings.ToLower(path.Ext(name))
	ct := strings.ToLower(ctype)
	switch {
	case ext == ".docx" || strings.Contains(ct, "wordprocessingml"):
		if truncated {
			return "", fmt.Errorf("%s is too large to read", name)
		}
		return docx(data)
	case ext == ".pdf" || strings.Contains(ct, "application/pdf"):
		if truncated {
			return "", fmt.Errorf("%s is too large to read", name)
		}
		return pdfText(data)
	case ext == ".html" || ext == ".htm" || strings.HasPrefix(ct, "text/html"):
		return HTML(string(data)), nil
	case strings.HasPrefix(ct, "text/"), strings.Contains(ct, "json"), strings.Contains(ct, "xml"), textExt[ext]:
		if !utf8.Valid(data) && !truncated {
			return "", fmt.Errorf("%s is not UTF-8 text", name)
		}
		return strings.ToValidUTF8(string(data), ""), nil
	}
	return "", fmt.Errorf("can't read %s files yet (%s); text, Markdown, CSV, HTML, PDF and .docx work", ext, ctype)
}

var (
	reHiddenRun = regexp.MustCompile(`(?s)<w:r\b[^>]*>(?:(?:[^<]|<[^/]|</[^w]|</w:[^r]|</w:r[^>])*?)<w:(?:vanish|specVanish)\b[^>]*/>.*?</w:r>`)
	reVanishOff = regexp.MustCompile(`<w:(?:vanish|specVanish)\s+w:val="(?:false|0|off)"`)
)

func docx(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("not a valid .docx: %v", err)
	}
	for _, f := range zr.File {
		if f.Name != "word/document.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		defer rc.Close()
		x, err := io.ReadAll(io.LimitReader(rc, 32<<20))
		if err != nil {
			return "", err
		}
		// Runs marked hidden (w:vanish) aren't shown by Word: drop them.
		s := reHiddenRun.ReplaceAllStringFunc(string(x), func(run string) string {
			if reVanishOff.MatchString(run) {
				return run
			}
			return ""
		})
		s = strings.NewReplacer("</w:p>", "\n", "<w:tab/>", "\t", "<w:br/>", "\n").Replace(s)
		return strings.TrimSpace(HTML(s)), nil
	}
	return "", fmt.Errorf("not a valid .docx: no word/document.xml")
}

func pdfText(data []byte) (s string, err error) {
	defer func() { // the PDF library panics on some malformed files
		if r := recover(); r != nil {
			s, err = "", fmt.Errorf("can't read this PDF")
		}
	}()
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("not a readable PDF: %v", err)
	}
	rd, err := r.GetPlainText()
	if err != nil {
		return "", fmt.Errorf("not a readable PDF: %v", err)
	}
	b, err := io.ReadAll(io.LimitReader(rd, 4<<20))
	if err != nil {
		return "", err
	}
	t := strings.TrimSpace(string(b))
	if t == "" {
		return "", fmt.Errorf("this PDF has no text layer (a scan?)")
	}
	return t, nil
}
