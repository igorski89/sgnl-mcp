// Package jmap: a minimal JMAP client (RFC 8620, 8621; JMAP Calendars,
// Contacts, Principals) with hard limits on what it may send:
//   - only the methods in allowed;
//   - never EmailSubmission (no sending mail);
//   - no destroy, except a draft being replaced (Call.DestroyDraft);
//   - calendar events without participants (no invitations go out).
package jmap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

const (
	Core         = "urn:ietf:params:jmap:core"
	Mail         = "urn:ietf:params:jmap:mail"
	Submission   = "urn:ietf:params:jmap:submission"
	Calendars    = "urn:ietf:params:jmap:calendars"
	Contacts     = "urn:ietf:params:jmap:contacts"
	Principals   = "urn:ietf:params:jmap:principals"
	Availability = "urn:ietf:params:jmap:principals:availability"
)

var allowed = []string{
	"Mailbox/get", "Mailbox/query", "Thread/get", "Identity/get",
	"Email/get", "Email/query", "Email/set",
	"Calendar/get", "CalendarEvent/get", "CalendarEvent/query", "CalendarEvent/set",
	"AddressBook/get", "ContactCard/get", "ContactCard/query",
	"Principal/get", "Principal/query", "Principal/getAvailability",
}

// checkSend: EmailSubmission/set as mail_send uses it, and nothing else.
func checkSend(call Call) error {
	if !call.Send {
		return fmt.Errorf("jmap: EmailSubmission/set is not allowed")
	}
	for k, v := range call.Args {
		switch k {
		case "accountId", "create":
		case "onSuccessUpdateEmail":
			m, _ := v.(map[string]any)
			for ref, patch := range m {
				p, _ := patch.(map[string]any)
				if !strings.HasPrefix(ref, "#") || p == nil {
					return fmt.Errorf("jmap: send: onSuccessUpdateEmail must patch the submitted mail")
				}
				for f, val := range p {
					switch {
					case f == "keywords/$draft" && val == nil:
					case strings.HasPrefix(f, "mailboxIds/") && (val == nil || val == true):
					default:
						return fmt.Errorf("jmap: send: on success only Drafts -> Sent is allowed, not %s", f)
					}
				}
			}
		default:
			return fmt.Errorf("jmap: send: %s is not allowed", k)
		}
	}
	c, _ := call.Args["create"].(map[string]any)
	if len(c) != 1 {
		return fmt.Errorf("jmap: send: exactly one mail at a time")
	}
	for _, sub := range c {
		m, _ := sub.(map[string]any)
		if m["emailId"] == nil || m["identityId"] == nil {
			return fmt.Errorf("jmap: send: emailId and identityId are required")
		}
		for f := range m {
			if f != "emailId" && f != "identityId" {
				return fmt.Errorf("jmap: send: %s is not allowed (recipients come from the mail itself)", f)
			}
		}
	}
	return nil
}

// Session: the parts of the JMAP session object we use.
type Session struct {
	APIURL          string            `json:"apiUrl"`
	DownloadURL     string            `json:"downloadUrl"`
	UploadURL       string            `json:"uploadUrl"`
	Capabilities    map[string]any    `json:"capabilities"`
	PrimaryAccounts map[string]string `json:"primaryAccounts"`
}

type Client struct {
	SessionURL string
	HTTP       *http.Client
}

