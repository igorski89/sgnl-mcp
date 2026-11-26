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

	"github.com/igorski89/sgnl-mcp/internal/auth"
	"github.com/igorski89/sgnl-mcp/internal/office"
	"github.com/igorski89/sgnl-mcp/internal/opencloud"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---- file_office_edit: change an existing .xlsx or .docx in place, as
// file_edit does for text: only after the person confirms every change,
// bound to the version shown, written only if the file is still that
// version; the file app keeps the previous one. Only the parts that must
// change are rewritten; anything the editor doesn't fully understand is
// refused.

type FileOfficeEditIn struct {
	Path    string               `json:"path" jsonschema:"the .xlsx or .docx: a path from file_search or folder_list, or a short one like /budget.xlsx"`
	Changes []office.XlsxEdit    `json:"changes,omitempty" jsonschema:"for .xlsx: append_rows, set_cells (A1-style) or add_sheet"`
	Append  string               `json:"append,omitempty" jsonschema:"for .docx: Markdown to add at the end (headings, paragraphs, lists, | tables |)"`
	Replace []office.TextReplace `json:"replace,omitempty" jsonschema:"for .docx: exact text replacements (each must occur exactly once, within one formatting run)"`
}

type FileOfficeEditOut struct {
	Path    string   `json:"path"`
	Changes []string `json:"changes"`
	Note    string   `json:"note"`
}

var officeTypes = map[string]string{
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
}

func (t *Tools) fileOfficeEdit(ctx context.Context, req *mcp.CallToolRequest, in FileOfficeEditIn) (*mcp.CallToolResult, FileOfficeEditOut, error) {
	tok, err := t.Auth.FilesToken(ctx)
	if err != nil {
		return nil, FileOfficeEditOut{}, err
	}
	spaces, err := t.Files.Spaces(ctx, tok)
	if err != nil {
		return nil, FileOfficeEditOut{}, err
	}
	p := personalPath(spaces, strings.TrimSpace(in.Path))
	sp := spaceOf(spaces, p)
	if sp == nil {
		return nil, FileOfficeEditOut{}, fmt.Errorf("path %q not recognised: use a path from file_search or folder_list", oneLine(p, 200))
	}
	name := path.Base(p)
	ext := strings.ToLower(path.Ext(name))
	ct, ok := officeTypes[ext]
	switch {
	case ext == ".xlsm" || ext == ".docm":
		return nil, FileOfficeEditOut{}, fmt.Errorf("files with macros (%s) aren't edited here", ext)
	case ext == ".xls" || ext == ".doc":
		return nil, FileOfficeEditOut{}, fmt.Errorf("old Office files (%s) can't be edited here: save it as %sx in your office app first", ext, ext)
	case !ok:
		return nil, FileOfficeEditOut{}, fmt.Errorf("only .xlsx and .docx are edited here (text files: file_edit)")
	}
	if ext == ".xlsx" && (in.Append != "" || len(in.Replace) > 0) || ext == ".docx" && len(in.Changes) > 0 {
		return nil, FileOfficeEditOut{}, fmt.Errorf("for .xlsx give changes; for .docx give append and/or replace")
	}
	if len(in.Changes) > 50 || len(in.Replace) > 50 {
		return nil, FileOfficeEditOut{}, fmt.Errorf("at most 50 changes at once")
	}
	data, etag, err := t.Files.ReadForEdit(ctx, tok, p, office.MaxEditFile)
	if err != nil {
		return nil, FileOfficeEditOut{}, err
	}
	var out []byte
	var lines []string
	if ext == ".xlsx" {
		out, lines, err = office.EditXlsx(data, in.Changes)
	} else {
		out, lines, err = office.EditDocx(data, in.Append, in.Replace)
	}
	if err != nil {
		return nil, FileOfficeEditOut{}, err
	}

	me, _ := auth.Person(ctx)
	shown := shownPath(sp, p)
	shared := sp.Type != "personal"
	if !shared {
		if shared, err = t.Files.SharedWithOthers(ctx, tok, sp.Path, p); err != nil {
			return nil, FileOfficeEditOut{}, err
		}
	}
	first := fmt.Sprintf("Change %q: %s", shown, oneLine(lines[0], 160))
	if shared {
		first = fmt.Sprintf("Change the SHARED file %q (others see the change): %s", shown, oneLine(lines[0], 160))
	}
	if n := len(lines); n > 1 {
		first += fmt.Sprintf(", and %d more change(s)", n-1)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s?\n\nFile: %s, %s → %s\n", first, shown, size(len(data)), size(len(out)))
	for i, l := range lines {
		if i == 20 {
			fmt.Fprintf(&b, "… and %d more change(s)\n", len(lines)-20)
			break
		}
		fmt.Fprintf(&b, "%d. %s\n", i+1, oneLine(l, 300))
	}
	b.WriteString("Everything else in the file stays as it is; the previous version is kept.\n")
	if t.outside.recent(me.Username) {
		b.WriteString("\nWARNING: your assistant read email from outside the company in the last 30 minutes; make sure this is what you asked for.\n")
	}
	sum := sha256.Sum256(out)
	res, ok, err := t.confirm(ctx, req, b.String(), p+"|"+etag+"|"+hex.EncodeToString(sum[:]))
	if err != nil || res != nil {
		return res, FileOfficeEditOut{}, err
	}
	if !ok {
		return nil, FileOfficeEditOut{}, fmt.Errorf("the person declined: the file wasn't changed")
	}
	if _, err := t.Files.Overwrite(ctx, tok, p, etag, ct, out); err != nil {
		if errors.Is(err, opencloud.ErrChanged) {
			return nil, FileOfficeEditOut{}, fmt.Errorf("the file changed after the person was asked: nothing was written; ask again")
		}
		return nil, FileOfficeEditOut{}, err
	}
	slog.Info("office file changed", "user", me.Username, "changes", len(lines), "bytes_before", len(data), "bytes_after", len(out))
	return nil, FileOfficeEditOut{p, lines, "Changed with the person's confirmation. The previous version is kept in the file's version history (file app: Versions)."}, nil
}
