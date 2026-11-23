package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	add(s, &mcp.Tool{Name: "token"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, out, error) {
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
