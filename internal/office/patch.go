package office

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"
)

// Editing existing Office files: only the parts that must change are
// rewritten, everything else is copied byte for byte (compressed data
// included). Anything the patcher doesn't fully understand where it would
// have to change it is refused: no half-edited files.

// MaxEditFile: the largest file edited.
const MaxEditFile = 20 << 20

var errNotUnderstood = errors.New("not understood")

// pkg: an Office package (a zip) opened for patching.
type pkg struct {
	zr      *zip.Reader
	files   map[string]*zip.File
	changed map[string][]byte
	added   []string
}

func openPkg(data []byte, kind string) (*pkg, error) {
	if len(data) > MaxEditFile {
		return nil, fmt.Errorf("the file is larger than %d MB: too large to edit here", MaxEditFile>>20)
	}
	if bytes.HasPrefix(data, []byte{0xD0, 0xCF, 0x11, 0xE0}) {
		return nil, fmt.Errorf("this file is encrypted (password-protected) or an old binary Office file: it can't be edited here")
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("not a valid %s file: %v", kind, err)
	}
	p := &pkg{zr: zr, files: map[string]*zip.File{}, changed: map[string][]byte{}}
	for _, f := range zr.File {
		lower := strings.ToLower(f.Name)
		if strings.Contains(lower, "vbaproject") || strings.HasSuffix(lower, ".bin") && strings.Contains(lower, "vba") {
			return nil, fmt.Errorf("this file contains macros: it isn't edited here")
		}
		if f.Flags&0x1 != 0 {
			return nil, fmt.Errorf("this file is encrypted: it can't be edited here")
		}
		p.files[f.Name] = f
	}
	ct, err := p.read("[Content_Types].xml")
	if err != nil || ct == nil {
		return nil, fmt.Errorf("not a valid %s file: no content types", kind)
	}
	if bytes.Contains(ct, []byte("macroEnabled")) || bytes.Contains(ct, []byte("vbaProject")) {
		return nil, fmt.Errorf("this file is macro-enabled: it isn't edited here")
	}
	return p, nil
}

func (p *pkg) read(name string) ([]byte, error) {
	if b, ok := p.changed[name]; ok {
		return b, nil
	}
	f := p.files[name]
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
		return nil, fmt.Errorf("a part of this file is too large to edit")
	}
	return b, nil
}

const maxPart = 32 << 20

func (p *pkg) set(name string, b []byte) {
	if _, ok := p.files[name]; !ok {
		if _, already := p.changed[name]; !already {
			p.added = append(p.added, name)
		}
	}
	p.changed[name] = b
}

// bytes: the package with the changes; unchanged parts copied raw, in
// their order, new parts at the end.
func (p *pkg) bytes() ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range p.zr.File {
		if b, ok := p.changed[f.Name]; ok {
			w, err := zw.CreateHeader(&zip.FileHeader{Name: f.Name, Method: zip.Deflate, Modified: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
			if err != nil {
				return nil, err
			}
			if _, err := w.Write(b); err != nil {
				return nil, err
			}
			continue
		}
		raw, err := f.OpenRaw()
		if err != nil {
			return nil, err
		}
		fh := f.FileHeader
		w, err := zw.CreateRaw(&fh)
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(w, raw); err != nil {
			return nil, err
		}
	}
	for _, name := range p.added {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(p.changed[name]); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// rels: a part's relationships: id → (target, type), resolved targets.
func (p *pkg) rels(part string) (map[string][2]string, []byte, string, error) {
	dir, file := path.Split(part)
	name := dir + "_rels/" + file + ".rels"
	b, err := p.read(name)
	if err != nil || b == nil {
		return nil, nil, name, fmt.Errorf("no relationships for %s", part)
	}
	out := map[string][2]string{}
	d := xml.NewDecoder(bytes.NewReader(b))
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, name, errNotUnderstood
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "Relationship" {
			t := attr(se, "Target")
			if strings.HasPrefix(t, "/") {
				t = strings.TrimPrefix(t, "/")
			} else {
				t = path.Join(dir, t)
			}
			out[attr(se, "Id")] = [2]string{t, attr(se, "Type")}
		}
	}
	return out, b, name, nil
}

func attr(se xml.StartElement, local string) string {
	for _, a := range se.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// splice: b with each [start,end) replaced; the ranges don't overlap.
type edit struct {
	start, end int
	with       string
}

func splice(b []byte, edits []edit) ([]byte, error) {
	// in order, checking overlap
	for i := 1; i < len(edits); i++ {
		for j := i; j > 0 && edits[j].start < edits[j-1].start; j-- {
			edits[j], edits[j-1] = edits[j-1], edits[j]
		}
	}
	var out bytes.Buffer
	last := 0
	for _, e := range edits {
		if e.start < last || e.end < e.start || e.end > len(b) {
			return nil, errNotUnderstood
		}
		out.Write(b[last:e.start])
		out.WriteString(e.with)
		last = e.end
	}
	out.Write(b[last:])
	return out.Bytes(), nil
}
