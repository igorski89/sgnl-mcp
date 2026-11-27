package listmonk

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Only the allowed routes reach the app: never raw SQL, bulk or "all"
// forms, settings, users, roles or imports; a page is at most 50.
func TestOnlyAllowedCalls(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.RequestURI())
		w.Write([]byte(`{"data":{"results":[],"total":0}}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	ctx := context.Background()
	for _, x := range []struct {
		method, path string
		q            url.Values
	}{
		{"DELETE", "/api/subscribers", nil},
		{"POST", "/api/subscribers/query/delete", nil},
		{"PUT", "/api/subscribers/query/blocklist", nil},
		{"GET", "/api/settings", nil},
		{"GET", "/api/users", nil},
		{"POST", "/api/import/subscribers", nil},
		{"DELETE", "/api/bounces", nil},
		{"DELETE", "/api/campaigns", nil},
		{"GET", "/api/subscribers", url.Values{"query": {"1=1"}}},
		{"DELETE", "/api/lists/1", url.Values{"all": {"true"}}},
		{"GET", "/api/subscribers/1/../../settings", nil},
	} {
		if err := c.Call(ctx, "t", x.method, x.path, x.q, nil, nil); err == nil {
			t.Errorf("%s %s %v: allowed", x.method, x.path, x.q)
		}
	}
	if len(got) != 0 {
		t.Fatalf("reached the app: %v", got)
	}
	var p Page[Subscriber]
	if err := c.Call(ctx, "t", "GET", "/api/subscribers", url.Values{"search": {"a"}, "per_page": {"all"}}, nil, &p); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(ctx, "t", "GET", "/api/lists", url.Values{"per_page": {"10000"}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got[0], "per_page=20") || !strings.Contains(got[1], "per_page=50") {
		t.Errorf("pages: %v", got)
	}
}

func TestNoAccount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte(`{"message":"unknown user"}`))
	}))
	defer srv.Close()
	err := New(srv.URL).Call(context.Background(), "t", "GET", "/api/lists", url.Values{}, nil, nil)
	if !errors.Is(err, ErrNoAccount) || !strings.Contains(err.Error(), "Ask an admin") {
		t.Errorf("%v", err)
	}
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"message":"Invalid list IDs."}`))
	}))
	defer srv2.Close()
	err = New(srv2.URL).Call(context.Background(), "t", "POST", "/api/campaigns", url.Values{}, map[string]any{}, nil)
	if err == nil || !strings.Contains(err.Error(), "400: Invalid list IDs.") {
		t.Errorf("%v", err)
	}
}
