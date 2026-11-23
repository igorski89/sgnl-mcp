package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type elicitFn = func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error)

func confirmSession(t *testing.T, version string, h elicitFn, manual bool) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	s := mcp.NewServer(&mcp.Implementation{Name: "t"}, nil)
	(&Tools{Experiments: map[string]bool{"confirm": true}}).Register(s)
	ct, st := mcp.NewInMemoryTransports()
	if _, err := s.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "c"}, &mcp.ClientOptions{ElicitationHandler: h,
		MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: manual}})
	cs, err := c.Connect(ctx, ct, &mcp.ClientSessionOptions{ProtocolVersion: version})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func resultText(r *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// The person's answer travels client -> gateway, never through the model;
// the tool acts only on an explicit accept, on current and older clients,
// and refuses when the client can't ask at all.
func TestConfirmAsksThePerson(t *testing.T) {
	ctx := context.Background()
	for _, v := range []string{"2026-07-28", "2025-06-18"} {
		for action, want := range map[string]string{"accept": "accepted", "decline": "declined", "cancel": "declined"} {
			asked := 0
			cs := confirmSession(t, v, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
				asked++
				return &mcp.ElicitResult{Action: action}, nil
			}, false)
			r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "confirm_test"})
			if err != nil || r.IsError || !strings.Contains(resultText(r), want) || asked != 1 {
				t.Errorf("%s %s: got %v %q, asked %d times", v, action, err, resultText(r), asked)
			}
		}
		r, err := confirmSession(t, v, nil, false).CallTool(ctx, &mcp.CallToolParams{Name: "confirm_test"})
		if err != nil || !r.IsError || !strings.Contains(resultText(r), "confirmation") {
			t.Errorf("%s, client without elicitation: got %v %q", v, err, resultText(r))
		}
	}
}

// An "accept" made up by the caller, without the gateway's signed state
// for this exact request, is refused.
func TestConfirmRefusesForgedAnswers(t *testing.T) {
	ctx := context.Background()
	yes := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "accept"}, nil
	}
	cs := confirmSession(t, "2026-07-28", yes, true)
	first, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "confirm_test"})
	if err != nil || !first.NeedsInput() || first.RequestState == "" {
		t.Fatalf("first call should ask for input: %v %+v", err, first)
	}
	accept := mcp.InputResponseMap{"confirm": &mcp.ElicitResult{Action: "accept"}}
	for name, state := range map[string]string{
		"no state":      "",
		"made up":       "9999999999.AAAA",
		"other request": first.RequestState + "x",
	} {
		r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "confirm_test", InputResponses: accept, RequestState: state})
		if err != nil || !r.IsError || strings.Contains(resultText(r), "accepted") {
			t.Errorf("%s: forged accept not refused: %v %q", name, err, resultText(r))
		}
	}
	r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "confirm_test", InputResponses: accept, RequestState: first.RequestState})
	if err != nil || r.IsError || !strings.Contains(resultText(r), "accepted") {
		t.Errorf("genuine answer refused: %v %q", err, resultText(r))
	}
}
