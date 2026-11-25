package tools

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/igorski89/sgnl-mcp/internal/opencloud"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---- file_move: rename and/or move within one space. In the person's
// own unshared folders it just happens (it can be moved back); wherever
// others see the place it leaves or enters, the person is asked first.

type FileMoveIn struct {
	Path     string `json:"path" jsonschema:"the file or folder: a path from file_search or folder_list, or a short one like /notes.md"`
	ToFolder string `json:"to_folder,omitempty" jsonschema:"move into this folder (same space); default: stay in its folder"`
	NewName  string `json:"new_name,omitempty" jsonschema:"rename to this; default: keep the name"`
}

type FileChangeOut struct {
	Path string `json:"path,omitempty"`
	Note string `json:"note"`
}

func plainName(n string) error {
	if n == "" || strings.ContainsAny(n, "/\\") || strings.HasPrefix(n, ".") || len(n) > 200 || strings.ContainsFunc(n, func(r rune) bool { return r < 32 }) {
		return fmt.Errorf("new_name: a plain name (no slashes, not hidden)")
	}
	return nil
}

func (t *Tools) fileMove(ctx context.Context, req *mcp.CallToolRequest, in FileMoveIn) (*mcp.CallToolResult, FileChangeOut, error) {
	tok, err := t.Auth.FilesToken(ctx)
	if err != nil {
		return nil, FileChangeOut{}, err
	}
	spaces, err := t.Files.Spaces(ctx, tok)
	if err != nil {
		return nil, FileChangeOut{}, err
	}
	src := personalPath(spaces, strings.TrimSpace(in.Path))
	sp := spaceOf(spaces, src)
	if sp == nil {
		return nil, FileChangeOut{}, fmt.Errorf("path %q not recognised: use a path from file_search or folder_list", oneLine(src, 200))
	}
	isFolder := strings.HasSuffix(src, "/")
	src = strings.TrimSuffix(src, "/")
	if src+"/" == sp.Path || src == strings.TrimSuffix(sp.Path, "/") {
		return nil, FileChangeOut{}, fmt.Errorf("a whole space can't be moved or renamed")
	}
	from := path.Dir(src) + "/"
	to := from
	if f := strings.TrimSpace(in.ToFolder); f != "" {
		to = personalPath(spaces, f)
		if !strings.HasSuffix(to, "/") {
			to += "/"
		}
		if dsp := spaceOf(spaces, to); dsp == nil || dsp.Path != sp.Path {
			return nil, FileChangeOut{}, fmt.Errorf("to_folder must be in the same space (moving between spaces isn't offered)")
		}
		if strings.HasPrefix(to, src+"/") {
			return nil, FileChangeOut{}, fmt.Errorf("a folder can't be moved into itself")
		}
	}
	name := path.Base(src)
	if in.NewName != "" {
		if err := plainName(strings.TrimSpace(in.NewName)); err != nil {
			return nil, FileChangeOut{}, err
		}
		name = url.PathEscape(strings.TrimSpace(in.NewName))
	}
	dst := to + name
	if dst == src {
		return nil, FileChangeOut{}, fmt.Errorf("nothing to change")
	}
	if ex, err := t.Files.Exists(ctx, tok, src); err != nil {
		return nil, FileChangeOut{}, err
	} else if !ex {
		return nil, FileChangeOut{}, fmt.Errorf("nothing at %q", shownPath(sp, src))
	}

	// Who else sees either place?
	seenFrom, err := t.othersSee(ctx, tok, sp, src)
	if err != nil {
		return nil, FileChangeOut{}, err
	}
	seenTo, err := t.othersSee(ctx, tok, sp, to)
	if err != nil {
		return nil, FileChangeOut{}, err
	}
	if seenFrom != "" || seenTo != "" {
		what := "file"
		if isFolder {
			what = "folder"
		}
		first := fmt.Sprintf("Move the %s %q to %q?", what, shownPath(sp, src), shownPath(sp, dst))
		if seenTo != "" {
			first += " Others will see it there."
		} else {
			first += " Others who see it now won't anymore."
		}
		var b strings.Builder
		b.WriteString(first + "\n\n")
		if seenFrom != "" && seenTo == "" {
			b.WriteString("Where it is now, others can see it; after the move they no longer can.\n")
		}
		if seenTo != "" {
			b.WriteString("Where it goes: " + seenTo + "\n")
		}
		res, ok, err := t.confirm(ctx, req, b.String(), src+"|"+dst)
		if err != nil || res != nil {
			return res, FileChangeOut{}, err
		}
		if !ok {
			return nil, FileChangeOut{}, fmt.Errorf("the person declined: nothing was moved")
		}
	}
	if err := t.Files.Move(ctx, tok, src, dst); err != nil {
		return nil, FileChangeOut{}, err
	}
	if isFolder {
		dst += "/"
	}
	return nil, FileChangeOut{dst, "Moved/renamed. It can be moved back the same way."}, nil
}

