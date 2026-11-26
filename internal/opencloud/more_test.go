package opencloud

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Nothing but listing reaches a trash bin: a DELETE there destroys.
func TestTrashOnlyListed(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path)
		w.WriteHeader(207)
		fmt.Fprint(w, `<d:multistatus xmlns:d="DAV:"/>`)
	}))
	defer srv.Close()
	c := New(srv.URL)
	ctx := context.Background()
	for _, m := range []string{"GET", "PUT", "DELETE", "MOVE", "MKCOL"} {
		if _, err := c.do(ctx, "t", m, trashPrefix+"p/x", nil, map[string]string{"If-None-Match": "*"}); err == nil {
			t.Errorf("%s on the trash bin allowed", m)
		}
	}
	if _, err := c.Mkcol(ctx, "t", trashPrefix+"p/", "x"); err == nil {
		t.Error("mkcol in the trash bin allowed")
	}
	if _, err := c.Trash(ctx, "t", "p"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.HasPrefix(got[0], "PROPFIND "+trashPrefix) {
		t.Fatalf("requests: %v", got)
	}
}

func TestSharedWithOthersWalksUp(t *testing.T) {
	shared := davPrefix + "p/Q4"
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		w.WriteHeader(207)
		types := ""
		if r.URL.Path == shared {
			types = "<oc:share-type>0</oc:share-type>"
		}
		fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:response><d:propstat><d:status>HTTP/1.1 200 OK</d:status><d:prop><oc:share-types>%s</oc:share-types></d:prop></d:propstat></d:response></d:multistatus>`, types)
	}))
	defer srv.Close()
	c := New(srv.URL)
	if ok, err := c.SharedWithOthers(context.Background(), "t", davPrefix+"p/", davPrefix+"p/Q4/drafts/"); err != nil || !ok {
		t.Fatalf("inside a shared folder: %v %v (%v)", ok, err, seen)
	}
	if ok, _ := c.SharedWithOthers(context.Background(), "t", davPrefix+"p/", davPrefix+"p/Other/"); ok {
		t.Fatal("not shared, yet reported shared")
	}
	if ok, _ := c.SharedWithOthers(context.Background(), "t", davPrefix+"p/", davPrefix+"p/"); ok {
		t.Fatal("the space's top reported shared")
	}
}

func TestMoveTrashRestoreGuards(t *testing.T) {
	var got []string
	exists := map[string]bool{davPrefix + "p/taken.md": true, davPrefix + "p/back.md": true}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path+" "+r.Header.Get("Overwrite"))
		if r.Method == "PROPFIND" {
			if !exists[r.URL.Path] {
				w.WriteHeader(404)
				return
			}
			w.WriteHeader(207)
			fmt.Fprint(w, `<d:multistatus xmlns:d="DAV:"/>`)
			return
		}
		w.WriteHeader(201)
	}))
	defer srv.Close()
	c := New(srv.URL)
	ctx := context.Background()
	if err := c.Move(ctx, "t", davPrefix+"p/a.md", davPrefix+"p/taken.md"); err == nil {
		t.Error("move over an existing name allowed")
	}
	if err := c.Move(ctx, "t", davPrefix+"p/a.md", davPrefix+"p/b.md"); err != nil {
		t.Fatal(err)
	}
	if last := got[len(got)-1]; last != "MOVE "+davPrefix+"p/a.md F" {
		t.Errorf("move sent: %q", last)
	}
	for _, bad := range []string{trashPrefix + "p/x", davPrefix + "p/../q/x", "/elsewhere"} {
		if err := c.ToTrash(ctx, "t", bad); err == nil {
			t.Errorf("trash of %s allowed", bad)
		}
		if err := c.Move(ctx, "t", bad, davPrefix+"p/z"); err == nil {
			t.Errorf("move of %s allowed", bad)
		}
	}
	if err := c.Restore(ctx, "t", "p", davPrefix+"p/", "k1", "back.md"); err == nil {
		t.Error("restore over an existing file allowed")
	}
	if err := c.Restore(ctx, "t", "p", davPrefix+"p/", "k1", "../x"); err == nil {
		t.Error("restore outside the space allowed")
	}
	if err := c.Restore(ctx, "t", "p", davPrefix+"p/", "k1", "Q4/gone.md"); err != nil {
		t.Fatal(err)
	}
	if last := got[len(got)-1]; last != "MOVE "+trashPrefix+"p/k1 F" {
		t.Errorf("restore sent: %q", last)
	}
	for _, g := range got {
		if strings.HasPrefix(g, "DELETE "+trashPrefix) {
			t.Fatalf("a DELETE reached the trash bin: %v", got)
		}
	}
}

func TestUnshareAndVersionGuards(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path)
		w.WriteHeader(204)
	}))
	defer srv.Close()
	c := New(srv.URL)
	ctx := context.Background()
	if err := c.Unshare(ctx, "t", Shared{Drive: "d", ItemID: "i"}, Grant{ID: "p1"}); err != nil {
		t.Fatal(err)
	}
	if got[0] != "DELETE /graph/v1beta1/drives/d/items/i/permissions/p1" {
		t.Fatalf("sent %v", got)
	}
	for _, bad := range []Shared{{Drive: "", ItemID: "i"}, {Drive: "d", ItemID: ""}} {
		if err := c.Unshare(ctx, "t", bad, Grant{ID: "p1"}); err == nil {
			t.Errorf("unshare %v allowed", bad)
		}
	}
	if err := c.Unshare(ctx, "t", Shared{Drive: "d", ItemID: "i"}, Grant{}); err == nil {
		t.Error("unshare without a grant id allowed")
	}
	if err := c.RestoreVersion(ctx, "t", davPrefix+"p/x.md", "a/b"); err == nil {
		t.Error("version id with a slash allowed")
	}
	if len(got) != 1 {
		t.Fatalf("extra requests: %v", got)
	}
}

func TestCopyNeverOverwrites(t *testing.T) {
	var got []string
	exists := map[string]bool{davPrefix + "p/taken.md": true}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path+" "+r.Header.Get("Overwrite")+" "+r.Header.Get("Destination"))
		if r.Method == "PROPFIND" {
			if !exists[r.URL.Path] {
				w.WriteHeader(404)
				return
			}
			w.WriteHeader(207)
			fmt.Fprint(w, `<d:multistatus xmlns:d="DAV:"/>`)
			return
		}
		w.WriteHeader(201)
	}))
	defer srv.Close()
	c := New(srv.URL)
	ctx := context.Background()
	if err := c.Copy(ctx, "t", davPrefix+"p/a.md", davPrefix+"p/taken.md"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("copy over an existing name: %v", err)
	}
	if err := c.Copy(ctx, "t", davPrefix+"p/a.md", davPrefix+"q/a.md"); err != nil {
		t.Fatal(err)
	}
	if last := got[len(got)-1]; last != "COPY "+davPrefix+"p/a.md F "+srv.URL+davPrefix+"q/a.md" {
		t.Errorf("copy sent: %q", last)
	}
	for _, bad := range [][2]string{{trashPrefix + "p/x", davPrefix + "p/y"}, {davPrefix + "p/x", davPrefix + "p/../../y"}, {davPrefix + "p/x", "/elsewhere"}} {
		if err := c.Copy(ctx, "t", bad[0], bad[1]); err == nil {
			t.Errorf("copy %v allowed", bad)
		}
	}
	// The general client still refuses COPY: only Copy sends it.
	if _, err := c.do(ctx, "t", "COPY", davPrefix+"p/a.md", nil, nil); err == nil {
		t.Error("COPY through do allowed")
	}
}
