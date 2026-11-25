package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/igorski89/sgnl-mcp/internal/jmap"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func trashSession(t *testing.T, m *fakeMail, h elicitFn) *mcp.ClientSession {
	t.Helper()
	srv := m.serve(t)
	s := mcp.NewServer(&mcp.Implementation{Name: "t"}, nil)
	(&Tools{Auth: fakeTokens{}, JMAP: jmap.New(srv.URL + "/session"), Access: Access{All: true}}).Register(s)
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

func TestTrashOnlyWithThePersonsYes(t *testing.T) {
	mails := []any{
		map[string]any{"id": "m1", "subject": "Newsletter", "from": []map[string]any{{"email": "news@x.example"}}, "mailboxIds": map[string]bool{"in": true}},
		map[string]any{"id": "m2", "subject": "Old", "from": []map[string]any{{"email": "a@co.example"}}, "mailboxIds": map[string]bool{"in": true}},
		map[string]any{"id": "m3", "subject": "Gone", "mailboxIds": map[string]bool{"tr": true}}}
	m := newFakeMail()
	m.list = mails
	var asked string
	cs := trashSession(t, m, func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = r.Params.Message
		return &mcp.ElicitResult{Action: "decline"}, nil
	})
	trash := func(ids ...string) *mcp.CallToolResult {
		r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "mail_trash", Arguments: map[string]any{"ids": ids}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := trash("m1", "m2", "m3"); !r.IsError || len(m.updates) != 0 {
		t.Fatalf("declined, yet %q, %v", resultText(r), m.updates)
	}
	first, _, _ := strings.Cut(asked, "\n")
	if !strings.Contains(first, "Move 2 mail(s) to Trash") || !strings.Contains(first, `"Newsletter" from news@x.example`) {
		t.Errorf("first line: %q", first)
	}
	if !strings.Contains(asked, "restore them from Trash in your mail app.") {
		t.Errorf("restore hint: %q", asked)
	}

	m2 := newFakeMail()
	m2.list = mails
	cs = trashSession(t, m2, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "accept"}, nil
	})
	r, _ := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "mail_trash", Arguments: map[string]any{"ids": []string{"m1", "m2", "m3"}}})
	if r.IsError || len(m2.updates) != 1 {
		t.Fatalf("accepted: %q, %v", resultText(r), m2.updates)
	}
	up := m2.updates[0]["update"].(map[string]any)
	if len(up) != 2 || up["m3"] != nil {
		t.Fatalf("moved: %v", up)
	}
	if mb := up["m1"].(map[string]any)["mailboxIds"].(map[string]any); len(mb) != 1 || mb["tr"] != true {
		t.Fatalf("not to Trash only: %v", mb)
	}
	many := make([]string, maxTrash+1)
	for i := range many {
		many[i] = string(rune('a'+i%26)) + strings.Repeat("x", i)
	}
	if r, _ := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "mail_trash", Arguments: map[string]any{"ids": many}}); !r.IsError {
		t.Error("too many: not refused")
	}
}
