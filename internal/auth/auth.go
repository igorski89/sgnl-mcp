// Package auth: the gateway as an OAuth resource server. It accepts only
// access tokens from the configured issuer whose audience names the
// gateway, and trades each one (RFC 8693 token exchange) for a token of
// its own before calling an app: the person's token never reaches an app.
package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"golang.org/x/sync/singleflight"
)

// Claims: what the gateway reads from a token (and logs; never the token).
type Claims struct {
	Sub      string   `json:"sub"`
	Azp      string   `json:"azp"`
	ClientID string   `json:"client_id"` // RFC 9068 (Authelia); Keycloak uses azp
	Aud      Audience `json:"aud"`
	Scope    string   `json:"scope"` // Keycloak: space-separated
	Scp      []string `json:"scp"`   // RFC 9068 issuers (Authelia): a list
	Username string   `json:"preferred_username"`
	Name     string   `json:"name"`
	Email    string   `json:"email"`
	Groups   []string `json:"groups"`
	Iat      int64    `json:"iat"`
	Exp      int64    `json:"exp"`
}

// Scopes: from scp, else scope.
func (c Claims) Scopes() []string {
	if len(c.Scp) > 0 {
		return c.Scp
	}
	return strings.Fields(c.Scope)
}

// Client: the client the token was issued to: azp, else RFC 9068's client_id.
func (c Claims) Client() string {
	if c.Azp != "" {
		return c.Azp
	}
	return c.ClientID
}

// Audience: "aud" is a string or a list.
type Audience []string

func (a *Audience) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*a = Audience{one}
		return nil
	}
	var many []string
	err := json.Unmarshal(b, &many)
	*a = many
	return err
}

// LogValue: the claims worth logging.
func (c Claims) LogValue() slog.Value {
	return slog.GroupValue(slog.String("user", c.Username), slog.String("azp", c.Client()),
		slog.Any("aud", []string(c.Aud)), slog.Time("iat", time.Unix(c.Iat, 0)), slog.Duration("exp_in", time.Until(time.Unix(c.Exp, 0)).Round(time.Second)))
}

// PeekClaims reads a JWT's payload without checking it: only for tokens
// the gateway just got from the issuer itself.
func PeekClaims(tok string) (c Claims) {
	parts := strings.Split(tok, ".")
	if len(parts) == 3 {
		if b, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
			_ = json.Unmarshal(b, &c)
		}
	}
	return
}

type Config struct {
	Issuer       string // e.g. https://id.example.com/realms/example
	// Audiences: a person's token must carry one of these in "aud": the
	// gateway's client id (Keycloak's audience mapper) or its public URL
	// (RFC 8707 resource indicators, which Claude Code sends).
	Audiences    []string
	ClientID     string // the gateway's client, for the exchange
	ClientSecret string
	// Per-app audiences asked for in the exchange (the apps' client ids at
	// the issuer), so a token for one app is refused by the other. Empty:
	// the issuer's default audience.
	MailAudience  string // JMAP (mail, calendar, contacts)
	FilesAudience string // OpenCloud
}

type Gateway struct {
	cfg      Config
	verifier *oidc.IDTokenVerifier
	tokenURL string
	http     *http.Client

	// App tokens, per presented login token (its SHA-256), reused until
	// shortly before either expires; concurrent calls with the same login
	// token share one exchange. Bound to the login token, so a login the
	// issuer has revoked or rotated out never rides on another one's
	// cached app token.
	flight singleflight.Group
	mu     sync.Mutex
	cache  map[string]appTok
	now    func() time.Time
}

type appTok struct {
	tok   string
	c     Claims
	until time.Time
}

// reuseMargin: an app token is reused until this long before it expires.
const reuseMargin = 30 * time.Second

// jwksEvery: the issuer's key set is fetched at most this often.
const jwksEvery = time.Minute

