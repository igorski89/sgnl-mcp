package tools

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/igorski89/sgnl-mcp/internal/auth"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Confirmation by the person, not the model: the gateway asks through MCP
// elicitation; the client shows the question to the person and the answer
// comes back to the gateway, never through the model. A hijacked model can
// call a tool but cannot answer the question.

var errNoConfirm = fmt.Errorf("this needs the person's confirmation, and their app can't ask for it (no MCP elicitation support)")

// confirmKey signs pending confirmations; new per process, so a restart
// drops them (the person is simply asked again).
var confirmKey = func() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}()

const confirmTTL = 10 * time.Minute

// confirmState binds an answer to this tool, these exact arguments and this
// person, until a deadline: an accept can't be replayed for another action.
// again: this is the second asking (after an Accept without the box ticked).
func confirmState(tool string, args []byte, who, bind string, until int64, again bool) string {
	m := hmac.New(sha256.New, confirmKey)
	fmt.Fprintf(m, "%s\x00%s\x00%d\x00%t\x00%s\x00", tool, who, until, again, bind)
	m.Write(canonical(args))
	mark := "1"
	if again {
		mark = "2"
	}
	return strconv.FormatInt(until, 10) + "." + mark + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// canonical: the arguments with keys sorted and spacing normalised, so a
// client that re-serialises them on the second call isn't refused.
func canonical(args []byte) []byte {
	var v any
	if json.Unmarshal(args, &v) != nil {
		return args
	}
	b, err := json.Marshal(v)
	if err != nil {
		return args
	}
	return b
}

// confirm asks the person and reports whether they accepted. The first
// call returns a result asking the client for input (multi round-trip,
// SEP-2322; for older clients the SDK asks directly and calls again); the
// caller returns it as is. On the call that carries the answer, res is nil
// and ok says whether the person explicitly accepted.
//
// bind: what the person was shown, beyond the arguments (e.g. the exact
// mail and its recipients); the answer only counts if it is unchanged.
//
// check: the label of a box the person must tick. Some clients answer a
// bare Accept/Decline question by themselves (Codex in its permissive
// modes accepts an empty form); an accept without the ticked box counts
// as a no. The first such accept asks once more, saying the box wasn't
// ticked; the second ends with errNotTicked (never a loop, for clients
// that keep accepting by themselves).
func confirm(ctx context.Context, req *mcp.CallToolRequest, message, bind, check string) (res *mcp.CallToolResult, ok bool, err error) {
	if req == nil || req.Session == nil || req.Params == nil {
		return nil, false, errNoConfirm
	}
	if p := req.Session.InitializeParams(); p == nil || p.Capabilities == nil || p.Capabilities.Elicitation == nil {
		return nil, false, errNoConfirm
	}
	who := ""
	if c, err := auth.Person(ctx); err == nil {
		who = c.Username
	}
	ask := func(msg string, again bool) *mcp.CallToolResult {
		until := time.Now().Add(confirmTTL).Unix()
		return &mcp.CallToolResult{
			InputRequests: mcp.InputRequestMap{"confirm": &mcp.ElicitParams{
				Message: msg,
				RequestedSchema: map[string]any{"type": "object", "required": []string{"confirmed"},
					"properties": map[string]any{"confirmed": map[string]any{"type": "boolean", "title": check, "default": false}}},
			}},
			RequestState: confirmState(req.Params.Name, req.Params.Arguments, who, bind, until, again),
		}
	}
	if req.Params.InputResponses == nil {
		return ask(message, false), false, nil
	}
	ts, rest, _ := strings.Cut(req.Params.RequestState, ".")
	mark, _, _ := strings.Cut(rest, ".")
	again := mark == "2"
	until, perr := strconv.ParseInt(ts, 10, 64)
	want := confirmState(req.Params.Name, req.Params.Arguments, who, bind, until, again)
	if perr != nil || !hmac.Equal([]byte(want), []byte(req.Params.RequestState)) {
		return nil, false, fmt.Errorf("the confirmation doesn't match this request, or what it shows has changed since: ask again")
	}
	if time.Now().Unix() > until {
		return nil, false, fmt.Errorf("the confirmation expired: ask again")
	}
	r, _ := req.Params.InputResponses["confirm"].(*mcp.ElicitResult)
	action, ticked := "none", false
	if r != nil {
		action, ticked = r.Action, r.Content["confirmed"] == true
	}
	ok = action == "accept" && ticked
	outcome := map[bool]string{true: "accepted", false: "declined"}[ok]
	if action == "accept" && !ticked {
		outcome = "accepted without ticking the box"
	}
	slog.Info("person answered", "tool", req.Params.Name, "user", who, "action", action, "ticked", ticked, "again", again, "outcome", outcome)
	if action == "accept" && !ticked {
		if !again {
			return ask("The box wasn't ticked, so nothing was done yet. Tick \""+check+"\" and press Accept to go ahead, or Decline.\n\n"+message, true), false, nil
		}
		return nil, false, errNotTicked
	}
	return nil, ok, nil
}

var errNotTicked = fmt.Errorf("not done: the person pressed Accept twice without ticking the box, which counts as a no. Nothing was changed; if they meant yes, call the tool again and they tick the box")

// ---- confirm_test (experiment: SGNL_MCP_EXPERIMENTS=confirm)

type ConfirmTestOut struct {
	Answer string `json:"answer" jsonschema:"what the person chose: accepted or declined"`
	Note   string `json:"note"`
}

func (t *Tools) confirmTest(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ConfirmTestOut, error) {
	res, ok, err := confirm(ctx, req, "SGNL MCP test: approve this test question? Nothing happens either way.", "", "Yes, I approve this test")
	if err != nil || res != nil {
		return res, ConfirmTestOut{}, err
	}
	a := "declined"
	if ok {
		a = "accepted"
	}
	return nil, ConfirmTestOut{a, "test only: nothing was changed"}, nil
}
