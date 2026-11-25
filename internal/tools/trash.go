package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/igorski89/sgnl-mcp/internal/jmap"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---- mail_trash: move mail to Trash, after the person confirms. Nothing
// is destroyed: the mail can be restored from Trash in the mail app.

const maxTrash = 25

type MailTrashIn struct {
	IDs []string `json:"ids" jsonschema:"message ids (at most 25)"`
}

func (t *Tools) mailTrash(ctx context.Context, req *mcp.CallToolRequest, in MailTrashIn) (*mcp.CallToolResult, MailChangeOut, error) {
	ids := slices.Compact(slices.Sorted(slices.Values(in.IDs)))
	if len(ids) == 0 || len(ids) > maxTrash {
		return nil, MailChangeOut{}, fmt.Errorf("give 1-%d message ids", maxTrash)
	}
	tok, s, acct, err := t.mailSession(ctx)
	if err != nil {
		return nil, MailChangeOut{}, err
	}
	mbs, err := t.mailboxes(ctx, tok, s, acct)
	if err != nil {
		return nil, MailChangeOut{}, err
	}
	trash := byRole(mbs, "trash")
	if trash == nil {
		return nil, MailChangeOut{}, fmt.Errorf("this mailbox has no Trash folder")
	}
	r, err := t.JMAP.Invoke(ctx, tok, s, mailUsing, jmap.Call{Method: "Email/get",
		Args: map[string]any{"accountId": acct, "ids": ids, "properties": []string{"id", "from", "subject", "receivedAt", "mailboxIds"}}})
	if err != nil {
		return nil, MailChangeOut{}, err
	}
	var got struct{ List []emailFull }
	if err := json.Unmarshal(r[0], &got); err != nil {
		return nil, MailChangeOut{}, err
	}
	if len(got.List) != len(ids) {
		return nil, MailChangeOut{}, fmt.Errorf("%d of the %d messages weren't found (ids come from mail_search)", len(ids)-len(got.List), len(ids))
	}
	var move []emailFull
	for _, e := range got.List {
		if !(len(e.MailboxIDs) == 1 && e.MailboxIDs[trash.ID]) {
			move = append(move, e)
		}
	}
	if len(move) == 0 {
		return nil, MailChangeOut{Changed: 0}, nil
	}

	// The question: count and the first ones on the first line.
	line := func(e emailFull, n int) string {
		from := "(no sender)"
		if len(e.From) > 0 {
			from = e.From[0].Email
		}
		return fmt.Sprintf("%q from %s", oneLine(e.Subject, n), oneLine(from, 60))
	}
	var first []string
	for _, e := range move[:min(2, len(move))] {
		first = append(first, line(e, 50))
	}
	q := fmt.Sprintf("Move %d mail(s) to Trash: %s", len(move), strings.Join(first, "; "))
	if len(move) > 2 {
		q += fmt.Sprintf("; and %d more", len(move)-2)
	}
	var b strings.Builder
	b.WriteString(q + "?\n\n")
	for _, e := range move {
		b.WriteString("- " + oneLine(e.ReceivedAt, 20) + "  " + line(e, 120) + "\n")
	}
	if t.TrashDays > 0 {
		fmt.Fprintf(&b, "\nYou can restore them from Trash in your mail app for %d days; then the mail server deletes them.\n", t.TrashDays)
	} else {
		b.WriteString("\nYou can restore them from Trash in your mail app.\n")
	}
	bind := make([]string, len(move))
	for i, e := range move {
		bind[i] = e.ID
	}
	res, ok, err := t.confirm(ctx, req, b.String(), strings.Join(bind, ","))
	if err != nil || res != nil {
		return res, MailChangeOut{}, err
	}
	if !ok {
		return nil, MailChangeOut{}, fmt.Errorf("the person declined: nothing was moved")
	}
	return t.updateEmails(ctx, bind, func(string) map[string]any { return map[string]any{"mailboxIds": map[string]bool{trash.ID: true}} })
}
