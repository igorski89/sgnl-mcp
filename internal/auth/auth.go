// Package auth: the gateway as an OAuth resource server. It accepts only
// access tokens from the configured issuer whose audience names the
// gateway, and trades each one (RFC 8693 token exchange) for a token of
// its own before calling an app: the person's token never reaches an app.
package auth

import (
	"context"
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
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
)

// Claims: what the gateway reads from a token (and logs; never the token).
type Claims struct {
	Sub      string   `json:"sub"`
	Azp      string   `json:"azp"`
	Aud      Audience `json:"aud"`
	Scope    string   `json:"scope"`
	Username string   `json:"preferred_username"`
	Name     string   `json:"name"`
	Email    string   `json:"email"`
	Groups   []string `json:"groups"`
	Exp      int64    `json:"exp"`
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
	return slog.GroupValue(slog.String("user", c.Username), slog.String("azp", c.Azp),
		slog.Any("aud", []string(c.Aud)), slog.Duration("exp_in", time.Until(time.Unix(c.Exp, 0)).Round(time.Second)))
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
	Audience     string // what a person's token must carry in "aud" (the gateway's client id)
	ClientID     string // the gateway's client, for the exchange
	ClientSecret string
}

type Gateway struct {
	cfg      Config
	verifier *oidc.IDTokenVerifier
	tokenURL string
	http     *http.Client
}

func New(ctx context.Context, cfg Config) (*Gateway, error) {
	p, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, err
	}
	return &Gateway{cfg: cfg, tokenURL: p.Endpoint().TokenURL, http: &http.Client{Timeout: 15 * time.Second},
		verifier: p.Verifier(&oidc.Config{SkipClientIDCheck: true})}, nil
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
	if !slices.Contains(c.Aud, g.cfg.Audience) {
		slog.Info("token refused", "reason", "audience", "claims", c)
		return nil, fmt.Errorf("%w: audience", mcpauth.ErrInvalidToken)
	}
	return &mcpauth.TokenInfo{Scopes: strings.Fields(c.Scope), Expiration: t.Expiry, UserID: c.Sub,
		Extra: map[string]any{"person": person{tok, c}}}, nil
}

// Person: the verified caller of a tool.
func Person(ctx context.Context) (Claims, error) {
	p, err := from(ctx)
	return p.claims, err
}

func from(ctx context.Context) (person, error) {
	ti := mcpauth.TokenInfoFromContext(ctx)
	if ti == nil {
		return person{}, errors.New("not logged in")
	}
	p, ok := ti.Extra["person"].(person)
	if !ok {
		return person{}, errors.New("not logged in")
	}
	return p, nil
}

// ErrLoginExpired: the issuer no longer accepts the person's login; they
// must log in again (in Claude Code: /mcp, Authenticate).
var ErrLoginExpired = errors.New("your SGNL login has expired or was ended: log in again (in Claude Code: /mcp, then Authenticate)")

// AppToken trades the caller's token for a short-lived one issued to the
// gateway (never cached: one per call).
func (g *Gateway) AppToken(ctx context.Context) (string, error) {
	p, err := from(ctx)
	if err != nil {
		return "", err
	}
	f := url.Values{
		"grant_type":           {"urn:ietf:params:oauth:grant-type:token-exchange"},
		"client_id":            {g.cfg.ClientID},
		"client_secret":        {g.cfg.ClientSecret},
		"subject_token":        {p.token},
		"subject_token_type":   {"urn:ietf:params:oauth:token-type:access_token"},
		"requested_token_type": {"urn:ietf:params:oauth:token-type:access_token"},
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", g.tokenURL, strings.NewReader(f.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := g.http.Do(req)
	if err != nil {
		return "", err
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
		slog.Warn("token exchange failed", "status", resp.StatusCode, "error", r.Error, "desc", r.Desc, "claims", p.claims)
		if r.Desc == "Invalid token" || r.Error == "invalid_token" {
			return "", ErrLoginExpired
		}
		return "", fmt.Errorf("token exchange: %d %s %s", resp.StatusCode, r.Error, r.Desc)
	}
	if c := PeekClaims(r.AccessToken); c.Azp != g.cfg.ClientID {
		return "", fmt.Errorf("exchanged token issued to %q, not %s", c.Azp, g.cfg.ClientID)
	}
	return r.AccessToken, nil
}
