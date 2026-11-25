package opencloud

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeDAV: one file with a version counter; PUT honours If-Match and
// changes the version unless frozen.
type fakeDAV struct {
	mu           sync.Mutex
	data         string
	ver          int
	puts         int
	frozen       bool
	changeBefore bool // someone else saves right before our PUT arrives
}

func (f *fakeDAV) etag() string { return fmt.Sprintf(`"v%d"`, f.ver) }

func (f *fakeDAV) serve(t *testing.T) *Client {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case "GET":
			w.Header().Set("ETag", f.etag())
			io.WriteString(w, f.data)
		case "PROPFIND":
			w.WriteHeader(207)
			fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:"><d:response><d:propstat><d:prop><d:getetag>%s</d:getetag></d:prop></d:propstat></d:response></d:multistatus>`, f.etag())
		case "PUT":
			if f.changeBefore {
				f.ver++
			}
			if r.Header.Get("If-Match") != f.etag() {
				w.WriteHeader(412)
				return
			}
			b, _ := io.ReadAll(r.Body)
			f.puts++
			f.data = string(b)
			if !f.frozen {
				f.ver++
			}
			w.WriteHeader(204)
		default:
			w.WriteHeader(405)
		}
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL)
}

const p = davPrefix + "s/notes.md"

func TestOverwriteOnlyAtTheVersionRead(t *testing.T) {
	ctx := context.Background()
	f := &fakeDAV{data: "hello"}
	c := f.serve(t)
	data, etag, err := c.ReadForEdit(ctx, "tok", p, 100)
	if err != nil || string(data) != "hello" {
		t.Fatal(data, err)
	}
	if _, err := c.Overwrite(ctx, "tok", p, etag, "text/markdown", []byte("hello world")); err != nil || f.data != "hello world" {
		t.Fatalf("overwrite: %v, %q", err, f.data)
	}
	// The old etag is stale now: refused before any PUT.
	if _, err := c.Overwrite(ctx, "tok", p, etag, "text/markdown", []byte("x")); !errors.Is(err, ErrChanged) || f.puts != 1 {
		t.Fatalf("stale etag: %v, %d puts", err, f.puts)
	}
	// Changed between our check and our PUT: the server's 412.
	f.changeBefore = true
	_, etag, _ = c.ReadForEdit(ctx, "tok", p, 100)
	if _, err := c.Overwrite(ctx, "tok", p, etag, "text/markdown", []byte("x")); !errors.Is(err, ErrChanged) || f.data != "hello world" {
		t.Fatalf("race: %v, %q", err, f.data)
	}
}

func TestOverwriteDoesNotClaimWhatDidntHappen(t *testing.T) {
	ctx := context.Background()
	f := &fakeDAV{data: "hello", frozen: true}
	c := f.serve(t)
	_, etag, _ := c.ReadForEdit(ctx, "tok", p, 100)
	if _, err := c.Overwrite(ctx, "tok", p, etag, "text/plain", []byte("x")); err == nil {
		t.Fatal("success claimed although the version didn't change")
	}
}

func TestReadForEditRefusesTooLarge(t *testing.T) {
	f := &fakeDAV{data: strings.Repeat("a", 101)}
	if _, _, err := f.serve(t).ReadForEdit(context.Background(), "tok", p, 100); err == nil {
		t.Fatal("a file over the limit was read for editing (would be cut)")
	}
}

// Nothing but Overwrite can replace a file: a plain PUT, even with If-Match,
// is refused by the client before it is sent.
func TestOnlyOverwriteReplaces(t *testing.T) {
	f := &fakeDAV{data: "hello"}
	c := f.serve(t)
	for _, h := range []map[string]string{nil, {"If-Match": `"v0"`}, {"If-None-Match": "x"}} {
		if _, err := c.do(context.Background(), "tok", "PUT", p, strings.NewReader("x"), h); err == nil {
			t.Errorf("PUT with %v was allowed", h)
		}
	}
	for _, bad := range []string{"/elsewhere/notes.md", davPrefix + "s/../t/notes.md", davPrefix + "s/dir/", davPrefix + "s/run.sh?x.md", davPrefix + "s/run.sh#x.md"} {
		if _, err := c.Overwrite(context.Background(), "tok", bad, `"v0"`, "text/plain", []byte("x")); err == nil {
			t.Errorf("overwrite of %s allowed", bad)
		}
	}
	if _, err := c.Overwrite(context.Background(), "tok", p, "", "text/plain", []byte("x")); err == nil {
		t.Error("overwrite without an etag allowed")
	}
	if f.puts != 0 {
		t.Fatalf("%d PUTs reached the server", f.puts)
	}
}
