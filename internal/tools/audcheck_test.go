package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/igorski89/sgnl-mcp/internal/jmap"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A probed app that accepts one of the gateway's tokens is flagged; one
// that redirects or refuses is fine.
func TestAudCheckProbes(t *testing.T) {
	app := func(accept bool) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if accept && r.Header.Get("Authorization") == "Bearer tok" {
				w.WriteHeader(200)
				return
			}
			http.Redirect(w, r, "/login", http.StatusFound)
		}))
	}
	leaky, safe := app(true), app(false)
	defer leaky.Close()
	defer safe.Close()
	tl := &Tools{Auth: fakeTokens{}, JMAP: jmap.New(safe.URL + "/session"), Access: Access{All: true},
		Probes: map[string]string{"leaky": leaky.URL + "/api", "safe": safe.URL + "/api"}}
	_, out, err := tl.audCheck(context.Background(), &mcp.CallToolRequest{}, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, p := range out.Probes {
		got[p.App] = p.OK
	}
	if got["leaky"] || !got["safe"] || len(got) != 2 {
		t.Fatalf("%+v", out.Probes)
	}
}
