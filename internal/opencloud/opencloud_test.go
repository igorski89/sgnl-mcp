package opencloud

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOnlyReadMethods(t *testing.T) {
	c := New("http://127.0.0.1:1")
	for _, m := range []string{"DELETE", "MOVE", "COPY", "MKCOL", "POST", "PROPPATCH"} {
		if _, err := c.do(context.Background(), "t", m, "/x", nil, nil); err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Errorf("%s: got %v", m, err)
		}
	}
}

func TestDownloadPathMustComeFromSearch(t *testing.T) {
	c := New("http://127.0.0.1:1")
	for _, p := range []string{"/ocs/v1.php/x", "https://evil/x", "/remote.php/dav/spaces/a/../../x"} {
		if _, _, _, err := c.Download(context.Background(), "t", p, 10); err == nil || !strings.Contains(err.Error(), "path not recognised") {
			t.Errorf("%s: got %v", p, err)
		}
	}
}

func TestPutOnlyForNewFiles(t *testing.T) {
	c := New("http://127.0.0.1:1")
	if _, err := c.do(context.Background(), "t", "PUT", "/x", nil, nil); err == nil || !strings.Contains(err.Error(), "never overwrite") {
		t.Errorf("PUT without If-None-Match: got %v", err)
	}
	if _, err := c.SaveNew(context.Background(), "t", "/remote.php/dav/spaces/a/", "../x.md", "text/plain", nil); err == nil {
		t.Error("name with slash accepted")
	}
}

func TestSearchKQL(t *testing.T) {
	if got := ContentKQL(`invoice "ACME" OR name:*x* (a)`); got != `content:"invoice" AND content:"ACME" AND content:"OR" AND content:"namex" AND content:"a"` {
		t.Errorf("content: %s", got)
	}
	if got := ContentKQL(`"* () :`); got != "" {
		t.Errorf("nothing left: %q", got)
	}
	if got := NameKQL(`q"x`); got != `name:"*qx*"` {
		t.Errorf("name: %s", got)
	}
}

// A content search sends the KQL, asks for the score and never for
// highlights (they hold text from hidden parts).
func TestSearchAsksNoHighlights(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(207)
		fmt.Fprint(w, `<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:response><d:href>/remote.php/dav/spaces/p/a.xlsx</d:href><d:propstat><d:status>HTTP/1.1 200 OK</d:status><d:prop><oc:name>a.xlsx</oc:name><oc:score>2.5</oc:score><oc:highlights>secret x</oc:highlights></d:prop></d:propstat></d:response></d:multistatus>`)
	}))
	defer srv.Close()
	fs, err := New(srv.URL).SearchKQL(context.Background(), "t", ContentKQL("Licences"), 5)
	if err != nil || len(fs) != 1 || fs[0].Score != "2.5" {
		t.Fatalf("%v %v", fs, err)
	}
	if !strings.Contains(body, "content:&#34;Licences&#34;") && !strings.Contains(body, `content:&quot;Licences&quot;`) {
		t.Errorf("body: %s", body)
	}
	if strings.Contains(body, "highlights") {
		t.Error("asked for highlights")
	}
	b, _ := json.Marshal(fs)
	if strings.Contains(string(b), "secret") {
		t.Errorf("highlight text got through: %s", b)
	}
}
