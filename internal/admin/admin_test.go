package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/igorski89/sgnl-mcp/internal/auth"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeIssuer: a token endpoint (client credentials) and the admin API.
func fakeIssuer(t *testing.T) (*Client, *atomic.Int32) {
	var tokens atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		id, secret, _ := r.BasicAuth()
		if id != "gw-admin" || secret != "s" || r.Form.Get("grant_type") != "client_credentials" ||
			r.Form.Get("scope") != "authelia.admin.read" || r.Form.Get("audience") != "http://"+r.Host+"/api/admin" {
			w.WriteHeader(401)
			return
		}
		tokens.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"access_token": "admintok", "expires_in": 300})
	})
	api := func(path string, v any) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer admintok" || r.Method != "GET" {
				w.WriteHeader(403)
				return
			}
			json.NewEncoder(w).Encode(v)
		})
	}
	api("/api/admin/users", map[string]any{"users": []map[string]any{
		{"username": "igor", "email": "i@co.example", "groups": []string{"admins", "sgnl_users"}},
		{"username": "hugo", "email": "h@co.example", "groups": []string{"sgnl_users"}, "disabled": false}}})
	api("/api/admin/users/hugo", map[string]any{"username": "hugo", "groups": []string{"sgnl_users"},
		"credentials": []string{"webauthn", "password"}, "sessions": 2})
	api("/api/admin/groups", map[string]any{"groups": []map[string]any{{"name": "admins"}, {"name": "sgnl_users"}, {"name": "empty"}}})
	api("/api/admin/events", map[string]any{"events": []map[string]any{{"username": "hugo", "type": "1FA", "successful": false}}})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &Client{Base: srv.URL + "/api/admin", TokenURL: srv.URL + "/token", ID: "gw-admin", Secret: "s",
		Scope: "authelia.admin.read", HTTP: srv.Client(), Now: time.Now}, &tokens
}

// session: an MCP client connected to the admin tools as a person whose
// current groups (at the login server) are cur; nil: not checkable.
func session(t *testing.T, c *Client, cur *auth.Claims) *mcp.ClientSession {
	t.Helper()
	s := mcp.NewServer(&mcp.Implementation{Name: "a", Version: "0"}, nil)
	(&Tools{API: c}).Register(s)
	login := auth.Claims{Sub: "s", Username: "u", Groups: []string{"admins"}} // the login still says admin
	verify := func(context.Context, string, *http.Request) (*mcpauth.TokenInfo, error) {
		if cur == nil {
			return auth.TokenInfo("t", login, time.Now().Add(time.Hour)), nil
		}
		return auth.TokenInfoNow("t", login, *cur, time.Now().Add(time.Hour)), nil
	}
	srv := httptest.NewServer(mcpauth.RequireBearerToken(verify, nil)(
		mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)))
	t.Cleanup(srv.Close)
	hc := &http.Client{Transport: rt(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer t")
		return http.DefaultTransport.RoundTrip(r)
	})}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: hc}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

type rt func(*http.Request) (*http.Response, error)

func (f rt) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, c := range r.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), r.IsError
}

func TestAdminsOnlyByCurrentGroups(t *testing.T) {
	c, tokens := fakeIssuer(t)
	// Removed from admins at the login server, though the login says admin.
	if txt, isErr := call(t, session(t, c, &auth.Claims{Sub: "s", Username: "u", Groups: []string{"sgnl_users"}}), "admin_users", nil); !isErr || !strings.Contains(txt, "admins only") {
		t.Fatalf("non-admin: %q", txt)
	}
	if txt, isErr := call(t, session(t, c, nil), "admin_users", nil); !isErr || !strings.Contains(txt, "couldn't check") {
		t.Fatalf("unchecked: %q", txt)
	}
	if tokens.Load() != 0 {
		t.Fatal("the admin API was called for a refused person")
	}
}

func TestAdminReads(t *testing.T) {
	c, tokens := fakeIssuer(t)
	cs := session(t, c, &auth.Claims{Sub: "s", Username: "u", Groups: []string{"admins"}})
	if txt, isErr := call(t, cs, "admin_users", nil); isErr || !strings.Contains(txt, `"count":2`) || !strings.Contains(txt, "hugo") {
		t.Fatalf("users: %q", txt)
	}
	if txt, isErr := call(t, cs, "admin_user", map[string]any{"username": "hugo"}); isErr || !strings.Contains(txt, `"sessions":2`) || !strings.Contains(txt, `"credentials":["password","webauthn"]`) {
		t.Fatalf("user: %q", txt)
	}
	if txt, isErr := call(t, cs, "admin_user", map[string]any{"username": "nobody"}); !isErr || !strings.Contains(txt, "no account") {
		t.Fatalf("missing user: %q", txt)
	}
	if txt, isErr := call(t, cs, "admin_groups", nil); isErr || !strings.Contains(txt, `{"members":["igor"],"name":"admins"}`) || !strings.Contains(txt, `{"members":[],"name":"empty"}`) {
		t.Fatalf("groups: %q", txt)
	}
	if txt, isErr := call(t, cs, "admin_events", map[string]any{"username": "hugo"}); isErr || !strings.Contains(txt, `"successful":false`) {
		t.Fatalf("events: %q", txt)
	}
	if tokens.Load() != 1 {
		t.Fatalf("the admin token should be reused: %d", tokens.Load())
	}
}