// expiryMargin: a login token this close to expiry is refused (401), so the
// client refreshes it before the gateway exchanges it.
//
// Verify also exchanges the login token before the request reaches a tool:
// if the issuer refuses it (revoked, rotated out by a refresh, logged out),
// the client gets 401 invalid_token, refreshes and retries, instead of a
// tool error.
const expiryMargin = 10 * time.Second

func New(ctx context.Context, cfg Config) (*Gateway, error) {
	p, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, err
	}
	var meta struct {
		JWKS string   `json:"jwks_uri"`
		Algs []string `json:"id_token_signing_alg_values_supported"`
	}
	if err := p.Claims(&meta); err != nil || meta.JWKS == "" {
		return nil, fmt.Errorf("the issuer's discovery has no jwks_uri: %v", err)
	}
	// Public-key algorithms only (as go-oidc's own provider does): never
	// HS* (a shared-secret "signature" anyone with the key set could forge).
	algs := slices.DeleteFunc(meta.Algs, func(a string) bool { return !strings.HasPrefix(a, "RS") && !strings.HasPrefix(a, "ES") && !strings.HasPrefix(a, "PS") && a != "EdDSA" })
	keys := &http.Client{Timeout: 15 * time.Second, Transport: &jwksCooldown{next: http.DefaultTransport,
		url: meta.JWKS, every: jwksEvery, now: time.Now}}
	ks := oidc.NewRemoteKeySet(oidc.ClientContext(context.WithoutCancel(ctx), keys), meta.JWKS)
	return &Gateway{cfg: cfg, tokenURL: p.Endpoint().TokenURL, http: &http.Client{Timeout: 15 * time.Second},
		cache: map[string]appTok{}, now: time.Now,
		verifier: oidc.NewVerifier(cfg.Issuer, ks, &oidc.Config{SkipClientIDCheck: true, SupportedSigningAlgs: algs,
			Now: func() time.Time { return time.Now().Add(expiryMargin) }})}, nil
}

type ctxKey struct{}

type person struct {
	token  string
	claims Claims
}

// Verify is the MCP SDK's TokenVerifier: signature, issuer, expiry, audience.
func (g *Gateway) Verify(ctx context.Context, tok string, _ *http.Request) (*mcpauth.TokenInfo, error) {
	t, err := g.verifier.Verify(ctx, tok)
	if err != nil {
		slog.Info("token refused", "reason", err.Error())
		return nil, fmt.Errorf("%w: %v", mcpauth.ErrInvalidToken, err)
	}
	var c Claims
	if err := t.Claims(&c); err != nil {
		return nil, fmt.Errorf("%w: %v", mcpauth.ErrInvalidToken, err)
	}
	return g.verified(ctx, tok, c, t.Expiry)
}

// verified: the checks after the signature, issuer and expiry.
func (g *Gateway) verified(ctx context.Context, tok string, c Claims, exp time.Time) (*mcpauth.TokenInfo, error) {
	if !slices.ContainsFunc(g.cfg.Audiences, func(a string) bool { return slices.Contains(c.Aud, a) }) {
		slog.Info("token refused", "reason", "audience", "claims", c)
		return nil, fmt.Errorf("%w: audience", mcpauth.ErrInvalidToken)
	}
	if err := g.admit(ctx, person{tok, c}); err != nil {
		return nil, err
	}
	return TokenInfo(tok, c, exp), nil
}

// admit exchanges a verified login token up front (cached for the tools).
// A refusal by the issuer is a 401; any other failure (issuer down,
// configuration) lets the request through, and the tool reports it.
func (g *Gateway) admit(ctx context.Context, p person) error {
	// Checked without an app audience: whether the login is still valid,
	// not whether one app's access policy lets the person in.
	_, _, err := g.appTokenFor(ctx, p, "")
	if errors.Is(err, ErrAppTokenRefused) {
		slog.Info("token refused", "reason", "the login server refused to exchange it", "claims", p.claims)
		return fmt.Errorf("%w: the login server no longer accepts this login", mcpauth.ErrInvalidToken)
	}
	if err != nil {
		slog.Warn("exchange at login check failed; the tool will retry", "error", err.Error(), "claims", p.claims)
	}
	return nil
}

