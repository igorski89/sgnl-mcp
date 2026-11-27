package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/igorski89/sgnl-mcp/internal/listmonk"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// askNews: the person's confirmation of a newsletter change; bind is what
// the question showed (the answer counts only while it is unchanged).
func (t *Tools) askNews(ctx context.Context, req *mcp.CallToolRequest, q string, bind any) (*mcp.CallToolResult, bool, error) {
	b, _ := json.Marshal(bind)
	return t.confirm(ctx, req, q, string(b))
}

// campaignBind, listsBind: what the person approves of a campaign and
// its lists. Live counters (sent, views, subscribers) are shown but left
// out, or a campaign that is sending (or a list someone joins) could
// never be confirmed: they change while the question is open.
func campaignBind(c listmonk.Campaign) any {
	return []any{c.ID, c.Status, c.Type, c.Name, c.Subject, c.FromEmail, c.ContentType, c.Body, c.AltBody, c.TemplateID, c.SendAt, c.Lists}
}

// subBind: who a subscriber is and their subscriptions (not the times).
func subBind(subs []listmonk.Subscriber) any {
	var out []any
	for _, s := range subs {
		out = append(out, []any{s.ID, s.Email, s.Name, s.Status, s.Attribs, s.Lists})
	}
	return out
}

func listsBind(ls []listmonk.List) any {
	var out []any
	for _, l := range ls {
		out = append(out, []any{l.ID, l.Name, l.Optin, l.Type, l.Status})
	}
	return out
}

func declined(what string) error {
	return fmt.Errorf("the person declined: %s", what)
}

// listNames: "A (double opt-in, 12 recipients), B (…)".
func listNames(ls []listmonk.List) string {
	var parts []string
	for _, l := range ls {
		parts = append(parts, fmt.Sprintf("%s (%s opt-in, %d recipient(s))", oneLine(l.Name, 80), l.Optin, l.Recipients()))
	}
	return strings.Join(parts, ", ")
}

func (t *Tools) lists(ctx context.Context, ids []int) ([]listmonk.List, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("lists: give at least one list id (newsletter_lists)")
	}
	if len(ids) > 20 {
		return nil, fmt.Errorf("lists: at most 20")
	}
	var out []listmonk.List
	for _, id := range ids {
		var l listmonk.List
		if err := t.news(ctx, "GET", listmonk.Path("/api/lists/:id", id), nil, nil, &l); err != nil {
			return nil, fmt.Errorf("list %d: %w", id, err)
		}
		out = append(out, l)
	}
	return out, nil
}

// ---- drafts (no question: nothing goes out until a send is confirmed)

var contentTypes = []string{"richtext", "html", "markdown", "plain"}

type NewsDraftIn struct {
	Name        string   `json:"name" jsonschema:"internal name of the campaign"`
	Subject     string   `json:"subject"`
	Lists       []int    `json:"lists" jsonschema:"ids of the lists it goes to (newsletter_lists)"`
	FromEmail   string   `json:"from_email,omitempty" jsonschema:"Name <address>; default: listmonk's configured sender"`
	ContentType string   `json:"content_type,omitempty" jsonschema:"richtext (default), html, markdown or plain"`
	Body        string   `json:"body"`
	AltBody     string   `json:"altbody,omitempty" jsonschema:"plain-text alternative"`
	TemplateID  int      `json:"template_id,omitempty" jsonschema:"a campaign template (newsletter_templates); default: listmonk's default"`
	Tags        []string `json:"tags,omitempty"`
}

type NewsChangeOut struct {
	ID     int    `json:"id,omitempty"`
	Status string `json:"status,omitempty"`
	Note   string `json:"note"`
}

func (t *Tools) newsDraftCreate(ctx context.Context, _ *mcp.CallToolRequest, in NewsDraftIn) (*mcp.CallToolResult, NewsChangeOut, error) {
	if err := oneOf("content_type", in.ContentType, contentTypes...); err != nil {
		return nil, NewsChangeOut{}, err
	}
	if len(in.Lists) == 0 {
		return nil, NewsChangeOut{}, fmt.Errorf("lists: give at least one list id (newsletter_lists)")
	}
	body := map[string]any{"name": in.Name, "subject": in.Subject, "lists": in.Lists, "from_email": in.FromEmail,
		"content_type": in.ContentType, "body": in.Body, "type": "regular", "tags": nonNilS(in.Tags), "messenger": "email"}
	if in.ContentType == "" {
		body["content_type"] = "richtext"
	}
	if in.AltBody != "" {
		body["altbody"] = in.AltBody
	}
	if in.TemplateID > 0 {
		body["template_id"] = in.TemplateID
	}
	var c listmonk.Campaign
	if err := t.news(ctx, "POST", "/api/campaigns", nil, body, &c); err != nil {
		return nil, NewsChangeOut{}, err
	}
	return nil, NewsChangeOut{c.ID, c.Status, "Saved as a draft; nothing was sent. Send it with newsletter_campaign_send (the person confirms) or test it with newsletter_test_send."}, nil
}

type NewsDraftUpdateIn struct {
	ID          int       `json:"id"`
	Name        *string   `json:"name,omitempty"`
	Subject     *string   `json:"subject,omitempty"`
	Lists       []int     `json:"lists,omitempty" jsonschema:"replaces the lists"`
	FromEmail   *string   `json:"from_email,omitempty"`
	ContentType *string   `json:"content_type,omitempty" jsonschema:"richtext, html, markdown or plain"`
	Body        *string   `json:"body,omitempty"`
	AltBody     *string   `json:"altbody,omitempty"`
	TemplateID  *int      `json:"template_id,omitempty"`
	Tags        *[]string `json:"tags,omitempty"`
}

