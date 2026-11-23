package opencloud

import (
	"context"
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
		if _, _, _, err := c.Download(context.Background(), "t", p, 10); err == nil || !strings.Contains(err.Error(), "files_search") {
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
