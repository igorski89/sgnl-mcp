package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/igorski89/sgnl-mcp/internal/audit"
)

func offboard(t *testing.T, at *Tools, user string) (OffboardingOut, string) {
	t.Helper()
	cs, _ := wsession(t, at, nil)
	txt, isErr := call(t, cs, "admin_offboarding", map[string]any{"username": user})
	if isErr {
		t.Fatalf("offboarding %s: %s", user, txt)
	}
	var out OffboardingOut
	if err := json.Unmarshal([]byte(txt), &out); err != nil {
		t.Fatalf("%v: %s", err, txt)
	}
	return out, txt
}

func section(t *testing.T, out OffboardingOut, name string) Check {
	t.Helper()
	for _, c := range out.Sections {
		if strings.HasPrefix(c.Section, name) {
			return c
		}
	}
	t.Fatalf("no section %q in %+v", name, out.Sections)
	return Check{}
}

func offTools(t *testing.T) (*Tools, *audit.Store) {
	at, st, _, _ := offToolsWith(t)
	return at, st
}

func offToolsWith(t *testing.T) (*Tools, *audit.Store, *fakeLogin, *fakeDrive) {
	fl, read, _ := newFake(t)
	m, _ := fakeStalwart(t)
	fd, files := newFakeDrive(t)
	st := auditStore(t)
	return &Tools{API: read, Mail: m, Files: files, Audit: st, UserDomain: "co.example"}, st, fl, fd
}

