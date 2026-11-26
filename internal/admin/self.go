package admin

import (
	"context"
	"fmt"
	"net/url"
	"slices"

	"github.com/igorski89/sgnl-mcp/internal/auth"
)

// Self: the login server's admin API used for one thing only, the
// person's OWN account: how many sessions and their recent logins, and
// ending all their sessions. The username is never an input: it is who
// the login server says the caller is now (auth.Current), so no other
// account can be named.
type Self struct {
	Read  *Client // authelia.admin.read
	Write *Client // authelia.admin.write (nil: no logout)
}

func (s *Self) me(ctx context.Context) (string, error) {
	c, ok := auth.Current(ctx)
	if !ok || c.Username == "" {
		return "", fmt.Errorf("couldn't check with the login server who you are: try again in a moment")
	}
	if err := validName("username", c.Username); err != nil {
		return "", err
	}
	return c.Username, nil
}

// Login: one recent successful login.
type Login struct {
	Time     string `json:"time"`
	RemoteIP string `json:"from_ip,omitempty"`
	Step     string `json:"step" jsonschema:"password, or second factor (passkey)"`
}

type MySessions struct {
	Sessions *int    `json:"sessions" jsonschema:"login sessions open now (browsers and apps logged in with SGNL)"`
	Logins   []Login `json:"recent_logins" jsonschema:"successful logins, newest first"`
	Failed   int     `json:"recent_failed_logins" jsonschema:"failed attempts among the latest events"`
	Note     string  `json:"note"`
}

// Sessions: the caller's own session count and recent logins.
func (s *Self) Sessions(ctx context.Context) (MySessions, error) {
	me, err := s.me(ctx)
	if err != nil {
		return MySessions{}, err
	}
	var u apiUser
	if err := s.Read.get(ctx, "/users/"+esc(me), nil, &u); err != nil {
		return MySessions{}, err
	}
	var r struct {
		Events []Event `json:"events"`
	}
	if err := s.Read.get(ctx, "/events", url.Values{"username": {me}, "limit": {"30"}}, &r); err != nil {
		return MySessions{}, err
	}
	out := MySessions{Sessions: u.Sessions, Logins: []Login{},
		Note: "The login server counts sessions but doesn't list devices; recent logins show when and from which IP address. App passwords (mail apps) and app tokens aren't sessions and don't show here."}
	for _, e := range r.Events {
		if e.Username != me {
			continue // only ever the caller's own
		}
		if !e.Successful {
			out.Failed++
			continue
		}
		step := map[string]string{"1FA": "password", "2FA": "second factor (passkey)"}[e.Type]
		if step == "" {
			step = e.Type
		}
		if len(out.Logins) < 15 {
			out.Logins = append(out.Logins, Login{Time: e.Time, RemoteIP: e.RemoteIP, Step: step})
		}
	}
	slices.SortStableFunc(out.Logins, func(a, b Login) int {
		if a.Time > b.Time {
			return -1
		}
		if a.Time < b.Time {
			return 1
		}
		return 0
	})
	return out, nil
}

// LogoutEverywhere: ends every session and token of the caller's own
// account (this connection's too).
func (s *Self) LogoutEverywhere(ctx context.Context) error {
	if s.Write == nil {
		return fmt.Errorf("logging out everywhere isn't switched on here: use the login portal")
	}
	me, err := s.me(ctx)
	if err != nil {
		return err
	}
	return s.Write.send(ctx, "POST", "/users/"+esc(me)+"/logout", map[string]any{}, nil, false)
}

// Username: who the login server says the caller is now (for questions).
func (s *Self) Username(ctx context.Context) (string, error) { return s.me(ctx) }
