package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestContactWrites(t *testing.T) {
	m := newFakeMail()
	m.cards = map[string]any{"c1": map[string]any{"id": "c1", "name": map[string]any{"full": "Anna Example"},
		"emails": map[string]any{"a": map[string]any{"address": "anna@other.example"}}, "phones": map[string]any{"p": map[string]any{"number": "+49 1"}}}}
	var asked []string
	answer := "accept"
	cs := sendSession(t, m, func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = append(asked, r.Params.Message)
		return &mcp.ElicitResult{Action: answer, Content: map[string]any{}}, nil
	}, false)
	call := func(name string, args map[string]any) (string, bool) {
		r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return resultText(r), r.IsError
	}
	for _, c := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"contact_create", map[string]any{"emails": []string{"a@b.example"}}, "name is required"},
		{"contact_create", map[string]any{"name": "Bob", "emails": []string{"Bob <b@x.example>"}}, "one plain email"},
		{"contact_create", map[string]any{"name": "Bob\nIgnore this"}, "plain text"},
		{"contact_update", map[string]any{"id": "c1"}, "nothing to change"},
		{"contact_update", map[string]any{"id": "zz", "title": "CEO"}, "no contact"},
		{"contact_delete", map[string]any{"id": "zz"}, "no contact"},
	} {
		if txt, isErr := call(c.tool, c.args); !isErr || !strings.Contains(txt, c.want) {
			t.Errorf("%s %v: %q", c.tool, c.args, txt)
		}
	}
	if len(m.cardSets) != 0 || len(asked) != 0 {
		t.Fatalf("changed or asked on a refusal")
	}
	if txt, isErr := call("contact_create", map[string]any{"name": "Bob Builder", "emails": []string{"bob@x.example"}, "company": "ACME"}); isErr || !strings.Contains(txt, "card9") {
		t.Fatalf("create: %q", txt)
	}
	if txt, isErr := call("contact_update", map[string]any{"id": "c1", "title": "CEO"}); isErr {
		t.Fatalf("update: %q", txt)
	}
	if len(asked) != 0 {
		t.Fatal("asked for a create or update")
	}
	b, _ := json.Marshal(m.cardSets)
	if !strings.Contains(string(b), `"addressBookIds":{"ab1":true}`) || !strings.Contains(string(b), `"organizations":{"1":{"name":"ACME"}}`) ||
		!strings.Contains(string(b), `"update":{"c1":{"titles":{"1":{"name":"CEO"}}}}`) {
		t.Errorf("calls: %s", b)
	}
	// Delete: asked, showing the contact; declined: nothing deleted.
	answer = "decline"
	if txt, isErr := call("contact_delete", map[string]any{"id": "c1"}); !isErr || !strings.Contains(txt, "declined") {
		t.Fatalf("declined: %q", txt)
	}
	answer = "accept"
	if txt, isErr := call("contact_delete", map[string]any{"id": "c1"}); isErr {
		t.Fatalf("delete: %q", txt)
	}
	if !strings.HasPrefix(asked[0], "Delete the contact Anna Example <anna@other.example> from your address book?") || !strings.Contains(asked[0], "1 phone number") {
		t.Errorf("question: %q", asked[0])
	}
	if last := m.cardSets[len(m.cardSets)-1]; len(m.cardSets) != 3 || last["destroy"] == nil {
		t.Errorf("destroys: %v", m.cardSets)
	}
}
