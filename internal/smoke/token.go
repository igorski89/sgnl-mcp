package smoke

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrRelogin: the saved login has ended; a fresh login is needed (outside
// the runner).
var ErrRelogin = errors.New("re-login needed")

// tokenFile: what a token file holds (JSON, mode 600).
type tokenFile struct {
	RefreshToken string    `json:"refresh_token"`
	AccessToken  string    `json:"access_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitzero"`
}

// Login: one account's tokens, from and back to its file.
type Login struct {
	Path     string
	TokenURL string
	ClientID string
	HTTP     *http.Client
	mu       sync.Mutex // one refresh at a time: a rotated token is used once
	tf       tokenFile
}

// Load: the file, refused unless only its owner can read it.
func (l *Login) Load() error {
	st, err := os.Stat(l.Path)
	if err != nil {
		return fmt.Errorf("token file: %w", err)
	}
	if st.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("token file %s: readable by others (mode %o): chmod 600 it", l.Path, st.Mode().Perm())
	}
	b, err := os.ReadFile(l.Path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &l.tf); err != nil || l.tf.RefreshToken == "" {
		return fmt.Errorf("token file %s: no refresh_token in it", l.Path)
	}
	return nil
}

// save: the file replaced in one step (a new file, then rename).
func (l *Login) save() error {
	b, _ := json.Marshal(l.tf)
	tmp, err := os.CreateTemp(filepath.Dir(l.Path), ".smoke-token-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), l.Path)
}

// Access: a valid access token, refreshed (and the file rewritten) when
// it has less than a minute left.
func (l *Login) Access(ctx context.Context) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.tf.AccessToken != "" && time.Until(l.tf.ExpiresAt) > time.Minute {
		return l.tf.AccessToken, nil
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {l.tf.RefreshToken}, "client_id": {l.ClientID}}
	req, _ := http.NewRequestWithContext(ctx, "POST", l.TokenURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := l.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("the login server didn't answer: %w", err)
	}
	defer resp.Body.Close()
	var r struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r)
	if r.Error == "invalid_grant" || r.Error == "unauthorized_client" {
		return "", fmt.Errorf("%w for %s (%s)", ErrRelogin, l.Path, r.Error)
	}
	if resp.StatusCode != 200 || r.AccessToken == "" {
		return "", fmt.Errorf("token refresh: the login server answered %d %s", resp.StatusCode, r.Error)
	}
	// The new refresh token (rotation) is saved before the access token
	// is used: a crash after this point can't strand the account.
	if r.RefreshToken != "" {
		l.tf.RefreshToken = r.RefreshToken
	}
	l.tf.AccessToken, l.tf.ExpiresAt = r.AccessToken, time.Now().Add(time.Duration(r.ExpiresIn)*time.Second)
	if err := l.save(); err != nil {
		return "", fmt.Errorf("token file %s: couldn't save the refreshed login: %w", l.Path, err)
	}
	return l.tf.AccessToken, nil
}

// TokenURL: the issuer's token endpoint, from its discovery document.
func TokenURL(ctx context.Context, hc *http.Client, issuer string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(issuer, "/")+"/.well-known/openid-configuration", nil)
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var d struct {
		TokenEndpoint string `json:"token_endpoint"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&d); err != nil || d.TokenEndpoint == "" {
		return "", fmt.Errorf("the issuer's discovery document has no token endpoint (%d)", resp.StatusCode)
	}
	return d.TokenEndpoint, nil
}
