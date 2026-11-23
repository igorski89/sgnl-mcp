package tools

import (
	"context"
	"time"

	"github.com/igorski89/sgnl-mcp/internal/opencloud"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---- day_overview: today across apps, in one call

type DayOverviewOut struct {
	Date        string           `json:"date"`
	Events      []Event          `json:"events"`
	UnreadCount int              `json:"unread_in_inbox"`
	Unread      []MailSummary    `json:"latest_unread" jsonschema:"newest 10 unread in the inbox"`
	Files       []opencloud.File `json:"files_changed_today,omitempty"`
	Note        string           `json:"note"`
	Errors      []string         `json:"errors,omitempty" jsonschema:"parts that couldn't be read"`
}

func (t *Tools) dayOverview(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, DayOverviewOut, error) {
	now := time.Now().In(t.loc())
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, t.loc())
	out := DayOverviewOut{Date: day.Format("2006-01-02 (Monday)"), Events: []Event{}, Unread: []MailSummary{}, Note: untrusted}
	if evs, err := t.events(ctx, day, day.AddDate(0, 0, 1)); err == nil {
		out.Events = evs
	} else {
		out.Errors = append(out.Errors, "calendar: "+err.Error())
	}
	if tok, s, acct, err := t.mailSession(ctx); err == nil {
		mbs, err := t.mailboxes(ctx, tok, s, acct)
		if inbox := byRole(mbs, "inbox"); err == nil && inbox != nil {
			out.UnreadCount = inbox.UnreadEmails
			if msgs, err := t.query(ctx, tok, s, acct, map[string]any{"inMailbox": inbox.ID, "notKeyword": "$seen"}, 10); err == nil {
				out.Unread = msgs
			} else {
				out.Errors = append(out.Errors, "mail: "+err.Error())
			}
		}
	} else {
		out.Errors = append(out.Errors, "mail: "+err.Error())
	}
	if t.Files != nil {
		if tok, err := t.Auth.SameIdentityToken(ctx); err == nil {
			if fs, err := t.Files.SearchKQL(ctx, tok, "mtime:today", 15); err == nil {
				out.Files = fs
			} else {
				out.Errors = append(out.Errors, "files: "+err.Error())
			}
		}
	}
	return nil, out, nil
}
