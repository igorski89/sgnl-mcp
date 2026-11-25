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
	"slices"
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

var errUntrustedClient = fmt.Errorf("this needs the person's confirmation, and this AI app isn't trusted yet to ask them (it might answer by itself): send the draft from your mail app, or use Claude Code")

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

// minAnswer: no person accepts a question faster than this.
const minAnswer = 1500 * time.Millisecond

// confirmNow: the clock (a variable for tests).
var confirmNow = time.Now

// confirmState binds an answer to this tool, these exact arguments and this
// person, until a deadline: an accept can't be replayed for another action.
func confirmState(tool string, args []byte, who, bind string, until int64) string {
	m := hmac.New(sha256.New, confirmKey)
	fmt.Fprintf(m, "%s\x00%s\x00%d\x00%s\x00", tool, who, until, bind)
	m.Write(canonical(args))
	return strconv.FormatInt(until, 10) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
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
// and ok says whether the person pressed Accept (Decline or Esc: no).
//
// bind: what the person was shown, beyond the arguments (e.g. the exact
// mail and its recipients); the answer only counts if it is unchanged.
//
// The question is a plain Accept/Decline (igor, 2026-11-24: a box to tick
// before Accept confused people, and a client that answers by itself could
// tick it as easily). Clients without elicitation are refused.
func (t *Tools) confirm(ctx context.Context, req *mcp.CallToolRequest, message, bind string) (res *mcp.CallToolResult, ok bool, err error) {
	if req == nil || req.Session == nil || req.Params == nil {
		return nil, false, errNoConfirm
	}
	if len(t.ConfirmClients) > 0 {
		c, _ := auth.Person(ctx)
		if !slices.Contains(t.ConfirmClients, c.Client()) {
			slog.Warn("confirmation refused: client not trusted to ask the person", "user", c.Username, "client", c.Client())
			return nil, false, errUntrustedClient
		}
	}
	if p := req.Session.InitializeParams(); p == nil || p.Capabilities == nil || p.Capabilities.Elicitation == nil {
		return nil, false, errNoConfirm
	}
	who := ""
	if c, err := auth.Person(ctx); err == nil {
		who = c.Username
	}
	if req.Params.InputResponses == nil {
		until := confirmNow().Add(confirmTTL).UnixMilli()
		return &mcp.CallToolResult{
			InputRequests: mcp.InputRequestMap{"confirm": &mcp.ElicitParams{
				Message:         message,
				RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			}},
			RequestState: confirmState(req.Params.Name, req.Params.Arguments, who, bind, until),
		}, false, nil
	}
	ts, _, _ := strings.Cut(req.Params.RequestState, ".")
	until, perr := strconv.ParseInt(ts, 10, 64)
	want := confirmState(req.Params.Name, req.Params.Arguments, who, bind, until)
	if perr != nil || !hmac.Equal([]byte(want), []byte(req.Params.RequestState)) {
		return nil, false, fmt.Errorf("the confirmation doesn't match this request, or what it shows has changed since: ask again")
	}
	now := confirmNow().UnixMilli()
	if now > until {
		return nil, false, fmt.Errorf("the confirmation expired: ask again")
	}
	action := "none"
	if r, _ := req.Params.InputResponses["confirm"].(*mcp.ElicitResult); r != nil {
		action = r.Action
	}
	ok = action == "accept"
	// A yes faster than anyone can read the question came from the client,
	// not the person (some clients answer such questions by themselves).
	// Clients the person runs without approvals on purpose are exempt.
	asked := until - confirmTTL.Milliseconds()
	if ok && now-asked < minAnswer.Milliseconds() {
		c, _ := auth.Person(ctx)
		if !slices.Contains(t.ConfirmFastOK, c.Client()) {
			slog.Warn("confirmation refused: answered too fast to be a person", "tool", req.Params.Name, "user", who, "client", c.Client(), "ms", now-asked)
			return nil, false, fmt.Errorf("the answer came back faster than a person can read the question, so the app likely answered by itself: nothing was done. Ask the person to try again and read the question")
		}
	}
	slog.Info("person answered", "tool", req.Params.Name, "user", who, "action", action,
		"outcome", map[bool]string{true: "accepted", false: "declined"}[ok])
	return nil, ok, nil
}

// ---- confirm_test (experiment: SGNL_MCP_EXPERIMENTS=confirm)

type ConfirmTestOut struct {
	Answer string `json:"answer" jsonschema:"what the person chose: accepted or declined"`
	Note   string `json:"note"`
}

func (t *Tools) confirmTest(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ConfirmTestOut, error) {
	res, ok, err := t.confirm(ctx, req, "SGNL MCP test: approve this test question? Nothing happens either way.", "")
	if err != nil || res != nil {
		return res, ConfirmTestOut{}, err
	}
	a := "declined"
	if ok {
		a = "accepted"
	}
	return nil, ConfirmTestOut{a, "test only: nothing was changed"}, nil
}
