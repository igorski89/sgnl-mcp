package smoke

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/igorski89/sgnl-mcp/internal/auth"
	"github.com/igorski89/sgnl-mcp/internal/confirm"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var ppl = People{A: "smoke@co.example", B: "smoke2@co.example", Shared: "team@co.example"}

// The questions as the gateway words them (send.go, invite.go, self.go).
func TestDecide(t *testing.T) {
	send := "Send \"[smoke] hello r1\" to smoke2@co.example?\n\nFrom: smoke@co.example\nTo: smoke2@co.example\nSubject: [smoke] hello r1\nText begins: hi\n"
	sendOut := "Send \"[smoke] outside r1\" to x@example.com (1 OUTSIDE the company)?\n\nFrom: smoke@co.example\nTo: x@example.com  (OUTSIDE the company, never written to before)\nSubject: [smoke] outside r1\n\nWARNING: 1 recipient(s) outside the company.\n"
	sendAs := "Send AS team@co.example \"[smoke] as shared r1\" to smoke2@co.example?\n\nFrom: team@co.example\nTo: smoke2@co.example\n"
	sendStranger := "Send \"[smoke] hello r1\" to boss@co.example?\n\nFrom: smoke@co.example\nTo: boss@co.example\n"
	sendFromOther := "Send \"[smoke] hello r1\" FROM ceo@co.example to smoke2@co.example?\n\nFrom: ceo@co.example\nTo: smoke2@co.example\n"
	for _, c := range []struct {
		name    string
		msg     string
		e       Expect
		accept  bool
		problem bool
	}{
		{"send between the two", send, Expect{Dialog: Accept, Must: []string{"r1", ppl.B}}, true, false},
		{"another run's item", send, Expect{Dialog: Accept, Must: []string{"r2"}}, false, true},
		{"outside, expected accept", sendOut, Expect{Dialog: Accept, Must: []string{"r1"}}, false, true},
		{"outside, declined as planned", sendOut, Expect{Dialog: Decline, Must: []string{"OUTSIDE", "x@example.com", "From: smoke@co.example"}}, false, false},
		{"outside, the question lacks the warning", send, Expect{Dialog: Decline, Must: []string{"OUTSIDE"}}, false, true},
		{"as the shared mailbox", sendAs, Expect{Dialog: Accept, Must: []string{"Send AS team@co.example"}, From: ppl.Shared}, true, false},
		{"as the shared mailbox, From not expected", sendAs, Expect{Dialog: Accept}, false, true},
		{"a colleague who isn't a test account", sendStranger, Expect{Dialog: Accept}, false, true},
		{"from someone else", sendFromOther, Expect{Dialog: Accept, Allow: []string{"ceo@co.example"}}, false, true},
		{"no question expected", send, Expect{}, false, true},
		{"names no [smoke] item", "Move 1 mail(s) to Trash: Quarterly report", Expect{Dialog: Accept}, false, true},
		{"automatic reply: OUTSIDE allowed only when the step says so", "Switch on your automatic reply from 2026-12-26? Everyone who mails you gets it, people OUTSIDE the company too.", Expect{Dialog: Accept, Unnamed: true, Outward: true}, true, false},
		{"automatic reply, not marked outward", "Switch on your automatic reply? [smoke] people OUTSIDE the company too.", Expect{Dialog: Accept}, false, true},
		{"allowed contact address", "Delete the contact [smoke] Contact r1 <contact-r1@example.invalid> from your address book?", Expect{Dialog: Accept, Allow: []string{"contact-r1@example.invalid"}}, true, false},
	} {
		p := ppl
		p.Run = "r1"
		acc, prob := Decide(c.msg, c.e, p, ppl.A)
		if acc != c.accept || (prob != "") != c.problem {
			t.Errorf("%s: accept %v, problem %q", c.name, acc, prob)
		}
	}
}

// ---- tokens

func tokenServer(t *testing.T, answer func(rt string) (int, map[string]any)) (string, *atomic.Int32) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		n.Add(1)
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("client_id") != "cli" {
			w.WriteHeader(400)
			return
		}
		code, body := answer(r.Form.Get("refresh_token"))
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &n
}

func writeToken(t *testing.T, mode os.FileMode, rt string) string {
	p := filepath.Join(t.TempDir(), "tok.json")
	if err := os.WriteFile(p, []byte(`{"refresh_token":"`+rt+`"}`), mode); err != nil {
		t.Fatal(err)
	}
	os.Chmod(p, mode)
	return p
}

func TestTokenFile(t *testing.T) {
	u, n := tokenServer(t, func(rt string) (int, map[string]any) {
		if rt != "r1" {
			return 400, map[string]any{"error": "invalid_grant"}
		}
		return 200, map[string]any{"access_token": "a1", "refresh_token": "r2", "expires_in": 3600}
	})
	l := &Login{Path: writeToken(t, 0o644, "r1"), TokenURL: u, ClientID: "cli", HTTP: http.DefaultClient}
	if err := l.Load(); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("a world-readable file: %v", err)
	}
	l.Path = writeToken(t, 0o600, "r1")
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	if a, err := l.Access(context.Background()); err != nil || a != "a1" {
		t.Fatalf("access: %q %v", a, err)
	}
	// The rotated refresh token is in the file, still 600; the access
	// token is reused while valid.
	b, _ := os.ReadFile(l.Path)
	st, _ := os.Stat(l.Path)
	if !strings.Contains(string(b), `"refresh_token":"r2"`) || st.Mode().Perm() != 0o600 {
		t.Errorf("file after refresh: %s %o", b, st.Mode().Perm())
	}
	l.Access(context.Background())
	if n.Load() != 1 {
		t.Errorf("refreshed %d times", n.Load())
	}
	// A login that has ended: re-login needed.
	l2 := &Login{Path: writeToken(t, 0o600, "old"), TokenURL: u, ClientID: "cli", HTTP: http.DefaultClient}
	l2.Load()
	if _, err := l2.Access(context.Background()); !errors.Is(err, ErrRelogin) {
		t.Errorf("ended login: %v", err)
	}
}

