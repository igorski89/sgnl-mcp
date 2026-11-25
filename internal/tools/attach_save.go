package tools

import (
	"context"
	"fmt"
	"path"
	"strings"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---- mail_attachment_save: a mail's attachment into the drive, as a new
// file (never overwriting). The personal space needs no question; a shared
// space asks first, as file_save does.

type MailAttachmentSaveIn struct {
	ID     string `json:"id" jsonschema:"message id"`
	Index  int    `json:"index" jsonschema:"attachment index from mail_read"`
	Folder string `json:"folder,omitempty" jsonschema:"folder path from folder_list; default: the top of your personal space"`
	Name   string `json:"name,omitempty" jsonschema:"file name to save as; default: the attachment's own name"`
}

type MailAttachmentSaveOut struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Size int    `json:"size"`
	Note string `json:"note"`
}

// safeName: a file name from a mail (written by the sender) made harmless:
// no folders, not hidden, no control or invisible characters, not too long.
func safeName(n string) string {
	n = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return '_'
		}
		return r
	}, strings.TrimSpace(n))
	n = strings.TrimLeft(n, ". ")
	if r := []rune(n); len(r) > 150 {
		ext := path.Ext(n)
		if len([]rune(ext)) > 10 {
			ext = ""
		}
		n = string(r[:150-len([]rune(ext))]) + ext
	}
	if n == "" {
		n = "attachment"
	}
	return n
}

func (t *Tools) mailAttachmentSave(ctx context.Context, req *mcp.CallToolRequest, in MailAttachmentSaveIn) (*mcp.CallToolResult, MailAttachmentSaveOut, error) {
	if t.Files == nil {
		return nil, MailAttachmentSaveOut{}, fmt.Errorf("files aren't connected here")
	}
	ftok, err := t.Auth.FilesToken(ctx)
	if err != nil {
		return nil, MailAttachmentSaveOut{}, err
	}
	spaces, err := t.Files.Spaces(ctx, ftok)
	if err != nil {
		return nil, MailAttachmentSaveOut{}, err
	}
	folder := strings.TrimSpace(in.Folder)
	if folder == "" {
		folder = "/"
	}
	folder = personalPath(spaces, folder)
	sp := spaceOf(spaces, folder)
	if sp == nil {
		return nil, MailAttachmentSaveOut{}, fmt.Errorf("folder %q not recognised: use a path from folder_list", oneLine(in.Folder, 200))
	}

	tok, s, acct, err := t.mailSession(ctx)
	if err != nil {
		return nil, MailAttachmentSaveOut{}, err
	}
	es, err := t.getEmails(ctx, tok, s, acct, []string{in.ID}, 1)
	if err != nil {
		return nil, MailAttachmentSaveOut{}, err
	}
	if len(es) == 0 || in.Index < 0 || in.Index >= len(es[0].Attachments) {
		return nil, MailAttachmentSaveOut{}, fmt.Errorf("no attachment %d on message %q", in.Index, in.ID)
	}
	a := es[0].Attachments[in.Index]
	name := safeName(firstNonEmptyStr(in.Name, a.Name))

	if sp.Type != "personal" {
		res, ok, err := t.confirm(ctx, req, fmt.Sprintf("Save the attachment %q into the SHARED space %q? Everyone with access to that space will see it.", oneLine(name, 80), oneLine(sp.Name, 60)),
			folder+"|"+in.ID+"|"+fmt.Sprint(in.Index)+"|"+name)
		if err != nil || res != nil {
			return res, MailAttachmentSaveOut{}, err
		}
		if !ok {
			return nil, MailAttachmentSaveOut{}, fmt.Errorf("the person declined: nothing was saved")
		}
	}
	data, trunc, err := t.JMAP.Download(ctx, tok, s, acct, a.BlobID, a.Name, a.Type, maxAttach)
	if err != nil {
		return nil, MailAttachmentSaveOut{}, err
	}
	if trunc {
		return nil, MailAttachmentSaveOut{}, fmt.Errorf("the attachment is larger than %d MB: save it from your mail app", maxAttach>>20)
	}
	ct := a.Type
	if ct == "" {
		ct = "application/octet-stream"
	}
	p, err := t.Files.SaveNew(ctx, ftok, folder, name, ct, data)
	if err != nil {
		return nil, MailAttachmentSaveOut{}, err
	}
	note := "Saved as a new file in the personal space (private). Nothing existing was changed."
	if sp.Type != "personal" {
		note = "Saved as a new file in the shared space " + sp.Name + ", with the person's confirmation."
	}
	return nil, MailAttachmentSaveOut{Path: p, Name: name, Size: len(data), Note: note}, nil
}
