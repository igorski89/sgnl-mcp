package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
)

func jwt(c Claims) string {
	b, _ := json.Marshal(c)
	return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
}

// fakeIssuer: a token endpoint that exchanges any subject token except
// those revoked, counting exchanges.
func fakeIssuer(t *testing.T) (g *Gateway, n *atomic.Int32, revoke func(string)) {
	n = new(atomic.Int32)
	var mu sync.Mutex
	revoked := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		bad := revoked[r.Form.Get("subject_token")]
		mu.Unlock()
		if bad {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"invalid_request","error_description":"... Token is not valid or has expired."}`)
			return
		}
		n.Add(1)
		sub := PeekClaims(r.Form.Get("subject_token"))
		sub.ClientID, sub.Azp, sub.Exp = "gw", "", time.Now().Add(5*time.Minute).Unix()
		fmt.Fprintf(w, `{"access_token":%q}`, jwt(sub))
	}))
	t.Cleanup(srv.Close)
	g = &Gateway{cfg: Config{ClientID: "gw", Audiences: []string{"gw"}}, tokenURL: srv.URL, http: srv.Client(),
		cache: map[string]appTok{}, now: time.Now}
	return g, n, func(tok string) { mu.Lock(); revoked[tok] = true; mu.Unlock() }
}

func login(iat int64) string {
	return jwt(Claims{Sub: "s1", Azp: "cc", Aud: Audience{"gw"}, Username: "u", Email: "e", Name: "n",
		Groups: []string{"g"}, Iat: iat, Exp: time.Now().Add(5 * time.Minute).Unix()})
}

func as(tok string) context.Context {
	return WithRequest(context.Background(), TokenInfo(tok, PeekClaims(tok), time.Now().Add(time.Minute)))
}

func TestAppTokenPerLoginToken(t *testing.T) {
	g, n, _ := fakeIssuer(t)
	a, b := login(1), login(2)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := g.SameIdentityToken(as(a)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n.Load() != 1 {
		t.Fatalf("concurrent calls with one login token: %d exchanges, want 1", n.Load())
	}
	if _, err := g.AppToken(as(b)); err != nil || n.Load() != 2 {
		t.Fatalf("another login token of the same person must be exchanged itself: %v, %d", err, n.Load())
	}
	g.now = func() time.Time { return time.Now().Add(5 * time.Minute) }
	if _, err := g.AppToken(as(a)); err != nil || n.Load() != 3 {
		t.Fatalf("an expiring app token should be replaced: %v, %d", err, n.Load())
	}
}

// A revoked (or rotated-out) login token gets 401, even while the same
// person's newer login token has a warm cache.
func TestRevokedLoginTokenIs401(t *testing.T) {
	g, _, revoke := fakeIssuer(t)
	old, cur := login(1), login(2)
	revoke(old)
	verifier := func(ctx context.Context, tok string, _ *http.Request) (*mcpauth.TokenInfo, error) {
		return g.verified(ctx, tok, PeekClaims(tok), time.Now().Add(time.Minute))
	}
	var reached atomic.Int32
	h := Protect(mcpauth.RequireBearerToken(verifier, &mcpauth.RequireBearerTokenOptions{ResourceMetadataURL: "http://x/prm"}),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1) }))
	call := func(tok string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/mcp", nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := call(cur); w.Code != 200 || reached.Load() != 1 {
		t.Fatalf("current login: %d", w.Code)
	}
	w := call(old)
	if w.Code != 401 || reached.Load() != 1 {
		t.Fatalf("revoked login with a warm cache for the person: %d, reached tool %d times", w.Code, reached.Load())
	}
	if c := w.Header().Get("WWW-Authenticate"); !strings.Contains(c, `error="invalid_token"`) || !strings.Contains(c, "resource_metadata=") {
		t.Fatalf("challenge: %q", c)
	}
	if _, err := g.AppToken(as(old)); !errors.Is(err, ErrAppTokenRefused) {
		t.Fatalf("a tool with the revoked login: %v", err)
	}
	r := httptest.NewRequest("POST", "/mcp", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if c := w.Header().Get("WWW-Authenticate"); w.Code != 401 || strings.Contains(c, "error=") {
		t.Fatalf("no token at all: %d %q (no error code, RFC 6750 §3.1)", w.Code, c)
	}
}
