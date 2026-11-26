package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorski89/sgnl-mcp/internal/auth"
	"github.com/igorski89/sgnl-mcp/internal/confirm"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tests answer at once; each look at the clock moves it 2 s, so an answer
// counts as a person's.
func TestMain(m *testing.M) {
	var mu sync.Mutex
	now := time.Now()
	confirm.Now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(2 * time.Second)
		return now
	}
	os.Exit(m.Run())
}

// fakeLogin: the login server's admin API with state, both clients, and
// a record of every change.
type fakeLogin struct {
	mu      sync.Mutex
	users   map[string]*apiUser
	groups  []string
	changes []string
}

func newFake(t *testing.T) (*fakeLogin, *Client, *Client) {
	f := &fakeLogin{groups: []string{"admins", "sgnl_users", "sgnl_invited", "developers"}, users: map[string]*apiUser{
		"boss": {Username: "boss", Email: "boss@co.example", Groups: []string{"admins", "sgnl_users"}, Credentials: []string{"password", "webauthn"}},
		"hugo": {Username: "hugo", Email: "hugo@co.example", Groups: []string{"sgnl_users"}, Credentials: []string{"password", "webauthn"}},
		"anna": {Username: "anna", Email: "anna@personal.example", Groups: []string{"sgnl_invited"}, Credentials: []string{"password"}},
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		id, _, _ := r.BasicAuth()
		tok := map[string]string{"gw-admin authelia.admin.read": "readtok", "gw-write authelia.admin.read authelia.admin.write": "writetok"}[id+" "+r.Form.Get("scope")]
		if tok == "" {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": tok, "expires_in": 300})
	})
	auth := func(write bool, h func(w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			a := r.Header.Get("Authorization")
			if a != "Bearer writetok" && (write || a != "Bearer readtok") {
				w.WriteHeader(403)
				return
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if write {
				f.changes = append(f.changes, r.Method+" "+r.URL.Path)
			}
			h(w, r)
		}
	}
	user := func(w http.ResponseWriter, r *http.Request) *apiUser {
		u := f.users[r.PathValue("u")]
		if u == nil {
			w.WriteHeader(404)
		}
		return u
	}
	mux.HandleFunc("GET /api/admin/users/{u}", auth(false, func(w http.ResponseWriter, r *http.Request) {
		if u := user(w, r); u != nil {
			json.NewEncoder(w).Encode(u)
		}
	}))
	mux.HandleFunc("GET /api/admin/groups", auth(false, func(w http.ResponseWriter, r *http.Request) {
		var gs []map[string]string
		for _, g := range f.groups {
			gs = append(gs, map[string]string{"name": g})
		}
		json.NewEncoder(w).Encode(map[string]any{"groups": gs})
	}))
	mux.HandleFunc("POST /api/admin/users", auth(true, func(w http.ResponseWriter, r *http.Request) {
		var u apiUser
		json.NewDecoder(r.Body).Decode(&u)
		f.users[u.Username] = &u
		w.WriteHeader(201)
	}))
	mux.HandleFunc("POST /api/admin/users/{u}/invite", auth(true, func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		json.NewDecoder(r.Body).Decode(&b)
		if u := user(w, r); u != nil {
			f.changes[len(f.changes)-1] += " " + b["lifespan"]
			json.NewEncoder(w).Encode(map[string]string{"email": u.Email, "expires_at": "later"})
		}
	}))
	mux.HandleFunc("POST /api/admin/users/{u}/logout", auth(true, func(w http.ResponseWriter, r *http.Request) { user(w, r) }))
	mux.HandleFunc("PATCH /api/admin/users/{u}", auth(true, func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		if u := user(w, r); u != nil {
			if e, ok := b["email"].(string); ok {
				u.Email = e
			}
			if d, ok := b["disabled"].(bool); ok {
				u.Disabled = d
			}
		}
	}))
	mux.HandleFunc("PUT /api/admin/users/{u}/groups/{g}", auth(true, func(w http.ResponseWriter, r *http.Request) {
		if u := user(w, r); u != nil && !slices.Contains(u.Groups, r.PathValue("g")) {
			u.Groups = append(u.Groups, r.PathValue("g"))
		}
	}))
	mux.HandleFunc("DELETE /api/admin/users/{u}/groups/{g}", auth(true, func(w http.ResponseWriter, r *http.Request) {
		if u := user(w, r); u != nil {
			u.Groups = slices.DeleteFunc(u.Groups, func(g string) bool { return g == r.PathValue("g") })
		}
	}))
	mux.HandleFunc("POST /api/admin/groups", auth(true, func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		json.NewDecoder(r.Body).Decode(&b)
		f.groups = append(f.groups, b["name"])
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := func(id, scope string) *Client {
		return &Client{Base: srv.URL + "/api/admin", TokenURL: srv.URL + "/token", ID: id, Secret: "s", Scope: scope, HTTP: srv.Client(), Now: time.Now}
	}
	return f, c("gw-admin", "authelia.admin.read"), c("gw-write", "authelia.admin.read authelia.admin.write")
}

func (f *fakeLogin) changed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.changes)
}

