package tools

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/igorski89/sgnl-mcp/internal/jmap"
	"github.com/igorski89/sgnl-mcp/internal/opencloud"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{
		"../../etc/passwd":                "_.._etc_passwd",
		".hidden":                         "hidden",
		"a‮exe.pdf":                       "a_exe.pdf",
		"":                                "attachment",
		strings.Repeat("x", 300) + ".pdf": strings.Repeat("x", 146) + ".pdf",
	} {
		if got := safeName(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

// An attachment lands as a new file in the personal space without a
// question; into a shared space only after the person's yes.
func TestAttachmentSave(t *testing.T) {
	m := newFakeMail()
	m.list = []any{map[string]any{"id": "m1", "subject": "Invoice", "attachments": []map[string]any{
		{"partId": "2", "blobId": "B2", "name": "../Invoice 7.pdf", "type": "application/pdf", "size": 16}}}}
	mail := m.serve(t)
	var mu sync.Mutex
	saved := map[string]string{}
	files := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/graph/v1.0/me/drives":
			io.WriteString(w, `{"value":[{"id":"p","name":"Personal","driveType":"personal","root":{"webDavUrl":"http://x/remote.php/dav/spaces/p"}},`+
				`{"id":"t","name":"Team","driveType":"project","root":{"webDavUrl":"http://x/remote.php/dav/spaces/t"}}]}`)
		case r.Method == "PROPFIND":
			w.WriteHeader(404)
		case r.Method == "PUT":
			b, _ := io.ReadAll(r.Body)
			saved[r.URL.Path] = string(b)
			w.WriteHeader(201)
		}
	}))
	t.Cleanup(files.Close)
	asked := 0
	s := mcp.NewServer(&mcp.Implementation{Name: "t"}, nil)
	(&Tools{Auth: fakeTokens{}, JMAP: jmap.New(mail.URL + "/session"), Files: opencloud.New(files.URL), Access: Access{All: true}}).Register(s)
	ct, st := mcp.NewInMemoryTransports()
	if _, err := s.Connect(context.Background(), st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c"}, &mcp.ClientOptions{ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked++
		return &mcp.ElicitResult{Action: "decline"}, nil
	}}).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	call := func(args map[string]any) *mcp.CallToolResult {
		r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "mail_attachment_save", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := call(map[string]any{"id": "m1", "index": 0}); r.IsError || saved["/remote.php/dav/spaces/p/_Invoice 7.pdf"] != "%PDF-1.4 invoice" || asked != 0 {
		t.Fatalf("personal: %q, %v, asked %d", resultText(r), saved, asked)
	}
	if r := call(map[string]any{"id": "m1", "index": 0, "folder": "/remote.php/dav/spaces/t/"}); !r.IsError || asked != 1 || len(saved) != 1 {
		t.Fatalf("shared, declined: %q, %v, asked %d", resultText(r), saved, asked)
	}
	if r := call(map[string]any{"id": "m1", "index": 3}); !r.IsError {
		t.Fatal("no such attachment: not refused")
	}
}