func (t *Tools) newsDraftUpdate(ctx context.Context, _ *mcp.CallToolRequest, in NewsDraftUpdateIn) (*mcp.CallToolResult, NewsChangeOut, error) {
	c, err := t.campaign(ctx, in.ID)
	if err != nil {
		return nil, NewsChangeOut{}, err
	}
	// A scheduled or paused campaign was already approved to go out as it
	// is: changing it here would change mail the person said yes to.
	if c.Status != "draft" {
		return nil, NewsChangeOut{}, fmt.Errorf("only drafts can be changed here; this campaign is %s (unschedule it first with newsletter_campaign_stop, or change it in listmonk)", c.Status)
	}
	if in.ContentType != nil {
		if err := oneOf("content_type", *in.ContentType, contentTypes...); err != nil {
			return nil, NewsChangeOut{}, err
		}
	}
	w := c.Write()
	setIf := func(k string, v any, ok bool) {
		if ok {
			w[k] = v
		}
	}
	setIf("name", deref(in.Name), in.Name != nil)
	setIf("subject", deref(in.Subject), in.Subject != nil)
	setIf("from_email", deref(in.FromEmail), in.FromEmail != nil)
	setIf("content_type", deref(in.ContentType), in.ContentType != nil)
	setIf("body", deref(in.Body), in.Body != nil)
	setIf("altbody", deref(in.AltBody), in.AltBody != nil)
	setIf("lists", in.Lists, len(in.Lists) > 0)
	if in.TemplateID != nil {
		w["template_id"] = *in.TemplateID
	}
	if in.Tags != nil {
		w["tags"] = nonNilS(*in.Tags)
	}
	var out listmonk.Campaign
	if err := t.news(ctx, "PUT", listmonk.Path("/api/campaigns/:id", in.ID), nil, w, &out); err != nil {
		return nil, NewsChangeOut{}, err
	}
	return nil, NewsChangeOut{out.ID, out.Status, "Draft changed; nothing was sent."}, nil
}

func deref[T any](p *T) T {
	var z T
	if p == nil {
		return z
	}
	return *p
}

