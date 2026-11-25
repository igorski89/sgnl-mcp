package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/igorski89/sgnl-mcp/internal/jmap"
	"github.com/igorski89/sgnl-mcp/internal/opencloud"
)

// A drive file is fetched as the person and uploaded to the mail server;
// the draft then carries it. Too much is refused.
func TestAttachFiles(t *testing.T) {
	files := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		if strings.HasSuffix(r.URL.Path, "big.pdf") {
			w.Write(make([]byte, maxAttach+1))
			return
		}
		fmt.Fprint(w, "%PDF-1.4 report")
	}))
	defer files.Close()
	var uploaded string
	mail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		uploaded = r.Header.Get("Content-Type") + " " + string(b)
		fmt.Fprint(w, `{"blobId":"UP1","type":"application/pdf","size":15}`)
	}))
	defer mail.Close()
	tl := &Tools{Auth: fakeTokens{}, Files: opencloud.New(files.URL), JMAP: jmap.New(mail.URL)}
	s := &jmap.Session{UploadURL: mail.URL + "/upload/{accountId}/"}
	d := draft{attachments: []part{{Name: "kept.txt", BlobID: "K", Size: 10}}}
	if err := tl.attachFiles(context.Background(), "tok", s, "a1", &d, []string{"/remote.php/dav/spaces/p/Q4 report.pdf"}); err != nil {
		t.Fatal(err)
	}
	if len(d.attachments) != 2 || d.attachments[1].BlobID != "UP1" || d.attachments[1].Name != "Q4 report.pdf" ||
		d.attachments[1].Type != "application/pdf" || uploaded != "application/pdf %PDF-1.4 report" {
		t.Fatalf("%+v, uploaded %q", d.attachments, uploaded)
	}
	if err := tl.attachFiles(context.Background(), "tok", s, "a1", &d, []string{"/remote.php/dav/spaces/p/big.pdf"}); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("too big: %v", err)
	}
}
