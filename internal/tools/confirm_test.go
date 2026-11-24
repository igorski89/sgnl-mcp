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
	(&Tools{Experiments: map[string]bool{"confirm": true}, Access: Access{All: true}}).Register(s)
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
				return &mcp.ElicitResult{Action: action, Content: map[string]any{"confirmed": true}}, nil
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
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirmed": true}}, nil
	}
	cs := confirmSession(t, "2026-07-28", yes, true)
	first, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "confirm_test"})
	if err != nil || !first.NeedsInput() || first.RequestState == "" {
		t.Fatalf("first call should ask for input: %v %+v", err, first)
	}
	accept := mcp.InputResponseMap{"confirm": &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirmed": true}}}
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

// Some clients accept a question by themselves (an empty answer): without
// the ticked box, an accept is asked once more, then counts as a no.
func TestConfirmNeedsTheBoxTicked(t *testing.T) {
	ctx := context.Background()
	for name, content := range map[string]map[string]any{"empty": nil, "unticked": {"confirmed": false}, "text": {"confirmed": "true"}} {
		cs := confirmSession(t, "2026-07-28", func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			return &mcp.ElicitResult{Action: "accept", Content: content}, nil
		}, false)
		// The SDK client may already reject an answer that doesn't fit the
		// form; either way it must not count as a yes.
		r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "confirm_test"})
		if err == nil && !strings.Contains(resultText(r), "without ticking the box") {
			t.Errorf("%s accept: got %q", name, resultText(r))
		}
	}
	// And at the gateway itself (a client that sends whatever it likes):
	cs := confirmSession(t, "2026-07-28", func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "accept"}, nil
	}, true)
	first, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "confirm_test"})
	if err != nil || !first.NeedsInput() {
		t.Fatalf("expected a question: %v", err)
	}
	bare := mcp.InputResponseMap{"confirm": &mcp.ElicitResult{Action: "accept", Content: map[string]any{}}}
	second, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "confirm_test", RequestState: first.RequestState, InputResponses: bare})
	if err != nil || !second.NeedsInput() {
		t.Fatalf("a bare accept should ask once more: %v %q", err, resultText(second))
	}
	q, _ := second.InputRequests["confirm"].(*mcp.ElicitParams)
	if q == nil || !strings.Contains(q.Message, "wasn't ticked") {
		t.Fatalf("the second question should say the box wasn't ticked: %+v", q)
	}
	r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "confirm_test", RequestState: second.RequestState, InputResponses: bare})
	if err != nil || !r.IsError || !strings.Contains(resultText(r), "without ticking the box") {
		t.Errorf("a second bare accept: %v %q", err, resultText(r))
	}
	// Ticking it on the second asking goes ahead.
	r, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "confirm_test", RequestState: second.RequestState,
		InputResponses: mcp.InputResponseMap{"confirm": &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirmed": true}}}})
	if err != nil || !strings.Contains(resultText(r), "accepted") {
		t.Errorf("ticked on the second asking: %v %q", err, resultText(r))
	}
	// The first question's state can't be passed off as the second's.
	forged := strings.Replace(first.RequestState, ".1.", ".2.", 1)
	r, _ = cs.CallTool(ctx, &mcp.CallToolParams{Name: "confirm_test", RequestState: forged, InputResponses: bare})
	if r == nil || !r.IsError || !strings.Contains(resultText(r), "doesn't match") {
		t.Errorf("forged state: %q", resultText(r))
	}
}
