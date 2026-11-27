package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
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
var asked = struct {
	sync.Mutex
	aud    []string
	groups []string // the user store's groups now (nil: as in the login)
}{}

func fakeIssuer(t *testing.T) (g *Gateway, n *atomic.Int32, revoke func(string)) {
	n = new(atomic.Int32)
	var mu sync.Mutex
	revoked := map[string]bool{}
	asked.Lock()
	asked.aud, asked.groups = nil, nil
	asked.Unlock()
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
		sub.Aud = Audience{"issuer"}
		if a := r.Form.Get("audience"); a != "" {
			sub.Aud = Audience{a}
		}
		asked.Lock()
		asked.aud = append(asked.aud, r.Form.Get("audience"))
		if asked.groups != nil {
			sub.Groups = asked.groups
		}
		asked.Unlock()
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
			if _, err := g.FilesToken(as(a)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n.Load() != 1 {
		t.Fatalf("concurrent calls with one login token: %d exchanges, want 1", n.Load())
	}
	if _, err := g.MailToken(as(b)); err != nil || n.Load() != 2 {
		t.Fatalf("another login token of the same person must be exchanged itself: %v, %d", err, n.Load())
	}
	g.now = func() time.Time { return time.Now().Add(5 * time.Minute) }
	if _, err := g.MailToken(as(a)); err != nil || n.Load() != 3 {
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
	if _, err := g.MailToken(as(old)); !errors.Is(err, ErrAppTokenRefused) {
		t.Fatalf("a tool with the revoked login: %v", err)
	}
	r := httptest.NewRequest("POST", "/mcp", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if c := w.Header().Get("WWW-Authenticate"); w.Code != 401 || strings.Contains(c, "error=") {
		t.Fatalf("no token at all: %d %q (no error code, RFC 6750 §3.1)", w.Code, c)
	}
}

// Mail and files each get a token for their own audience; the login check
// asks for none (an app's policy mustn't turn into a 401 for the login).
func TestPerAppAudience(t *testing.T) {
	g, n, _ := fakeIssuer(t)
	g.cfg.MailAudience, g.cfg.FilesAudience = "stalwart", "opencloud"
	tok := login(1)
	verifier := func(ctx context.Context, tok string, _ *http.Request) (*mcpauth.TokenInfo, error) {
		return g.verified(ctx, tok, PeekClaims(tok), time.Now().Add(time.Minute))
	}
	if _, err := verifier(context.Background(), tok, nil); err != nil {
		t.Fatal(err)
	}
	m, err := g.MailToken(as(tok))
	if err != nil || !slices.Equal(PeekClaims(m).Aud, Audience{"stalwart"}) {
		t.Fatalf("mail: %v %v", err, PeekClaims(m).Aud)
	}
	f, err := g.FilesToken(as(tok))
	if err != nil || !slices.Equal(PeekClaims(f).Aud, Audience{"opencloud"}) {
		t.Fatalf("files: %v %v", err, PeekClaims(f).Aud)
	}
	if _, err := g.NewsletterToken(as(tok)); err == nil || !strings.Contains(err.Error(), "isn't connected") {
		t.Fatalf("newsletter without its audience: %v", err)
	}
	g.cfg.NewsletterAudience = "listmonk"
	if nl, err := g.NewsletterToken(as(tok)); err != nil || !slices.Equal(PeekClaims(nl).Aud, Audience{"listmonk"}) {
		t.Fatalf("newsletter: %v %v", err, PeekClaims(nl).Aud)
	}
	g.MailToken(as(tok))
	asked.Lock()
	defer asked.Unlock()
	if n.Load() != 4 || !slices.Equal(asked.aud, []string{"", "stalwart", "opencloud", "listmonk"}) {
		t.Fatalf("%d exchanges, audiences %q", n.Load(), asked.aud)
	}
}

func TestPolicyDeniedMessages(t *testing.T) {
	g := &Gateway{cfg: Config{MailAudience: "stalwart-webui", FilesAudience: "opencloud"}}
	deny := "The request is otherwise malformed. The authorization policy of the audience client 'opencloud' does not permit this user."
	if err := g.policyDenied(deny, "opencloud"); err == nil || !strings.Contains(err.Error(), "don't have access to files") || loginEnded("invalid_grant", deny) && errors.Is(err, ErrAppTokenRefused) {
		t.Fatalf("policy denial: %v", err)
	}
	mfa := "The authentication level of the 'subject_token' is insufficient for the authorization policy of the audience client 'stalwart-webui'."
	if err := g.policyDenied(mfa, "stalwart-webui"); err == nil || !strings.Contains(err.Error(), "second factor") || !strings.Contains(err.Error(), "mail") {
		t.Fatalf("2FA: %v", err)
	}
	if g.policyDenied("... Token is not valid or has expired.", "opencloud") != nil {
		t.Fatal("an ended login is not a policy denial")
	}
}

// Removing someone from a group takes effect at the next login check after
// the cached exchange runs out (minutes), though their login token (and its
// refreshes) still lists the group.
func TestCurrentGroupsFollowTheUserStore(t *testing.T) {
	g, _, _ := fakeIssuer(t)
	tok := login(1) // lists group "g"
	check := func() (Claims, bool) {
		ti, err := g.verified(context.Background(), tok, PeekClaims(tok), time.Now().Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		return Current(WithRequest(context.Background(), ti))
	}
	if c, ok := check(); !ok || !slices.Contains(c.Groups, "g") {
		t.Fatalf("before: %v %v", ok, c.Groups)
	}
	asked.Lock()
	asked.groups = []string{"other"} // removed from "g" at the login server
	asked.Unlock()
	if c, _ := check(); !slices.Contains(c.Groups, "g") {
		t.Fatalf("within the cache time the cached answer stands: %v", c.Groups)
	}
	g.now = func() time.Time { return time.Now().Add(5 * time.Minute) }
	if c, ok := check(); !ok || slices.Contains(c.Groups, "g") {
		t.Fatalf("after the cache time the removal must show: %v %v", ok, c.Groups)
	}
}

// If the login check couldn't ask the issuer, there is no current view.
func TestCurrentUnknownWithoutExchange(t *testing.T) {
	if _, ok := Current(as(login(1))); ok {
		t.Fatal("no exchange, yet a current view")
	}
}

// A token for the normal endpoint can't be used at the admin endpoint: that
// one only takes tokens whose aud names it.
func TestAdminEndpointNeedsItsOwnAudience(t *testing.T) {
	g, _, _ := fakeIssuer(t)
	tok := login(1) // aud [gw]
	if _, err := g.verifiedFor(context.Background(), tok, PeekClaims(tok), time.Now().Add(time.Minute), []string{"https://x/admin/mcp"}); err == nil {
		t.Fatal("a normal token was accepted at the admin endpoint")
	}
	c := PeekClaims(tok)
	c.Aud = Audience{"gw", "https://x/admin/mcp"}
	tok = jwt(c)
	if _, err := g.verifiedFor(context.Background(), tok, PeekClaims(tok), time.Now().Add(time.Minute), []string{"https://x/admin/mcp"}); err != nil {
		t.Fatal(err)
	}
}