// ---- file_trash: into the space's trash bin, after the person confirms.

type FileTrashIn struct {
	Path string `json:"path" jsonschema:"the file or folder: a path from file_search or folder_list, or a short one like /notes.md"`
}

func (t *Tools) fileTrash(ctx context.Context, req *mcp.CallToolRequest, in FileTrashIn) (*mcp.CallToolResult, FileChangeOut, error) {
	tok, err := t.Auth.FilesToken(ctx)
	if err != nil {
		return nil, FileChangeOut{}, err
	}
	spaces, err := t.Files.Spaces(ctx, tok)
	if err != nil {
		return nil, FileChangeOut{}, err
	}
	p := personalPath(spaces, strings.TrimSpace(in.Path))
	sp := spaceOf(spaces, p)
	if sp == nil {
		return nil, FileChangeOut{}, fmt.Errorf("path %q not recognised: use a path from file_search or folder_list", oneLine(p, 200))
	}
	p = strings.TrimSuffix(p, "/")
	if p+"/" == sp.Path || p == strings.TrimSuffix(sp.Path, "/") {
		return nil, FileChangeOut{}, fmt.Errorf("a whole space can't be moved to the trash")
	}
	it, err := t.Files.Item(ctx, tok, p)
	if err != nil {
		return nil, FileChangeOut{}, err
	}
	what := "FILE"
	count := ""
	if it.Folder {
		what = "FOLDER"
		if fs, err := t.Files.List(ctx, tok, p+"/"); err == nil {
			count = fmt.Sprintf(" with %d item(s) directly in it", len(fs))
		}
	}
	seen, err := t.othersSee(ctx, tok, sp, p)
	if err != nil {
		return nil, FileChangeOut{}, err
	}
	first := fmt.Sprintf("Move the %s %q%s to the trash bin?", what, shownPath(sp, p), count)
	if seen != "" {
		first += " Others who can see it lose it too."
	}
	var b strings.Builder
	b.WriteString(first + "\n\n")
	if sp.Type != "personal" {
		fmt.Fprintf(&b, "It is in the shared space %q.\n", oneLine(sp.Name, 60))
	}
	if t.FilesTrashDays > 0 {
		fmt.Fprintf(&b, "You can restore it from the trash bin in your file app for %d days.\n", t.FilesTrashDays)
	} else {
		b.WriteString("You can restore it from the trash bin in your file app.\n")
	}
	res, ok, err := t.confirm(ctx, req, b.String(), p+"|"+it.ID)
	if err != nil || res != nil {
		return res, FileChangeOut{}, err
	}
	if !ok {
		return nil, FileChangeOut{}, fmt.Errorf("the person declined: nothing was moved to the trash")
	}
	// The same item as shown (not something put there since).
	if now, err := t.Files.Item(ctx, tok, p); err != nil || now.ID != it.ID {
		return nil, FileChangeOut{}, fmt.Errorf("it changed after the person was asked: nothing was moved to the trash")
	}
	if err := t.Files.ToTrash(ctx, tok, p); err != nil {
		return nil, FileChangeOut{}, err
	}
	return nil, FileChangeOut{Note: "Moved to the trash bin, with the person's confirmation; restorable (file_restore, or the file app)."}, nil
}

// ---- file_restore: from the trash bin back to where it was.

type FileRestoreIn struct {
	ID    string `json:"id" jsonschema:"the trash item's id from file_trash_list"`
	Space string `json:"space,omitempty" jsonschema:"the space's name, as for file_trash_list; default: your personal space"`
}