// answer: what the admin does with a question (nil: the client can't ask).
type answer func(*mcp.ElicitRequest) *mcp.ElicitResult

var (
	yes = func(*mcp.ElicitRequest) *mcp.ElicitResult {
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{}}
	}
	no = func(*mcp.ElicitRequest) *mcp.ElicitResult { return &mcp.ElicitResult{Action: "decline"} }
)

func typed(s string) answer {
	return func(*mcp.ElicitRequest) *mcp.ElicitResult {
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"username": s}}
	}
}

// wsession: the admin endpoint with write tools, as admin "boss"; asked
// collects every question shown.
func wsession(t *testing.T, at *Tools, a answer) (*mcp.ClientSession, *[]*mcp.ElicitParams) {
	t.Helper()
	return wsessionAs(t, at, a, "admins", "sgnl_users")
}

// wsessionAs: the same as "boss" with these current groups.
func wsessionAs(t *testing.T, at *Tools, a answer, groups ...string) (*mcp.ClientSession, *[]*mcp.ElicitParams) {
	t.Helper()
	s := mcp.NewServer(&mcp.Implementation{Name: "a", Version: "0"}, nil)
	at.Register(s)
	me := auth.Claims{Sub: "s", Username: "boss", Groups: groups}
	verify := func(context.Context, string, *http.Request) (*mcpauth.TokenInfo, error) {
		return auth.TokenInfoNow("t", me, me, time.Now().Add(time.Hour)), nil
	}
	srv := httptest.NewServer(mcpauth.RequireBearerToken(verify, nil)(
		mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)))
	t.Cleanup(srv.Close)
	hc := &http.Client{Transport: rt(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer t")
		return http.DefaultTransport.RoundTrip(r)
	})}
	var asked []*mcp.ElicitParams
	opts := &mcp.ClientOptions{}
	if a != nil {
		opts.ElicitationHandler = func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			asked = append(asked, r.Params)
			return a(r), nil
		}
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, opts).
		Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: hc}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs, &asked
}

func writeTools(read, write *Client) *Tools {
	return &Tools{API: read, Write: write, Ask: &confirm.Asker{}, UserDomain: "co.example", Domains: []string{"co.example", "co2.example"}}
}

func TestNoWriteToolsWithoutWriteClient(t *testing.T) {
	_, read, write := newFake(t)
	for _, tc := range []struct {
		at   *Tools
		want int
	}{{&Tools{API: read}, 0}, {&Tools{API: read, Write: write, Ask: &confirm.Asker{}}, 7}, {writeTools(read, write), 9}} {
		cs, _ := wsession(t, tc.at, yes)
		r, err := cs.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, x := range r.Tools {
			if !x.Annotations.ReadOnlyHint {
				n++
			}
		}
		if n != tc.want {
			t.Errorf("write tools: %d, want %d", n, tc.want)
		}
	}
}

func TestClientMakesOnlyItsChanges(t *testing.T) {
	for _, c := range []struct {
		method, path string
		body         map[string]any
		ok           bool
	}{
		{"DELETE", "/users/hugo", nil, false},
		{"PATCH", "/users/hugo", map[string]any{"password": "x"}, false},
		{"PATCH", "/users/hugo", map[string]any{"email": "a@b", "disabled": true}, false},
		{"PATCH", "/users/hugo", map[string]any{"disabled": true}, true},
		{"POST", "/users/hugo/webauthn", nil, false},
		{"DELETE", "/users/hugo/groups/admins", nil, true},
		{"PUT", "/users/hugo/groups/a/b", nil, false},
		{"POST", "/groups", map[string]any{"name": "x"}, true},
		{"DELETE", "/groups/x", nil, false},
	} {
		if allowed(c.method, c.path, c.body) != c.ok {
			t.Errorf("%s %s %v: allowed should be %v", c.method, c.path, c.body, c.ok)
		}
	}
}

