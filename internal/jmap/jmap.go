// Package jmap: a minimal JMAP client (RFC 8620, 8621; JMAP Calendars,
// Contacts, Principals) with hard limits on what it may send:
//   - only the methods in allowed;
//   - never EmailSubmission (no sending mail);
//   - no destroy, except a draft being replaced (Call.DestroyDraft);
//   - calendar events without participants (no invitations go out);
//   - mail folders: create or rename one, never delete;
//   - contact cards: create or update one; delete one only after a confirm.
package jmap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/igorski89/sgnl-mcp/internal/audit"
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
	Vacation     = "urn:ietf:params:jmap:vacationresponse"
	Quota        = "urn:ietf:params:jmap:quota"
)

var allowed = []string{
	"Mailbox/get", "Mailbox/query", "Thread/get", "Identity/get",
	"Email/get", "Email/query", "Email/set",
	"Calendar/get", "CalendarEvent/get", "CalendarEvent/query", "CalendarEvent/set",
	"AddressBook/get", "ContactCard/get", "ContactCard/query",
	"Principal/get", "Principal/query", "Principal/getAvailability",
	"VacationResponse/get", "VacationResponse/set", "CalendarEvent/parse",
	"Mailbox/set", "ContactCard/set", "Quota/get",
}

// cardFields: what a contact card may carry when created or changed here.
var cardFields = []string{"name", "emails", "phones", "organizations", "titles", "notes", "members"}

// checkCard: ContactCard/set creates or updates one card (only cardFields;
// a new card also its @type, version, kind, uid and address book), or,
// with Call.DeleteContact, destroys exactly one card and nothing else.
func checkCard(call Call) error {
	n := 0
	for k, v := range call.Args {
		switch k {
		case "accountId":
		case "destroy":
			ids, _ := v.([]string)
			if !call.DeleteContact || len(ids) != 1 {
				return fmt.Errorf("jmap: contact destroy is not allowed")
			}
			n++
		case "create", "update":
			if call.DeleteContact {
				return fmt.Errorf("jmap: contact delete: only the destroy")
			}
			m, _ := v.(map[string]any)
			for _, x := range m {
				n++
				f, _ := x.(map[string]any)
				for fk, fv := range f {
					if fk == "members" {
						mm, ok := fv.(map[string]any)
						if !ok {
							return fmt.Errorf("jmap: contact: members only as a whole object")
						}
						for _, on := range mm {
							if on != true {
								return fmt.Errorf("jmap: contact: a member must be true")
							}
						}
						continue
					}
					if fk == "kind" && k == "create" && fv != "individual" && fv != "group" {
						return fmt.Errorf("jmap: contact: kind %v is not allowed", fv)
					}
					if slices.Contains(cardFields, fk) {
						continue
					}
					if k == "create" && slices.Contains([]string{"@type", "version", "kind", "uid", "addressBookIds"}, fk) {
						continue
					}
					return fmt.Errorf("jmap: contact: %s is not allowed", fk)
				}
			}
		default:
			return fmt.Errorf("jmap: contact: %s is not allowed", k)
		}
	}
	if n != 1 {
		return fmt.Errorf("jmap: contact: exactly one create, update or destroy")
	}
	return nil
}

// checkMailbox: Mailbox/set creates or renames one folder: only its name
// and parent, never a role, rights or subscription, and never a destroy.
func checkMailbox(call Call) error {
	n := 0
	for k, v := range call.Args {
		switch k {
		case "accountId":
		case "create", "update":
			m, _ := v.(map[string]any)
			for _, x := range m {
				n++
				f, _ := x.(map[string]any)
				if len(f) == 0 {
					return fmt.Errorf("jmap: folder: nothing to set")
				}
				for k := range f {
					if k != "name" && k != "parentId" {
						return fmt.Errorf("jmap: folder: %s is not allowed", k)
					}
				}
			}
		default:
			return fmt.Errorf("jmap: folder: %s is not allowed", k)
		}
	}
	if n != 1 {
		return fmt.Errorf("jmap: folder: exactly one create or update")
	}
	return nil
}

// vacationFields: what an out-of-office update may set (plain text only).
var vacationFields = []string{"isEnabled", "fromDate", "toDate", "subject", "textBody", "htmlBody"}

