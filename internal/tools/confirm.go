package tools

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
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
func confirmState(tool string, args []byte, who string, until int64) string {
	m := hmac.New(sha256.New, confirmKey)
	fmt.Fprintf(m, "%s\x00%s\x00%d\x00", tool, who, until)
	m.Write(args)
	return strconv.FormatInt(until, 10) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// confirm asks the person and reports whether they accepted. The first
// call returns a result asking the client for input (multi round-trip,
// SEP-2322; for older clients the SDK asks directly and calls again); the
// caller returns it as is. On the call that carries the answer, res is nil
// and ok says whether the person explicitly accepted.
func confirm(ctx context.Context, req *mcp.CallToolRequest, message string) (res *mcp.CallToolResult, ok bool, err error) {
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
	if req.Params.InputResponses == nil {
		until := time.Now().Add(confirmTTL).Unix()
		return &mcp.CallToolResult{
			InputRequests: mcp.InputRequestMap{"confirm": &mcp.ElicitParams{
				Message:         message,
				RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			}},
			RequestState: confirmState(req.Params.Name, req.Params.Arguments, who, until),
		}, false, nil
	}
	ts, _, _ := strings.Cut(req.Params.RequestState, ".")
	until, perr := strconv.ParseInt(ts, 10, 64)
	want := confirmState(req.Params.Name, req.Params.Arguments, who, until)
	if perr != nil || !hmac.Equal([]byte(want), []byte(req.Params.RequestState)) {
		return nil, false, fmt.Errorf("the confirmation doesn't match this request: ask again")
	}
	if time.Now().Unix() > until {
		return nil, false, fmt.Errorf("the confirmation expired: ask again")
	}
	r, _ := req.Params.InputResponses["confirm"].(*mcp.ElicitResult)
	return nil, r != nil && r.Action == "accept", nil
}

// ---- confirm_test (experiment: SGNL_MCP_EXPERIMENTS=confirm)

type ConfirmTestOut struct {
	Answer string `json:"answer" jsonschema:"what the person chose: accepted or declined"`
	Note   string `json:"note"`
}

func (t *Tools) confirmTest(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ConfirmTestOut, error) {
	res, ok, err := confirm(ctx, req, "SGNL MCP test: approve this test question? Nothing happens either way.")
	if err != nil || res != nil {
		return res, ConfirmTestOut{}, err
	}
	a := "declined"
	if ok {
		a = "accepted"
	}
	return nil, ConfirmTestOut{a, "test only: nothing was changed"}, nil
}
