package admin

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/igorski89/sgnl-mcp/internal/auth"
	"github.com/igorski89/sgnl-mcp/internal/textx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The admin endpoint's changes to people and groups, each the same as a
// bin/sgnl-id command (so the outcome is identical), each only after the
// acting admin confirms in their app. They go through a separate login
// client (Tools.Write, scopes authelia.admin.read + .write): without it
// there are no write tools. Not here: deleting people, passwords and
// passkeys, lockouts (the login server's own CLI, break-glass).

const (
	invitedGroup = "sgnl_invited"
	usersGroup   = "sgnl_users"
)

// Built-in meaning of SGNL's own groups (docs/USERS.md); an instance adds
// or overrides its own with Tools.GroupApps.
var defaultApps = map[string]string{
	usersGroup:      "ALL the company's apps (mail, calendar, files, chat, passwords, …)",
	"forgejo_users": "Forgejo, the code app (together with sgnl_users)",
	"admins":        "ADMIN rights: admin in Forgejo, OpenCloud and the dashboard, and these admin tools",
	invitedGroup:    "only the login portal's settings page (set-up)",
}

// freeBusyNote: where everyone's free/busy is shared by an operator sync,
// a new person is in it only once the sync has run for them.
func (t *Tools) freeBusyNote() string {
	if !t.FreeBusyShared {
		return ""
	}
	return " Calendar free/busy: shared with colleagues only after bin/sgnl-freebusy-sync runs for them (now, and once after their first login); tell whoever runs the instance."
}

const vaultNote = "Password manager: its members and collections follow the groups only when bin/sgnl-vault-sync runs on an admin's laptop; tell whoever runs the instance."

var (
	reName  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	errDecl = errors.New("the person declined: nothing was changed")
)

func validName(kind, s string) error {
	if !reName.MatchString(s) {
		return fmt.Errorf("%s %q isn't valid: lowercase letters, digits, '.', '_' or '-', at most 64", kind, short(s))
	}
	return nil
}

// short: a value from the model for an error message, shortened.
func short(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, textx.StripHidden(s))
	if r := []rune(s); len(r) > 64 {
		s = string(r[:64]) + "…"
	}
	return s
}

// clean: only visible characters, nothing that could fake dialog lines.
func clean(s string) bool {
	return textx.StripHidden(s) == s && !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) })
}

// plain: a name for a dialog; refused (not shortened) if it is long or
// carries anything that isn't plain visible text, so the dialog shows it
// whole.
func plain(kind, s string, max int) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("%s is required", kind)
	}
	if len([]rune(s)) > max || !clean(s) || strings.ContainsAny(s, "\"<>") {
		return "", fmt.Errorf("%s %q isn't plain text of at most %d characters", kind, short(s), max)
	}
	return s, nil
}

// address: one bare email address, refused if long or anything else.
func address(s string) (string, error) {
	s = strings.TrimSpace(s)
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || a.Name != "" || len(s) > 120 || !clean(s) || strings.ContainsAny(s, " \"") {
		return "", fmt.Errorf("%q isn't one plain email address", short(s))
	}
	return s, nil
}

func domainOf(email string) string {
	_, d, _ := strings.Cut(email, "@")
	return strings.ToLower(d)
}

func (t *Tools) company(email string) bool {
	return slices.Contains(t.domains(), domainOf(email))
}

func (t *Tools) domains() []string {
	var ds []string
	for _, d := range append([]string{t.UserDomain}, t.Domains...) {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" && !slices.Contains(ds, d) {
			ds = append(ds, d)
		}
	}
	return ds
}

func (t *Tools) mark(email string) string {
	if t.company(email) {
		return "(a COMPANY mailbox)"
	}
	return "(outside)"
}

// apps: what a group opens, as the person reads it.
func (t *Tools) apps(g string) string {
	a := t.GroupApps[g]
	if a == "" {
		a = defaultApps[g]
	}
	if a == "" {
		a = "no app access known (a group for sharing, e.g. folders, mailboxes or password collections)"
	}
	if slices.Contains(t.admins(), g) && !strings.Contains(a, "ADMIN") {
		a = "ADMIN rights (these admin tools); " + a
	}
	return g + ": " + a
}

func (t *Tools) appsOf(gs []string) []string {
	out := []string{}
	for _, g := range gs {
		out = append(out, t.apps(g))
	}
	return out
}

