package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// opencode 2.4.1 accepts questions without fields by itself (13 ms after
// they were asked, live 2026-11-26). An accept that fast is refused,
// unless the client is exempt on purpose.
func TestAcceptTooFastIsRefused(t *testing.T) {
	saved := confirmNow
	t.Cleanup(func() { confirmNow = saved })
	frozen := time.Now()
	confirmNow = func() time.Time { return frozen }

	run := func(fastOK []string, action string) *mcp.CallToolResult {
		s := mcp.NewServer(&mcp.Implementation{Name: "t"}, nil)
		(&Tools{Experiments: map[string]bool{"confirm": true}, Access: Access{All: true}, ConfirmFastOK: fastOK}).Register(s)
		ct, st := mcp.NewInMemoryTransports()
		s.Connect(context.Background(), st, nil)
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "c"}, &mcp.ClientOptions{ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			return &mcp.ElicitResult{Action: action}, nil
		}}).Connect(context.Background(), ct, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer cs.Close()
		r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "confirm_test"})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := run(nil, "accept"); !r.IsError || !strings.Contains(resultText(r), "faster than a person") {
		t.Fatalf("instant accept: %q", resultText(r))
	}
	if r := run(nil, "decline"); r.IsError {
		t.Fatalf("instant decline must stay a decline: %q", resultText(r))
	}
	// The test session has no login client (""): exempt it explicitly.
	if r := run([]string{""}, "accept"); r.IsError {
		t.Fatalf("exempt client refused: %q", resultText(r))
	}
}
