// Package textx: plain text from documents (text formats, HTML, .docx,
// PDF), for the model to read. Other formats are refused clearly.
package textx

import (
	"archive/zip"
	"bytes"
	"fmt"
	"html"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
)

var (
	reDrop  = regexp.MustCompile(`(?is)<(script|style|head)[^>]*>.*?</(script|style|head)>`)
	reBreak = regexp.MustCompile(`(?i)<(br|/p|/div|/tr|/li|/h[1-6])[^>]*>`)
	reTag   = regexp.MustCompile(`<[^>]*>`)
	reBlank = regexp.MustCompile(`\n[ \t]*(\n[ \t]*)+`)
)

// HTML: readable text from HTML (good enough for a model).
func HTML(s string) string {
	s = reDrop.ReplaceAllString(s, "")
	s = reBreak.ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	return strings.TrimSpace(reBlank.ReplaceAllString(s, "\n\n"))
}

var textExt = map[string]bool{".md": true, ".txt": true, ".csv": true, ".json": true, ".yaml": true, ".yml": true, ".log": true, ".ics": true, ".vcf": true}

// Extract: text of a document by name and content type. truncated: data
// is only the start of the file (binary formats then can't be read).
func Extract(name, ctype string, data []byte, truncated bool) (string, error) {
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
