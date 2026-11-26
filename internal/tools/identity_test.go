package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return resultText(r), r.IsError
}

func TestIdentitiesAndSendingFromAnAlias(t *testing.T) {
	asPerson(t, "igor@co.example")
	f := newFakeMail()
	var asked string
	cs := sendSession(t, f, func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = r.Params.Message
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{}}, nil
	}, false)
	txt, isErr := callTool(t, cs, "identity_list", nil)
	if isErr || !strings.Contains(txt, `"identities":[{"email":"igor@co.example","main":true,"name":"Igor"},{"email":"sales@co.example","name":"Sales"}]`) {
		t.Errorf("identity_list: %s", txt)
	}
	// A draft from the alias.
	if txt, isErr := callTool(t, cs, "mail_draft_create", map[string]any{"to": []string{"a@co.example"}, "subject": "s", "body": "b", "from": "Sales@co.example"}); isErr {
		t.Fatalf("draft: %s", txt)
	}
	b, _ := json.Marshal(f.updates[len(f.updates)-1]["create"])
	if !strings.Contains(string(b), `"from":[{"email":"sales@co.example","name":"Sales"}]`) {
		t.Errorf("draft from: %s", b)
	}
	// Not an address of theirs: refused, nothing saved.
	n := len(f.updates)
	if txt, isErr := callTool(t, cs, "mail_draft_create", map[string]any{"to": []string{"a@co.example"}, "body": "b", "from": "ceo@co.example"}); !isErr || !strings.Contains(txt, "isn't one of your sending addresses") {
		t.Errorf("foreign from: %s", txt)
	}
	if len(f.updates) != n {
		t.Error("a draft was saved from a foreign address")
	}
	// Sending a draft from the alias: the first line says so; that identity is used.
	f.draft["from"] = []map[string]any{{"email": "sales@co.example"}}
	if r := send(t, cs); r.IsError {
		t.Fatalf("send: %s", resultText(r))
	}
	if !strings.HasPrefix(asked, `Send "Offer" FROM sales@co.example to colleague@co.example`) {
		t.Errorf("first line: %s", asked)
	}
	if sub := f.submissions[0]["create"].(map[string]any)["s"].(map[string]any); sub["identityId"] != "id2" {
		t.Errorf("identity: %v", sub)
	}
}