// checkVacation: VacationResponse/set only with Call.Vacation: an update
// of the singleton, the known fields, never HTML.
func checkVacation(call Call) error {
	if !call.Vacation {
		return fmt.Errorf("jmap: VacationResponse/set is not allowed outside out_of_office_set")
	}
	for k := range call.Args {
		if k != "accountId" && k != "update" {
			return fmt.Errorf("jmap: out-of-office: %s is not allowed", k)
		}
	}
	up, _ := call.Args["update"].(map[string]any)
	patch, _ := up["singleton"].(map[string]any)
	if len(up) != 1 || len(patch) == 0 {
		return fmt.Errorf("jmap: out-of-office: only an update of the singleton")
	}
	for k, v := range patch {
		if !slices.Contains(vacationFields, k) {
			return fmt.Errorf("jmap: out-of-office: %s can't be set", k)
		}
		if k == "htmlBody" && v != nil {
			return fmt.Errorf("jmap: out-of-office: plain text only")
		}
	}
	return nil
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
	APIURL          string             `json:"apiUrl"`
	DownloadURL     string             `json:"downloadUrl"`
	UploadURL       string             `json:"uploadUrl"`
	Capabilities    map[string]any     `json:"capabilities"`
	PrimaryAccounts map[string]string  `json:"primaryAccounts"`
	Accounts        map[string]Account `json:"accounts"`
}

// Account: one account the person may use (their own, or a shared one).
type Account struct {
	Name                string         `json:"name"`
	IsPersonal          bool           `json:"isPersonal"`
	IsReadOnly          bool           `json:"isReadOnly"`
	AccountCapabilities map[string]any `json:"accountCapabilities"`
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
	// attendees each only at their own email (calendarAddress mailto:), and sendSchedulingMessages, so
	// the mail server adds the person as organizer and sends the invitations.
	Invite bool
	// Reply: the only update of a participant, for event_respond after the
	// person confirmed: one event, only participants/<id>/participationStatus,
	// with sendSchedulingMessages (the mail server mails the organizer).
	Reply bool
	// Reschedule: an update of the person's own meeting after they
	// confirmed: one event, only time, title, place and text (never
	// participants), with sendSchedulingMessages (attendees get the update).
	Reschedule bool
	// Cancel: the only destroy of an event, for event_cancel after the
	// person confirmed: exactly one id, nothing else.
	Cancel bool
	// Import: an invitation received by mail into the calendar, for
	// event_import after the person confirmed: one create, the event as
	// the organizer wrote it (participants and organizer included), and
	// never sendSchedulingMessages: importing mails no one.
	Import bool
	// DeleteContact: the only destroy of a contact card, for contact_delete
	// after the person confirmed: exactly one id, nothing else.
	DeleteContact bool
	// Vacation: the out-of-office reply, for out_of_office_set (switching it
	// on only after the person confirmed the dates and text).
	Vacation bool
}

// overrideFields: what an override of one meeting of a series may carry.
var overrideFields = []string{"start", "duration", "title", "locations", "description", "showWithoutTime", "excluded"}

// checkOverrides: recurrenceOverrides only as the whole object; each key a
// local date-time (the meeting's original start), each override only
// overrideFields, excluded only true. Never participants or the rule.
func checkOverrides(v any) error {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("jmap: recurrenceOverrides: an object")
	}
	for k, o := range m {
		if _, err := time.Parse("2006-01-02T15:04:05", k); err != nil {
			return fmt.Errorf("jmap: recurrenceOverrides: %q isn't a date-time", k)
		}
		f, ok := o.(map[string]any)
		if !ok || len(f) == 0 {
			return fmt.Errorf("jmap: recurrenceOverrides: %s: an object", k)
		}
		for fk, fv := range f {
			if !slices.Contains(overrideFields, fk) {
				return fmt.Errorf("jmap: recurrenceOverrides: %s can't be changed", fk)
			}
			if fk == "excluded" && fv != true {
				return fmt.Errorf("jmap: recurrenceOverrides: excluded only true")
			}
		}
	}
	return nil
}

// rescheduleFields: what Reschedule may change.
var rescheduleFields = []string{"title", "start", "duration", "timeZone", "showWithoutTime", "locations", "description", "recurrenceOverrides"}

var answers = []string{"accepted", "declined", "tentative"}

// importFields: what an imported invitation may carry.
var importFields = []string{"@type", "uid", "title", "description", "start", "timeZone", "duration", "showWithoutTime",
	"locations", "virtualLocations", "status", "sequence", "organizerCalendarAddress", "participants", "recurrenceRules",
	"excludedRecurrenceRules", "recurrenceOverrides", "calendarIds"}

// checkImport: see Call.Import.
func checkImport(call Call) error {
	for k, v := range call.Args {
		switch k {
		case "accountId", "create":
		case "sendSchedulingMessages":
			if v != false {
				return fmt.Errorf("jmap: import: mails no one")
			}
		default:
			return fmt.Errorf("jmap: import: %s is not allowed", k)
		}
	}
	c, _ := call.Args["create"].(map[string]any)
	if len(c) != 1 {
		return fmt.Errorf("jmap: import: exactly one event")
	}
	for _, ev := range c {
		e, _ := ev.(map[string]any)
		if e["organizerCalendarAddress"] == nil {
			return fmt.Errorf("jmap: import: an invitation has an organizer")
		}
		for f := range e {
			if !slices.Contains(importFields, f) {
				return fmt.Errorf("jmap: import: %s is not taken over", f)
			}
		}
	}
	return nil
}