func TestInvite(t *testing.T) {
	f, read, write := newFake(t)
	cs, asked := wsession(t, writeTools(read, write), yes)
	// The account's own future address, in any company domain: refused, nothing asked.
	for _, e := range []string{"lena@co.example", "Lena@CO2.example"} {
		if txt, isErr := call(t, cs, "admin_invite", map[string]any{"username": "lena", "name": "Lena Test", "email": e}); !isErr || !strings.Contains(txt, "future address") {
			t.Errorf("%s: %q", e, txt)
		}
	}
	for _, in := range []map[string]any{
		{"username": "hugo", "name": "Hugo X", "email": "h@personal.example"},            // exists
		{"username": "Lena", "name": "Lena Test", "email": "l@personal.example"},         // not a valid name
		{"username": "lena", "name": "Lena ‮Test", "email": "l@personal.example"},        // hidden characters
		{"username": "lena", "name": "Lena Test", "email": "Lena <l@personal.example>"},  // not a bare address
		{"username": "lena", "name": "Lena Test", "email": "l@personal.example\nBcc: x"}, // line break
	} {
		if txt, isErr := call(t, cs, "admin_invite", in); !isErr {
			t.Errorf("%v: accepted: %q", in, txt)
		}
	}
	if len(*asked) != 0 || len(f.changed()) != 0 {
		t.Fatalf("asked or changed on a refused invite: %v %v", *asked, f.changed())
	}
	// A company mailbox that isn't theirs: allowed, marked.
	txt, isErr := call(t, cs, "admin_invite", map[string]any{"username": "lena", "name": "Lena Test", "email": "boss+lena@co.example"})
	if isErr || !strings.Contains(txt, `"done":"invited"`) {
		t.Fatalf("invite: %q", txt)
	}
	first := strings.SplitN((*asked)[0].Message, "\n", 2)[0]
	if first != `Invite "Lena Test" as lena@co.example, set-up link to boss+lena@co.example (a COMPANY mailbox)?` {
		t.Errorf("first line: %q", first)
	}
	if got := f.changed(); !slices.Equal(got, []string{"POST /api/admin/users", "POST /api/admin/users/lena/invite 72h"}) {
		t.Errorf("changes: %v", got)
	}
	if u := f.users["lena"]; !slices.Equal(u.Groups, []string{"sgnl_invited"}) || u.Email != "boss+lena@co.example" {
		t.Errorf("created: %+v", u)
	}
	call(t, cs, "admin_invite", map[string]any{"username": "mia", "name": "Mia", "email": "mia@personal.example", "hours": 5})
	if first := strings.SplitN((*asked)[1].Message, "\n", 2)[0]; !strings.HasSuffix(first, "mia@personal.example (outside)?") {
		t.Errorf("outside: %q", first)
	}
}

func TestDeclinedChangesNothing(t *testing.T) {
	f, read, write := newFake(t)
	cs, asked := wsession(t, writeTools(read, write), no)
	for name, args := range map[string]map[string]any{
		"admin_invite":       {"username": "lena", "name": "Lena Test", "email": "l@personal.example"},
		"admin_group_add":    {"username": "hugo", "groups": []string{"developers"}},
		"admin_group_remove": {"username": "hugo", "groups": []string{"sgnl_users"}},
		"admin_group_create": {"name": "sales"},
		"admin_setup_email":  {"username": "hugo"},
		"admin_logout":       {"username": "hugo"},
		"admin_disable":      {"username": "hugo"},
	} {
		if txt, isErr := call(t, cs, name, args); !isErr || !strings.Contains(txt, "declined") {
			t.Errorf("%s: %q", name, txt)
		}
	}
	if len(*asked) != 7 || len(f.changed()) != 0 {
		t.Fatalf("asked %d, changed %v", len(*asked), f.changed())
	}
}

// Where everyone's free/busy is shared by an operator sync, finishing an
// invitation says to run it for the new person.
func TestFinishInviteFreeBusyReminder(t *testing.T) {
	f, read, write := newFake(t)
	at := writeTools(read, write)
	at.FreeBusyShared = true
	cs, _ := wsession(t, at, yes)
	f.users["anna"].Credentials = []string{"password", "webauthn"}
	txt, isErr := call(t, cs, "admin_finish_invite", map[string]any{"username": "anna"})
	if isErr || !strings.Contains(txt, "bin/sgnl-freebusy-sync runs for them") {
		t.Fatalf("finish: %q", txt)
	}
}

