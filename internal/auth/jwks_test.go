package auth

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestJWKSCooldown(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"keys":[]}`))
	}))
	defer srv.Close()
	now := time.Now()
	j := &jwksCooldown{next: http.DefaultTransport, url: srv.URL + "/jwks", every: time.Minute, now: func() time.Time { return now }}
	c := &http.Client{Transport: j}
	get := func(u string) string {
		r, err := c.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r.Body)
		return string(b)
	}
	for range 50 { // e.g. 50 junk tokens
		if b := get(srv.URL + "/jwks"); b != `{"keys":[]}` {
			t.Fatal(b)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("%d fetches within the cooldown, want 1", hits.Load())
	}
	now = now.Add(61 * time.Second)
	get(srv.URL + "/jwks")
	if hits.Load() != 2 {
		t.Fatalf("after the cooldown: %d fetches, want 2", hits.Load())
	}
	get(srv.URL + "/other") // other URLs pass straight through
	if hits.Load() != 3 {
		t.Fatalf("other URL: %d", hits.Load())
	}
}
