// Package listmonk: a minimal client for the newsletter app (listmonk,
// the SGNL fork that accepts the login server's tokens as the listmonk
// user with the same email). Only the routes in allowed can be called:
// never raw SQL (`query` on subscribers, the /query/ routes), bulk or
// "all" deletes, imports, settings, users or roles.
package listmonk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	Base string // e.g. http://listmonk:9000
	HTTP *http.Client
}

func New(base string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// route: one method on one path shape (":id" is a number).
type route struct {
	method string
	re     *regexp.Regexp
}

func r(method, path string) route {
	return route{method, regexp.MustCompile("^" + strings.ReplaceAll(path, ":id", `[0-9]+`) + "$")}
}

// allowed: every call the gateway may make. Bulk forms (?id=…&id=…,
// all=true), the /query/ routes and raw SQL are not among them.
var allowed = []route{
	r("GET", "/api/profile"),
	r("GET", "/api/dashboard/counts"),
	r("GET", "/api/lists"), r("GET", "/api/lists/:id"), r("POST", "/api/lists"), r("PUT", "/api/lists/:id"), r("DELETE", "/api/lists/:id"),
	r("GET", "/api/lists/analytics/(subscriptions|unsubscriptions|views|clicks|bounces)"),
	r("GET", "/api/subscribers"), r("GET", "/api/subscribers/:id"), r("POST", "/api/subscribers"), r("PATCH", "/api/subscribers/:id"),
	r("PUT", "/api/subscribers/:id/blocklist"), r("PUT", "/api/subscribers/lists/:id"), r("DELETE", "/api/subscribers/:id"),
	r("GET", "/api/subscribers/:id/bounces"),
	r("GET", "/api/campaigns"), r("GET", "/api/campaigns/:id"), r("POST", "/api/campaigns"), r("PUT", "/api/campaigns/:id"),
	r("PUT", "/api/campaigns/:id/status"), r("POST", "/api/campaigns/:id/test"), r("DELETE", "/api/campaigns/:id"),
	r("GET", "/api/campaigns/running/stats"),
	r("GET", "/api/campaigns/analytics/(views|clicks|bounces|links)"),
	r("GET", "/api/templates"), r("GET", "/api/templates/:id"), r("POST", "/api/templates"), r("PUT", "/api/templates/:id"),
	r("GET", "/api/bounces"), r("GET", "/api/bounces/:id"), r("DELETE", "/api/bounces/:id"),
	r("POST", "/api/tx"),
}

// forbiddenQuery: parameters never sent: "everything" (bulk forms).
var forbiddenQuery = []string{"all"}

// MaxPerPage: the most items one page may ask for (the API has no cap and
// takes "all").
const MaxPerPage = 50

// Error: listmonk's answer to a refused call.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("the newsletter app answered %d: %s", e.Status, e.Message)
}

// ErrNoAccount: listmonk has no user with the person's email (or that
// user may not sign in).
var ErrNoAccount = errors.New("you have no newsletter (listmonk) account that can be used here: listmonk looks for a user with your login's email address. Ask an admin to create one")

// Call: one allowed call with the person's token; out (if not nil) gets
// the answer's "data". query values are sent as given (per_page clamped).
func (c *Client) Call(ctx context.Context, token, method, path string, q url.Values, body, out any) error {
	if !permitted(method, path) {
		return fmt.Errorf("listmonk: %s %s is not allowed", method, path)
	}
	if path == "/api/subscribers" && q.Has("query") {
		return fmt.Errorf("listmonk: raw SQL queries are not allowed (use search)")
	}
	for _, k := range forbiddenQuery {
		if q.Has(k) {
			return fmt.Errorf("listmonk: %s is not allowed", k)
		}
	}
	if q.Has("per_page") {
		n, err := strconv.Atoi(q.Get("per_page"))
		if err != nil || n < 1 {
			n = 20
		}
		q.Set("per_page", strconv.Itoa(min(n, MaxPerPage)))
	}
	u := c.Base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("the newsletter app didn't answer: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(b, &e)
		if resp.StatusCode == 403 && (e.Message == "unknown user" || e.Message == "user not allowed") {
			return ErrNoAccount
		}
		if e.Message == "" {
			e.Message = http.StatusText(resp.StatusCode)
		}
		return &Error{resp.StatusCode, e.Message}
	}
	if out == nil {
		return nil
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return fmt.Errorf("the newsletter app's answer isn't JSON: %w", err)
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	return json.Unmarshal(env.Data, out)
}

func permitted(method, path string) bool {
	return slices.ContainsFunc(allowed, func(x route) bool { return x.method == method && x.re.MatchString(path) })
}

// Path: a route with its numeric id filled in.
func Path(format string, id int) string {
	return strings.Replace(format, ":id", strconv.Itoa(id), 1)
}

// ---- the app's objects (the fields the gateway uses)

type Profile struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	UserRole struct {
		Name        string   `json:"name"`
		Permissions []string `json:"permissions"`
	} `json:"user_role"`
}

