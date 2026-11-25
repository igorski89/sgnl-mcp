package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/igorski89/sgnl-mcp/internal/opencloud"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeFiles struct {
	mu      sync.Mutex
	invites []map[string]any
	users   []map[string]string
}

func (f *fakeFiles) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.URL.Path == "/graph/v1.0/me/drives":
			json.NewEncoder(w).Encode(map[string]any{"value": []map[string]any{
				{"id": "p", "name": "Personal", "driveType": "personal", "root": map[string]string{"webDavUrl": "http://x/remote.php/dav/spaces/p"}},
				{"id": "t", "name": "Team", "driveType": "project", "root": map[string]string{"webDavUrl": "http://x/remote.php/dav/spaces/t"}}}})
		case r.Method == "PROPFIND":
			coll := ""
			if strings.HasSuffix(r.URL.Path, "/") || strings.HasSuffix(r.URL.Path, "Q4") {
				coll = "<d:collection/>"
			}
			w.WriteHeader(207)
			fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:response><d:propstat><d:prop><oc:fileid>s$p!item1</oc:fileid><d:resourcetype>%s</d:resourcetype></d:prop></d:propstat></d:response></d:multistatus>`, coll)
		case r.URL.Path == "/graph/v1.0/users":
			json.NewEncoder(w).Encode(map[string]any{"value": f.users})
		case strings.HasSuffix(r.URL.Path, "/permissions"):
			json.NewEncoder(w).Encode(map[string]any{"@libre.graph.permissions.roles.allowedValues": []map[string]string{
				{"id": opencloud.RoleViewer}, {"id": opencloud.RoleEditor}, {"id": opencloud.RoleFileEditor}}})
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/invite"):
			b, _ := io.ReadAll(r.Body)
			var v map[string]any
			json.Unmarshal(b, &v)
			v["_path"] = r.URL.Path
			f.invites = append(f.invites, v)
			json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]string{"id": "perm1"}}})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func shareSession(t *testing.T, f *fakeFiles, h elicitFn) *mcp.ClientSession {
	t.Helper()
	srv := f.serve(t)
	s := mcp.NewServer(&mcp.Implementation{Name: "t"}, nil)
	(&Tools{Auth: fakeTokens{}, Files: opencloud.New(srv.URL), Access: Access{All: true}}).Register(s)
	ct, st := mcp.NewInMemoryTransports()
	if _, err := s.Connect(context.Background(), st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c"}, &mcp.ClientOptions{ElicitationHandler: h}).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func share(t *testing.T, cs *mcp.ClientSession, p, with, access string) *mcp.CallToolResult {
	t.Helper()
	r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "file_share", Arguments: map[string]any{"path": p, "with": with, "access": access}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

var hugo = map[string]string{"id": "u-hugo", "displayName": "Hugo Example", "mail": "hugo@co.example", "onPremisesSamAccountName": "hugo"}

func TestShareOnlyWithThePersonsYes(t *testing.T) {
	f := &fakeFiles{users: []map[string]string{hugo}}
	var asked string
	cs := shareSession(t, f, func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = r.Params.Message
		return &mcp.ElicitResult{Action: "decline"}, nil
	})
	if r := share(t, cs, "/remote.php/dav/spaces/p/Q4", "hugo", "edit"); !r.IsError || len(f.invites) != 0 {
		t.Fatalf("declined, yet %q, %d invites", resultText(r), len(f.invites))
	}
	first, _, _ := strings.Cut(asked, "\n")
	for _, want := range []string{"FOLDER", `"Q4"`, "Hugo Example <hugo@co.example>", "VIEW AND CHANGE"} {
		if !strings.Contains(first, want) {
			t.Errorf("first line lacks %q: %q", want, first)
		}
	}

	f = &fakeFiles{users: []map[string]string{hugo}}
	cs = shareSession(t, f, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "accept"}, nil
	})
	if r := share(t, cs, "/remote.php/dav/spaces/p/report.pdf", "hugo@co.example", ""); r.IsError || len(f.invites) != 1 {
		t.Fatalf("accepted: %q, %d invites", resultText(r), len(f.invites))
	}
	inv := f.invites[0]
	rec := inv["recipients"].([]any)[0].(map[string]any)
	if rec["objectId"] != "u-hugo" || rec["@libre.graph.recipient.type"] != "user" || inv["roles"].([]any)[0] != opencloud.RoleViewer ||
		inv["_path"] != "/graph/v1beta1/drives/s$p/items/s$p!item1/invite" {
		t.Errorf("invite: %v", inv)
	}
}

func TestShareRefusals(t *testing.T) {
	two := map[string]string{"id": "u-h2", "displayName": "Hugo Other", "mail": "hugo.o@co.example", "onPremisesSamAccountName": "hugoo"}
	f := &fakeFiles{users: []map[string]string{hugo, two}}
	cs := shareSession(t, f, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		t.Error("asked, though it should have been refused first")
		return &mcp.ElicitResult{Action: "accept"}, nil
	})
	for name, c := range map[string][3]string{
		"project space": {"/remote.php/dav/spaces/t/x.md", "hugo", ""},
		"whole space":   {"/remote.php/dav/spaces/p/", "hugo", ""},
		"ambiguous":     {"/remote.php/dav/spaces/p/x.md", "Hugo E", ""},
		"too short":     {"/remote.php/dav/spaces/p/x.md", "hu", ""},
		"bad access":    {"/remote.php/dav/spaces/p/x.md", "hugo", "own"},
		"not a path":    {"../etc", "hugo", ""},
	} {
		if r := share(t, cs, c[0], c[1], c[2]); !r.IsError {
			t.Errorf("%s: not refused (%q)", name, resultText(r))
		}
	}
	if len(f.invites) != 0 {
		t.Fatal("something was shared")
	}
	// An exact username picks one of several matches.
	f2 := &fakeFiles{users: []map[string]string{hugo, two}}
	cs = shareSession(t, f2, func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "accept"}, nil
	})
	if r := share(t, cs, "/remote.php/dav/spaces/p/x.md", "hugoo", ""); r.IsError || len(f2.invites) != 1 {
		t.Fatalf("exact username: %q", resultText(r))
	}
}

// The file client posts only the share invite.
func TestOnlyPostIsTheInvite(t *testing.T) {
	c := opencloud.New("http://127.0.0.1:1")
	if err := c.Share(context.Background(), "t", opencloud.Item{ID: "a!b", Drive: "a"}, "u", "some-owner-role"); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("other role: %v", err)
	}
}
