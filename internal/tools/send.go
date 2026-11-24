package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/igorski89/sgnl-mcp/internal/auth"
	"github.com/igorski89/sgnl-mcp/internal/jmap"
	"github.com/igorski89/sgnl-mcp/internal/textx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---- mail_send: sends an existing draft, only after the person says yes
// in their app. The model can call the tool; it can't answer the question.

const maxRecipients = 50

// outsideWindow: how long after reading mail from outside a send gets the
// extra warning (that is when injected instructions would act).
const outsideWindow = 30 * time.Minute

// seen: when each person last had mail from outside the company read
// through the gateway (any session).
type seen struct {
	mu sync.Mutex
	m  map[string]time.Time
}

func (s *seen) mark(who string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]time.Time{}
	}
	s.m[who] = time.Now()
}

func (s *seen) recent(who string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Since(s.m[who]) < outsideWindow
}

func domainOf(email string) string {
	_, d, _ := strings.Cut(strings.ToLower(strings.TrimSpace(email)), "@")
	return d
}

// internal: the address is in one of the company's domains (configured,
// or by default the person's own).
func (t *Tools) internal(ctx context.Context, email string) bool {
	d := domainOf(email)
	if d == "" {
		return false
	}
	if len(t.InternalDomains) > 0 {
		return slices.ContainsFunc(t.InternalDomains, func(x string) bool { return strings.EqualFold(x, d) })
	}
	me, _ := auth.Person(ctx)
	return d == domainOf(me.Email)
}

// sawMail: note mail from outside entering the conversation.
func (t *Tools) sawMail(ctx context.Context, from []address) {
	for _, a := range from {
		if !t.internal(ctx, a.Email) {
			me, _ := auth.Person(ctx)
			t.outside.mark(me.Username)
			return
		}
	}
}

// oneLine: text from a mail made safe for the confirmation dialog: visible
// characters only, no line breaks (so it can't fake dialog lines), short.
func oneLine(s string, max int) string {
	s = textx.Visible(s)
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + "…"
	}
	return s
}

type MailSendIn struct {
	ID string `json:"id" jsonschema:"id of a draft in Drafts (from mail_draft_create or mail_draft_update)"`
}

type MailSendOut struct {
	Sent       bool   `json:"sent"`
	Recipients int    `json:"recipients"`
	Outside    int    `json:"outside_company"`
	Note       string `json:"note"`
}

type identity struct {
	ID      string    `json:"id"`
	Email   string    `json:"email"`
	ReplyTo []address `json:"replyTo"`
}

