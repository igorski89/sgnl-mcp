// Package jmap: a minimal, read-only JMAP client (RFC 8620, 8621; JMAP for
// Calendars). Only the methods in readOnly can be sent.
package jmap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

const (
	Core      = "urn:ietf:params:jmap:core"
	Mail      = "urn:ietf:params:jmap:mail"
	Calendars = "urn:ietf:params:jmap:calendars"
)

// The gateway is read-only in this version: nothing else can be sent.
var readOnly = []string{
	"Mailbox/get", "Mailbox/query",
	"Email/get", "Email/query",
	"Calendar/get", "CalendarEvent/get", "CalendarEvent/query",
}

// Session: the parts of the JMAP session object we use.
type Session struct {
	APIURL          string            `json:"apiUrl"`
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
}

// Invoke sends calls in one request and returns each response's
// arguments by call index. A method error fails the whole request.
func (c *Client) Invoke(ctx context.Context, token string, s *Session, using []string, calls ...Call) ([]json.RawMessage, error) {
	mc := make([]any, len(calls))
	for i, call := range calls {
		if !slices.Contains(readOnly, call.Method) {
			return nil, fmt.Errorf("jmap: %s is not allowed (read-only)", call.Method)
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
		var i int
		if _, err := fmt.Sscan(id, &i); err == nil && i >= 0 && i < len(out) {
			out[i] = mr[1]
		}
	}
	return out, nil
}

// Ref: a back-reference to an earlier call's result (by index).
func Ref(call int, method, path string) map[string]any {
	return map[string]any{"resultOf": fmt.Sprint(call), "name": method, "path": path}
}
