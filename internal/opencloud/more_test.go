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