func (t *Tools) mailSend(ctx context.Context, req *mcp.CallToolRequest, in MailSendIn) (*mcp.CallToolResult, MailSendOut, error) {
	tok, s, acct, err := t.mailSession(ctx)
	if err != nil {
		return nil, MailSendOut{}, err
	}
	mbs, err := t.mailboxes(ctx, tok, s, acct)
	if err != nil {
		return nil, MailSendOut{}, err
	}
	drafts, sent := byRole(mbs, "drafts"), byRole(mbs, "sent")
	es, err := t.getEmails(ctx, tok, s, acct, []string{in.ID}, 2000)
	if err != nil {
		return nil, MailSendOut{}, err
	}
	if len(es) == 0 || drafts == nil || !es[0].Keywords["$draft"] || !es[0].MailboxIDs[drafts.ID] {
		return nil, MailSendOut{}, fmt.Errorf("%q is not a draft in Drafts: only drafts can be sent", in.ID)
	}
	e := es[0]

	// The sender must be one of the person's own identities, and replies
	// may only go back to them.
	r, err := t.JMAP.Invoke(ctx, tok, s, mailUsing, jmap.Call{Method: "Identity/get", Args: map[string]any{"accountId": acct}})
	if err != nil {
		return nil, MailSendOut{}, err
	}
	var ids struct{ List []identity }
	_ = json.Unmarshal(r[0], &ids)
	if len(e.From) != 1 {
		return nil, MailSendOut{}, fmt.Errorf("the draft must have exactly one sender")
	}
	var idn *identity
	for i, x := range ids.List {
		if strings.EqualFold(x.Email, e.From[0].Email) {
			idn = &ids.List[i]
		}
	}
	if idn == nil {
		return nil, MailSendOut{}, fmt.Errorf("the draft's sender %s is not one of your sending addresses", e.From[0].Email)
	}
	own := func(a address) bool {
		return slices.ContainsFunc(ids.List, func(x identity) bool { return strings.EqualFold(x.Email, a.Email) })
	}
	for _, a := range e.ReplyTo {
		if !own(a) {
			return nil, MailSendOut{}, fmt.Errorf("the draft's Reply-To (%s) is not one of your addresses: replies would go elsewhere", a.Email)
		}
	}

	// Recipients, as the mail server will send them (from the headers).
	type rcpt struct {
		field, email  string
		outside, new_ bool
	}
	var rs []rcpt
	seenAddr := map[string]bool{}
	for _, f := range []struct {
		name string
		list []address
	}{{"To", e.To}, {"Cc", e.Cc}, {"Bcc", e.Bcc}} {
		for _, a := range f.list {
			k := strings.ToLower(strings.TrimSpace(a.Email))
			if k == "" || seenAddr[k] {
				continue
			}
			seenAddr[k] = true
			rs = append(rs, rcpt{field: f.name, email: k, outside: !t.internal(ctx, k)})
		}
	}
	if len(rs) == 0 {
		return nil, MailSendOut{}, fmt.Errorf("the draft has no recipients")
	}
	if len(rs) > maxRecipients {
		return nil, MailSendOut{}, fmt.Errorf("at most %d recipients per mail (this draft has %d)", maxRecipients, len(rs))
	}
	// New: never written to before (no mail to them in Sent).
	if sent != nil {
		calls := make([]jmap.Call, len(rs))
		for i, x := range rs {
			calls[i] = jmap.Call{Method: "Email/query", Args: map[string]any{"accountId": acct, "limit": 1,
				"filter": map[string]any{"operator": "AND", "conditions": []any{
					map[string]any{"inMailbox": sent.ID},
					map[string]any{"operator": "OR", "conditions": []any{
						map[string]any{"to": x.email}, map[string]any{"cc": x.email}, map[string]any{"bcc": x.email}}},
				}}}}
		}
		if rr, err := t.JMAP.Invoke(ctx, tok, s, mailUsing, calls...); err == nil {
			for i := range rs {
				var q struct{ IDs []string }
				_ = json.Unmarshal(rr[i], &q)
				rs[i].new_ = len(q.IDs) == 0
			}
		}
	}

	// What the person is asked, built only from the draft (made one-line,
	// so nothing in it can pose as part of the question).
	// First line: the essentials (clients may fold the rest away).
	var b strings.Builder
	outside, fresh := 0, 0
	for _, x := range rs {
		if x.outside {
			outside++
		}
		if x.new_ {
			fresh++
		}
	}
	shown := make([]string, 0, 3)
	for _, x := range rs[:min(3, len(rs))] {
		shown = append(shown, oneLine(x.email, 80))
	}
	first := fmt.Sprintf("Send %q to %s", oneLine(e.Subject, 60), strings.Join(shown, ", "))
	if len(rs) > 3 {
		first += fmt.Sprintf(" and %d more", len(rs)-3)
	}
	if outside > 0 {
		first += fmt.Sprintf(" (%d OUTSIDE the company)", outside)
	}
	b.WriteString(first + "?\n\nFrom: " + oneLine(e.From[0].Email, 200) + "\n")
	for _, x := range rs {
		var tags []string
		if x.outside {
			tags = append(tags, "OUTSIDE the company")
		}
		if x.new_ {
			tags = append(tags, "never written to before")
		}
		line := x.field + ": " + oneLine(x.email, 200)
		if len(tags) > 0 {
			line += "  (" + strings.Join(tags, ", ") + ")"
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("Subject: " + oneLine(e.Subject, 200) + "\n")
	if len(e.Attachments) > 0 {
		names := make([]string, len(e.Attachments))
		for i, a := range e.Attachments {
			names[i] = oneLine(a.Name, 80)
		}
		b.WriteString(fmt.Sprintf("Attachments (%d): %s\n", len(names), strings.Join(names, ", ")))
	}
	b.WriteString("Text begins: " + oneLine(toMessage(e, 2000).Text, 300) + "\n")
	var warn []string
	if outside > 0 {
		warn = append(warn, fmt.Sprintf("%d recipient(s) outside the company", outside))
	}
	if len(rs) > 10 {
		warn = append(warn, fmt.Sprintf("%d recipients", len(rs)))
	}
	me, _ := auth.Person(ctx)
	if t.outside.recent(me.Username) {
		warn = append(warn, "your assistant read email from outside the company in the last 30 minutes; make sure this is what you asked for")
	}
	if len(warn) > 0 {
		b.WriteString("\nWARNING: " + strings.Join(warn, "; ") + ".\n")
	}
	bind := e.BlobID + "|" + idn.ID
	for _, x := range rs {
		bind += "|" + x.field + ":" + x.email
	}
	res, ok, err := confirm(ctx, req, b.String(), bind, fmt.Sprintf("Yes, send this email to %d recipient(s)", len(rs)))
	if err != nil || res != nil {
		return res, MailSendOut{}, err
	}
	if !ok {
		return nil, MailSendOut{}, fmt.Errorf("the person declined: nothing was sent, the draft is still in Drafts")
	}

	patch := map[string]any{"keywords/$draft": nil}
	if sent != nil {
		patch["mailboxIds/"+drafts.ID] = nil
		patch["mailboxIds/"+sent.ID] = true
	}
	r, err = t.JMAP.Invoke(ctx, tok, s, mailUsing, jmap.Call{Method: "EmailSubmission/set", Send: true, Args: map[string]any{
		"accountId":            acct,
		"create":               map[string]any{"s": map[string]any{"emailId": e.ID, "identityId": idn.ID}},
		"onSuccessUpdateEmail": map[string]any{"#s": patch},
	}})
	if err == nil {
		err = jmap.SetErrors(r[0])
	}
	if err != nil {
		slog.Warn("mail not sent", "user", me.Username, "error", err.Error())
		return nil, MailSendOut{}, fmt.Errorf("the mail server didn't send it: %v (the draft is still in Drafts)", err)
	}
	slog.Info("mail sent", "user", me.Username, "recipients", len(rs), "outside", outside, "new", fresh)
	return nil, MailSendOut{true, len(rs), outside, "Sent with the person's confirmation; it is now in Sent."}, nil
}