// TokenInfo: what Verify hands the SDK for a verified token.
func TokenInfo(tok string, c Claims, exp time.Time) *mcpauth.TokenInfo {
	return &mcpauth.TokenInfo{Scopes: c.Scopes(), Expiration: exp, UserID: c.Sub,
		Extra: map[string]any{"person": person{tok, c}}}
}

// Token: the caller's raw token (for tests and the exchange).
func Token(ctx context.Context) string {
	p, _ := from(ctx)
	return p.token
}

// Person: the verified caller of a tool.
func Person(ctx context.Context) (Claims, error) {
	p, err := from(ctx)
	return p.claims, err
}

// WithRequest puts the token of the current HTTP request (the SDK's
// req.Extra.TokenInfo) into ctx. Tool handlers must use it: the ctx they
// get may carry the token the MCP session was opened with, which expires
// while the session goes on (the person's client refreshes its token).
func WithRequest(ctx context.Context, ti *mcpauth.TokenInfo) context.Context {
	if ti == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, ti)
}

func from(ctx context.Context) (person, error) {
	ti, _ := ctx.Value(ctxKey{}).(*mcpauth.TokenInfo)
	if ti == nil {
		return person{}, errors.New("not logged in")
	}
	p, ok := ti.Extra["person"].(person)
	if !ok {
		return person{}, errors.New("not logged in")
	}
	return p, nil
}

// ErrAppTokenRefused: the issuer refused to trade the person's (verified)
// login for an app token. Usually passing (a login the client just
// replaced); if it persists, the login was ended at the issuer.
var ErrAppTokenRefused = errors.New("the SGNL login server refused access to this app just now: try again. If it keeps failing, log in again (in Claude Code: /mcp, then Authenticate)")

// MailToken trades the caller's token for a short-lived one for the mail
// server (JMAP), reused for the same login token until shortly before
// either expires.
func (g *Gateway) MailToken(ctx context.Context) (string, error) {
	tok, _, err := g.appToken(ctx, g.cfg.MailAudience)
	return tok, err
}

// FilesToken: the same for OpenCloud, and only if its identity claims
// (username, name, email, groups) equal the person's: OpenCloud updates
// the account (and its role) from the token it gets, so a token that
// differs is refused, never sent.
func (g *Gateway) FilesToken(ctx context.Context) (string, error) {
	tok, c, err := g.appToken(ctx, g.cfg.FilesAudience)
	if err != nil {
		return "", err
	}
	p, _ := from(ctx)
	if d := identityDiff(p.claims, c); d != "" {
		slog.Warn("app token refused: identity differs from the person's", "field", d, "person", p.claims, "app", c)
		return "", fmt.Errorf("the app token's %s differs from your login's; refusing to call the app", d)
	}
	return tok, nil
}

func identityDiff(a, b Claims) string {
	switch {
	case a.Username == "" || a.Username != b.Username:
		return "username"
	case a.Email != b.Email:
		return "email"
	case a.Name != b.Name:
		return "name"
	case !slices.Equal(slices.Sorted(slices.Values(a.Groups)), slices.Sorted(slices.Values(b.Groups))):
		return "groups"
	}
	return ""
}

// ExchangeScope: what the gateway asks for in the exchange; the login must
// have been granted all of it (the PRM advertises the same list).
const ExchangeScope = "openid profile email groups"