// ---- the matrix and the scenario

// Every covered tool has a step; a declined one a step that expects the
// decline; every step's tool is in the matrix.
func TestStepsCoverTheMatrix(t *testing.T) {
	has, declined := map[string]bool{}, map[string]bool{}
	for _, s := range Steps() {
		if _, ok := Matrix[s.Tool]; !ok {
			t.Errorf("step %q: %s isn't in the matrix", s.Name, s.Tool)
		}
		has[s.Tool] = true
		for tool := range ByRun {
			has[tool] = true
		}
		if s.WantErr != "" && s.Expect != nil {
			declined[s.Tool] = true
		}
	}
	for tool, e := range Matrix {
		for _, m := range e.Modes {
			switch {
			case m == NotCovered:
			case !has[tool]:
				t.Errorf("%s (%s) has no step", tool, m)
			case m == Declined && !declined[tool]:
				t.Errorf("%s: no step checks and declines its question", tool)
			}
		}
	}
}

// Read-only runs only the leading reads: none needs the run's own items.
func TestReadBlock(t *testing.T) {
	steps := Steps()
	n := readBlock(steps)
	if n < 10 || n == len(steps) {
		t.Fatalf("read block of %d steps", n)
	}
	for _, s := range steps[:n] {
		if s.Write || len(s.Needs) > 0 || s.Poll > 0 {
			t.Errorf("%q in the read block depends on the run's writes", s.Name)
		}
	}
}

// ---- a run against a small gateway: the guard and the answers

func fakeGateway(t *testing.T, users map[string]string) string {
	s := mcp.NewServer(&mcp.Implementation{Name: "g"}, nil)
	type none struct{}
	mcp.AddTool(s, &mcp.Tool{Name: "whoami"}, func(ctx context.Context, req *mcp.CallToolRequest, _ none) (*mcp.CallToolResult, map[string]any, error) {
		u := users[req.Extra.Header.Get("Authorization")]
		return nil, map[string]any{"username": u, "email": u + "@co.example", "groups": []string{"sgnl_users"}}, nil
	})
	// As the gateway's: asks first, and does nothing unless accepted.
	mcp.AddTool(s, &mcp.Tool{Name: "logout_everywhere"}, func(ctx context.Context, req *mcp.CallToolRequest, _ none) (*mcp.CallToolResult, map[string]any, error) {
		u := users[req.Extra.Header.Get("Authorization")]
		ctx = auth.WithRequest(ctx, req.Extra.TokenInfo)
		res, ok, err := (&confirm.Asker{}).Ask(ctx, req, "Log "+u+" out EVERYWHERE now?", "logout")
		if err != nil || res != nil {
			return res, nil, err
		}
		if !ok {
			return nil, nil, errors.New("the person declined: no one was logged out")
		}
		return nil, map[string]any{"done": "logged out"}, nil
	})
	verify := func(_ context.Context, tok string, _ *http.Request) (*mcpauth.TokenInfo, error) {
		u := users["Bearer "+tok]
		c := auth.Claims{Sub: u, Username: u}
		return auth.TokenInfoNow(tok, c, c, time.Now().Add(time.Hour)), nil
	}
	srv := httptest.NewServer(mcpauth.RequireBearerToken(verify, nil)(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)))
	t.Cleanup(srv.Close)
	return srv.URL
}

func runFake(t *testing.T, users map[string]string) (*Runner, error) {
	u, _ := tokenServer(t, func(rt string) (int, map[string]any) {
		return 200, map[string]any{"access_token": "tok-" + rt, "expires_in": 3600}
	})
	acct := func(user, rt string) Account {
		return Account{User: user, Login: &Login{Path: writeToken(t, 0o600, rt), TokenURL: u, ClientID: "cli", HTTP: http.DefaultClient}}
	}
	return Run(context.Background(), Config{URL: fakeGateway(t, users), Accounts: [2]Account{acct("smoke", "ra"), acct("smoke2", "rb")},
		Shared: "team@co.example", AnswerAfter: confirm.MinAnswer + 100*time.Millisecond, Now: time.Now})
}

func TestRunGuard(t *testing.T) {
	// The second token file is someone else's: stop before anything.
	_, err := runFake(t, map[string]string{"Bearer tok-ra": "smoke", "Bearer tok-rb": "igor"})
	if err == nil || !strings.Contains(err.Error(), `logs in as "igor"`) {
		t.Errorf("wrong account: %v", err)
	}
	r, err := runFake(t, map[string]string{"Bearer tok-ra": "smoke", "Bearer tok-rb": "smoke2"})
	if err != nil {
		t.Fatal(err)
	}
	// The small gateway offers only whoami: every other step is skipped, none fails.
	for _, x := range r.Results {
		if x.Status == "failed" {
			t.Errorf("%+v", x)
		}
	}
	// logout_everywhere was asked, checked and declined (its step is ok).
	found := false
	for _, x := range r.Results {
		found = found || (x.Tool == "logout_everywhere" && x.Status == "ok")
	}
	if !found {
		t.Errorf("logout_everywhere: %+v", r.Results)
	}
	if r.people.B != "smoke2@co.example" {
		t.Errorf("people: %+v", r.people)
	}
}
