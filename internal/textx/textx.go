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

// reHiddenStyle: inline styles that hide text from a reader. Hidden text
// is the usual carrier for instructions aimed at an AI.
var reHiddenStyle = regexp.MustCompile(`(?i)(display\s*:\s*none|visibility\s*:\s*(hidden|collapse)|opacity\s*:\s*0(\.0*)?\s*(;|$|!)|font-size\s*:\s*(0|0?\.\d+|1)(px|pt|em|rem|%)?\s*(;|$|!)|(max-)?(height|width)\s*:\s*0(px)?\s*(;|$|!)|text-indent\s*:\s*-\d{3,})`)

func hiddenElement(n *nethtml.Node) bool {
	if invisibleTags[n.Data] {
		return true
	}
	for _, a := range n.Attr {
		switch strings.ToLower(a.Key) {
		case "hidden":
			return true
		case "style":
			if reHiddenStyle.MatchString(a.Val) {
				return true
			}
		}
	}
	return false
}

// HTML: the text a person would see, as plain text. Hidden elements,
// comments and invisible characters are dropped.
func HTML(s string) string {
	doc, err := nethtml.Parse(strings.NewReader(s))
	if err != nil {
		return ""
	}
	var b strings.Builder
	var walk func(*nethtml.Node)
	walk = func(n *nethtml.Node) {
		switch n.Type {
		case nethtml.CommentNode:
			return
		case nethtml.TextNode:
			b.WriteString(n.Data)
			return
		case nethtml.ElementNode:
			if hiddenElement(n) {
				return
			}
			if blockTags[n.Data] {
				b.WriteString("\n")
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == nethtml.ElementNode && blockTags[n.Data] && n.Data != "br" {
			b.WriteString("\n")
		}
	}
	walk(doc)
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
		s := strings.NewReplacer("</w:p>", "\n", "<w:tab/>", "\t", "<w:br/>", "\n").Replace(string(x))
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