func TestFinishInvite(t *testing.T) {
	f, read, write := newFake(t)
	cs, asked := wsession(t, writeTools(read, write), yes)
	if txt, isErr := call(t, cs, "admin_finish_invite", map[string]any{"username": "anna"}); !isErr || !strings.Contains(txt, "has: password") {
		t.Fatalf("unfinished set-up: %q", txt)
	}
	if txt, isErr := call(t, cs, "admin_finish_invite", map[string]any{"username": "hugo"}); !isErr || !strings.Contains(txt, "isn't an invited account") {
		t.Fatalf("not invited: %q", txt)
	}
	f.users["anna"].Credentials = []string{"password", "webauthn"}
	if txt, isErr := call(t, cs, "admin_finish_invite", map[string]any{"username": "anna", "groups": []string{"nope"}}); !isErr || !strings.Contains(txt, "no group") {
		t.Fatalf("unknown group: %q", txt)
	}
	if len(*asked) != 0 || len(f.changed()) != 0 {
		t.Fatal("asked or changed before the checks passed")
	}
	txt, isErr := call(t, cs, "admin_finish_invite", map[string]any{"username": "anna", "groups": []string{"developers"}})
	if isErr || !strings.Contains(txt, `"groups":["developers","sgnl_users"]`) || !strings.Contains(txt, "sgnl-vault-sync") || strings.Contains(txt, "freebusy") {
		t.Fatalf("finish: %q", txt)
	}
	if first := strings.SplitN((*asked)[0].Message, "\n", 2)[0]; first != "Finish the set-up of anna: email anna@co.example, groups sgnl_users, developers?" {
		t.Errorf("first line: %q", first)
	}
	if f.users["anna"].Email != "anna@co.example" {
		t.Errorf("email: %q", f.users["anna"].Email)
	}
}

func TestGroupChanges(t *testing.T) {
	f, read, write := newFake(t)
	cs, asked := wsession(t, writeTools(read, write), yes)
	// No self-lockout.
	for _, g := range []string{"admins", "sgnl_users"} {
		if txt, isErr := call(t, cs, "admin_group_remove", map[string]any{"username": "boss", "groups": []string{g}}); !isErr || !strings.Contains(txt, "lock yourself out") {
			t.Errorf("self %s: %q", g, txt)
		}
	}
	if txt, isErr := call(t, cs, "admin_group_add", map[string]any{"username": "hugo", "groups": []string{"sgnl_users"}}); !isErr || !strings.Contains(txt, "nothing to change") {
		t.Errorf("no-op: %q", txt)
	}
	if txt, isErr := call(t, cs, "admin_group_add", map[string]any{"username": "hugo", "groups": []string{"sgnl_invited"}}); !isErr {
		t.Errorf("invited by hand: %q", txt)
	}
	if len(*asked) != 0 || len(f.changed()) != 0 {
		t.Fatal("asked or changed on a refusal")
	}
	txt, isErr := call(t, cs, "admin_group_add", map[string]any{"username": "hugo", "groups": []string{"admins"}})
	if isErr || !strings.Contains(txt, "ADMIN rights") || !strings.Contains(txt, "sgnl-vault-sync") {
		t.Fatalf("add: %q", txt)
	}
	if !strings.HasPrefix((*asked)[0].Message, "Give hugo ADMIN RIGHTS: add to admins?") {
		t.Errorf("add question: %q", (*asked)[0].Message)
	}
	txt, isErr = call(t, cs, "admin_group_remove", map[string]any{"username": "hugo", "groups": []string{"sgnl_users", "admins"}})
	if isErr || !strings.Contains(txt, `"done":"removed from sgnl_users, admins"`) {
		t.Fatalf("remove: %q", txt)
	}
	if q := (*asked)[1].Message; !strings.HasPrefix(q, "Remove hugo from sgnl_users, admins? They lose EVERY app.") || !strings.Contains(q, "They lose:\n- sgnl_users: ALL") {
		t.Errorf("remove question: %q", q)
	}
	if len(f.users["hugo"].Groups) != 0 {
		t.Errorf("groups left: %v", f.users["hugo"].Groups)
	}
	call(t, cs, "admin_group_create", map[string]any{"name": "sales"})
	if txt, isErr := call(t, cs, "admin_group_create", map[string]any{"name": "sales"}); !isErr || !strings.Contains(txt, "already exists") {
		t.Errorf("twice: %q", txt)
	}
}

