package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

)

func jwt(c Claims) string {
	b, _ := json.Marshal(c)
	return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
}

// fakeIssuer: a token endpoint that exchanges any subject token except
// those in refuse, counting exchanges.
func fakeIssuer(t *testing.T, refuse map[string]bool) (*Gateway, *atomic.Int32) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		time.Sleep(20 * time.Millisecond)
		if refuse[r.Form.Get("subject_token")] {
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
	return &Gateway{cfg: Config{ClientID: "gw"}, tokenURL: srv.URL, http: srv.Client(),
		cache: map[string]appTok{}, now: time.Now}, &n
}

func as(tok string) context.Context {
	return WithRequest(context.Background(), TokenInfo(tok, PeekClaims(tok), time.Now().Add(time.Minute)))
}

var igor = Claims{Sub: "s1", Azp: "cc", Username: "u", Email: "e", Name: "n", Groups: []string{"g"}}

func TestAppTokenSharedAndReused(t *testing.T) {
	g, n := fakeIssuer(t, nil)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := igor
			c.Iat = int64(i) // each call with a different login token of the same person
			if _, err := g.SameIdentityToken(as(jwt(c))); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if _, err := g.AppToken(as(jwt(igor))); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 1 {
		t.Fatalf("%d exchanges, want 1", n.Load())
	}
	other := igor
	other.Groups = []string{"g", "admins"}
	if _, err := g.SameIdentityToken(as(jwt(other))); err != nil || n.Load() != 2 {
		t.Fatalf("changed groups should get a fresh token: %v, %d exchanges", err, n.Load())
	}
	g.now = func() time.Time { return time.Now().Add(5 * time.Minute) }
	if _, err := g.AppToken(as(jwt(igor))); err != nil || n.Load() != 3 {
		t.Fatalf("an expiring token should be replaced: %v, %d exchanges", err, n.Load())
	}
}

func TestAppTokenRefusedIsTryAgain(t *testing.T) {
	old := igor
	old.Iat = 1
	g, n := fakeIssuer(t, map[string]bool{jwt(old): true})
	_, err := g.AppToken(as(jwt(old)))
	if !errors.Is(err, ErrAppTokenRefused) {
		t.Fatalf("got %v", err)
	}
	// A newer login of the same person still works.
	if _, err := g.AppToken(as(jwt(igor))); err != nil || n.Load() != 1 {
		t.Fatalf("%v, %d", err, n.Load())
	}
}

