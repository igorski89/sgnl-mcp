// Package confirm: confirmation by the person, not the model. The gateway
// asks through MCP elicitation; the client shows the question to the
// person and the answer comes back to the gateway, never through the
// model. A hijacked model can call a tool but cannot answer the question.
// Used by the person's tools and by the admin endpoint alike.
package confirm

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/igorski89/sgnl-mcp/internal/audit"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/igorski89/sgnl-mcp/internal/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var ErrNoConfirm = fmt.Errorf("this needs the person's confirmation, and their app can't ask for it (no MCP elicitation support)")

var ErrUntrustedClient = fmt.Errorf("this needs the person's confirmation, and this AI app isn't trusted yet to ask them (it might answer by itself): use Claude Code, or do it in the app itself (a draft can be sent from the mail app)")

// key signs pending confirmations; new per process, so a restart drops
// them (the person is simply asked again).
var key = func() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}()

const TTL = 10 * time.Minute

// MinAnswer: no person accepts a question faster than this.
const MinAnswer = 1500 * time.Millisecond

// Now: the clock (a variable for tests).
var Now = time.Now

// Asker: who may be asked.
type Asker struct {
	// Clients: the login clients (azp / client_id) whose apps are trusted
	// to show a confirmation to the person rather than answer it
	// themselves; others can't do confirmed actions. Empty: all.
	Clients []string
	// FastOK: trusted clients whose instant answers are accepted (the
	// person runs them without approvals on purpose).
	FastOK []string
}

// state binds an answer to this tool, these exact arguments and this
// person, until a deadline: an accept can't be replayed for another action.
func state(tool string, args []byte, who, bind string, until int64) string {
	m := hmac.New(sha256.New, key)
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

// Ask asks the person and reports whether they accepted. The first call
// returns a result asking the client for input (multi round-trip,
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
func (a *Asker) Ask(ctx context.Context, req *mcp.CallToolRequest, message, bind string) (res *mcp.CallToolResult, ok bool, err error) {
	return a.ask(ctx, req, message, bind, "", "")
}

// AskTyped: as Ask, and the person must also type want (e.g. a username)
// into the field named field. The field is never prefilled; only exactly
// want counts as a yes. For the actions a plain Accept is too little for.
func (a *Asker) AskTyped(ctx context.Context, req *mcp.CallToolRequest, message, bind, field, want string) (res *mcp.CallToolResult, ok bool, err error) {
	if field == "" || want == "" {
		return nil, false, fmt.Errorf("confirm: nothing to type")
	}
	return a.ask(ctx, req, message, bind, field, want)
}

func (a *Asker) ask(ctx context.Context, req *mcp.CallToolRequest, message, bind, field, want string) (res *mcp.CallToolResult, ok bool, err error) {
	if req == nil || req.Session == nil || req.Params == nil {
		_ = audit.Answered(ctx, "", "no-elicitation", -1)
		return nil, false, ErrNoConfirm
	}
	c, _ := auth.Person(ctx)
	if len(a.Clients) > 0 && !slices.Contains(a.Clients, c.Client()) {
		slog.Warn("confirmation refused: client not trusted to ask the person", "user", c.Username, "client", c.Client())
		_ = audit.Answered(ctx, "", "untrusted-client", -1)
		return nil, false, ErrUntrustedClient
	}
	if p := req.Session.InitializeParams(); p == nil || p.Capabilities == nil || p.Capabilities.Elicitation == nil {
		_ = audit.Answered(ctx, "", "no-elicitation", -1)
		return nil, false, ErrNoConfirm
	}
	who := c.Username
	bind += "\x00" + field + "\x00" + want
	if req.Params.InputResponses == nil {
		until := Now().Add(TTL).UnixMilli()
		schema := map[string]any{"type": "object", "properties": map[string]any{}}
		if field != "" {
			schema = map[string]any{"type": "object", "required": []string{field},
				"properties": map[string]any{field: map[string]any{"type": "string", "title": field}}}
		}
		st := state(req.Params.Name, req.Params.Arguments, who, bind, until)
		audit.Asked(ctx, audit.DialogID(st), message)
		return &mcp.CallToolResult{
			InputRequests: mcp.InputRequestMap{"confirm": &mcp.ElicitParams{Message: message, RequestedSchema: schema}},
			RequestState:  st,
		}, false, nil
	}
	dialog := audit.DialogID(req.Params.RequestState)
	ts, _, _ := strings.Cut(req.Params.RequestState, ".")
	until, perr := strconv.ParseInt(ts, 10, 64)
	expect := state(req.Params.Name, req.Params.Arguments, who, bind, until)
	if perr != nil || !hmac.Equal([]byte(expect), []byte(req.Params.RequestState)) {
		_ = audit.Answered(ctx, dialog, "mismatch", -1)
		return nil, false, fmt.Errorf("the confirmation doesn't match this request, or what it shows has changed since: ask again")
	}
	now := Now().UnixMilli()
	asked := until - TTL.Milliseconds()
	if now > until {
		_ = audit.Answered(ctx, dialog, "expired", now-asked)
		return nil, false, fmt.Errorf("the confirmation expired: ask again")
	}
	action := "none"
	var r *mcp.ElicitResult
	if r, _ = req.Params.InputResponses["confirm"].(*mcp.ElicitResult); r != nil {
		action = r.Action
	}
	ok = action == "accept"
	if ok && field != "" {
		typed, _ := r.Content[field].(string)
		if strings.TrimSpace(typed) != want {
			_ = audit.Answered(ctx, dialog, "typed-mismatch", now-asked)
			slog.Info("person answered", "tool", req.Params.Name, "user", who, "action", action, "outcome", "typed text didn't match")
			return nil, false, fmt.Errorf("what was typed isn't %q: nothing was done. Ask again if it was a typo", want)
		}
	}
	// A yes faster than anyone can read the question came from the client,
	// not the person (some clients answer such questions by themselves).
	// Clients the person runs without approvals on purpose are exempt.
	if ok && now-asked < MinAnswer.Milliseconds() && !slices.Contains(a.FastOK, c.Client()) {
		_ = audit.Answered(ctx, dialog, "too-fast", now-asked)
		slog.Warn("confirmation refused: answered too fast to be a person", "tool", req.Params.Name, "user", who, "client", c.Client(), "ms", now-asked)
		return nil, false, fmt.Errorf("the answer came back faster than a person can read the question, so the app likely answered by itself: nothing was done. Ask the person to try again and read the question")
	}
	slog.Info("person answered", "tool", req.Params.Name, "user", who, "action", action,
		"outcome", map[bool]string{true: "accepted", false: "declined"}[ok])
	// An accepted change only goes on once the answer is on record.
	if err := audit.Answered(ctx, dialog, action, now-asked); err != nil {
		return nil, false, err
	}
	return nil, ok, nil
}