type List struct {
	ID          int            `json:"id"`
	Name        string         `json:"name"`
	Type        string         `json:"type"`   // public | private
	Optin       string         `json:"optin"`  // single | double
	Status      string         `json:"status"` // active | archived
	Tags        []string       `json:"tags"`
	Description string         `json:"description"`
	Count       int            `json:"subscriber_count"`
	Statuses    map[string]int `json:"subscriber_statuses"`
	CreatedAt   string         `json:"created_at"`
	UpdatedAt   string         `json:"updated_at"`
}

// Recipients: how many a campaign to this list reaches at most: on a
// double opt-in list only confirmed subscriptions, otherwise all but
// unsubscribed (listmonk's own rule; blocklisted people are unsubscribed
// everywhere).
func (l List) Recipients() int {
	if l.Optin == "double" {
		return l.Statuses["confirmed"]
	}
	return l.Statuses["confirmed"] + l.Statuses["unconfirmed"]
}

type Subscription struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Optin  string `json:"optin"`
	Status string `json:"subscription_status"`
}

type Subscriber struct {
	ID        int            `json:"id"`
	Email     string         `json:"email"`
	Name      string         `json:"name"`
	Status    string         `json:"status"` // enabled | disabled | blocklisted
	Attribs   map[string]any `json:"attribs"`
	Lists     []Subscription `json:"lists"`
	CreatedAt string         `json:"created_at"`
	UpdatedAt string         `json:"updated_at"`
}

type CampaignList struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type Campaign struct {
	ID          int               `json:"id"`
	Name        string            `json:"name"`
	Subject     string            `json:"subject"`
	FromEmail   string            `json:"from_email"`
	Status      string            `json:"status"`
	Type        string            `json:"type"`
	ContentType string            `json:"content_type"`
	Body        string            `json:"body"`
	BodySource  *string           `json:"body_source"`
	AltBody     *string           `json:"altbody"`
	SendAt      *string           `json:"send_at"`
	Tags        []string          `json:"tags"`
	Headers     []json.RawMessage `json:"headers"`
	Attribs     json.RawMessage   `json:"attribs"`
	TemplateID  *int              `json:"template_id"`
	Messenger   string            `json:"messenger"`
	Archive     bool              `json:"archive"`
	ArchiveSlug *string           `json:"archive_slug"`
	ArchiveTmpl *int              `json:"archive_template_id"`
	ArchiveMeta json.RawMessage   `json:"archive_meta"`
	Lists       []CampaignList    `json:"lists"`
	Media       []struct {
		ID int `json:"id"`
	} `json:"media"`
	Views     int    `json:"views"`
	Clicks    int    `json:"clicks"`
	Bounces   int    `json:"bounces"`
	ToSend    int    `json:"to_send"`
	Sent      int    `json:"sent"`
	StartedAt string `json:"started_at"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// Write: the campaign as a PUT or test body: the stored fields as they
// are, lists and media by id (a PUT without lists fails, without media
// drops them).
func (c Campaign) Write() map[string]any {
	lists := make([]int, 0, len(c.Lists))
	for _, l := range c.Lists {
		lists = append(lists, l.ID)
	}
	media := make([]int, 0, len(c.Media))
	for _, m := range c.Media {
		media = append(media, m.ID)
	}
	w := map[string]any{"name": c.Name, "subject": c.Subject, "from_email": c.FromEmail, "content_type": c.ContentType,
		"body": c.Body, "type": c.Type, "tags": nonNil(c.Tags), "messenger": c.Messenger, "archive": c.Archive,
		"lists": lists, "media": media}
	set := func(k string, v any, ok bool) {
		if ok {
			w[k] = v
		}
	}
	set("body_source", c.BodySource, c.BodySource != nil)
	set("altbody", c.AltBody, c.AltBody != nil)
	set("send_at", c.SendAt, c.SendAt != nil)
	set("template_id", c.TemplateID, c.TemplateID != nil)
	set("archive_slug", c.ArchiveSlug, c.ArchiveSlug != nil)
	set("archive_template_id", c.ArchiveTmpl, c.ArchiveTmpl != nil)
	set("headers", c.Headers, c.Headers != nil)
	set("attribs", c.Attribs, len(c.Attribs) > 0 && string(c.Attribs) != "null")
	set("archive_meta", c.ArchiveMeta, len(c.ArchiveMeta) > 0 && string(c.ArchiveMeta) != "null")
	return w
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

type Template struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"` // campaign | campaign_visual | tx
	Subject   string `json:"subject"`
	Body      string `json:"body"`
	IsDefault bool   `json:"is_default"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type Bounce struct {
	ID           int             `json:"id"`
	Type         string          `json:"type"`
	Source       string          `json:"source"`
	Email        string          `json:"email"`
	SubscriberID int             `json:"subscriber_id"`
	Campaign     *CampaignList   `json:"campaign"`
	Meta         json.RawMessage `json:"meta"`
	CreatedAt    string          `json:"created_at"`
}

// Page: a paged answer.
type Page[T any] struct {
	Results []T `json:"results"`
	Total   int `json:"total"`
	PerPage int `json:"per_page"`
	Page    int `json:"page"`
}
