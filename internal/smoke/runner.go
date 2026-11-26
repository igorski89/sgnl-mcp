package smoke

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Config: one run.
type Config struct {
	URL      string // the gateway's MCP endpoint
	Accounts [2]Account
	Shared   string // the shared mailbox both accounts are in
	Outside  string // an outside address (reserved domain): only ever declined
	Only     []string
	ReadOnly bool // only the read-only steps (no sweep, no changes)
	HTTP     *http.Client
	// AnswerAfter: how long to "read" a question before answering (the
	// gateway refuses answers faster than a person; default 2 s).
	AnswerAfter time.Duration
	Log         io.Writer
	Now         func() time.Time
}

// Account: a test account: the username it must be, and its login.
type Account struct {
	User  string
	Login *Login
}

// Result: one step's outcome.
type Result struct {
	Step   string
	Tool   string
	Status string // ok, failed, skipped
	MS     int64
	Detail string
}

// person: one account's connection.
type person struct {
	key, user, email string
	login            *Login
	cs               *mcp.ClientSession
	mu               sync.Mutex
	expect           Expect
	asked            []string
	problem          string
}

// Runner: the two connections and what the steps found.
type Runner struct {
	cfg     Config
	p       map[string]*person
	people  People
	run     string
	vars    map[string]string
	offer   map[string]bool // the tools the gateway offers
	Results []Result
}

func (c *Config) logf(f string, a ...any) {
	if c.Log != nil {
		fmt.Fprintf(c.Log, f+"\n", a...)
	}
}

type bearer struct {
	l    *Login
	next http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	tok, err := b.l.Access(r.Context())
	if err != nil {
		return nil, err
	}
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+tok)
	return b.next.RoundTrip(r)
}

// connect: an MCP session for p that answers questions by p's current
// expectation.
func (r *Runner) connect(ctx context.Context, p *person) error {
	wait := r.cfg.AnswerAfter
	if wait == 0 {
		wait = 2 * time.Second
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "sgnl-mcp-smoke", Version: "1"}, &mcp.ClientOptions{
		ElicitationHandler: func(ctx context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			p.mu.Lock()
			exp := p.expect
			p.asked = append(p.asked, req.Params.Message)
			p.mu.Unlock()
			accept, problem := Decide(req.Params.Message, exp, r.people, p.email)
			if problem != "" {
				p.mu.Lock()
				p.problem = problem
				p.mu.Unlock()
			}
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			if accept {
				return &mcp.ElicitResult{Action: "accept", Content: map[string]any{}}, nil
			}
			return &mcp.ElicitResult{Action: "decline"}, nil
		}})
	next := http.DefaultTransport
	if r.cfg.HTTP != nil && r.cfg.HTTP.Transport != nil {
		next = r.cfg.HTTP.Transport
	}
	cs, err := c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: r.cfg.URL, HTTPClient: &http.Client{Transport: bearer{p.login, next}}}, nil)
	if err != nil {
		return fmt.Errorf("%s: couldn't connect: %w", p.user, err)
	}
	p.cs = cs
	return nil
}

// callOut: what one call gave.
type callOut struct {
	out   map[string]any
	err   string // the tool's error ("" ok)
	asked []string
}

// call: one tool call as p, with exp for any question; retried after a
// pause when the gateway's per-person rate limit says so.
func (r *Runner) call(ctx context.Context, p *person, tool string, args map[string]any, exp Expect) (callOut, error) {
	for try := 0; ; try++ {
		p.mu.Lock()
		p.expect, p.asked, p.problem = exp, nil, ""
		p.mu.Unlock()
		res, err := p.cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
		if err != nil {
			return callOut{}, err
		}
		var o callOut
		p.mu.Lock()
		o.asked = slices.Clone(p.asked)
		problem := p.problem
		p.mu.Unlock()
		if res.IsError {
			for _, c := range res.Content {
				if t, ok := c.(*mcp.TextContent); ok {
					o.err += t.Text
				}
			}
			if o.err == "" {
				o.err = "error"
			}
		}
		if strings.Contains(o.err, "in a short time: wait a minute") && try < 3 {
			r.cfg.logf("  (rate limited, waiting 40 s)")
			select {
			case <-time.After(40 * time.Second):
				continue
			case <-ctx.Done():
				return o, ctx.Err()
			}
		}
		if b, _ := json.Marshal(res.StructuredContent); len(b) > 0 {
			_ = json.Unmarshal(b, &o.out)
		}
		if problem != "" {
			return o, errors.New("confirmation: " + problem)
		}
		switch exp.Dialog {
		case Accept, Decline:
			if len(o.asked) == 0 {
				return o, errors.New("expected a confirmation, none was asked")
			}
		case NoDialog:
			if len(o.asked) > 0 {
				return o, errors.New("a confirmation was asked where none was expected")
			}
		}
		return o, nil
	}
}

// ---- reading answers

func get(m any, path ...any) any {
	for _, k := range path {
		switch x := k.(type) {
		case string:
			mm, _ := m.(map[string]any)
			m = mm[x]
		case int:
			l, _ := m.([]any)
			if x >= len(l) {
				return nil
			}
			m = l[x]
		}
	}
	return m
}

func gstr(m any, path ...any) string {
	s, _ := get(m, path...).(string)
	return s
}

func glist(m any, path ...any) []map[string]any {
	l, _ := get(m, path...).([]any)
	var out []map[string]any
	for _, x := range l {
		if mm, ok := x.(map[string]any); ok {
			out = append(out, mm)
		}
	}
	return out
}

// find: the first item whose field contains sub.
func find(items []map[string]any, field, sub string) map[string]any {
	for _, it := range items {
		if strings.Contains(gstr(it, field), sub) {
			return it
		}
	}
	return nil
}
