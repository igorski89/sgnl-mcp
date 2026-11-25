package tools

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"

	"github.com/igorski89/sgnl-mcp/internal/auth"
	"github.com/igorski89/sgnl-mcp/internal/opencloud"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---- file_share: give one colleague access to a file or folder in the
// person's own space, only after the person confirms. No public links.

type FileShareIn struct {
	Path   string `json:"path" jsonschema:"the file or folder in your personal space: a path from file_search or folder_list, or a short one like /Q4/report.pdf"`
	With   string `json:"with" jsonschema:"the colleague: their email, username or name"`
	Access string `json:"access,omitempty" jsonschema:"view (default) or edit"`
}

type FileShareOut struct {
	Shared string `json:"shared"`
	With   string `json:"with"`
	Access string `json:"access"`
	Note   string `json:"note"`
}

func (t *Tools) fileShare(ctx context.Context, req *mcp.CallToolRequest, in FileShareIn) (*mcp.CallToolResult, FileShareOut, error) {
	access := strings.ToLower(strings.TrimSpace(in.Access))
	if access == "" {
		access = "view"
	}
	if access != "view" && access != "edit" {
		return nil, FileShareOut{}, fmt.Errorf("access: view or edit")
	}
	q := strings.TrimSpace(in.With)
	if len([]rune(q)) < 3 {
		return nil, FileShareOut{}, fmt.Errorf("with: give the colleague's email, username or at least 3 letters of their name")
	}
	if t.Files == nil {
		return nil, FileShareOut{}, fmt.Errorf("files aren't connected here")
	}
	tok, err := t.Auth.FilesToken(ctx)
	if err != nil {
		return nil, FileShareOut{}, err
	}
	spaces, err := t.Files.Spaces(ctx, tok)
	if err != nil {
		return nil, FileShareOut{}, err
	}
	in.Path = personalPath(spaces, strings.TrimSpace(in.Path))
	if sp := spaceOf(spaces, in.Path); sp == nil {
		return nil, FileShareOut{}, fmt.Errorf("path %q not recognised: use a path from file_search or folder_list", oneLine(in.Path, 200))
	} else if sp.Type != "personal" {
		return nil, FileShareOut{}, fmt.Errorf("only files and folders in your personal space can be shared here; for a project space, ask its manager")
	}
	it, err := t.Files.Item(ctx, tok, in.Path)
	if err != nil {
		return nil, FileShareOut{}, err
	}
	if strings.TrimSuffix(in.Path, "/") == strings.TrimSuffix(spaceOf(spaces, in.Path).Path, "/") {
		return nil, FileShareOut{}, fmt.Errorf("the whole personal space can't be shared: pick a folder or file in it")
	}

	// Exactly one colleague: an exact email/username match, else the only result.
	users, err := t.Files.FindUsers(ctx, tok, q)
	if err != nil {
		return nil, FileShareOut{}, err
	}
	me, _ := auth.Person(ctx)
	users = slices.DeleteFunc(users, func(u opencloud.User) bool {
		return strings.EqualFold(u.Username, me.Username) || (me.Email != "" && strings.EqualFold(u.Mail, me.Email))
	})
	var to *opencloud.User
	for i, u := range users {
		if strings.EqualFold(u.Mail, q) || strings.EqualFold(u.Username, q) {
			to = &users[i]
		}
	}
	if to == nil {
		switch len(users) {
		case 0:
			return nil, FileShareOut{}, fmt.Errorf("no colleague matches %q", oneLine(q, 80))
		case 1:
			to = &users[0]
		default:
			c := make([]string, 0, 5)
			for _, u := range users[:min(5, len(users))] {
				c = append(c, oneLine(u.Name, 60)+" <"+oneLine(u.Mail, 80)+">")
			}
			return nil, FileShareOut{}, fmt.Errorf("%d colleagues match %q: say which (by email): %s", len(users), oneLine(q, 80), strings.Join(c, "; "))
		}
	}

	role := opencloud.RoleViewer
	if access == "edit" {
		role = opencloud.RoleFileEditor
		if it.Folder {
			role = opencloud.RoleEditor
		}
	}
	allowed, err := t.Files.AllowedRoles(ctx, tok, it)
	if err != nil {
		return nil, FileShareOut{}, err
	}
	if !slices.Contains(allowed, role) {
		return nil, FileShareOut{}, fmt.Errorf("the file server doesn't allow %s access on this item for you", access)
	}

	kind := "FILE"
	if it.Folder {
		kind = "FOLDER (and everything in it)"
	}
	name := shownPath(spaceOf(spaces, in.Path), in.Path)
	who := oneLine(to.Name, 60)
	if to.Mail != "" {
		who += " <" + oneLine(to.Mail, 80) + ">"
	} else if to.Username != "" {
		who += " (" + oneLine(to.Username, 60) + ")"
	}
	verb := map[string]string{"view": "VIEW", "edit": "VIEW AND CHANGE"}[access]
	var b strings.Builder
	fmt.Fprintf(&b, "Share the %s %q with %s, who can %s it?\n\n", kind, name, who, verb)
	b.WriteString("They see it under \"Shared with me\" in their file app. You can stop sharing later (ask the assistant, or in your file app).\n")
	if t.outside.recent(me.Username) {
		b.WriteString("\nWARNING: your assistant read email from outside the company in the last 30 minutes; make sure this is what you asked for.\n")
	}
	res, ok, err := t.confirm(ctx, req, b.String(), it.ID+"|"+to.ID+"|"+role)
	if err != nil || res != nil {
		return res, FileShareOut{}, err
	}
	if !ok {
		return nil, FileShareOut{}, fmt.Errorf("the person declined: nothing was shared")
	}
	if err := t.Files.Share(ctx, tok, it, to.ID, role); err != nil {
		return nil, FileShareOut{}, err
	}
	slog.Info("shared", "user", me.Username, "folder", it.Folder, "access", access)
	return nil, FileShareOut{Shared: name, With: who, Access: access, Note: "Shared. They find it under \"Shared with me\"."}, nil
}

// personalPath: a short path ("/notes.md", "Q4/report.pdf") is taken as
// inside the personal space (live test: the assistant used one); a path
// that already names a space is kept.
func personalPath(spaces []opencloud.Space, p string) string {
	if p == "" || spaceOf(spaces, p) != nil || strings.HasPrefix(p, "/remote.php/") {
		return p
	}
	for _, sp := range spaces {
		if sp.Type == "personal" {
			// A short path is as the person writes it: escape each part the
			// way the file server's own paths are.
			parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
			for i, x := range parts {
				parts[i] = url.PathEscape(x)
			}
			return sp.Path + strings.Join(parts, "/")
		}
	}
	return p
}

// shownPath: a path as the person knows it (unescaped, from the space's
// top), for confirmations.
func shownPath(sp *opencloud.Space, p string) string {
	rel := strings.Trim(strings.TrimPrefix(p, sp.Path), "/")
	if u, err := url.PathUnescape(rel); err == nil {
		rel = u
	}
	return oneLine(rel, 120)
}
