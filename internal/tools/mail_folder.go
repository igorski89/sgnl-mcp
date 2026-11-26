package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/igorski89/sgnl-mcp/internal/jmap"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---- mail_folder_create / mail_folder_rename: the person's own folders.
// In their own mailbox nothing is asked (private, reversible); in a shared
// mailbox everyone in it sees the change, so the person confirms. Folders
// are never deleted here, and the standard ones (Inbox, Sent, …) are never
// renamed.

type MailFolderCreateIn struct {
	InAccount
	Name   string `json:"name" jsonschema:"the new folder's name"`
	Parent string `json:"parent,omitempty" jsonschema:"put it inside this folder (a name from mailbox_list); default: top level"`
}

type MailFolderRenameIn struct {
	InAccount
	Folder string `json:"folder" jsonschema:"the folder to rename (a name from mailbox_list)"`
	Name   string `json:"name" jsonschema:"its new name"`
}

type MailFolderOut struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Note string `json:"note"`
}

// folderName: a new folder name, refused (not changed) unless plain.
func folderName(n string) (string, error) {
	n = strings.TrimSpace(n)
	switch {
	case n == "":
		return "", fmt.Errorf("name is required")
	case len([]rune(n)) > 100 || oneLine(n, 100) != n:
		return "", fmt.Errorf("the name %q isn't plain text of at most 100 characters", oneLine(n, 60))
	case strings.ContainsAny(n, "/\\"):
		return "", fmt.Errorf("a folder name can't contain / or \\: use parent for a folder inside another")
	}
	return n, nil
}

// siblingTaken: a folder of that name (any case) under the same parent.
func siblingTaken(mbs []mailbox, parent, name, except string) bool {
	for _, m := range mbs {
		if m.ParentID == parent && m.ID != except && strings.EqualFold(m.Name, name) {
			return true
		}
	}
	return false
}

// sharedName: the shared mailbox's name, or "" for the person's own.
func sharedName(s *jmap.Session, acct string) string {
	if acct == s.PrimaryAccounts[jmap.Mail] {
		return ""
	}
	return s.Accounts[acct].Name
}

func (t *Tools) mailFolderCreate(ctx context.Context, req *mcp.CallToolRequest, in MailFolderCreateIn) (*mcp.CallToolResult, MailFolderOut, error) {
	name, err := folderName(in.Name)
	if err != nil {
		return nil, MailFolderOut{}, err
	}
	tok, s, acct, err := t.mailSession(ctx)
	if err != nil {
		return nil, MailFolderOut{}, err
	}
	mbs, err := t.mailboxes(ctx, tok, s, acct)
	if err != nil {
		return nil, MailFolderOut{}, err
	}
	parent, where := "", "at the top level"
	if strings.TrimSpace(in.Parent) != "" {
		p := findMailbox(mbs, in.Parent)
		if p == nil {
			return nil, MailFolderOut{}, fmt.Errorf("no folder %q (see mailbox_list)", oneLine(in.Parent, 80))
		}
		parent, where = p.ID, fmt.Sprintf("inside %q", oneLine(p.Name, 60))
	}
	if siblingTaken(mbs, parent, name, "") {
		return nil, MailFolderOut{}, fmt.Errorf("there's already a folder %q there", name)
	}
	note := "Created in the person's mailbox (only they see it)."
	if sh := sharedName(s, acct); sh != "" {
		res, ok, err := t.confirm(ctx, req, fmt.Sprintf("Create the folder %q %s in the SHARED mailbox %s? Everyone in it sees it.", name, where, oneLine(sh, 80)), acct+"|"+parent)
		if err != nil || res != nil {
			return res, MailFolderOut{}, err
		}
		if !ok {
			return nil, MailFolderOut{}, fmt.Errorf("the person declined: no folder was created")
		}
		note = "Created in the shared mailbox, with the person's confirmation; everyone in it sees it."
	}
	f := map[string]any{"name": name}
	if parent != "" {
		f["parentId"] = parent
	}
	r, err := t.JMAP.Invoke(ctx, tok, s, mailUsing, jmap.Call{Method: "Mailbox/set", Args: map[string]any{"accountId": acct, "create": map[string]any{"f": f}}})
	if err != nil {
		return nil, MailFolderOut{}, err
	}
	if err := jmap.SetErrors(r[0]); err != nil {
		return nil, MailFolderOut{}, err
	}
	var got struct {
		Created map[string]struct{ ID string } `json:"created"`
	}
	_ = json.Unmarshal(r[0], &got)
	if got.Created["f"].ID == "" {
		return nil, MailFolderOut{}, fmt.Errorf("the mail server didn't create the folder")
	}
	return nil, MailFolderOut{ID: got.Created["f"].ID, Name: name, Note: note}, nil
}

func (t *Tools) mailFolderRename(ctx context.Context, req *mcp.CallToolRequest, in MailFolderRenameIn) (*mcp.CallToolResult, MailFolderOut, error) {
	name, err := folderName(in.Name)
	if err != nil {
		return nil, MailFolderOut{}, err
	}
	tok, s, acct, err := t.mailSession(ctx)
	if err != nil {
		return nil, MailFolderOut{}, err
	}
	mbs, err := t.mailboxes(ctx, tok, s, acct)
	if err != nil {
		return nil, MailFolderOut{}, err
	}
	mb := findMailbox(mbs, in.Folder)
	if mb == nil {
		return nil, MailFolderOut{}, fmt.Errorf("no folder %q (see mailbox_list)", oneLine(in.Folder, 80))
	}
	if mb.Role != "" {
		return nil, MailFolderOut{}, fmt.Errorf("%s is a standard folder: it keeps its name", mb.Name)
	}
	if mb.Name == name {
		return nil, MailFolderOut{}, fmt.Errorf("it's already called %q", name)
	}
	if siblingTaken(mbs, mb.ParentID, name, mb.ID) {
		return nil, MailFolderOut{}, fmt.Errorf("there's already a folder %q there", name)
	}
	note := "Renamed in the person's mailbox."
	if sh := sharedName(s, acct); sh != "" {
		res, ok, err := t.confirm(ctx, req, fmt.Sprintf("Rename the folder %q to %q in the SHARED mailbox %s? Everyone in it sees the change.",
			oneLine(mb.Name, 60), name, oneLine(sh, 80)), acct+"|"+mb.ID+"|"+mb.Name)
		if err != nil || res != nil {
			return res, MailFolderOut{}, err
		}
		if !ok {
			return nil, MailFolderOut{}, fmt.Errorf("the person declined: nothing was renamed")
		}
		note = "Renamed in the shared mailbox, with the person's confirmation."
	}
	r, err := t.JMAP.Invoke(ctx, tok, s, mailUsing, jmap.Call{Method: "Mailbox/set", Args: map[string]any{"accountId": acct,
		"update": map[string]any{mb.ID: map[string]any{"name": name}}}})
	if err != nil {
		return nil, MailFolderOut{}, err
	}
	if err := jmap.SetErrors(r[0]); err != nil {
		return nil, MailFolderOut{}, err
	}
	return nil, MailFolderOut{ID: mb.ID, Name: name, Note: note}, nil
}