func (t *Tools) fileRestore(ctx context.Context, req *mcp.CallToolRequest, in FileRestoreIn) (*mcp.CallToolResult, FileChangeOut, error) {
	tok, err := t.Auth.FilesToken(ctx)
	if err != nil {
		return nil, FileChangeOut{}, err
	}
	spaces, err := t.Files.Spaces(ctx, tok)
	if err != nil {
		return nil, FileChangeOut{}, err
	}
	var sp *opencloud.Space
	for i := range spaces {
		if (in.Space == "" && spaces[i].Type == "personal") || (in.Space != "" && strings.EqualFold(spaces[i].Name, in.Space)) {
			sp = &spaces[i]
			break
		}
	}
	if sp == nil {
		return nil, FileChangeOut{}, fmt.Errorf("no space %q (see folder_list)", oneLine(in.Space, 80))
	}
	items, err := t.Files.Trash(ctx, tok, sp.ID)
	if err != nil {
		return nil, FileChangeOut{}, err
	}
	var item *opencloud.TrashItem
	for i := range items {
		if items[i].ID == in.ID {
			item = &items[i]
		}
	}
	if item == nil {
		return nil, FileChangeOut{}, fmt.Errorf("no item %q in that trash bin (see file_trash_list)", oneLine(in.ID, 80))
	}
	// Back into a place others see: ask, as for any write there.
	parent := sp.Path + strings.TrimPrefix(path.Dir("/"+item.Location), "/")
	if !strings.HasSuffix(parent, "/") {
		parent += "/"
	}
	if seen, err := t.othersSee(ctx, tok, sp, parent); err == nil && seen != "" {
		res, ok, err := t.confirm(ctx, req, fmt.Sprintf("Restore %q to %q? %s", oneLine(item.Name, 80), oneLine(item.Location, 120), seen), sp.ID+"|"+item.ID)
		if err != nil || res != nil {
			return res, FileChangeOut{}, err
		}
		if !ok {
			return nil, FileChangeOut{}, fmt.Errorf("the person declined: nothing was restored")
		}
	}
	if err := t.Files.Restore(ctx, tok, sp.ID, sp.Path, item.ID, item.Location); err != nil {
		return nil, FileChangeOut{}, err
	}
	return nil, FileChangeOut{Note: "Restored to " + oneLine(item.Location, 200) + "."}, nil
}

// ---- file_version_restore: a file back to an earlier version, after the
// person confirms (like an edit: the current content becomes a version).

type FileVersionRestoreIn struct {
	Path    string `json:"path" jsonschema:"the file: a path from file_search or folder_list, or a short one like /notes.md"`
	Version string `json:"version" jsonschema:"the version's id from file_versions"`
}

func (t *Tools) fileVersionRestore(ctx context.Context, req *mcp.CallToolRequest, in FileVersionRestoreIn) (*mcp.CallToolResult, FileChangeOut, error) {
	tok, err := t.Auth.FilesToken(ctx)
	if err != nil {
		return nil, FileChangeOut{}, err
	}
	spaces, err := t.Files.Spaces(ctx, tok)
	if err != nil {
		return nil, FileChangeOut{}, err
	}
	p := personalPath(spaces, strings.TrimSpace(in.Path))
	sp := spaceOf(spaces, p)
	if sp == nil {
		return nil, FileChangeOut{}, fmt.Errorf("path %q not recognised: use a path from file_search or folder_list", oneLine(p, 200))
	}
	vs, err := t.Files.Versions(ctx, tok, p)
	if err != nil {
		return nil, FileChangeOut{}, err
	}
	var v *opencloud.Version
	for i := range vs {
		if vs[i].ID == in.Version {
			v = &vs[i]
		}
	}
	if v == nil {
		return nil, FileChangeOut{}, fmt.Errorf("no version %q of this file (see file_versions)", oneLine(in.Version, 80))
	}
	etag, err := t.Files.Etag(ctx, tok, p)
	if err != nil {
		return nil, FileChangeOut{}, err
	}
	seen, err := t.othersSee(ctx, tok, sp, p)
	if err != nil {
		return nil, FileChangeOut{}, err
	}
	first := fmt.Sprintf("Restore your file %q to its version of %s (%s bytes)?", shownPath(sp, p), oneLine(v.Modified, 40), oneLine(v.Size, 12))
	if seen != "" {
		first = fmt.Sprintf("Restore your SHARED file %q (others see it) to its version of %s (%s bytes)?", shownPath(sp, p), oneLine(v.Modified, 40), oneLine(v.Size, 12))
	}
	res, ok, err := t.confirm(ctx, req, first+"\n\nThe current content is kept as a version, so this can be undone the same way.\n", p+"|"+etag+"|"+v.ID)
	if err != nil || res != nil {
		return res, FileChangeOut{}, err
	}
	if !ok {
		return nil, FileChangeOut{}, fmt.Errorf("the person declined: the file wasn't changed")
	}
	if now, err := t.Files.Etag(ctx, tok, p); err != nil || now != etag {
		return nil, FileChangeOut{}, fmt.Errorf("the file changed after the person was asked: nothing was restored")
	}
	if err := t.Files.RestoreVersion(ctx, tok, p, v.ID); err != nil {
		return nil, FileChangeOut{}, err
	}
	return nil, FileChangeOut{p, "Restored, with the person's confirmation; the content before is now a version."}, nil
}