func TestOffboardingFound(t *testing.T) {
	at, st := offTools(t)
	ctx, rec, err := st.Begin(context.Background(), audit.User, "mail_send", false, audit.Who{Username: "hugo", Client: "claude-code"}, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	rec.Done(ctx, "ok", "", nil)

	out, txt := offboard(t, at, "hugo")
	login := section(t, out, "Login server")
	if login.State != stFound || !strings.Contains(login.Line, "account enabled") || !strings.Contains(login.Line, "groups: sgnl_users") ||
		!strings.Contains(login.Line, "credentials set: password, passkey") {
		t.Errorf("login: %+v", login)
	}
	box := section(t, out, "Mail: mailbox")
	if box.State != stFound || !strings.Contains(box.Line, "Inherit = active") || !strings.Contains(strings.Join(box.Info, " "), "hugo@co.example (3 MiB used)") {
		t.Errorf("mailbox: %+v", box)
	}
	if sh := section(t, out, "Mail: shared"); sh.State != stFound || sh.Found[0] != "member of contact@co.example" {
		t.Errorf("shared: %+v", sh)
	}
	for _, s := range []string{"Mail: app passwords", "Files: app tokens", "Forgejo", "Vaultwarden", "listmonk", "Mail: forwarding"} {
		if c := section(t, out, s); c.State != stNot || !strings.HasPrefix(c.Line, "NOT CHECKED (") {
			t.Errorf("%s: %+v", s, c)
		}
	}
	if c := section(t, out, "Forgejo"); !strings.HasSuffix(c.Line, "run bin/sgnl-user-suspend --status hugo") {
		t.Errorf("forgejo hint: %s", c.Line)
	}
	if c := section(t, out, "Vaultwarden"); !strings.Contains(c.Line, "admin page") {
		t.Errorf("vaultwarden hint: %s", c.Line)
	}
	// hugo never logged in to the file app: nothing there, but checked.
	if c := section(t, out, "Files: account"); c.State != stNothing || !strings.Contains(c.Line, "never logged in") || !strings.Contains(c.Line, "listed all 3 accounts") {
		t.Errorf("files account: %+v", c)
	}
	if c := section(t, out, "Files: project spaces (only"); c.State != stNothing {
		t.Errorf("spaces: %+v", c)
	}
	// Finance's members are hidden from the admin: not checked, never nothing.
	if c := section(t, out, "Files: project spaces you"); c.State != stNot || !strings.Contains(c.Reason, "1 space(s)") {
		t.Errorf("hidden spaces: %+v", c)
	}
	if c := section(t, out, "AI gateway"); c.State != stFound || c.Found[0] != "1 call(s) in the last 30 days" || !strings.Contains(c.Line, "from claude-code (mail_send)") {
		t.Errorf("ai: %+v", c)
	}
	if c := section(t, out, "Files: project spaces through"); c.State != stNothing {
		t.Errorf("groups: %+v", c)
	}
	if c := section(t, out, "Files: project spaces (only"); !strings.Contains(c.Line, "3 space(s) listed, members hidden in 1") {
		t.Errorf("spaces diagnostics: %s", c.Line)
	}
	// login 3 + mail 1 + shared 1 + ai 1 = 6 open; 7 sections not checked.
	if out.Open != 6 || out.NotChecked != 7 || !strings.HasPrefix(out.Summary, "6 open item(s) in 4 section(s); 7 section(s) NOT CHECKED") {
		t.Errorf("summary: %s (%d/%d)", out.Summary, out.Open, out.NotChecked)
	}
	if strings.Contains(txt, "No such person") {
		t.Errorf("hugo exists: %s", out.Summary)
	}
}

func TestOffboardingSpacesAndMissingMailState(t *testing.T) {
	at, _, _, fd := offToolsWith(t)
	root := fd.drives[2]["root"].(map[string]any)
	root["permissions"] = append(root["permissions"].([]any), perm("", "group", "gs", "sgnl_users", "viewer"))
	out, _ := offboard(t, at, "boss")
	if c := section(t, out, "Files: project spaces through"); c.State != stFound || c.Found[0] != "Solo (viewer through group sgnl_users)" {
		t.Errorf("groups: %+v", c)
	}
	if c := section(t, out, "Files: account"); !strings.Contains(c.Line, "for boss given: false") {
		t.Errorf("files diagnostics: %s", c.Line)
	}
	sp := section(t, out, "Files: project spaces (only")
	j := strings.Join(sp.Found, " | ")
	if sp.State != stFound || !strings.Contains(j, "Marketing (manager): ONLY MANAGER") || !strings.Contains(j, "Solo (manager): ONLY MANAGER") {
		t.Errorf("spaces: %+v", sp)
	}
	if c := section(t, out, "Files: account"); c.State != stFound || c.Found[0] != "file-app account enabled" {
		t.Errorf("files account: %+v", c)
	}
	// boss has no mailbox in the fake: checked, nothing.
	if c := section(t, out, "Mail: mailbox"); c.State != stNothing || !strings.Contains(c.Line, "no mailbox boss@co.example") {
		t.Errorf("mailbox: %+v", c)
	}
	// igor's mailbox comes without its permissions: not checked, not active.
	out, _ = offboard(t, at, "igor")
	if c := section(t, out, "Mail: mailbox"); c.State != stNot || !strings.Contains(c.Reason, "didn't return whether it may log in") {
		t.Errorf("igor mailbox: %+v", c)
	}
	// anna: an editor next to a manager, no "only manager".
	out, _ = offboard(t, at, "anna")
	if c := section(t, out, "Files: project spaces (only"); len(c.Found) != 1 || c.Found[0] != "Marketing (editor)" {
		t.Errorf("anna spaces: %+v", c)
	}
	if c := section(t, out, "AI gateway"); c.State != stNothing || !strings.Contains(c.Line, "no calls recorded") {
		t.Errorf("ai: %+v", c)
	}
}

func TestOffboardingDisabledLogin(t *testing.T) {
	at, _, fl, _ := offToolsWith(t)
	fl.users["anna"].Disabled = true
	out, _ := offboard(t, at, "anna")
	c := section(t, out, "Login server")
	if strings.Contains(strings.Join(c.Found, " "), "enabled") || !strings.Contains(c.Line, "account disabled (no login)") {
		t.Errorf("disabled: %+v", c)
	}
}

func TestOffboardingNoSuchPerson(t *testing.T) {
	at, _ := offTools(t)
	out, _ := offboard(t, at, "nobody")
	if !strings.HasPrefix(out.Summary, "No such person in the login server.") {
		t.Errorf("summary: %s", out.Summary)
	}
	if c := section(t, out, "Login server"); c.State != stNothing || !strings.Contains(c.Line, "no such person in the login server") {
		t.Errorf("login: %+v", c)
	}
	cs, _ := wsession(t, at, nil)
	if txt, isErr := call(t, cs, "admin_offboarding", map[string]any{"username": "../x"}); !isErr {
		t.Errorf("bad username accepted: %s", txt)
	}
}

// A backend that fails is NOT CHECKED, never "nothing"; a login server
// that fails is never "no such person".
func TestOffboardingBackendsDown(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer down.Close()
	_, read, _ := newFake(t)
	broken := &Client{Base: down.URL, TokenURL: read.TokenURL, ID: read.ID, Secret: read.Secret, Scope: read.Scope, HTTP: read.HTTP, Now: read.Now}
	st := auditStore(t)
	at := &Tools{API: read, UserDomain: "co.example", Audit: st,
		Mail:  &Stalwart{Base: down.URL, Key: "k", HTTP: down.Client()},
		Files: &Files{Base: down.URL, HTTP: down.Client(), Token: func(context.Context) (string, error) { return "t", nil }}}
	out, _ := offboard(t, at, "hugo")
	for _, s := range []string{"Mail: mailbox", "Mail: shared", "Files: account", "Files: project spaces (only"} {
		if c := section(t, out, s); c.State != stNot {
			t.Errorf("%s: %+v", s, c)
		}
	}
	if out.NotChecked != 10 { // 4 mail, 3 files, Forgejo, Vaultwarden, listmonk
		t.Errorf("not checked: %d (%s)", out.NotChecked, out.Summary)
	}

	// Login server down; mail and files not configured; no audit trail.
	at2 := &Tools{API: broken}
	out, txt := offboard(t, at2, "hugo")
	if c := section(t, out, "Login server"); c.State != stNot || strings.Contains(txt, "No such person") {
		t.Errorf("login down: %+v / %s", c, out.Summary)
	}
	for _, s := range []string{"Mail: mailbox", "Files: account", "AI gateway"} {
		if c := section(t, out, s); c.State != stNot {
			t.Errorf("%s unconfigured: %+v", s, c)
		}
	}
	if out.Open != 0 || !strings.HasPrefix(out.Summary, "0 open item(s)") {
		t.Errorf("summary: %s", out.Summary)
	}
}

func TestMailState(t *testing.T) {
	for _, c := range []struct {
		p    any
		want string
		ok   bool
	}{
		{map[string]any{"@type": "Inherit"}, "active", true},
		{map[string]any{"@type": "Merge", "enabledPermissions": map[string]any{}, "disabledPermissions": map[string]any{"authenticate": true}}, "suspended", true},
		{map[string]any{"@type": "Merge", "enabledPermissions": map[string]any{}, "disabledPermissions": map[string]any{"authenticate": true, "emailSend": true}}, "custom", true},
		{map[string]any{"@type": "Merge", "disabledPermissions": map[string]any{"authenticate": true}}, "custom", true},
		{map[string]any{"@type": "Replace"}, "custom", true},
		{nil, "", false},
	} {
		a := map[string]any{}
		if c.p != nil {
			a["permissions"] = c.p
		}
		got, _, ok := mailState(a)
		if got != c.want || ok != c.ok {
			t.Errorf("%v: %q %v", c.p, got, ok)
		}
	}
}