func (t *Tools) anyAdmin(gs []string) bool {
	return slices.ContainsFunc(gs, func(g string) bool { return slices.Contains(t.admins(), g) })
}

func esc(s string) string { return url.PathEscape(s) }

// lookup: one person, as the login server has them now.
func (t *Tools) lookup(ctx context.Context, name string) (apiUser, error) {
	var u apiUser
	if err := validName("username", name); err != nil {
		return u, err
	}
	err := t.API.get(ctx, "/users/"+esc(name), nil, &u)
	if errors.Is(err, errNotFound) {
		return u, fmt.Errorf("no account %q", name)
	}
	return u, err
}

func (t *Tools) groupNames(ctx context.Context) ([]string, error) {
	var r struct{ Groups []struct{ Name string } }
	if err := t.API.get(ctx, "/groups", nil, &r); err != nil {
		return nil, err
	}
	var out []string
	for _, g := range r.Groups {
		out = append(out, g.Name)
	}
	return out, nil
}

// checkGroups: valid, existing, without duplicates.
func (t *Tools) checkGroups(ctx context.Context, gs []string) ([]string, error) {
	if len(gs) == 0 {
		return nil, fmt.Errorf("name at least one group")
	}
	if len(gs) > 20 {
		return nil, fmt.Errorf("at most 20 groups at once")
	}
	known, err := t.groupNames(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, g := range gs {
		g = strings.TrimSpace(g)
		if err := validName("group", g); err != nil {
			return nil, err
		}
		if !slices.Contains(known, g) {
			return nil, fmt.Errorf("no group %q (see admin_groups; admin_group_create makes one)", g)
		}
		if !slices.Contains(out, g) {
			out = append(out, g)
		}
	}
	return out, nil
}

// actor: the acting admin's username, as the login server has it now.
func actor(ctx context.Context) string {
	c, _ := auth.Current(ctx)
	return c.Username
}

func (u apiUser) bind() string {
	return fmt.Sprintf("%s|%s|%v|%s|%s", u.Username, u.Email, u.Disabled,
		strings.Join(slices.Sorted(slices.Values(u.Groups)), ","), strings.Join(slices.Sorted(slices.Values(u.Credentials)), ","))
}

func hours(h, def int) (int, error) {
	if h == 0 {
		return def, nil
	}
	if h < 1 || h > 168 {
		return 0, fmt.Errorf("hours: 1 to 168")
	}
	return h, nil
}

// ChangeOut: what was done.
type ChangeOut struct {
	Done      string   `json:"done"`
	Username  string   `json:"username,omitempty"`
	Email     string   `json:"email,omitempty"`
	Groups    []string `json:"groups,omitempty" jsonschema:"their groups now"`
	Apps      []string `json:"apps,omitempty" jsonschema:"what the changed groups open or close"`
	LinkUntil string   `json:"link_valid_until,omitempty"`
	Note      string   `json:"note,omitempty"`
}

// partial: an error saying what was already done before a step failed.
func partial(done []string, err error) error {
	if len(done) == 0 {
		return err
	}
	return fmt.Errorf("stopped half-way: done: %s; then failed: %w. Check with admin_user and finish it by hand", strings.Join(done, "; "), err)
}

func (t *Tools) groupsNow(ctx context.Context, name string) []string {
	u, err := t.lookup(ctx, name)
	if err != nil {
		return nil
	}
	return u.out(false).Groups
}

// ---- admin_invite

type InviteIn struct {
	Username string `json:"username" jsonschema:"the new login name, e.g. anna (lowercase); their company address will be <username>@<company domain>"`
	Name     string `json:"name" jsonschema:"first and last name"`
	Email    string `json:"email" jsonschema:"where the set-up link goes: an address they can read now (a personal one), not their future company address"`
	Hours    int    `json:"hours,omitempty" jsonschema:"how long the link is valid (default 72, at most 168)"`
}

func (t *Tools) invite(ctx context.Context, req *mcp.CallToolRequest, in InviteIn) (*mcp.CallToolResult, ChangeOut, error) {
	if err := validName("username", in.Username); err != nil {
		return nil, ChangeOut{}, err
	}
	name, err := plain("name", in.Name, 80)
	if err != nil {
		return nil, ChangeOut{}, err
	}
	email, err := address(in.Email)
	if err != nil {
		return nil, ChangeOut{}, err
	}
	h, err := hours(in.Hours, 72)
	if err != nil {
		return nil, ChangeOut{}, err
	}
	for _, d := range t.domains() {
		if strings.EqualFold(email, in.Username+"@"+d) {
			return nil, ChangeOut{}, fmt.Errorf("%s is the account's own future address: they can't read it before they can log in. Use an address they can read now (a personal one)", email)
		}
	}
	if _, err := t.lookup(ctx, in.Username); err == nil {
		return nil, ChangeOut{}, fmt.Errorf("%q already has an account (admin_user); for a new set-up link use admin_setup_email", in.Username)
	} else if !strings.HasPrefix(err.Error(), "no account") {
		return nil, ChangeOut{}, err
	}
	if _, err := t.checkGroups(ctx, []string{invitedGroup}); err != nil {
		return nil, ChangeOut{}, err
	}
	future := in.Username + "@" + t.UserDomain
	q := fmt.Sprintf("Invite %q as %s, set-up link to %s %s?\n\n", name, future, email, t.mark(email)) +
		fmt.Sprintf("Whoever reads %s can set the password and passkey of this new account (link valid %d h). "+
			"Until you finish the invite (admin_finish_invite) they reach no app, only their login settings.", email, h)
	res, ok, err := t.Ask.Ask(ctx, req, q, fmt.Sprint(t.UserDomain, "|", h))
	if err != nil || res != nil {
		return res, ChangeOut{}, err
	}
	if !ok {
		return nil, ChangeOut{}, errDecl
	}
	first, last, _ := strings.Cut(name, " ")
	if err := t.Write.send(ctx, "POST", "/users", map[string]any{"username": in.Username, "email": email, "display_name": name,
		"given_name": first, "family_name": last, "groups": []string{invitedGroup}}, nil, false); err != nil {
		return nil, ChangeOut{}, err
	}
	var link struct {
		Email     string `json:"email"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := t.Write.send(ctx, "POST", "/users/"+esc(in.Username)+"/invite", map[string]any{"lifespan": strconv.Itoa(h) + "h"}, &link, false); err != nil {
		return nil, ChangeOut{}, fmt.Errorf("the account %s was created (invited: no app access), but the set-up link wasn't sent (%w): send it with admin_setup_email", in.Username, err)
	}
	return nil, ChangeOut{Done: "invited", Username: in.Username, Email: firstOf(link.Email, email), Groups: []string{invitedGroup},
		LinkUntil: link.ExpiresAt,
		Note: "Created with the person's confirmation, in " + invitedGroup + " only; the set-up link was emailed. " +
			"Tell them on another channel to expect it. When they have set a password and a passkey: admin_finish_invite. " +
			"Their company mailbox (" + future + ") isn't made here: it comes from the instance's mail plan (bin/sgnl-add-user)."}, nil
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ---- admin_finish_invite

type FinishIn struct {
	Username string   `json:"username"`
	Groups   []string `json:"groups,omitempty" jsonschema:"groups besides sgnl_users (always added), e.g. developers"`
}

func (t *Tools) finishInvite(ctx context.Context, req *mcp.CallToolRequest, in FinishIn) (*mcp.CallToolResult, ChangeOut, error) {
	u, err := t.lookup(ctx, in.Username)
	if err != nil {
		return nil, ChangeOut{}, err
	}
	if !slices.Contains(u.Groups, invitedGroup) {
		return nil, ChangeOut{}, fmt.Errorf("%s isn't an invited account (not in %s): use admin_group_add", in.Username, invitedGroup)
	}
	if u.Disabled {
		return nil, ChangeOut{}, fmt.Errorf("%s is disabled: admin_enable first", in.Username)
	}
	if !slices.Contains(u.Credentials, "password") || !slices.Contains(u.Credentials, "webauthn") {
		have := strings.Join(slices.Sorted(slices.Values(u.Credentials)), ", ")
		if have == "" {
			have = "none"
		}
		return nil, ChangeOut{}, fmt.Errorf("%s hasn't finished set-up yet: needs a password and a passkey (has: %s)", in.Username, have)
	}
	add, err := t.checkGroups(ctx, append([]string{usersGroup}, in.Groups...))
	if err != nil {
		return nil, ChangeOut{}, err
	}
	if slices.Contains(add, invitedGroup) {
		return nil, ChangeOut{}, fmt.Errorf("%s is what the invite ends: don't name it", invitedGroup)
	}
	email := in.Username + "@" + t.UserDomain
	first := fmt.Sprintf("Finish the set-up of %s: email %s, groups %s?", in.Username, email, strings.Join(add, ", "))
	if t.anyAdmin(add) {
		first = fmt.Sprintf("Finish the set-up of %s WITH ADMIN RIGHTS: email %s, groups %s?", in.Username, email, strings.Join(add, ", "))
	}
	q := first + "\n\nThis opens:\n- " + strings.Join(t.appsOf(add), "\n- ")
	res, ok, err := t.Ask.Ask(ctx, req, q, u.bind()+"|"+email)
	if err != nil || res != nil {
		return res, ChangeOut{}, err
	}
	if !ok {
		return nil, ChangeOut{}, errDecl
	}
	var done []string
	if err := t.Write.send(ctx, "PATCH", "/users/"+esc(in.Username), map[string]any{"email": email}, nil, false); err != nil {
		return nil, ChangeOut{}, err
	}
	done = append(done, "email set to "+email)
	for _, g := range add {
		if err := t.Write.send(ctx, "PUT", "/users/"+esc(in.Username)+"/groups/"+esc(g), nil, nil, false); err != nil {
			return nil, ChangeOut{}, partial(done, err)
		}
		done = append(done, "added to "+g)
	}
	if err := t.Write.send(ctx, "DELETE", "/users/"+esc(in.Username)+"/groups/"+invitedGroup, nil, nil, true); err != nil {
		return nil, ChangeOut{}, partial(done, err)
	}
	return nil, ChangeOut{Done: "set-up finished", Username: in.Username, Email: email, Groups: t.groupsNow(ctx, in.Username), Apps: t.appsOf(add),
		Note: "Done with the person's confirmation. Tell them they can use the apps now. " + vaultNote + t.freeBusyNote()}, nil
}

// ---- admin_group_add / admin_group_remove

type GroupChangeIn struct {
	Username string   `json:"username"`
	Groups   []string `json:"groups"`
}

func (t *Tools) groupAdd(ctx context.Context, req *mcp.CallToolRequest, in GroupChangeIn) (*mcp.CallToolResult, ChangeOut, error) {
	return t.groupChange(ctx, req, in, true)
}

func (t *Tools) groupRemove(ctx context.Context, req *mcp.CallToolRequest, in GroupChangeIn) (*mcp.CallToolResult, ChangeOut, error) {
	return t.groupChange(ctx, req, in, false)
}

func (t *Tools) groupChange(ctx context.Context, req *mcp.CallToolRequest, in GroupChangeIn, add bool) (*mcp.CallToolResult, ChangeOut, error) {
	u, err := t.lookup(ctx, in.Username)
	if err != nil {
		return nil, ChangeOut{}, err
	}
	gs, err := t.checkGroups(ctx, in.Groups)
	if err != nil {
		return nil, ChangeOut{}, err
	}
	if slices.Contains(gs, invitedGroup) {
		return nil, ChangeOut{}, fmt.Errorf("%s is managed by admin_invite and admin_finish_invite", invitedGroup)
	}
	var change []string
	for _, g := range gs {
		if slices.Contains(u.Groups, g) != add {
			change = append(change, g)
		}
	}
	if len(change) == 0 {
		return nil, ChangeOut{}, fmt.Errorf("nothing to change: %s is %s", in.Username,
			map[bool]string{true: "already in those groups", false: "in none of those groups"}[add])
	}
	if !add && in.Username == actor(ctx) && slices.ContainsFunc(change, func(g string) bool { return g == usersGroup || slices.Contains(t.admins(), g) }) {
		return nil, ChangeOut{}, fmt.Errorf("you can't remove yourself from %s here: you'd lock yourself out. Another admin can", strings.Join(change, ", "))
	}
	var first, apps string
	switch {
	case add && t.anyAdmin(change):
		first = fmt.Sprintf("Give %s ADMIN RIGHTS: add to %s?", in.Username, strings.Join(change, ", "))
	case add:
		first = fmt.Sprintf("Add %s to %s?", in.Username, strings.Join(change, ", "))
	case slices.Contains(change, usersGroup):
		first = fmt.Sprintf("Remove %s from %s? They lose EVERY app.", in.Username, strings.Join(change, ", "))
	default:
		first = fmt.Sprintf("Remove %s from %s?", in.Username, strings.Join(change, ", "))
	}
	apps = map[bool]string{true: "This opens:", false: "They lose:"}[add]
	q := first + "\n\n" + apps + "\n- " + strings.Join(t.appsOf(change), "\n- ")
	res, ok, err := t.Ask.Ask(ctx, req, q, u.bind())
	if err != nil || res != nil {
		return res, ChangeOut{}, err
	}
	if !ok {
		return nil, ChangeOut{}, errDecl
	}
	var done []string
	for _, g := range change {
		if add {
			err = t.Write.send(ctx, "PUT", "/users/"+esc(in.Username)+"/groups/"+esc(g), nil, nil, false)
		} else {
			err = t.Write.send(ctx, "DELETE", "/users/"+esc(in.Username)+"/groups/"+esc(g), nil, nil, true)
		}
		if err != nil {
			return nil, ChangeOut{}, partial(done, err)
		}
		done = append(done, map[bool]string{true: "added to ", false: "removed from "}[add]+g)
	}
	d := map[bool]string{true: "added to " + strings.Join(change, ", "), false: "removed from " + strings.Join(change, ", ")}[add]
	return nil, ChangeOut{Done: d, Username: in.Username, Groups: t.groupsNow(ctx, in.Username), Apps: t.appsOf(change),
		Note: "Done with the person's confirmation; it applies at their next login to each app. " + vaultNote}, nil
}

// ---- admin_group_create

type GroupCreateIn struct {
	Name string `json:"name" jsonschema:"the new group's name (lowercase)"`
}

func (t *Tools) groupCreate(ctx context.Context, req *mcp.CallToolRequest, in GroupCreateIn) (*mcp.CallToolResult, ChangeOut, error) {
	if err := validName("group", in.Name); err != nil {
		return nil, ChangeOut{}, err
	}
	known, err := t.groupNames(ctx)
	if err != nil {
		return nil, ChangeOut{}, err
	}
	if slices.Contains(known, in.Name) {
		return nil, ChangeOut{}, fmt.Errorf("group %q already exists", in.Name)
	}
	res, ok, err := t.Ask.Ask(ctx, req, fmt.Sprintf("Create the empty group %q?\n\nIt opens no app by itself; apps or shared items may use it later.", in.Name), "")
	if err != nil || res != nil {
		return res, ChangeOut{}, err
	}
	if !ok {
		return nil, ChangeOut{}, errDecl
	}
	if err := t.Write.send(ctx, "POST", "/groups", map[string]any{"name": in.Name}, nil, false); err != nil {
		return nil, ChangeOut{}, err
	}
	return nil, ChangeOut{Done: "group " + in.Name + " created, empty", Note: "Created with the person's confirmation. Add people with admin_group_add."}, nil
}

// ---- admin_setup_email

type SetupIn struct {
	Username string `json:"username"`
	Hours    int    `json:"hours,omitempty" jsonschema:"how long the link is valid (default 24, at most 168)"`
}

func (t *Tools) setupEmail(ctx context.Context, req *mcp.CallToolRequest, in SetupIn) (*mcp.CallToolResult, ChangeOut, error) {
	u, err := t.lookup(ctx, in.Username)
	if err != nil {
		return nil, ChangeOut{}, err
	}
	h, err := hours(in.Hours, 24)
	if err != nil {
		return nil, ChangeOut{}, err
	}
	if u.Disabled {
		return nil, ChangeOut{}, fmt.Errorf("%s is disabled: admin_enable first", in.Username)
	}
	if u.Email == "" {
		return nil, ChangeOut{}, fmt.Errorf("%s has no email address to send the link to", in.Username)
	}
	q := fmt.Sprintf("Email %s a set-up link to %s %s?\n\nWhoever reads %s can set a new password and register a passkey for %s (link valid %d h).",
		in.Username, short(u.Email), t.mark(u.Email), short(u.Email), in.Username, h)
	res, ok, err := t.Ask.Ask(ctx, req, q, u.bind())
	if err != nil || res != nil {
		return res, ChangeOut{}, err
	}
	if !ok {
		return nil, ChangeOut{}, errDecl
	}
	var link struct {
		Email     string `json:"email"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := t.Write.send(ctx, "POST", "/users/"+esc(in.Username)+"/invite", map[string]any{"lifespan": strconv.Itoa(h) + "h"}, &link, false); err != nil {
		return nil, ChangeOut{}, err
	}
	return nil, ChangeOut{Done: "set-up link emailed", Username: in.Username, Email: firstOf(link.Email, u.Email), LinkUntil: link.ExpiresAt,
		Note: "Sent with the person's confirmation. Tell them on another channel to expect it."}, nil
}

// ---- admin_logout

type UserOnlyIn struct {
	Username string `json:"username"`
}

func (t *Tools) logout(ctx context.Context, req *mcp.CallToolRequest, in UserOnlyIn) (*mcp.CallToolResult, ChangeOut, error) {
	u, err := t.lookup(ctx, in.Username)
	if err != nil {
		return nil, ChangeOut{}, err
	}
	if in.Username == actor(ctx) {
		return nil, ChangeOut{}, fmt.Errorf("you can't log yourself out here: it would end this connection too. Use the login portal")
	}
	q := fmt.Sprintf("Log %s out everywhere now?\n\nEvery login session and app token of theirs ends; they can log in again right away with their own password and passkey.", in.Username)
	res, ok, err := t.Ask.Ask(ctx, req, q, u.bind())
	if err != nil || res != nil {
		return res, ChangeOut{}, err
	}
	if !ok {
		return nil, ChangeOut{}, errDecl
	}
	if err := t.Write.send(ctx, "POST", "/users/"+esc(in.Username)+"/logout", map[string]any{}, nil, false); err != nil {
		return nil, ChangeOut{}, err
	}
	return nil, ChangeOut{Done: "all sessions and tokens ended", Username: in.Username,
		Note: "Done with the person's confirmation. Apps that keep their own session may stay open until it runs out."}, nil
}

// ---- admin_disable / admin_enable

func (t *Tools) disable(ctx context.Context, req *mcp.CallToolRequest, in UserOnlyIn) (*mcp.CallToolResult, ChangeOut, error) {
	u, err := t.lookup(ctx, in.Username)
	if err != nil {
		return nil, ChangeOut{}, err
	}
	if in.Username == actor(ctx) {
		return nil, ChangeOut{}, fmt.Errorf("you can't disable yourself: another admin can")
	}
	if u.Disabled {
		return nil, ChangeOut{}, fmt.Errorf("%s is already disabled", in.Username)
	}
	q := fmt.Sprintf("Disable %s? They lose access to EVERYTHING now.\n\nEvery session and token of theirs ends and they can't log in until enabled again (admin_enable). Nothing of theirs is deleted.\n\nType their username, %s, to confirm.", in.Username, in.Username)
	res, ok, err := t.Ask.AskTyped(ctx, req, q, u.bind(), "username", in.Username)
	if err != nil || res != nil {
		return res, ChangeOut{}, err
	}
	if !ok {
		return nil, ChangeOut{}, errDecl
	}
	if err := t.Write.send(ctx, "PATCH", "/users/"+esc(in.Username), map[string]any{"disabled": true}, nil, false); err != nil {
		return nil, ChangeOut{}, err
	}
	return nil, ChangeOut{Done: "disabled (every session and token ended)", Username: in.Username,
		Note: "Done with the person's confirmation. " + vaultNote}, nil
}

func (t *Tools) enable(ctx context.Context, req *mcp.CallToolRequest, in UserOnlyIn) (*mcp.CallToolResult, ChangeOut, error) {
	u, err := t.lookup(ctx, in.Username)
	if err != nil {
		return nil, ChangeOut{}, err
	}
	if !u.Disabled {
		return nil, ChangeOut{}, fmt.Errorf("%s isn't disabled", in.Username)
	}
	q := fmt.Sprintf("Enable %s again?\n\nThey can log in again with their own password and passkey, with their groups as before: %s.", in.Username,
		strings.Join(u.out(false).Groups, ", "))
	res, ok, err := t.Ask.Ask(ctx, req, q, u.bind())
	if err != nil || res != nil {
		return res, ChangeOut{}, err
	}
	if !ok {
		return nil, ChangeOut{}, errDecl
	}
	if err := t.Write.send(ctx, "PATCH", "/users/"+esc(in.Username), map[string]any{"disabled": false}, nil, false); err != nil {
		return nil, ChangeOut{}, err
	}
	return nil, ChangeOut{Done: "enabled", Username: in.Username, Groups: u.out(false).Groups, Note: "Done with the person's confirmation. " + vaultNote}, nil
}
