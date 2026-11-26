package audit

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newStore(t *testing.T, user Level) *Store {
	t.Helper()
	s, err := OpenForTest(Config{Path: filepath.Join(t.TempDir(), "a.db"), Admin: Full, User: user,
		Clear: map[string]map[string]bool{User: {"path": true, "file_save.name": true}, Admin: {"username": true}}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCallRecorded(t *testing.T) {
	s := newStore(t, Meta)
	who := Who{Username: "anna", Sub: "s1", Client: "claude", Session: "sess", IP: "10.0.0.1"}
	ctx, c, err := s.Begin(context.Background(), User, "file_save", false, who,
		struct {
			Path    string `json:"path"`
			Name    string `json:"name"`
			Content string `json:"content"`
		}{"/x", "notes.md", "SECRET-CONTENT"})
	if err != nil {
		t.Fatal(err)
	}
	Asked(ctx, DialogID("state1"), "Save notes.md with SECRET-DIALOG?")
	if err := Answered(ctx, DialogID("state1"), "accept", 2300); err != nil {
		t.Fatal(err)
	}
	Backend(ctx, "drive.example", "PUT", "/remote.php/dav/spaces/p/notes.md?x=1", 201)
	c.Done(ctx, "ok", "", map[string]any{"path": "/x/notes.md", "note": "Saved."})
	evs, err := s.Search(context.Background(), Filter{Username: "anna"})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	all := ""
	for _, e := range evs {
		kinds = append(kinds, e.Event)
		all += e.Event + " " + mustJSON(e.Data) + "\n"
		if e.Call != evs[0].Call || e.IP != "10.0.0.1" || e.Client != "claude" {
			t.Errorf("event: %+v", e)
		}
	}
	if strings.Join(kinds, ",") != "done,backend,answered,asked,called" {
		t.Errorf("events: %v", kinds)
	}
	for _, w := range []string{`"path":"/x"`, `"name":"notes.md"`, `"outcome":"accept"`, `"ms":2300`, `"/remote.php/dav/spaces/p/notes.md"`, `"status":201`} {
		if !strings.Contains(all, w) {
			t.Errorf("missing %s in\n%s", w, all)
		}
	}
	// Meta: no content, not even in the dialog; the query string is cut.
	b, _ := os.ReadFile(s.cfg.Path)
	wal, _ := os.ReadFile(s.cfg.Path + "-wal")
	for _, secret := range []string{"SECRET-CONTENT", "SECRET-DIALOG", "x=1"} {
		if strings.Contains(string(b)+string(wal), secret) || strings.Contains(all, secret) {
			t.Errorf("%s stored", secret)
		}
	}
}

func mustJSON(v any) string {
	b, _ := jsonMarshal(v)
	return string(b)
}

func TestFullLevelAndSearchHidesUserFull(t *testing.T) {
	s := newStore(t, Full)
	ctx, c, _ := s.Begin(context.Background(), User, "mail_draft_create", false, Who{Username: "anna"}, map[string]any{"body": "THE-BODY"})
	c.Done(ctx, "ok", "", nil)
	ctx, c, _ = s.Begin(context.Background(), Admin, "admin_disable", false, Who{Username: "boss"}, map[string]any{"username": "hugo"})
	c.Done(ctx, "ok", "", nil)
	evs, _ := s.Search(context.Background(), Filter{})
	for _, e := range evs {
		if e.Connector == User && e.Full != nil {
			t.Errorf("user full returned: %+v", e)
		}
		if e.Connector == Admin && e.Event == "called" && e.Full == nil {
			t.Errorf("admin full missing")
		}
	}
	// The target filter matches meta only: the body isn't findable.
	if evs, _ := s.Search(context.Background(), Filter{Target: "THE-BODY"}); len(evs) != 0 {
		t.Errorf("found by content: %v", evs)
	}
	if evs, _ := s.Search(context.Background(), Filter{Target: "hugo"}); len(evs) != 1 {
		t.Errorf("admin target: %v", evs)
	}
}

func TestFailClosedForChangesOnly(t *testing.T) {
	s := newStore(t, Meta)
	FailForTest(s, "called")
	if _, _, err := s.Begin(context.Background(), Admin, "admin_disable", false, Who{Username: "boss"}, nil); err == nil {
		t.Error("admin change went on unrecorded")
	}
	if _, _, err := s.Begin(context.Background(), Admin, "admin_users", true, Who{Username: "boss"}, nil); err != nil {
		t.Error("admin read refused")
	}
	if _, _, err := s.Begin(context.Background(), User, "file_save", false, Who{Username: "a"}, nil); err != nil {
		t.Error("user change refused (fail open)")
	}
	s.cfg.UserStrict = true
	if _, _, err := s.Begin(context.Background(), User, "file_save", false, Who{Username: "a"}, nil); err == nil {
		t.Error("strict user change went on")
	}
	if s.failures.Load() == 0 || s.OK() {
		t.Error("failure not counted")
	}
	// An accepted answer that can't be written stops the change.
	FailForTest(s, "answered")
	ctx, _, err := s.Begin(context.Background(), Admin, "admin_disable", false, Who{Username: "boss"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if Answered(ctx, "d", "accept", 2000) == nil {
		t.Error("accept unrecorded but allowed")
	}
	if Answered(ctx, "d", "decline", 2000) != nil {
		t.Error("a decline needs no record to be safe")
	}
}

func TestPurgeAndRefusedLimit(t *testing.T) {
	s := newStore(t, Meta)
	s.cfg.RetainUser = time.Hour
	ctx, c, _ := s.Begin(context.Background(), User, "whoami", true, Who{Username: "a"}, nil)
	c.Done(ctx, "ok", "", nil)
	s.mu.Lock()
	s.db.Exec(`UPDATE events SET ts = ts - 7200000`)
	s.mu.Unlock()
	s.Purge()
	if evs, _ := s.Search(context.Background(), Filter{}); len(evs) != 0 {
		t.Errorf("not purged: %d", len(evs))
	}
	for range 50 {
		s.Refused(context.Background(), Admin, "admin_users", Who{Username: "eve"}, "not an admin")
	}
	if evs, _ := s.Search(context.Background(), Filter{Username: "eve"}); len(evs) != 20 {
		t.Errorf("refused rows: %d, want 20", len(evs))
	}
}

func TestTwoWritersOneFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.db")
	a, err := OpenForTest(Config{Path: p, User: Meta})
	if err != nil {
		t.Fatal(err)
	}
	b, err := OpenForTest(Config{Path: p, User: Meta})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, s := range []*Store{a, b} {
		wg.Add(1)
		go func(s *Store) {
			defer wg.Done()
			for range 100 {
				ctx, c, err := s.Begin(context.Background(), User, "whoami", true, Who{Username: "x"}, nil)
				if err != nil {
					t.Error(err)
				}
				c.Done(ctx, "ok", "", nil)
			}
		}(s)
	}
	wg.Wait()
	if evs, _ := a.Search(context.Background(), Filter{Limit: 500}); len(evs) != 400 || a.failures.Load()+b.failures.Load() != 0 {
		t.Errorf("rows %d, failures %d/%d", len(evs), a.failures.Load(), b.failures.Load())
	}
}

func TestParsing(t *testing.T) {
	if d, err := ParseRetention("90d", 0); err != nil || d != 90*24*time.Hour {
		t.Error(d, err)
	}
	for _, bad := range []string{"x", "0d", "10m", "-1d"} {
		if _, err := ParseRetention(bad, 0); err == nil {
			t.Errorf("%q allowed", bad)
		}
	}
	if _, err := ParseLevel("verbose", Meta); err == nil {
		t.Error("bad level")
	}
	h := http.Header{}
	h.Add("X-Forwarded-For", "6.6.6.6, 203.0.113.5")
	if ip := IPFrom(h); ip != "203.0.113.5" {
		t.Errorf("ip: %s", ip)
	}
}
