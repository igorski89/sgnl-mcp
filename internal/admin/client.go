// Package admin: the gateway's admin endpoint (/admin/mcp), for the
// instance's admins. It reads the login server's admin API with the
// gateway's own admin client (client credentials, scope
// authelia.admin.read). Changes to people and groups go through a second,
// optional client (scopes .read and .write), only the few bin/sgnl-id
// makes, each confirmed by the admin. Every call is logged with the
// acting person.
package admin

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
	"strings"
	"sync"
	"time"
)

// Client: the login server's admin API (<issuer>/api/admin).
type Client struct {
	Base     string // <issuer>/api/admin
	TokenURL string
	ID       string
	Secret   string
	Scope    string // authelia.admin.read
	HTTP     *http.Client
	Now      func() time.Time

	mu  sync.Mutex
	tok string
	exp time.Time
}

func (c *Client) token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tok != "" && c.Now().Before(c.exp) {
		return c.tok, nil
	}
	f := url.Values{"grant_type": {"client_credentials"}, "scope": {c.Scope}, "audience": {c.Base}}
	req, _ := http.NewRequestWithContext(ctx, "POST", c.TokenURL, strings.NewReader(f.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(url.QueryEscape(c.ID), url.QueryEscape(c.Secret))
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var r struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
		Desc        string `json:"error_description"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r)
	if resp.StatusCode != 200 || r.AccessToken == "" {
		return "", fmt.Errorf("the gateway's admin client couldn't get a token: %d %s %s", resp.StatusCode, r.Error, r.Desc)
	}
	c.tok, c.exp = r.AccessToken, c.Now().Add(time.Duration(max(r.ExpiresIn-30, 0))*time.Second)
	return c.tok, nil
}

// get: GET <Base><path>?q into out.
func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	return c.do(ctx, "GET", path, q, nil, out)
}

// The only changes the gateway makes, as bin/sgnl-id does them: never
// deleting a person, never credentials (passkeys, lockouts), never a
// path the tools don't name. The login server's admin API can do more.
var writes = []struct {
	method string
	path   *regexp.Regexp
	fields []string // PATCH: the only fields it may carry
}{
	{"POST", regexp.MustCompile(`^/users$`), nil},
	{"POST", regexp.MustCompile(`^/users/[^/]+/invite$`), nil},
	{"POST", regexp.MustCompile(`^/users/[^/]+/logout$`), nil},
	{"PATCH", regexp.MustCompile(`^/users/[^/]+$`), []string{"email", "disabled"}},
	{"PUT", regexp.MustCompile(`^/users/[^/]+/groups/[^/]+$`), nil},
	{"DELETE", regexp.MustCompile(`^/users/[^/]+/groups/[^/]+$`), nil},
	{"POST", regexp.MustCompile(`^/groups$`), nil},
}

// allowed: whether method path (escaped) with body is one of the writes.
func allowed(method, path string, body map[string]any) bool {
	if method == "GET" {
		return true
	}
	for _, w := range writes {
		if w.method != method || !w.path.MatchString(path) {
			continue
		}
		if method == "PATCH" {
			if len(body) != 1 {
				return false
			}
			for k := range body {
				if !slices.Contains(w.fields, k) {
					return false
				}
			}
		}
		return true
	}
	return false
}

// send: a change (one of the writes), body as JSON, the answer into out
// (nil: ignored). ok404: a missing target isn't an error (DELETE of a
// membership that's already gone).
func (c *Client) send(ctx context.Context, method, path string, body map[string]any, out any, ok404 bool) error {
	if !allowed(method, path, body) {
		return fmt.Errorf("the gateway doesn't make this change: %s %s", method, path)
	}
	err := c.do(ctx, method, path, nil, body, out)
	if ok404 && errors.Is(err, errNotFound) {
		return nil
	}
	return err
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body map[string]any, out any) error {
	tok, err := c.token(ctx)
	if err != nil {
		return err
	}
	u := c.Base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(ctx, method, u, rd)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	switch {
	case resp.StatusCode == 404:
		return errNotFound
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		var e struct {
			Message string `json:"message"`
			Desc    string `json:"error_description"`
		}
		_ = json.Unmarshal(data, &e)
		return fmt.Errorf("the login server's admin API answered %d %s%s", resp.StatusCode, e.Message, e.Desc)
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

var errNotFound = fmt.Errorf("not found")
