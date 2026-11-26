package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorski89/sgnl-mcp/internal/admin"
	"github.com/igorski89/sgnl-mcp/internal/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestLogoutEverywhereAsksAndOnlyTheCaller(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/token" {
			json.NewEncoder(w).Encode(map[string]any{"access_token": "x", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		calls = append(calls, r.Method+" "+r.URL.Path)
		json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer srv.Close()
	c := func(scope string) *admin.Client {
		return &admin.Client{Base: srv.URL + "/api/admin", TokenURL: srv.URL + "/token", ID: "c", Secret: "s", Scope: scope, HTTP: srv.Client(), Now: time.Now}
	}
	self := admin.SelfForTest(c("authelia.admin.read"), c("authelia.admin.read authelia.admin.write"),
		func(context.Context) (auth.Claims, bool) { return auth.Claims{Username: "hugo"}, true })
	for _, answer := range []string{"decline", "accept"} {
		var asked []string
		s := mcp.NewServer(&mcp.Implementation{Name: "t"}, nil)
		(&Tools{Auth: fakeTokens{}, Access: Access{All: true}, Self: self}).Register(s)
		ct, st := mcp.NewInMemoryTransports()
		if _, err := s.Connect(context.Background(), st, nil); err != nil {
			t.Fatal(err)
		}
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "c"}, &mcp.ClientOptions{ElicitationHandler: func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			asked = append(asked, r.Params.Message)
			return &mcp.ElicitResult{Action: answer, Content: map[string]any{}}, nil
		}}).Connect(context.Background(), ct, nil)
		if err != nil {
			t.Fatal(err)
		}
		// Any username given is ignored: the tool takes none.
		txt, isErr := callTool(t, cs, "logout_everywhere", map[string]any{})
		if len(asked) != 1 || !strings.HasPrefix(asked[0], "Log hugo out EVERYWHERE now?") || !strings.Contains(asked[0], "Forgejo git tokens") {
			t.Errorf("asked: %v", asked)
		}
		mu.Lock()
		n := 0
		for _, x := range calls {
			if strings.HasSuffix(x, "/logout") {
				n++
				if x != "POST /api/admin/users/hugo/logout" {
					t.Errorf("logout of: %s", x)
				}
			}
		}
		mu.Unlock()
		if answer == "decline" && (n != 0 || !isErr) {
			t.Errorf("declined but logged out (%d): %s", n, txt)
		}
		if answer == "accept" && (n != 1 || isErr) {
			t.Errorf("accepted: %d logouts: %s", n, txt)
		}
		cs.Close()
	}
}
