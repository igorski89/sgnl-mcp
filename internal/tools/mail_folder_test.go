package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMailFolders(t *testing.T) {
	m := newFakeMail()
	m.shared = true
	var asked []string
	cs := sendSession(t, m, func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = append(asked, r.Params.Message)
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{}}, nil
	}, false)
	call := func(name string, args map[string]any) (string, bool) {
		r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return resultText(r), r.IsError
	}
	// Refused before anything is changed.
	for _, c := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"mail_folder_create", map[string]any{"name": "test"}, "already a folder"},
		{"mail_folder_create", map[string]any{"name": "a/b"}, "can't contain"},
		{"mail_folder_create", map[string]any{"name": "x‮y"}, "plain text"},
		{"mail_folder_create", map[string]any{"name": "x", "parent": "Nope"}, "no folder"},
		{"mail_folder_rename", map[string]any{"folder": "Inbox", "name": "Post"}, "standard folder"},
		{"mail_folder_rename", map[string]any{"folder": "Test", "name": "Inbox"}, "already a folder"},
	} {
		if txt, isErr := call(c.tool, c.args); !isErr || !strings.Contains(txt, c.want) {
			t.Errorf("%s %v: %q", c.tool, c.args, txt)
		}
	}
	if len(m.folders) != 0 || len(asked) != 0 {
		t.Fatalf("changed or asked on a refusal: %v %v", m.folders, asked)
	}
	// Own mailbox: no question.
	if txt, isErr := call("mail_folder_create", map[string]any{"name": "Clients", "parent": "Inbox"}); isErr || !strings.Contains(txt, `"id":"new1"`) {
		t.Fatalf("create: %q", txt)
	}
	if txt, isErr := call("mail_folder_rename", map[string]any{"folder": "Test", "name": "Old tests"}); isErr {
		t.Fatalf("rename: %q", txt)
	}
	if len(asked) != 0 {
		t.Fatalf("asked in the own mailbox: %v", asked)
	}
	b, _ := json.Marshal(m.folders)
	if !strings.Contains(string(b), `"create":{"f":{"name":"Clients","parentId":"in"}}`) || !strings.Contains(string(b), `"update":{"te":{"name":"Old tests"}}`) {
		t.Errorf("calls: %s", b)
	}
	// Shared mailbox: asked, named on the first line.
	if txt, isErr := call("mail_folder_create", map[string]any{"name": "Leads", "account": "team@co.example"}); isErr {
		t.Fatalf("shared create: %q", txt)
	}
	if len(asked) != 1 || !strings.HasPrefix(asked[0], `Create the folder "Leads" at the top level in the SHARED mailbox team@co.example?`) {
		t.Errorf("asked: %v", asked)
	}
}
