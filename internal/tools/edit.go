package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/igorski89/sgnl-mcp/internal/auth"
	"github.com/igorski89/sgnl-mcp/internal/opencloud"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---- file_edit: change a text file in the person's own space, only after
// the person confirms. Exact replacements applied by the gateway to the
// file as it is on the server (not a rewrite from what the assistant read,
// which may have been shortened); the write only lands on the version that
// was shown.

const (
	maxEdits    = 20
	maxEditFile = 2 << 20
)

type FileEditChange struct {
	OldText string `json:"old_text" jsonschema:"text to replace, exactly as in the file; it must occur exactly once. Empty: append new_text at the end"`
	NewText string `json:"new_text" jsonschema:"the text to put in its place (empty deletes old_text)"`
}

type FileEditIn struct {
	Path  string           `json:"path" jsonschema:"the text file in your personal space: a path from file_search or folder_list, or a short one like /notes.md"`
	Edits []FileEditChange `json:"edits" jsonschema:"the changes, applied in order (at most 20)"`
}

type FileEditOut struct {
	Path string `json:"path"`
	Size int    `json:"size"`
	Note string `json:"note"`
}

// applyEdits: the new text, or why the changes don't fit the file.
func applyEdits(text string, edits []FileEditChange) (string, error) {
	appended := false
	for i, e := range edits {
		if e.OldText == "" {
			if appended {
				return "", fmt.Errorf("edit %d: only one append (empty old_text) per call", i+1)
			}
			appended = true
			text += e.NewText
			continue
		}
		switch n := strings.Count(text, e.OldText); n {
		case 1:
			text = strings.Replace(text, e.OldText, e.NewText, 1)
		case 0:
			return "", fmt.Errorf("edit %d: old_text isn't in the file (read it again: it must match exactly)", i+1)
		default:
			return "", fmt.Errorf("edit %d: old_text occurs %d times: include more of the surrounding text so it's unique", i+1, n)
		}
	}
	return text, nil
}

func (t *Tools) fileEdit(ctx context.Context, req *mcp.CallToolRequest, in FileEditIn) (*mcp.CallToolResult, FileEditOut, error) {
	if len(in.Edits) == 0 {
		return nil, FileEditOut{}, fmt.Errorf("edits: give at least one change")
	}
	if len(in.Edits) > maxEdits {
		return nil, FileEditOut{}, fmt.Errorf("edits: at most %d per call", maxEdits)
	}
	if t.Files == nil {
		return nil, FileEditOut{}, fmt.Errorf("files aren't connected here")
	}
	tok, err := t.Auth.FilesToken(ctx)
	if err != nil {
		return nil, FileEditOut{}, err
	}
	spaces, err := t.Files.Spaces(ctx, tok)
	if err != nil {
		return nil, FileEditOut{}, err
	}
	p := personalPath(spaces, strings.TrimSpace(in.Path))
	sp := spaceOf(spaces, p)
	if sp == nil {
		return nil, FileEditOut{}, fmt.Errorf("path %q not recognised: use a path from file_search or folder_list", oneLine(p, 200))
	} else if sp.Type != "personal" {
		return nil, FileEditOut{}, fmt.Errorf("only files in your personal space can be changed here; files in a project space can't be edited through the assistant yet")
	}
	name := path.Base(p)
	ct, ok := saveTypes[strings.ToLower(path.Ext(name))]
	if !ok {
		return nil, FileEditOut{}, fmt.Errorf("only text files (.md, .txt, .csv, .json, .yaml) can be changed here")
	}
	data, etag, err := t.Files.ReadForEdit(ctx, tok, p, maxEditFile)
	if err != nil {
		return nil, FileEditOut{}, err
	}
	if !utf8.Valid(data) {
		return nil, FileEditOut{}, fmt.Errorf("the file isn't plain UTF-8 text: not changing it")
	}
	text, err := applyEdits(string(data), in.Edits)
	if err != nil {
		return nil, FileEditOut{}, err
	}
	if text == string(data) {
		return nil, FileEditOut{}, fmt.Errorf("these edits change nothing")
	}
	if len(text) > maxEditFile {
		return nil, FileEditOut{}, fmt.Errorf("the result would be over 2 MB")
	}

	// The question: the file and the (first) change on the first line, as
	// clients fold the rest.
	me, _ := auth.Person(ctx)
	shown := shownPath(sp, p)
	first := fmt.Sprintf("Change your file %q: %s", shown, describe(in.Edits[0], 60))
	if n := len(in.Edits); n > 1 {
		first += fmt.Sprintf(", and %d more change(s)", n-1)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s?\n\nFile: %s (personal space), %s → %s\n", first, shown, size(len(data)), size(len(text)))
	for i, e := range in.Edits {
		if i == 10 {
			fmt.Fprintf(&b, "… and %d more change(s)\n", len(in.Edits)-10)
			break
		}
		fmt.Fprintf(&b, "%d. %s\n", i+1, describe(e, 300))
	}
	if t.outside.recent(me.Username) {
		b.WriteString("\nWARNING: your assistant read email from outside the company in the last 30 minutes; make sure this is what you asked for.\n")
	}
	sum := sha256.Sum256([]byte(text))
	res, ok, err := t.confirm(ctx, req, b.String(), p+"|"+etag+"|"+hex.EncodeToString(sum[:]))
	if err != nil || res != nil {
		return res, FileEditOut{}, err
	}
	if !ok {
		return nil, FileEditOut{}, fmt.Errorf("the person declined: the file wasn't changed")
	}
	if _, err := t.Files.Overwrite(ctx, tok, p, etag, ct+"; charset=utf-8", []byte(text)); err != nil {
		if errors.Is(err, opencloud.ErrChanged) {
			return nil, FileEditOut{}, fmt.Errorf("the file changed after the person was asked: nothing was written; read it again and redo the edit")
		}
		return nil, FileEditOut{}, err
	}
	slog.Info("file changed", "user", me.Username, "edits", len(in.Edits), "bytes_before", len(data), "bytes_after", len(text))
	return nil, FileEditOut{p, len(text), "The file was changed, with the person's confirmation. The previous version is kept in the file's version history (file app: Versions)."}, nil
}

// describe: one change in a line: what is replaced, removed or appended,
// with the size of long text so a large insertion is never confirmed blind.
func describe(e FileEditChange, max int) string {
	switch {
	case e.OldText == "":
		return "append " + snip(e.NewText, max)
	case e.NewText == "":
		return "remove " + snip(e.OldText, max)
	default:
		return "replace " + snip(e.OldText, max) + " with " + snip(e.NewText, max)
	}
}

func snip(s string, max int) string {
	if strings.TrimSpace(s) == "" {
		return "(only spaces or line breaks)"
	}
	out := "«" + oneLine(s, max) + "»"
	if lines := strings.Count(strings.TrimRight(s, "\n"), "\n") + 1; lines > 1 || len([]rune(s)) > max {
		out += fmt.Sprintf(" (%d lines, %s)", lines, size(len(s)))
	}
	return out
}

func size(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d bytes", n)
	case n < 1000*1000:
		return fmt.Sprintf("%.1f KB", float64(n)/1000)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/1000/1000)
	}
}