// loginEnded: the issuer refused the person's login itself (expired,
// revoked, logged out, or just replaced by a refresh), as opposed to a
// configuration mistake.
// Keycloak: invalid_token / "Invalid token". Authelia follows RFC 8693
// §2.2.2: invalid_request ending in "Token is not valid or has expired.".
func loginEnded(code, desc string) bool {
	switch code {
	case "invalid_token", "invalid_grant":
		return true
	case "invalid_request":
		return strings.HasSuffix(strings.TrimSpace(desc), "Token is not valid or has expired.")
	}
	return desc == "Invalid token"
}

func (g *Gateway) appToken(ctx context.Context, aud string) (string, Claims, error) {
	p, err := from(ctx)
	if err != nil {
		return "", Claims{}, err
	}
	return g.appTokenFor(ctx, p, aud)
}

func (g *Gateway) appTokenFor(ctx context.Context, p person, aud string) (string, Claims, error) {
	if p.token == "" {
		return "", Claims{}, errors.New("not logged in")
	}
	sum := sha256.Sum256([]byte(p.token))
	key := hex.EncodeToString(sum[:]) + "\x00" + aud
	g.mu.Lock()
	a, ok := g.cache[key]
	g.mu.Unlock()
	if ok && g.now().Before(a.until) {
		return a.tok, a.c, nil
	}
	v, err, _ := g.flight.Do(key, func() (any, error) {
		tok, c, err := g.exchange(ctx, p, aud)
		if err != nil {
			return nil, err
		}
		if aud != "" && !slices.Contains(c.Aud, aud) {
			return nil, fmt.Errorf("the app token's audience is %v, not %s", c.Aud, aud)
		}
		exp := min(c.Exp, p.claims.Exp)
		if exp == 0 {
			exp = max(c.Exp, p.claims.Exp)
		}
		a := appTok{tok, c, time.Unix(exp, 0).Add(-reuseMargin)}
		g.mu.Lock()
		for k, old := range g.cache { // drop expired entries
			if !g.now().Before(old.until) {
				delete(g.cache, k)
			}
		}
		g.cache[key] = a
		g.mu.Unlock()
		return a, nil
	})
	if err != nil {
		return "", Claims{}, err
	}
	a = v.(appTok)
	return a.tok, a.c, nil
}

func (g *Gateway) exchange(ctx context.Context, p person, aud string) (string, Claims, error) {
	f := url.Values{
		"grant_type":           {"urn:ietf:params:oauth:grant-type:token-exchange"},
		"client_id":            {g.cfg.ClientID},
		"client_secret":        {g.cfg.ClientSecret},
		"subject_token":        {p.token},
		"subject_token_type":   {"urn:ietf:params:oauth:token-type:access_token"},
		"requested_token_type": {"urn:ietf:params:oauth:token-type:access_token"},
		// openid: apps that look the person up at the issuer's userinfo
		// (Stalwart, OpenCloud) need it. profile, email, groups: issuers
		// that release claims by scope (Authelia) leave the identity
		// claims out of the new token without them.
		"scope": {ExchangeScope},
	}
	if aud != "" {
		f.Set("audience", aud)
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", g.tokenURL, strings.NewReader(f.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := g.http.Do(req)
	if err != nil {
		return "", Claims{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var r struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		Desc        string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &r)
	if resp.StatusCode != 200 || r.AccessToken == "" {
		slog.Warn("token exchange failed", "status", resp.StatusCode, "error", r.Error, "desc", r.Desc, "subject", p.claims)
		if loginEnded(r.Error, r.Desc) {
			return "", Claims{}, ErrAppTokenRefused
		}
		return "", Claims{}, fmt.Errorf("token exchange: %d %s %s", resp.StatusCode, r.Error, r.Desc)
	}
	c := PeekClaims(r.AccessToken)
	if c.Client() != g.cfg.ClientID {
		return "", Claims{}, fmt.Errorf("exchanged token issued to %q, not %s", c.Client(), g.cfg.ClientID)
	}
	slog.Debug("token exchanged", "subject", p.claims, "exchanged", c)
	return r.AccessToken, c, nil
}
