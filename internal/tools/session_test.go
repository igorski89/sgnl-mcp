package tools

import (
	"context"
	"fmt"
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

// A client refreshes its token while one MCP session goes on: tools must
// see the token of the current request, not the one the session opened
// with (that one expires; the token exchange then fails).
func TestToolsSeeTheCurrentRequestsToken(t *testing.T) {
	verify := func(_ context.Context, tok string, _ *http.Request) (*mcpauth.TokenInfo, error) {
		return auth.TokenInfo(tok, auth.Claims{Sub: "same-person", Username: "u"}, time.Now().Add(time.Hour)), nil
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	type out struct {
		Token string `json:"token"`
	}
	add(&Tools{Access: Access{All: true}}, s, &mcp.Tool{Name: "token"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, out, error) {
		return nil, out{auth.Token(ctx)}, nil
	})
	h := mcpauth.RequireBearerToken(verify, nil)(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil))
	srv := httptest.NewServer(h)
	defer srv.Close()

	var current atomic.Value
	current.Store("token-A")
	hc := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+current.Load().(string))
		return http.DefaultTransport.RoundTrip(r)
	})}
	ctx := context.Background()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).
		Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: hc}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	for _, tok := range []string{"token-A", "token-B"} {
		current.Store(tok)
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "token"})
		if err != nil {
			t.Fatal(err)
		}
		got := fmt.Sprint(res.StructuredContent)
		if want := fmt.Sprint(map[string]any{"token": tok}); got != want {
			t.Errorf("tool saw %s, want %s", got, want)
		}
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Access follows the person as the login server has them now: someone
// removed from the group is refused though their login still lists it;
// someone just added is let in; without a current view, refused.
func TestAccessUsesCurrentGroups(t *testing.T) {
	login := auth.Claims{Sub: "s", Username: "u", Groups: []string{"mcp_users"}}
	removed, added := login, auth.Claims{Sub: "s", Username: "u", Groups: []string{"mcp_users"}}
	removed.Groups = []string{"sgnl_users"}
	for name, c := range map[string]struct {
		ti   *mcpauth.TokenInfo
		want string
	}{
		"removed":   {auth.TokenInfoNow("t", login, removed, time.Now().Add(time.Hour)), "isn't switched on"},
		"added":     {auth.TokenInfoNow("t", auth.Claims{Sub: "s", Username: "u"}, added, time.Now().Add(time.Hour)), ""},
		"unchecked": {auth.TokenInfo("t", login, time.Now().Add(time.Hour)), "couldn't check"},
	} {
		s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
		add(&Tools{Access: Access{Groups: []string{"mcp_users"}}}, s, &mcp.Tool{Name: "x"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, struct{}, error) {
			return nil, struct{}{}, nil
		})
		ti := c.ti
		h := mcpauth.RequireBearerToken(func(context.Context, string, *http.Request) (*mcpauth.TokenInfo, error) { return ti, nil }, nil)(
			mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil))
		srv := httptest.NewServer(h)
		hc := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			r = r.Clone(r.Context())
			r.Header.Set("Authorization", "Bearer t")
			return http.DefaultTransport.RoundTrip(r)
		})}
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).
			Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: hc}, nil)
		if err != nil {
			t.Fatal(err)
		}
		r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "x"})
		cs.Close()
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		got := resultText(r)
		if c.want == "" && r.IsError || c.want != "" && !strings.Contains(got, c.want) {
			t.Errorf("%s: %v %q", name, r.IsError, got)
		}
	}
}