func TestDisableNeedsTheTypedUsername(t *testing.T) {
	f, read, write := newFake(t)
	at := writeTools(read, write)
	for _, a := range []answer{typed("hug"), typed(""), typed("HUGO")} {
		cs, _ := wsession(t, at, a)
		if txt, isErr := call(t, cs, "admin_disable", map[string]any{"username": "hugo"}); !isErr {
			t.Errorf("disabled without the right text: %q", txt)
		}
	}
	if len(f.changed()) != 0 || f.users["hugo"].Disabled {
		t.Fatal("changed")
	}
	cs, asked := wsession(t, at, typed("hugo"))
	if txt, isErr := call(t, cs, "admin_disable", map[string]any{"username": "boss"}); !isErr || !strings.Contains(txt, "yourself") {
		t.Errorf("self: %q", txt)
	}
	if txt, isErr := call(t, cs, "admin_disable", map[string]any{"username": "hugo"}); isErr || !f.users["hugo"].Disabled {
		t.Fatalf("disable: %q", txt)
	}
	q := (*asked)[0]
	if !strings.HasPrefix(q.Message, "Disable hugo? They lose access to EVERYTHING now.") {
		t.Errorf("question: %q", q.Message)
	}
	if b, _ := json.Marshal(q.RequestedSchema); !strings.Contains(string(b), `"required":["username"]`) || strings.Contains(string(b), "default") {
		t.Errorf("schema: %s", b)
	}
	cs, _ = wsession(t, at, yes)
	if txt, isErr := call(t, cs, "admin_enable", map[string]any{"username": "hugo"}); isErr || f.users["hugo"].Disabled {
		t.Fatalf("enable: %q", txt)
	}
}

func TestLogoutAndSetupEmail(t *testing.T) {
	f, read, write := newFake(t)
	cs, asked := wsession(t, writeTools(read, write), yes)
	if txt, isErr := call(t, cs, "admin_logout", map[string]any{"username": "boss"}); !isErr || !strings.Contains(txt, "yourself") {
		t.Errorf("self: %q", txt)
	}
	if txt, isErr := call(t, cs, "admin_logout", map[string]any{"username": "hugo"}); isErr {
		t.Errorf("logout: %q", txt)
	}
	if txt, isErr := call(t, cs, "admin_setup_email", map[string]any{"username": "anna"}); isErr || !strings.Contains(txt, "anna@personal.example") {
		t.Errorf("setup: %q", txt)
	}
	if q := (*asked)[1].Message; !strings.HasPrefix(q, "Email anna a set-up link to anna@personal.example (outside)?") {
		t.Errorf("question: %q", q)
	}
	if got := f.changed(); !slices.Equal(got, []string{"POST /api/admin/users/hugo/logout", "POST /api/admin/users/anna/invite 24h"}) {
		t.Errorf("changes: %v", got)
	}
}

// An answer counts only for what was shown: if the person changed while
// the admin was being asked, nothing is done.
func TestAnswerBoundToWhatWasShown(t *testing.T) {
	f, read, write := newFake(t)
	meanwhile := func(r *mcp.ElicitRequest) *mcp.ElicitResult {
		f.mu.Lock()
		f.users["hugo"].Groups = append(f.users["hugo"].Groups, "admins")
		f.mu.Unlock()
		return yes(r)
	}
	cs, asked := wsession(t, writeTools(read, write), meanwhile)
	txt, isErr := call(t, cs, "admin_group_add", map[string]any{"username": "hugo", "groups": []string{"developers"}})
	if !isErr || !strings.Contains(txt, "changed since") || len(*asked) != 1 || len(f.changed()) != 0 {
		t.Fatalf("acted on a stale answer: %q %v", txt, f.changed())
	}
}

// The same trusted clients as the person's own confirmed actions.
func TestUntrustedClientCantChange(t *testing.T) {
	f, read, write := newFake(t)
	at := writeTools(read, write)
	at.Ask = &confirm.Asker{Clients: []string{"some-other-app"}}
	cs, asked := wsession(t, at, yes)
	if txt, isErr := call(t, cs, "admin_logout", map[string]any{"username": "hugo"}); !isErr || !strings.Contains(txt, "isn't trusted") {
		t.Fatalf("untrusted: %q", txt)
	}
	if len(*asked) != 0 || len(f.changed()) != 0 {
		t.Fatal("asked or changed")
	}
}