// ---- file_unshare: stop sharing an item of the person's with someone.

type FileUnshareIn struct {
	Path string `json:"path" jsonschema:"the shared file or folder: a path from file_search or folder_list, or a short one"`
	With string `json:"with" jsonschema:"whom to stop sharing with: their name as file_shares shows it, or 'link' for a link"`
}

func (t *Tools) fileUnshare(ctx context.Context, req *mcp.CallToolRequest, in FileUnshareIn) (*mcp.CallToolResult, FileChangeOut, error) {
	tok, err := t.Auth.FilesToken(ctx)
	if err != nil {
		return nil, FileChangeOut{}, err
	}
	spaces, err := t.Files.Spaces(ctx, tok)
	if err != nil {
		return nil, FileChangeOut{}, err
	}
	p := personalPath(spaces, strings.TrimSpace(in.Path))
	sp := spaceOf(spaces, p)
	if sp == nil {
		return nil, FileChangeOut{}, fmt.Errorf("path %q not recognised: use a path from file_search or folder_list", oneLine(p, 200))
	}
	it, err := t.Files.Item(ctx, tok, strings.TrimSuffix(p, "/"))
	if err != nil {
		return nil, FileChangeOut{}, err
	}
	by, err := t.Files.SharedByMe(ctx, tok)
	if err != nil {
		return nil, FileChangeOut{}, err
	}
	var item *opencloud.Shared
	for i := range by {
		if by[i].ItemID == it.ID {
			item = &by[i]
		}
	}
	if item == nil {
		if shared, _ := t.Files.SharedWithOthers(ctx, tok, sp.Path, p); shared {
			return nil, FileChangeOut{}, fmt.Errorf("this item isn't shared by itself: it's in a folder you share; stop sharing that folder (see file_shares)")
		}
		return nil, FileChangeOut{}, fmt.Errorf("you don't share this item (see file_shares)")
	}
	want := strings.ToLower(strings.TrimSpace(in.With))
	var grants []opencloud.Grant
	for _, g := range item.With {
		if (want == "link" && g.Link) || (!g.Link && want != "" && strings.Contains(strings.ToLower(g.Who), want)) {
			grants = append(grants, g)
		}
	}
	if len(grants) != 1 {
		var names []string
		for _, g := range item.With {
			names = append(names, g.Who)
		}
		return nil, FileChangeOut{}, fmt.Errorf("%d grants match %q; this item is shared with: %s", len(grants), oneLine(in.With, 60), strings.Join(names, ", "))
	}
	g := grants[0]
	what := "FILE"
	if it.Folder {
		what = "FOLDER"
	}
	first := fmt.Sprintf("Stop sharing the %s %q with %s (%s)? They lose access.", what, shownPath(sp, p), oneLine(g.Who, 60), g.Access)
	res, ok, err := t.confirm(ctx, req, first, it.ID+"|"+g.ID)
	if err != nil || res != nil {
		return res, FileChangeOut{}, err
	}
	if !ok {
		return nil, FileChangeOut{}, fmt.Errorf("the person declined: the sharing is unchanged")
	}
	if err := t.Files.Unshare(ctx, tok, *item, g); err != nil {
		return nil, FileChangeOut{}, err
	}
	return nil, FileChangeOut{Note: "No longer shared with " + oneLine(g.Who, 60) + "."}, nil
}