func New(sessionURL string) *Client {
	return &Client{SessionURL: sessionURL, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) do(ctx context.Context, token, method, url string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode != 200 {
		return fmt.Errorf("mail server: %d %.200s", resp.StatusCode, b)
	}
	return json.Unmarshal(b, out)
}

func (c *Client) Session(ctx context.Context, token string) (*Session, error) {
	var s Session
	if err := c.do(ctx, token, "GET", c.SessionURL, nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Call: one method call; "#name" back-references are allowed in args.
type Call struct {
	Method string
	Args   map[string]any
	// DestroyDraft: the only destroy ever sent, for replacing a draft the
	// caller has checked is a draft ($draft, in the Drafts mailbox).
	DestroyDraft bool
	// Send: the only EmailSubmission ever sent, for mail_send after the
	// person confirmed. Only "create" of {emailId, identityId} (recipients
	// come from the draft's own headers, as shown to the person), and on
	// success only moving the mail from Drafts to Sent.
	Send bool
	// Invite: the only CalendarEvent/set with participants, for
	// event_invite after the person confirmed: exactly one create, the
	// attendees each only by email (imip), and sendSchedulingMessages, so
	// the mail server adds the person as organizer and sends the invitations.
	Invite bool
}

// check enforces the limits above on one call.
func check(call Call) error {
	if call.Method == "EmailSubmission/set" {
		return checkSend(call)
	}
	if !slices.Contains(allowed, call.Method) {
		return fmt.Errorf("jmap: %s is not allowed", call.Method)
	}
	if !strings.HasSuffix(call.Method, "/set") {
		return nil
	}
	if d, ok := call.Args["destroy"]; ok && d != nil {
		if call.Method != "Email/set" || !call.DestroyDraft {
			return fmt.Errorf("jmap: %s destroy is not allowed", call.Method)
		}
	}
	if call.Method == "CalendarEvent/set" && call.Invite {
		return checkInvite(call)
	}
	if call.Method == "CalendarEvent/set" {
		if v, ok := call.Args["sendSchedulingMessages"]; ok && v != false {
			return fmt.Errorf("jmap: scheduling messages only from event_invite")
		}
		for _, k := range []string{"create", "update"} {
			m, _ := call.Args[k].(map[string]any)
			for _, ev := range m {
				e, _ := ev.(map[string]any)
				for f := range e {
					if f == "participants" || strings.HasPrefix(f, "participants/") || f == "replyTo" {
						return fmt.Errorf("jmap: events with participants are not allowed (they send invitations)")
					}
				}
			}
		}
	}
	return nil
}

// checkInvite: see Call.Invite.
func checkInvite(call Call) error {
	for k := range call.Args {
		if k != "accountId" && k != "create" && k != "sendSchedulingMessages" {
			return fmt.Errorf("jmap: invite: %s is not allowed", k)
		}
	}
	if call.Args["sendSchedulingMessages"] != true {
		return fmt.Errorf("jmap: invite: without sendSchedulingMessages")
	}
	c, _ := call.Args["create"].(map[string]any)
	if len(c) != 1 {
		return fmt.Errorf("jmap: invite: exactly one event")
	}
	for _, ev := range c {
		e, _ := ev.(map[string]any)
		for f := range e {
			if f == "replyTo" || f == "organizerCalendarAddress" {
				return fmt.Errorf("jmap: invite: %s is set by the mail server, not the gateway", f)
			}
		}
		ps, _ := e["participants"].(map[string]any)
		if len(ps) == 0 {
			return fmt.Errorf("jmap: invite: no participants")
		}
		for _, pv := range ps {
			p, _ := pv.(map[string]any)
			for k, v := range p {
				switch k {
				case "@type", "email", "name", "participationStatus", "expectReply":
				case "roles":
					if r, _ := v.(map[string]bool); len(r) != 1 || !r["attendee"] {
						return fmt.Errorf("jmap: invite: participants may only be attendees")
					}
				case "sendTo":
					st, _ := v.(map[string]string)
					if len(st) != 1 || !strings.HasPrefix(st["imip"], "mailto:") || st["imip"] != "mailto:"+fmt.Sprint(p["email"]) {
						return fmt.Errorf("jmap: invite: an attendee is reached only at their own email")
					}
				default:
					return fmt.Errorf("jmap: invite: participant field %s is not allowed", k)
				}
			}
		}
	}
	return nil
}

// Invoke sends calls in one request and returns each response's
// arguments by call index. A method error fails the whole request.
func (c *Client) Invoke(ctx context.Context, token string, s *Session, using []string, calls ...Call) ([]json.RawMessage, error) {
	mc := make([]any, len(calls))
	for i, call := range calls {
		if err := check(call); err != nil {
			return nil, err
		}
		mc[i] = []any{call.Method, call.Args, fmt.Sprint(i)}
	}
	var r struct {
		MethodResponses [][]json.RawMessage `json:"methodResponses"`
	}
	if err := c.do(ctx, token, "POST", s.APIURL, map[string]any{"using": using, "methodCalls": mc}, &r); err != nil {
		return nil, err
	}
	out := make([]json.RawMessage, len(calls))
	for _, mr := range r.MethodResponses {
		if len(mr) != 3 {
			continue
		}
		var name, id string
		_ = json.Unmarshal(mr[0], &name)
		_ = json.Unmarshal(mr[2], &id)
		if name == "error" {
			return nil, fmt.Errorf("mail server: %s", strings.TrimSpace(string(mr[1])))
		}
		// A call can be followed by implicit responses under the same id
		// (EmailSubmission/set's onSuccessUpdateEmail adds an Email/set):
		// each call gets its own method's response.
		var i int
		if _, err := fmt.Sscan(id, &i); err == nil && i >= 0 && i < len(out) && name == calls[i].Method && out[i] == nil {
			out[i] = mr[1]
		}
	}
	return out, nil
}

// SetErrors: the notCreated/notUpdated/notDestroyed part of a */set
// response as an error, if any.
func SetErrors(raw json.RawMessage) error {
	var r map[string]map[string]struct {
		Type        string `json:"type"`
		Description string `json:"description"`
	}
	_ = json.Unmarshal(raw, &r)
	for _, k := range []string{"notCreated", "notUpdated", "notDestroyed"} {
		for id, e := range r[k] {
			return fmt.Errorf("mail server refused %s (%s): %s %s", strings.TrimPrefix(k, "not"), id, e.Type, e.Description)
		}
	}
	return nil
}

// Download: at most max bytes of a blob.
func (c *Client) Download(ctx context.Context, token string, s *Session, accountID, blobID, name, typ string, max int64) ([]byte, bool, error) {
	u := s.DownloadURL
	for k, v := range map[string]string{"{accountId}": accountID, "{blobId}": blobID, "{name}": url.PathEscape(name), "{type}": url.QueryEscape(typ)} {
		u = strings.ReplaceAll(u, k, v)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, false, fmt.Errorf("mail server: download %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if int64(len(b)) > max {
		return b[:max], true, err
	}
	return b, false, err
}

// Upload stores data as a blob in the account (RFC 8620 §6.1) and returns
// its id, for attaching it to a draft.
func (c *Client) Upload(ctx context.Context, token string, s *Session, accountID, typ string, data []byte) (string, error) {
	if s.UploadURL == "" {
		return "", fmt.Errorf("mail server: no upload URL")
	}
	u := strings.ReplaceAll(s.UploadURL, "{accountId}", accountID)
	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", typ)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return "", fmt.Errorf("mail server: upload %d %.200s", resp.StatusCode, b)
	}
	var r struct {
		BlobID string `json:"blobId"`
	}
	if err := json.Unmarshal(b, &r); err != nil || r.BlobID == "" {
		return "", fmt.Errorf("mail server: upload gave no blob id")
	}
	return r.BlobID, nil
}

// Ref: a back-reference to an earlier call's result (by index).
func Ref(call int, method, path string) map[string]any {
	return map[string]any{"resultOf": fmt.Sprint(call), "name": method, "path": path}
}