// checkCalendarChange: see Call.Reply, Call.Reschedule, Call.Cancel.
func checkCalendarChange(call Call) error {
	n := 0
	for _, f := range []bool{call.Invite, call.Reply, call.Reschedule, call.Cancel} {
		if f {
			n++
		}
	}
	if n > 1 {
		return fmt.Errorf("jmap: one kind of calendar change per call")
	}
	args := []string{"accountId", "sendSchedulingMessages"}
	switch {
	case call.Cancel:
		args = append(args, "destroy")
	default:
		args = append(args, "update")
	}
	for k := range call.Args {
		if !slices.Contains(args, k) {
			return fmt.Errorf("jmap: %s is not allowed here", k)
		}
	}
	if call.Cancel {
		ids, _ := call.Args["destroy"].([]string)
		if len(ids) != 1 || ids[0] == "" {
			return fmt.Errorf("jmap: cancel: exactly one event")
		}
		if v, ok := call.Args["sendSchedulingMessages"]; ok {
			if _, isBool := v.(bool); !isBool {
				return fmt.Errorf("jmap: cancel: sendSchedulingMessages must be true or false")
			}
		}
		return nil
	}
	if call.Args["sendSchedulingMessages"] != true {
		return fmt.Errorf("jmap: without sendSchedulingMessages")
	}
	up, _ := call.Args["update"].(map[string]any)
	if len(up) != 1 {
		return fmt.Errorf("jmap: exactly one event")
	}
	for _, pv := range up {
		patch, _ := pv.(map[string]any)
		if len(patch) == 0 {
			return fmt.Errorf("jmap: nothing to change")
		}
		for k, v := range patch {
			if call.Reply {
				id, ok := strings.CutPrefix(k, "participants/")
				id, ok2 := strings.CutSuffix(id, "/participationStatus")
				if !ok || !ok2 || id == "" || strings.Contains(id, "/") || len(patch) != 1 {
					return fmt.Errorf("jmap: reply: only one participationStatus")
				}
				if a, _ := v.(string); !slices.Contains(answers, a) {
					return fmt.Errorf("jmap: reply: %v is not an answer", v)
				}
				continue
			}
			if !slices.Contains(rescheduleFields, k) {
				return fmt.Errorf("jmap: reschedule: %s can't be changed", k)
			}
			if k == "recurrenceOverrides" {
				if err := checkOverrides(v); err != nil {
					return err
				}
			}
		}
	}
	return nil
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
	if call.Method == "VacationResponse/set" || call.Vacation {
		if call.Method != "VacationResponse/set" {
			return fmt.Errorf("jmap: vacation flag on %s", call.Method)
		}
		return checkVacation(call)
	}
	if call.Method == "CalendarEvent/set" && call.Import {
		if call.Invite || call.Reply || call.Reschedule || call.Cancel {
			return fmt.Errorf("jmap: one kind of calendar change per call")
		}
		return checkImport(call)
	}
	if call.Method == "CalendarEvent/set" && (call.Reply || call.Reschedule || call.Cancel) {
		return checkCalendarChange(call)
	}
	if call.Reply || call.Reschedule || call.Cancel || call.Import {
		return fmt.Errorf("jmap: calendar change flag on %s", call.Method)
	}
	if call.Method == "Mailbox/set" {
		return checkMailbox(call)
	}
	if call.Method == "ContactCard/set" {
		return checkCard(call)
	}
	if call.DeleteContact {
		return fmt.Errorf("jmap: contact delete flag on %s", call.Method)
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
				for f, v := range e {
					if f == "participants" || strings.HasPrefix(f, "participants/") || f == "replyTo" {
						return fmt.Errorf("jmap: events with participants are not allowed (they send invitations)")
					}
					if strings.HasPrefix(f, "recurrenceOverrides/") {
						return fmt.Errorf("jmap: recurrenceOverrides only as a whole")
					}
					if f == "recurrenceOverrides" {
						if err := checkOverrides(v); err != nil {
							return err
						}
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
			if _, ok := p["calendarAddress"]; !ok {
				return fmt.Errorf("jmap: invite: an attendee without calendarAddress gets no invitation")
			}
			for k, v := range p {
				switch k {
				case "@type", "email", "name", "participationStatus", "expectReply":
				case "roles":
					if r, _ := v.(map[string]bool); len(r) != 1 || !r["attendee"] {
						return fmt.Errorf("jmap: invite: participants may only be attendees")
					}
				case "calendarAddress":
					// JSCalendar-bis: the address the mail server schedules with
					// (it becomes the iCalendar ATTENDEE; sendTo is ignored).
					if a, _ := v.(string); a != "mailto:"+fmt.Sprint(p["email"]) {
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
	names := make([]string, len(calls))
	for i, call := range calls {
		names[i] = call.Method
	}
	audit.Methods(ctx, "jmap", names)
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
