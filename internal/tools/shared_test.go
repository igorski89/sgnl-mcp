package tools

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSharedMailboxes(t *testing.T) {
	m := newFakeMail()
	m.shared = true
	m.draft["from"] = []map[string]any{{"email": "team@co.example"}}
	m.draft["to"] = []map[string]any{{"email": "colleague@co.example"}}
	delete(m.draft, "cc")
	var asked string
	cs := trashSession(t, m, func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = r.Params.Message
		return &mcp.ElicitResult{Action: "decline"}, nil
	})
	call := func(name string, args map[string]any) *mcp.CallToolResult {
		r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	// Own mailbox: the shared ones are listed.
	r := call("mailbox_list", map[string]any{})
	b, _ := json.Marshal(r.StructuredContent)
	if !strings.Contains(string(b), `{"name":"alerts@co.example","read_only":true}`) || !strings.Contains(string(b), `{"can_send":true,"name":"team@co.example"}`) {
		t.Fatalf("accounts: %s", b)
	}
	// A tool in the shared mailbox works on its account only.
	m.acctIDs = nil
	if r := call("mail_search", map[string]any{"query": "x", "account": "Team@co.example"}); r.IsError {
		t.Fatal(resultText(r))
	}
	if len(m.acctIDs) == 0 || slices.ContainsFunc(m.acctIDs, func(a string) bool { return a != "T" }) {
		t.Fatalf("accounts used: %v", m.acctIDs)
	}
	// Sending from it says AS whom on the first line.
	call("mail_send", map[string]any{"id": "d1", "account": "team@co.example"})
	if first, _, _ := strings.Cut(asked, "\n"); !strings.HasPrefix(first, "Send AS team@co.example") {
		t.Errorf("first line: %q", first)
	}
	// An unknown name is refused, naming the person's shared mailboxes.
	if r := call("mail_search", map[string]any{"query": "x", "account": "hr@co.example"}); !r.IsError || !strings.Contains(resultText(r), "team@co.example") {
		t.Errorf("unknown account: %q", resultText(r))
	}
}
