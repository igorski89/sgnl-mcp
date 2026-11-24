// Package admin: the gateway's admin endpoint (/admin/mcp), for the
// instance's admins. It reads the login server's admin API with the
// gateway's own admin client (client credentials): the first master
// credential the gateway holds, so it is read-only (scope
// authelia.admin.read) and every call is logged with the acting person.
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	tok, err := c.token(ctx)
	if err != nil {
		return err
	}
	u := c.Base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	switch {
	case resp.StatusCode == 404:
		return errNotFound
	case resp.StatusCode != 200:
		var e struct {
			Message string `json:"message"`
			Desc    string `json:"error_description"`
		}
		_ = json.Unmarshal(body, &e)
		return fmt.Errorf("the login server's admin API answered %d %s%s", resp.StatusCode, e.Message, e.Desc)
	}
	return json.Unmarshal(body, out)
}

var errNotFound = fmt.Errorf("not found")
