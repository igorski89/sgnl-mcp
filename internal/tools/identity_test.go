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

func TestMailboxSize(t *testing.T) {
	f := newFakeMail()
	cs := sendSession(t, f, nil, false)
	txt, isErr := callTool(t, cs, "mailbox_size", nil)
	if isErr || !strings.Contains(txt, `"used_mb":300`) || !strings.Contains(txt, `"limit_mb":1024`) || !strings.Contains(txt, `"percent_used":29`) ||
		strings.Contains(txt, "messages_limit") || !strings.Contains(txt, `"largest_folders":[`) {
		t.Errorf("mailbox_size: %s", txt)
	}
}

func TestMailLabels(t *testing.T) {
	f := newFakeMail()
	f.draft["keywords"] = map[string]bool{"$seen": true, "invoices": true, "$label1": true}
	cs := sendSession(t, f, nil, false)
	txt, isErr := callTool(t, cs, "mail_flag", map[string]any{"ids": []string{"d1"}, "add_labels": []string{"Paid", "paid"}, "remove_labels": []string{"todo"}})
	if isErr {
		t.Fatalf("flag: %s", txt)
	}
	b, _ := json.Marshal(f.updates[len(f.updates)-1]["update"])
	if string(b) != `{"d1":{"keywords/paid":true,"keywords/todo":null}}` {
		t.Errorf("patch: %s", b)
	}
	for _, bad := range []map[string]any{
		{"add_labels": []string{"$seen"}}, {"add_labels": []string{"a/b"}}, {"remove_labels": []string{"$flagged"}},
		{"add_labels": []string{"x"}, "remove_labels": []string{"X"}}, {"add_labels": []string{"has space"}},
	} {
		bad["ids"] = []string{"d1"}
		n := len(f.updates)
		if txt, isErr := callTool(t, cs, "mail_flag", bad); !isErr || len(f.updates) != n {
			t.Errorf("%v allowed: %s", bad, txt)
		}
	}
	if txt, _ := callTool(t, cs, "mail_read", map[string]any{"id": "d1"}); !strings.Contains(txt, `"labels":["invoices"]`) {
		t.Errorf("read labels: %s", txt)
	}
}