func nonNilS(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ---- newsletter_campaign_send

type NewsSendIn struct {
	ID     int    `json:"id"`
	SendAt string `json:"send_at,omitempty" jsonschema:"schedule it instead: a time in the future (RFC 3339, e.g. 2026-12-01T09:00:00+01:00); default: now"`
}

func (t *Tools) newsCampaignSend(ctx context.Context, req *mcp.CallToolRequest, in NewsSendIn) (*mcp.CallToolResult, NewsChangeOut, error) {
	c, err := t.campaign(ctx, in.ID)
	if err != nil {
		return nil, NewsChangeOut{}, err
	}
	switch c.Status {
	case "draft", "paused":
	case "scheduled":
		return nil, NewsChangeOut{}, fmt.Errorf("this campaign is already scheduled (for %s); to change that, unschedule it with newsletter_campaign_stop first", deref(c.SendAt))
	default:
		return nil, NewsChangeOut{}, fmt.Errorf("a %s campaign can't be sent", c.Status)
	}
	if in.SendAt != "" && c.Status != "draft" {
		return nil, NewsChangeOut{}, fmt.Errorf("only a draft can be scheduled; a paused campaign is resumed now")
	}
	when := "NOW"
	sendAt := in.SendAt
	if sendAt == "" && c.Status == "draft" && c.SendAt != nil && *c.SendAt != "" {
		sendAt = *c.SendAt // listmonk schedules a draft that has a date
	}
	if sendAt != "" {
		at, err := time.Parse(time.RFC3339, sendAt)
		if err != nil {
			return nil, NewsChangeOut{}, fmt.Errorf("send_at: RFC 3339, e.g. 2026-12-01T09:00:00+01:00")
		}
		if !at.After(time.Now()) {
			return nil, NewsChangeOut{}, fmt.Errorf("send_at must be in the future")
		}
		when = "at " + at.Format("Mon 2 Jan 2006 15:04 MST")
	}
	ls, err := t.campaignLists(ctx, c)
	if err != nil {
		return nil, NewsChangeOut{}, err
	}
	n := recipientsFor(c, ls)
	verb := "SEND"
	if c.Status == "paused" {
		verb = fmt.Sprintf("RESUME sending (%d of %d sent so far)", c.Sent, c.ToSend)
	}
	from := c.FromEmail
	if from == "" {
		from = "listmonk's default sender"
	}
	q := fmt.Sprintf("%s the newsletter campaign %q %s to up to %d people?\n\nSubject: %s\nFrom: %s\nLists: %s\n\nThis mails everyone on these lists, outside the company too. It can be paused or cancelled while it runs, but what is sent can't be taken back.",
		verb, oneLine(c.Name, 100), when, n, oneLine(c.Subject, 200), oneLine(from, 120), listNames(ls))
	res, ok, err := t.askNews(ctx, req, q, []any{campaignBind(c), listsBind(ls), sendAt})
	if err != nil || res != nil {
		return res, NewsChangeOut{}, err
	}
	if !ok {
		return nil, NewsChangeOut{}, declined("nothing was sent")
	}
	target := "running"
	if in.SendAt != "" {
		w := c.Write()
		w["send_at"] = in.SendAt
		if err := t.news(ctx, "PUT", listmonk.Path("/api/campaigns/:id", c.ID), nil, w, nil); err != nil {
			return nil, NewsChangeOut{}, err
		}
	}
	if sendAt != "" {
		target = "scheduled"
	}
	var out listmonk.Campaign
	if err := t.news(ctx, "PUT", listmonk.Path("/api/campaigns/:id/status", c.ID), nil, map[string]string{"status": target}, &out); err != nil {
		return nil, NewsChangeOut{}, err
	}
	note := "Sending started, with the person's confirmation; follow it with newsletter_stats."
	if out.Status == "scheduled" {
		note = "Scheduled, with the person's confirmation: it goes out at its send time (" + deref(out.SendAt) + ")."
	}
	return nil, NewsChangeOut{out.ID, out.Status, note}, nil
}

// ---- newsletter_campaign_stop

type NewsStopIn struct {
	ID     int    `json:"id"`
	Action string `json:"action" jsonschema:"pause (a running campaign; resume with newsletter_campaign_send), cancel (running or paused, for good) or unschedule (a scheduled one back to draft)"`
}

func (t *Tools) newsCampaignStop(ctx context.Context, req *mcp.CallToolRequest, in NewsStopIn) (*mcp.CallToolResult, NewsChangeOut, error) {
	if err := oneOf("action", in.Action, "pause", "cancel", "unschedule"); err != nil || in.Action == "" {
		return nil, NewsChangeOut{}, fmt.Errorf("action: pause, cancel or unschedule")
	}
	c, err := t.campaign(ctx, in.ID)
	if err != nil {
		return nil, NewsChangeOut{}, err
	}
	need, target, q := map[string][]string{"pause": {"running"}, "cancel": {"running", "paused"}, "unschedule": {"scheduled"}}[in.Action],
		map[string]string{"pause": "paused", "cancel": "cancelled", "unschedule": "draft"}[in.Action], ""
	if !slices.Contains(need, c.Status) {
		return nil, NewsChangeOut{}, fmt.Errorf("can't %s a %s campaign", in.Action, c.Status)
	}
	switch in.Action {
	case "pause":
		q = fmt.Sprintf("Pause the newsletter campaign %q? %d of %d sent so far; the rest waits until it is resumed.", oneLine(c.Name, 100), c.Sent, c.ToSend)
	case "cancel":
		q = fmt.Sprintf("CANCEL the newsletter campaign %q for good? %d of %d sent so far; the rest is never sent.", oneLine(c.Name, 100), c.Sent, c.ToSend)
	default:
		q = fmt.Sprintf("Unschedule the newsletter campaign %q (it was to go out %s)? It goes back to being a draft.", oneLine(c.Name, 100), deref(c.SendAt))
	}
	res, ok, err := t.askNews(ctx, req, q, campaignBind(c))
	if err != nil || res != nil {
		return res, NewsChangeOut{}, err
	}
	if !ok {
		return nil, NewsChangeOut{}, declined("nothing changed")
	}
	var out listmonk.Campaign
	if err := t.news(ctx, "PUT", listmonk.Path("/api/campaigns/:id/status", c.ID), nil, map[string]string{"status": target}, &out); err != nil {
		return nil, NewsChangeOut{}, err
	}
	return nil, NewsChangeOut{out.ID, out.Status, "Done, with the person's confirmation."}, nil
}

// ---- newsletter_campaign_delete

func (t *Tools) newsCampaignDelete(ctx context.Context, req *mcp.CallToolRequest, in NewsIDIn) (*mcp.CallToolResult, NewsChangeOut, error) {
	c, err := t.campaign(ctx, in.ID)
	if err != nil {
		return nil, NewsChangeOut{}, err
	}
	if c.Status == "running" {
		return nil, NewsChangeOut{}, fmt.Errorf("this campaign is sending now: pause or cancel it first (newsletter_campaign_stop)")
	}
	q := fmt.Sprintf("Delete the newsletter campaign %q (%s, %d sent) with its statistics? It can't be undone.", oneLine(c.Name, 100), c.Status, c.Sent)
	if c.Status == "scheduled" {
		q += " It was to go out " + deref(c.SendAt) + "; it won't."
	}
	res, ok, err := t.askNews(ctx, req, q, campaignBind(c))
	if err != nil || res != nil {
		return res, NewsChangeOut{}, err
	}
	if !ok {
		return nil, NewsChangeOut{}, declined("nothing was deleted")
	}
	if err := t.news(ctx, "DELETE", listmonk.Path("/api/campaigns/:id", c.ID), nil, nil, nil); err != nil {
		return nil, NewsChangeOut{}, err
	}
	return nil, NewsChangeOut{c.ID, "deleted", "Deleted, with the person's confirmation."}, nil
}

// ---- recipients by address (test and transactional mail)

// subscribersByEmail: each address as an existing subscriber (exact
// match); listmonk mails only those and silently drops the rest, so an
// unknown or blocklisted address is refused before anything is sent.
func (t *Tools) subscribersByEmail(ctx context.Context, emails []string) ([]listmonk.Subscriber, error) {
	var out []listmonk.Subscriber
	var unknown, blocked []string
	for _, e := range emails {
		e = strings.ToLower(strings.TrimSpace(e))
		q := url.Values{"search": {e}, "per_page": {"50"}}
		var p listmonk.Page[listmonk.Subscriber]
		if err := t.news(ctx, "GET", "/api/subscribers", q, nil, &p); err != nil {
			return nil, err
		}
		i := slices.IndexFunc(p.Results, func(s listmonk.Subscriber) bool { return strings.EqualFold(s.Email, e) })
		switch {
		case i < 0:
			unknown = append(unknown, e)
		case p.Results[i].Status == "blocklisted":
			blocked = append(blocked, e)
		default:
			out = append(out, p.Results[i])
		}
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("not subscribers in listmonk: %s. listmonk sends these only to existing subscribers: add them first (newsletter_subscriber_add)", strings.Join(unknown, ", "))
	}
	if len(blocked) > 0 {
		return nil, fmt.Errorf("blocklisted (they asked for no mail): %s", strings.Join(blocked, ", "))
	}
	return out, nil
}

// who: the recipients for a question, outside ones marked.
func (t *Tools) who(ctx context.Context, subs []listmonk.Subscriber) (string, int) {
	var parts []string
	outside := 0
	for _, s := range subs {
		p := oneLine(s.Email, 120)
		if !t.internal(ctx, s.Email) {
			p += " (OUTSIDE the company)"
			outside++
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, ", "), outside
}

// ---- newsletter_test_send

type NewsTestIn struct {
	ID int      `json:"id"`
	To []string `json:"to,omitempty" jsonschema:"at most 5 addresses, each an existing subscriber; default: your own address"`
}

func (t *Tools) newsTestSend(ctx context.Context, req *mcp.CallToolRequest, in NewsTestIn) (*mcp.CallToolResult, NewsChangeOut, error) {
	to := in.To
	if len(to) == 0 {
		me, err := t.me(ctx)
		if err != nil {
			return nil, NewsChangeOut{}, err
		}
		to = []string{me}
	}
	if len(to) > 5 {
		return nil, NewsChangeOut{}, fmt.Errorf("to: at most 5 addresses")
	}
	c, err := t.campaign(ctx, in.ID)
	if err != nil {
		return nil, NewsChangeOut{}, err
	}
	subs, err := t.subscribersByEmail(ctx, to)
	if err != nil {
		return nil, NewsChangeOut{}, err
	}
	who, _ := t.who(ctx, subs)
	q := fmt.Sprintf("Send a TEST of the newsletter campaign %q to %s?\n\nSubject: %s\nOnly these addresses get it; the campaign itself isn't sent.", oneLine(c.Name, 100), who, oneLine(c.Subject, 200))
	res, ok, err := t.askNews(ctx, req, q, []any{campaignBind(c), subBind(subs)})
	if err != nil || res != nil {
		return res, NewsChangeOut{}, err
	}
	if !ok {
		return nil, NewsChangeOut{}, declined("no test was sent")
	}
	// The campaign as stored is what is tested (listmonk mails the body
	// it is given).
	w := c.Write()
	emails := []string{}
	for _, s := range subs {
		emails = append(emails, s.Email)
	}
	w["subscribers"] = emails
	if err := t.news(ctx, "POST", listmonk.Path("/api/campaigns/:id/test", c.ID), nil, w, nil); err != nil {
		return nil, NewsChangeOut{}, err
	}
	return nil, NewsChangeOut{c.ID, c.Status, fmt.Sprintf("Test sent to %d address(es), with the person's confirmation.", len(subs))}, nil
}

// me: the person's own email address.
func (t *Tools) me(ctx context.Context) (string, error) {
	var p listmonk.Profile
	if err := t.news(ctx, "GET", "/api/profile", nil, nil, &p); err != nil {
		return "", err
	}
	if p.Email == "" {
		return "", fmt.Errorf("your listmonk account has no email address")
	}
	return p.Email, nil
}

// ---- newsletter_tx_send

type NewsTxIn struct {
	TemplateID int            `json:"template_id" jsonschema:"a transactional (tx) template (newsletter_templates)"`
	To         []string       `json:"to" jsonschema:"at most 10 addresses, each an existing subscriber"`
	Data       map[string]any `json:"data,omitempty" jsonschema:"values for the template (.Tx.Data)"`
	Subject    string         `json:"subject,omitempty" jsonschema:"instead of the template's subject"`
	FromEmail  string         `json:"from_email,omitempty"`
}

func (t *Tools) newsTxSend(ctx context.Context, req *mcp.CallToolRequest, in NewsTxIn) (*mcp.CallToolResult, NewsChangeOut, error) {
	if len(in.To) == 0 || len(in.To) > 10 {
		return nil, NewsChangeOut{}, fmt.Errorf("to: 1 to 10 addresses")
	}
	tp, err := t.template(ctx, in.TemplateID)
	if err != nil {
		return nil, NewsChangeOut{}, err
	}
	if tp.Type != "tx" {
		return nil, NewsChangeOut{}, fmt.Errorf("template %d is a %s template, not a transactional (tx) one", tp.ID, tp.Type)
	}
	subs, err := t.subscribersByEmail(ctx, in.To)
	if err != nil {
		return nil, NewsChangeOut{}, err
	}
	who, _ := t.who(ctx, subs)
	subject := in.Subject
	if subject == "" {
		subject = tp.Subject
	}
	q := fmt.Sprintf("Send the transactional mail %q to %s?\n\nSubject: %s", oneLine(tp.Name, 100), who, oneLine(subject, 200))
	if in.FromEmail != "" {
		q += "\nFrom: " + oneLine(in.FromEmail, 120)
	}
	if len(in.Data) > 0 {
		b, _ := json.Marshal(in.Data)
		q += "\nData: " + oneLine(string(b), 400)
	}
	res, ok, err := t.askNews(ctx, req, q, []any{tp, subBind(subs), in})
	if err != nil || res != nil {
		return res, NewsChangeOut{}, err
	}
	if !ok {
		return nil, NewsChangeOut{}, declined("nothing was sent")
	}
	emails := []string{}
	for _, s := range subs {
		emails = append(emails, s.Email)
	}
	body := map[string]any{"subscriber_mode": "default", "subscriber_emails": emails, "template_id": tp.ID, "messenger": "email"}
	if in.Data != nil {
		body["data"] = in.Data
	}
	if in.Subject != "" {
		body["subject"] = in.Subject
	}
	if in.FromEmail != "" {
		body["from_email"] = in.FromEmail
	}
	if err := t.news(ctx, "POST", "/api/tx", nil, body, nil); err != nil {
		return nil, NewsChangeOut{}, err
	}
	return nil, NewsChangeOut{Note: fmt.Sprintf("Sent to %d address(es), with the person's confirmation.", len(subs))}, nil
}

// ---- lists

type NewsListCreateIn struct {
	Name        string   `json:"name"`
	Type        string   `json:"type,omitempty" jsonschema:"private (default) or public (offered on listmonk's public sign-up page)"`
	Optin       string   `json:"optin,omitempty" jsonschema:"single (default) or double (subscribers confirm by mail)"`
	Tags        []string `json:"tags,omitempty"`
	Description string   `json:"description,omitempty"`
}

func (t *Tools) newsListCreate(ctx context.Context, req *mcp.CallToolRequest, in NewsListCreateIn) (*mcp.CallToolResult, NewsChangeOut, error) {
	if err := oneOf("type", in.Type, "private", "public"); err != nil {
		return nil, NewsChangeOut{}, err
	}
	if err := oneOf("optin", in.Optin, "single", "double"); err != nil {
		return nil, NewsChangeOut{}, err
	}
	typ, optin := cmpOr(in.Type, "private"), cmpOr(in.Optin, "single")
	q := fmt.Sprintf("Create the newsletter list %q (%s, %s opt-in)?", oneLine(in.Name, 100), typ, optin)
	if typ == "public" {
		q += " Public lists are offered to anyone on listmonk's public sign-up page."
	}
	res, ok, err := t.askNews(ctx, req, q, in)
	if err != nil || res != nil {
		return res, NewsChangeOut{}, err
	}
	if !ok {
		return nil, NewsChangeOut{}, declined("no list was created")
	}
	var l listmonk.List
	if err := t.news(ctx, "POST", "/api/lists", nil, map[string]any{"name": in.Name, "type": typ, "optin": optin, "status": "active",
		"tags": nonNilS(in.Tags), "description": in.Description}, &l); err != nil {
		return nil, NewsChangeOut{}, err
	}
	return nil, NewsChangeOut{l.ID, l.Status, "List created, with the person's confirmation."}, nil
}

func cmpOr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

type NewsListUpdateIn struct {
	ID          int       `json:"id"`
	Name        *string   `json:"name,omitempty"`
	Type        *string   `json:"type,omitempty" jsonschema:"private or public"`
	Optin       *string   `json:"optin,omitempty" jsonschema:"single or double"`
	Status      *string   `json:"status,omitempty" jsonschema:"active or archived"`
	Tags        *[]string `json:"tags,omitempty" jsonschema:"replaces the tags"`
	Description *string   `json:"description,omitempty"`
}

func (t *Tools) newsListUpdate(ctx context.Context, req *mcp.CallToolRequest, in NewsListUpdateIn) (*mcp.CallToolResult, NewsChangeOut, error) {
	for _, c := range []struct {
		f  string
		v  *string
		ok []string
	}{{"type", in.Type, []string{"private", "public"}}, {"optin", in.Optin, []string{"single", "double"}}, {"status", in.Status, []string{"active", "archived"}}} {
		if c.v != nil {
			if err := oneOf(c.f, *c.v, c.ok...); err != nil || *c.v == "" {
				return nil, NewsChangeOut{}, fmt.Errorf("%s: one of %s", c.f, strings.Join(c.ok, ", "))
			}
		}
	}
	var l listmonk.List
	if err := t.news(ctx, "GET", listmonk.Path("/api/lists/:id", in.ID), nil, nil, &l); err != nil {
		return nil, NewsChangeOut{}, err
	}
	// A PUT replaces the tags, so the stored ones go along unless changed.
	w := map[string]any{"name": l.Name, "type": l.Type, "optin": l.Optin, "status": l.Status, "tags": nonNilS(l.Tags), "description": l.Description}
	var changes []string
	ch := func(k string, old string, v *string) {
		if v != nil && *v != old {
			w[k] = *v
			changes = append(changes, fmt.Sprintf("%s: %s → %s", k, oneLine(old, 80), oneLine(*v, 80)))
		}
	}
	ch("name", l.Name, in.Name)
	ch("type", l.Type, in.Type)
	ch("optin", l.Optin, in.Optin)
	ch("status", l.Status, in.Status)
	ch("description", l.Description, in.Description)
	if in.Tags != nil {
		w["tags"] = nonNilS(*in.Tags)
		changes = append(changes, fmt.Sprintf("tags: %s → %s", oneLine(strings.Join(l.Tags, ", "), 80), oneLine(strings.Join(*in.Tags, ", "), 80)))
	}
	if len(changes) == 0 {
		return nil, NewsChangeOut{l.ID, l.Status, "Nothing to change."}, nil
	}
	q := fmt.Sprintf("Change the newsletter list %q (%d subscriber(s))?\n\n%s", oneLine(l.Name, 100), l.Count, strings.Join(changes, "\n"))
	if in.Type != nil && *in.Type == "public" && l.Type != "public" {
		q += "\n\nPublic lists are offered to anyone on listmonk's public sign-up page."
	}
	res, ok, err := t.askNews(ctx, req, q, []any{listsBind([]listmonk.List{l}), w})
	if err != nil || res != nil {
		return res, NewsChangeOut{}, err
	}
	if !ok {
		return nil, NewsChangeOut{}, declined("nothing changed")
	}
	var out listmonk.List
	if err := t.news(ctx, "PUT", listmonk.Path("/api/lists/:id", l.ID), nil, w, &out); err != nil {
		return nil, NewsChangeOut{}, err
	}
	return nil, NewsChangeOut{out.ID, out.Status, "List changed, with the person's confirmation."}, nil
}

func (t *Tools) newsListDelete(ctx context.Context, req *mcp.CallToolRequest, in NewsIDIn) (*mcp.CallToolResult, NewsChangeOut, error) {
	var l listmonk.List
	if err := t.news(ctx, "GET", listmonk.Path("/api/lists/:id", in.ID), nil, nil, &l); err != nil {
		return nil, NewsChangeOut{}, err
	}
	q := fmt.Sprintf("Delete the newsletter list %q with its %d subscription(s)? The subscribers themselves stay; who agreed to this list is lost. It can't be undone.", oneLine(l.Name, 100), l.Count)
	res, ok, err := t.askNews(ctx, req, q, listsBind([]listmonk.List{l}))
	if err != nil || res != nil {
		return res, NewsChangeOut{}, err
	}
	if !ok {
		return nil, NewsChangeOut{}, declined("nothing was deleted")
	}
	if err := t.news(ctx, "DELETE", listmonk.Path("/api/lists/:id", l.ID), nil, nil, nil); err != nil {
		return nil, NewsChangeOut{}, err
	}
	return nil, NewsChangeOut{l.ID, "deleted", "Deleted, with the person's confirmation."}, nil
}

// ---- subscribers (real people's data: every change is asked)

type NewsSubAddIn struct {
	Email      string         `json:"email"`
	Name       string         `json:"name,omitempty"`
	Lists      []int          `json:"lists,omitempty" jsonschema:"list ids to subscribe them to"`
	Attributes map[string]any `json:"attributes,omitempty"`
	Consented  bool           `json:"consented,omitempty" jsonschema:"true only if the person gave their consent outside listmonk: their subscriptions are recorded as confirmed and no opt-in mail is sent"`
}

// optinNote: what adding to these lists sends or records.
func optinNote(ls []listmonk.List, consented bool) string {
	var double []string
	for _, l := range ls {
		if l.Optin == "double" {
			double = append(double, oneLine(l.Name, 80))
		}
	}
	switch {
	case consented:
		return "\n\nThis RECORDS THAT THEY AGREED (consent) to every list above; no opt-in mail is sent. Only say yes if they did agree."
	case len(double) > 0:
		return "\n\nlistmonk MAILS THEM a request to confirm their subscription to: " + strings.Join(double, ", ") + "."
	}
	return "\n\nNo mail is sent to them now; they get the campaigns sent to these lists."
}

func (t *Tools) newsSubscriberAdd(ctx context.Context, req *mcp.CallToolRequest, in NewsSubAddIn) (*mcp.CallToolResult, NewsChangeOut, error) {
	var ls []listmonk.List
	if len(in.Lists) > 0 {
		var err error
		if ls, err = t.lists(ctx, in.Lists); err != nil {
			return nil, NewsChangeOut{}, err
		}
	}
	who := oneLine(in.Email, 120)
	if !t.internal(ctx, in.Email) {
		who += " (OUTSIDE the company)"
	}
	q := fmt.Sprintf("Add %s %s as a newsletter subscriber", oneLine(in.Name, 80), who)
	if len(ls) > 0 {
		q += " to: " + listNames(ls)
	}
	q += "?"
	if len(in.Attributes) > 0 {
		q += "\nAttributes: " + oneLine(strings.Join(attribs(in.Attributes), "; "), 400)
	}
	if len(ls) > 0 {
		q += optinNote(ls, in.Consented)
	}
	res, ok, err := t.askNews(ctx, req, q, []any{in, listsBind(ls)})
	if err != nil || res != nil {
		return res, NewsChangeOut{}, err
	}
	if !ok {
		return nil, NewsChangeOut{}, declined("no one was added")
	}
	body := map[string]any{"email": in.Email, "name": in.Name, "status": "enabled", "lists": nonNilI(in.Lists), "preconfirm_subscriptions": in.Consented}
	if in.Attributes != nil {
		body["attribs"] = in.Attributes
	}
	var s listmonk.Subscriber
	if err := t.news(ctx, "POST", "/api/subscribers", nil, body, &s); err != nil {
		return nil, NewsChangeOut{}, err
	}
	return nil, NewsChangeOut{s.ID, s.Status, "Added, with the person's confirmation."}, nil
}

func nonNilI(s []int) []int {
	if s == nil {
		return []int{}
	}
	return s
}

type NewsSubUpdateIn struct {
	ID         int             `json:"id"`
	Email      *string         `json:"email,omitempty"`
	Name       *string         `json:"name,omitempty"`
	Status     *string         `json:"status,omitempty" jsonschema:"enabled or disabled (to blocklist: newsletter_subscriber_blocklist)"`
	Attributes *map[string]any `json:"attributes,omitempty" jsonschema:"replaces all attributes"`
}

func (t *Tools) subscriber(ctx context.Context, id int) (listmonk.Subscriber, error) {
	var s listmonk.Subscriber
	err := t.news(ctx, "GET", listmonk.Path("/api/subscribers/:id", id), nil, nil, &s)
	return s, err
}

func (t *Tools) newsSubscriberUpdate(ctx context.Context, req *mcp.CallToolRequest, in NewsSubUpdateIn) (*mcp.CallToolResult, NewsChangeOut, error) {
	if in.Status != nil {
		if err := oneOf("status", *in.Status, "enabled", "disabled"); err != nil || *in.Status == "" {
			return nil, NewsChangeOut{}, fmt.Errorf("status: enabled or disabled (to blocklist: newsletter_subscriber_blocklist)")
		}
	}
	s, err := t.subscriber(ctx, in.ID)
	if err != nil {
		return nil, NewsChangeOut{}, err
	}
	body := map[string]any{}
	var changes []string
	ch := func(k, old string, v *string) {
		if v != nil && *v != old {
			body[k] = *v
			changes = append(changes, fmt.Sprintf("%s: %s → %s", k, oneLine(old, 100), oneLine(*v, 100)))
		}
	}
	ch("email", s.Email, in.Email)
	ch("name", s.Name, in.Name)
	ch("status", s.Status, in.Status)
	if in.Attributes != nil {
		body["attribs"] = *in.Attributes
		changes = append(changes, "attributes replaced by: "+oneLine(strings.Join(attribs(*in.Attributes), "; "), 400))
	}
	if len(changes) == 0 {
		return nil, NewsChangeOut{s.ID, s.Status, "Nothing to change."}, nil
	}
	q := fmt.Sprintf("Change the newsletter subscriber %s <%s>?\n\n%s", oneLine(s.Name, 80), oneLine(s.Email, 120), strings.Join(changes, "\n"))
	// listmonk mails an opt-in for unconfirmed double opt-in subscriptions
	// on every change (unless consent is recorded, which this never does).
	var pending []string
	for _, l := range s.Lists {
		if l.Optin == "double" && l.Status == "unconfirmed" {
			pending = append(pending, oneLine(l.Name, 80))
		}
	}
	if len(pending) > 0 {
		q += "\n\nlistmonk MAILS THEM again a request to confirm: " + strings.Join(pending, ", ") + "."
	}
	res, ok, err := t.askNews(ctx, req, q, []any{subBind([]listmonk.Subscriber{s}), body})
	if err != nil || res != nil {
		return res, NewsChangeOut{}, err
	}
	if !ok {
		return nil, NewsChangeOut{}, declined("nothing changed")
	}
	var out listmonk.Subscriber
	if err := t.news(ctx, "PATCH", listmonk.Path("/api/subscribers/:id", s.ID), nil, body, &out); err != nil {
		return nil, NewsChangeOut{}, err
	}
	return nil, NewsChangeOut{out.ID, out.Status, "Changed, with the person's confirmation."}, nil
}

type NewsSubscriptionIn struct {
	ID        int    `json:"id" jsonschema:"the subscriber"`
	Action    string `json:"action" jsonschema:"add (subscribe to the lists), unsubscribe (they stay on the list as unsubscribed) or remove (taken off the list)"`
	Lists     []int  `json:"lists"`
	Consented bool   `json:"consented,omitempty" jsonschema:"with add: true only if they gave their consent outside listmonk (recorded as confirmed); default unconfirmed"`
}

func (t *Tools) newsSubscription(ctx context.Context, req *mcp.CallToolRequest, in NewsSubscriptionIn) (*mcp.CallToolResult, NewsChangeOut, error) {
	if err := oneOf("action", in.Action, "add", "unsubscribe", "remove"); err != nil || in.Action == "" {
		return nil, NewsChangeOut{}, fmt.Errorf("action: add, unsubscribe or remove")
	}
	if in.Consented && in.Action != "add" {
		return nil, NewsChangeOut{}, fmt.Errorf("consented goes with add only")
	}
	s, err := t.subscriber(ctx, in.ID)
	if err != nil {
		return nil, NewsChangeOut{}, err
	}
	ls, err := t.lists(ctx, in.Lists)
	if err != nil {
		return nil, NewsChangeOut{}, err
	}
	verb := map[string]string{"add": "Subscribe", "unsubscribe": "UNSUBSCRIBE", "remove": "Take"}[in.Action]
	q := fmt.Sprintf("%s %s <%s> %s %s?", verb, oneLine(s.Name, 80), oneLine(s.Email, 120),
		map[string]string{"add": "to", "unsubscribe": "from", "remove": "off"}[in.Action], listNames(ls))
	body := map[string]any{"ids": []int{s.ID}, "action": in.Action, "target_list_ids": in.Lists}
	switch in.Action {
	case "add":
		status := "unconfirmed"
		if in.Consented {
			status = "confirmed"
			q += "\n\nThis RECORDS THAT THEY AGREED (consent) to these lists. Only say yes if they did agree."
		} else {
			q += "\n\nNo mail is sent now; on double opt-in lists they get campaigns only once they confirm."
		}
		body["status"] = status
	case "unsubscribe":
		q += "\n\nThey get no more campaigns to these lists."
	case "remove":
		q += "\n\nWhether they had agreed to these lists is lost."
	}
	res, ok, err := t.askNews(ctx, req, q, []any{subBind([]listmonk.Subscriber{s}), listsBind(ls), body})
	if err != nil || res != nil {
		return res, NewsChangeOut{}, err
	}
	if !ok {
		return nil, NewsChangeOut{}, declined("nothing changed")
	}
	if err := t.news(ctx, "PUT", listmonk.Path("/api/subscribers/lists/:id", s.ID), nil, body, nil); err != nil {
		return nil, NewsChangeOut{}, err
	}
	return nil, NewsChangeOut{s.ID, s.Status, "Done, with the person's confirmation."}, nil
}

func (t *Tools) newsSubscriberBlocklist(ctx context.Context, req *mcp.CallToolRequest, in NewsIDIn) (*mcp.CallToolResult, NewsChangeOut, error) {
	s, err := t.subscriber(ctx, in.ID)
	if err != nil {
		return nil, NewsChangeOut{}, err
	}
	q := fmt.Sprintf("BLOCKLIST %s <%s>? They are unsubscribed from all %d list(s) and get no newsletter mail at all.", oneLine(s.Name, 80), oneLine(s.Email, 120), len(s.Lists))
	res, ok, err := t.askNews(ctx, req, q, subBind([]listmonk.Subscriber{s}))
	if err != nil || res != nil {
		return res, NewsChangeOut{}, err
	}
	if !ok {
		return nil, NewsChangeOut{}, declined("nothing changed")
	}
	if err := t.news(ctx, "PUT", listmonk.Path("/api/subscribers/:id/blocklist", s.ID), nil, map[string]any{}, nil); err != nil {
		return nil, NewsChangeOut{}, err
	}
	return nil, NewsChangeOut{s.ID, "blocklisted", "Blocklisted, with the person's confirmation."}, nil
}

func (t *Tools) newsSubscriberDelete(ctx context.Context, req *mcp.CallToolRequest, in NewsIDIn) (*mcp.CallToolResult, NewsChangeOut, error) {
	s, err := t.subscriber(ctx, in.ID)
	if err != nil {
		return nil, NewsChangeOut{}, err
	}
	q := fmt.Sprintf("Delete the newsletter subscriber %s <%s> with their %d subscription(s) and history? It can't be undone.", oneLine(s.Name, 80), oneLine(s.Email, 120), len(s.Lists))
	res, ok, err := t.askNews(ctx, req, q, subBind([]listmonk.Subscriber{s}))
	if err != nil || res != nil {
		return res, NewsChangeOut{}, err
	}
	if !ok {
		return nil, NewsChangeOut{}, declined("nothing was deleted")
	}
	if err := t.news(ctx, "DELETE", listmonk.Path("/api/subscribers/:id", s.ID), nil, nil, nil); err != nil {
		return nil, NewsChangeOut{}, err
	}
	return nil, NewsChangeOut{s.ID, "deleted", "Deleted, with the person's confirmation."}, nil
}

// ---- templates

type NewsTemplateIn struct {
	Name    string `json:"name"`
	Type    string `json:"type" jsonschema:"campaign (must contain {{ template \"content\" . }} once) or tx (transactional; needs a subject)"`
	Subject string `json:"subject,omitempty" jsonschema:"tx templates only"`
	Body    string `json:"body" jsonschema:"HTML with listmonk's {{ }} template tags"`
}

func (t *Tools) newsTemplateCreate(ctx context.Context, req *mcp.CallToolRequest, in NewsTemplateIn) (*mcp.CallToolResult, NewsChangeOut, error) {
	if err := oneOf("type", in.Type, "campaign", "tx"); err != nil || in.Type == "" {
		return nil, NewsChangeOut{}, fmt.Errorf("type: campaign or tx")
	}
	q := fmt.Sprintf("Create the newsletter template %q (%s, %d characters)?", oneLine(in.Name, 100), in.Type, len([]rune(in.Body)))
	if in.Type == "tx" {
		q += " Subject: " + oneLine(in.Subject, 200) + ". Transactional mail sent with it shows only its name and subject in the question."
	}
	res, ok, err := t.askNews(ctx, req, q, in)
	if err != nil || res != nil {
		return res, NewsChangeOut{}, err
	}
	if !ok {
		return nil, NewsChangeOut{}, declined("no template was created")
	}
	body := map[string]any{"name": in.Name, "type": in.Type, "body": in.Body}
	if in.Type == "tx" {
		body["subject"] = in.Subject
	}
	var x listmonk.Template
	if err := t.news(ctx, "POST", "/api/templates", nil, body, &x); err != nil {
		return nil, NewsChangeOut{}, err
	}
	return nil, NewsChangeOut{ID: x.ID, Note: "Template created, with the person's confirmation; nothing uses it until a campaign or transactional mail is given it."}, nil
}

type NewsTemplateUpdateIn struct {
	ID      int     `json:"id"`
	Name    *string `json:"name,omitempty"`
	Subject *string `json:"subject,omitempty" jsonschema:"tx templates only"`
	Body    *string `json:"body,omitempty"`
}

func (t *Tools) newsTemplateUpdate(ctx context.Context, req *mcp.CallToolRequest, in NewsTemplateUpdateIn) (*mcp.CallToolResult, NewsChangeOut, error) {
	x, err := t.template(ctx, in.ID)
	if err != nil {
		return nil, NewsChangeOut{}, err
	}
	// listmonk checks a template by the type in the request: always send it.
	w := map[string]any{"name": x.Name, "type": x.Type, "subject": x.Subject, "body": x.Body}
	var changes []string
	if in.Name != nil && *in.Name != x.Name {
		w["name"] = *in.Name
		changes = append(changes, fmt.Sprintf("name: %s → %s", oneLine(x.Name, 80), oneLine(*in.Name, 80)))
	}
	if in.Subject != nil && *in.Subject != x.Subject {
		if x.Type != "tx" {
			return nil, NewsChangeOut{}, fmt.Errorf("only tx templates have a subject")
		}
		w["subject"] = *in.Subject
		changes = append(changes, fmt.Sprintf("subject: %s → %s", oneLine(x.Subject, 100), oneLine(*in.Subject, 100)))
	}
	if in.Body != nil && *in.Body != x.Body {
		w["body"] = *in.Body
		changes = append(changes, fmt.Sprintf("body: %d → %d characters", len([]rune(x.Body)), len([]rune(*in.Body))))
	}
	if len(changes) == 0 {
		return nil, NewsChangeOut{ID: x.ID, Note: "Nothing to change."}, nil
	}
	q := fmt.Sprintf("Change the newsletter template %q (%s)?\n\n%s\n\nEvery campaign and transactional mail that uses it goes out with the new version from now on, scheduled ones too.", oneLine(x.Name, 100), x.Type, strings.Join(changes, "\n"))
	if x.IsDefault {
		q += " It is the default template."
	}
	res, ok, err := t.askNews(ctx, req, q, []any{x, w})
	if err != nil || res != nil {
		return res, NewsChangeOut{}, err
	}
	if !ok {
		return nil, NewsChangeOut{}, declined("nothing changed")
	}
	var out listmonk.Template
	if err := t.news(ctx, "PUT", listmonk.Path("/api/templates/:id", x.ID), nil, w, &out); err != nil {
		return nil, NewsChangeOut{}, err
	}
	return nil, NewsChangeOut{ID: x.ID, Note: "Template changed, with the person's confirmation."}, nil
}

// ---- bounces

func (t *Tools) newsBounceDelete(ctx context.Context, req *mcp.CallToolRequest, in NewsIDIn) (*mcp.CallToolResult, NewsChangeOut, error) {
	var b listmonk.Bounce
	if err := t.news(ctx, "GET", listmonk.Path("/api/bounces/:id", in.ID), nil, nil, &b); err != nil {
		return nil, NewsChangeOut{}, err
	}
	q := fmt.Sprintf("Delete the %s bounce record of %s (%s)? listmonk counts bounces to decide when to blocklist an address; this one stops counting. It can't be undone.",
		oneLine(b.Type, 20), oneLine(b.Email, 120), b.CreatedAt)
	res, ok, err := t.askNews(ctx, req, q, b)
	if err != nil || res != nil {
		return res, NewsChangeOut{}, err
	}
	if !ok {
		return nil, NewsChangeOut{}, declined("nothing was deleted")
	}
	if err := t.news(ctx, "DELETE", listmonk.Path("/api/bounces/:id", in.ID), nil, nil, nil); err != nil {
		return nil, NewsChangeOut{}, err
	}
	return nil, NewsChangeOut{in.ID, "deleted", "Deleted, with the person's confirmation."}, nil
}
