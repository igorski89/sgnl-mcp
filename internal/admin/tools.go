package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/igorski89/sgnl-mcp/internal/auth"
	"github.com/igorski89/sgnl-mcp/internal/confirm"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tools: the admin endpoint's tools: reads with API; changes to people
// and groups (write.go) only with Write, each confirmed by the admin.
type Tools struct {
	API    *Client
	Admins []string // groups whose members may use them (default: admins)
	// Write: the gateway's admin write client (scopes authelia.admin.read
	// and .write). nil: no write tools.
	Write *Client
	// Ask: who may be asked to confirm a change (the same trusted clients
	// as the person's own confirmed actions).
	Ask *confirm.Asker
	// UserDomain: the company domain of people's addresses
	// (<username>@UserDomain). "": no admin_invite or admin_finish_invite.
	UserDomain string
	// Domains: the company's other mail domains (an invitation's set-up
	// address there is marked a company mailbox).
	Domains []string
	// Grafana: the instance's monitoring, called as the admin (nil: no
	// ops_alerts, ops_host, ops_apps, ops_backups).
	Grafana *Grafana
	// Mail: the mail server's management API with the gateway's read-only
	// key (nil: no mail admin tools).
	Mail *Stalwart
	// MailWrite: the gateway's mail key that may also change accounts
	// (read + update; Write set). nil: no mail changes. Needs Ask.
	MailWrite *Stalwart
	// MailProtected: mail accounts never changed here (machine accounts;
	// default mcp-gateway, ses-relay). People also need a login account.
	MailProtected []string
	// CertHosts: public hosts whose certificates ops_certs checks.
	CertHosts []string
	// FreeBusyShared: the instance shares everyone's free/busy by an
	// operator sync; finishing an invitation reminds to run it.
	FreeBusyShared bool
	// GroupApps: what the instance's own groups open, shown when someone
	// is added or removed (SGNL's own groups are built in).
	GroupApps map[string]string
}

const callTimeout = 30 * time.Second

func (t *Tools) Register(s *mcp.Server) {
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true}
	add(t, s, &mcp.Tool{Name: "admin_users", Annotations: ro,
		Description: "All people with an account: username, email, name, groups, and whether the account is disabled."}, t.users)
	add(t, s, &mcp.Tool{Name: "admin_user", Annotations: ro,
		Description: "One person in detail: groups, disabled, which kinds of credentials they have (password, passkey, one-time codes), how many login sessions, created and changed when."}, t.user)
	add(t, s, &mcp.Tool{Name: "admin_groups", Annotations: ro,
		Description: "All groups, with their members' usernames."}, t.groups)
	add(t, s, &mcp.Tool{Name: "admin_events", Annotations: ro,
		Description: "Recent login attempts (successful or not, from which IP), newest first; optionally for one person."}, t.events)
	add(t, s, &mcp.Tool{Name: "admin_audit", Annotations: ro,
		Description: "Recent changes to accounts and groups (who changed what), newest first; optionally for one person."}, t.audit)
	if t.Grafana != nil {
		add(t, s, &mcp.Tool{Name: "ops_alerts", Annotations: ro,
			Description: "Alerts firing now (most severe first): name, severity, since when, summary; silenced ones listed after."}, t.alerts)
		add(t, s, &mcp.Tool{Name: "ops_host", Annotations: ro,
			Description: "The server: disk, memory and CPU now and their worst in 24 h, a 24 h disk forecast, load, out-of-memory kills, up since."}, t.host)
		add(t, s, &mcp.Tool{Name: "ops_apps", Annotations: ro,
			Description: "Every app container: the deploy version, started when (≈ last deploy), restarts in 24 h, memory against its limit."}, t.appsStatus)
		add(t, s, &mcp.Tool{Name: "ops_backups", Annotations: ro,
			Description: "Backups: per job the last successful run and its age, whether the last run is unfinished (failed or still running); the last successful restore drill."}, t.backups)
	}
	if t.Mail != nil {
		const noContent = " Never any mail content."
		add(t, s, &mcp.Tool{Name: "mail_mailboxes", Annotations: ro,
			Description: "Every mailbox on the mail server: shared (group) mailboxes with their members, and people, with addresses, aliases and quota used." + noContent}, t.mailboxes)
		add(t, s, &mcp.Tool{Name: "mail_mailbox", Annotations: ro,
			Description: "One mailbox (person or shared) by name, address or alias: addresses, aliases, members or the shared mailboxes they're in, quota and used." + noContent}, t.mailbox)
		add(t, s, &mcp.Tool{Name: "mail_queue", Annotations: ro,
			Description: "Outbound mail still waiting in the queue: sender, recipients with status (scheduled, failing and why), retries, when it gives up. Optionally only to or from an address." + noContent}, t.queue)
		add(t, s, &mcp.Tool{Name: "mail_delivery", Annotations: ro,
			Description: "Was mail to this address delivered? Shows anything to it still queued or failing, and explains what an empty answer means." + noContent}, t.delivery)
	}
	if len(t.CertHosts) > 0 {
		add(t, s, &mcp.Tool{Name: "ops_certs", Annotations: ro,
			Description: "TLS certificates of the instance's public sites: expiry and days left, soonest first; broken ones first."}, t.certs)
	}
	const asks = " The admin is asked in their app first; nothing changes without their yes."
	w := &mcp.ToolAnnotations{DestructiveHint: ptr(false), OpenWorldHint: ptr(false)}
	d := &mcp.ToolAnnotations{DestructiveHint: ptr(true), OpenWorldHint: ptr(false)}
	if t.MailWrite != nil && t.Ask != nil {
		add(t, s, &mcp.Tool{Name: "mail_group_member_add", Annotations: w,
			Description: "Add a person to a shared mailbox (e.g. contact@): they read it and can send as it." + asks}, t.memberAdd)
		add(t, s, &mcp.Tool{Name: "mail_group_member_remove", Annotations: d,
			Description: "Remove a person from a shared mailbox: they lose access to it and its mail (the mailbox and mail stay). Never its last member." + asks}, t.memberRemove)
		add(t, s, &mcp.Tool{Name: "mail_quota_set", Annotations: w,
			Description: "Set a mailbox's size limit (e.g. 5 GB, or 'no limit'); never below what it already uses." + asks}, t.quotaSet)
	}
	if t.Write == nil || t.Ask == nil {
		return
	}
	if t.UserDomain != "" {
		add(t, s, &mcp.Tool{Name: "admin_invite", Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(false), OpenWorldHint: ptr(true)},
			Description: "Invite a new person: creates their login (group sgnl_invited only: no app yet) and emails a set-up link (password + passkey) to an address they can read now, usually a personal one. Afterwards: admin_finish_invite. Their company mailbox isn't created here." + asks}, t.invite)
		add(t, s, &mcp.Tool{Name: "admin_finish_invite", Annotations: w,
			Description: "End an invitation once the person has set a password and a passkey: sets their company address and gives them sgnl_users (all apps) plus the groups named." + asks}, t.finishInvite)
	}
	add(t, s, &mcp.Tool{Name: "admin_group_add", Annotations: w,
		Description: "Add a person to groups; the admin sees what each group opens." + asks}, t.groupAdd)
	add(t, s, &mcp.Tool{Name: "admin_group_remove", Annotations: d,
		Description: "Remove a person from groups; the admin sees what they lose. You can't remove yourself from the admin group or sgnl_users." + asks}, t.groupRemove)
	add(t, s, &mcp.Tool{Name: "admin_group_create", Annotations: w,
		Description: "Create a new, empty group." + asks}, t.groupCreate)
	add(t, s, &mcp.Tool{Name: "admin_setup_email", Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(false), OpenWorldHint: ptr(true)},
		Description: "Email a person a set-up link to their address on file, to set a new password and register a passkey (e.g. a lost passkey)." + asks}, t.setupEmail)
	add(t, s, &mcp.Tool{Name: "admin_logout", Annotations: d,
		Description: "End all of a person's login sessions and app tokens now (they can log in again). Not yourself." + asks}, t.logout)
	add(t, s, &mcp.Tool{Name: "admin_disable", Annotations: d,
		Description: "Disable a person's account: they lose access to everything now, every session and token ends; nothing is deleted. The admin must type the username to confirm. Not yourself." + asks}, t.disable)
	add(t, s, &mcp.Tool{Name: "admin_enable", Annotations: w,
		Description: "Enable a disabled account again." + asks}, t.enable)
}

func ptr[T any](v T) *T { return &v }

// add: every admin tool only for members of an admin group, as the login
// server has them now (not as their login says), each call logged.
func add[In, Out any](t *Tools, s *mcp.Server, tool *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	mcp.AddTool(s, tool, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		var zero Out
		start := time.Now()
		if req != nil && req.Extra != nil {
			ctx = auth.WithRequest(ctx, req.Extra.TokenInfo)
		}
		cur, ok := auth.Current(ctx)
		who := cur.Username
		if !ok {
			login, _ := auth.Person(ctx)
			who = login.Username
		}
		l := slog.With("admin_tool", tool.Name, "user", who, "args", fmt.Sprint(in))
		if !ok {
			l.Warn("admin call refused", "outcome", "not checkable")
			return nil, zero, fmt.Errorf("couldn't check with the login server whether you're an admin: try again in a moment")
		}
		if !slices.ContainsFunc(t.admins(), func(g string) bool { return slices.Contains(cur.Groups, g) }) {
			l.Warn("admin call refused", "outcome", "not an admin")
			return nil, zero, fmt.Errorf("these tools are for admins only")
		}
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		res, out, err := h(ctx, req, in)
		l = l.With("ms", time.Since(start).Milliseconds())
		switch {
		case errors.Is(err, errDecl):
			l.Info("admin call", "outcome", "declined")
		case err != nil:
			l.Warn("admin call", "outcome", "error", "error", err.Error())
		case res != nil && len(res.InputRequests) > 0:
			l.Info("admin call", "outcome", "asked")
		default:
			l.Info("admin call", "outcome", "ok")
		}
		return res, out, err
	})
}

func (t *Tools) admins() []string {
	if len(t.Admins) == 0 {
		return []string{"admins"}
	}
	return t.Admins
}

// ---- users

type User struct {
	Username    string   `json:"username"`
	Email       string   `json:"email,omitempty"`
	Name        string   `json:"name,omitempty"`
	Groups      []string `json:"groups"`
	Disabled    bool     `json:"disabled,omitempty"`
	Credentials []string `json:"credentials,omitempty" jsonschema:"kinds of credentials (password, webauthn = passkey, totp = one-time codes)"`
	Sessions    *int     `json:"sessions,omitempty" jsonschema:"login (portal) sessions"`
	Created     string   `json:"created,omitempty"`
	Updated     string   `json:"updated,omitempty"`
}

type apiUser struct {
	Username    string   `json:"username"`
	Email       string   `json:"email"`
	DisplayName string   `json:"display_name"`
	Groups      []string `json:"groups"`
	Disabled    bool     `json:"disabled"`
	Credentials []string `json:"credentials"`
	Sessions    *int     `json:"sessions"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

func (u apiUser) out(detail bool) User {
	g := slices.Sorted(slices.Values(u.Groups))
	if g == nil {
		g = []string{}
	}
	o := User{Username: u.Username, Email: u.Email, Name: u.DisplayName, Groups: g, Disabled: u.Disabled}
	if detail {
		o.Credentials, o.Sessions, o.Created, o.Updated = slices.Sorted(slices.Values(u.Credentials)), u.Sessions, u.CreatedAt, u.UpdatedAt
	}
	return o
}

type UsersOut struct {
	Users []User `json:"users"`
	Count int    `json:"count"`
}

func (t *Tools) allUsers(ctx context.Context) ([]apiUser, error) {
	var all []apiUser
	for page := 0; page < 20; page++ { // up to 10 000 accounts
		var r struct{ Users []apiUser }
		if err := t.API.get(ctx, "/users", url.Values{"limit": {"500"}, "page": {strconv.Itoa(page)}}, &r); err != nil {
			return nil, err
		}
		all = append(all, r.Users...)
		if len(r.Users) < 500 {
			break
		}
	}
	return all, nil
}

func (t *Tools) users(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, UsersOut, error) {
	all, err := t.allUsers(ctx)
	if err != nil {
		return nil, UsersOut{}, err
	}
	out := UsersOut{Users: []User{}}
	for _, u := range all {
		out.Users = append(out.Users, u.out(false))
	}
	slices.SortFunc(out.Users, func(a, b User) int { return strings.Compare(a.Username, b.Username) })
	out.Count = len(out.Users)
	return nil, out, nil
}

type UserIn struct {
	Username string `json:"username" jsonschema:"the person's username"`
}

func (t *Tools) user(ctx context.Context, _ *mcp.CallToolRequest, in UserIn) (*mcp.CallToolResult, User, error) {
	if strings.TrimSpace(in.Username) == "" {
		return nil, User{}, fmt.Errorf("username is required")
	}
	var u apiUser
	if err := t.API.get(ctx, "/users/"+url.PathEscape(in.Username), nil, &u); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, User{}, fmt.Errorf("no account %q", in.Username)
		}
		return nil, User{}, err
	}
	return nil, u.out(true), nil
}

// ---- groups

type Group struct {
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

type GroupsOut struct {
	Groups []Group `json:"groups"`
}

func (t *Tools) groups(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, GroupsOut, error) {
	var r struct {
		Groups []struct {
			Name string `json:"name"`
		}
	}
	if err := t.API.get(ctx, "/groups", nil, &r); err != nil {
		return nil, GroupsOut{}, err
	}
	// Members from the user list (one call, not one per group).
	all, err := t.allUsers(ctx)
	if err != nil {
		return nil, GroupsOut{}, err
	}
	members := map[string][]string{}
	for _, u := range all {
		for _, g := range u.Groups {
			members[g] = append(members[g], u.Username)
		}
	}
	out := GroupsOut{Groups: []Group{}}
	for _, g := range r.Groups {
		m := slices.Sorted(slices.Values(members[g.Name]))
		if m == nil {
			m = []string{}
		}
		out.Groups = append(out.Groups, Group{g.Name, m})
	}
	slices.SortFunc(out.Groups, func(a, b Group) int { return strings.Compare(a.Name, b.Name) })
	return nil, out, nil
}

// ---- events and audit

type LogIn struct {
	Username string `json:"username,omitempty" jsonschema:"only this person"`
	Limit    int    `json:"limit,omitempty" jsonschema:"how many (default 20, at most 100)"`
}

func (in LogIn) query() url.Values {
	if in.Limit < 1 || in.Limit > 100 {
		in.Limit = 20
	}
	q := url.Values{"limit": {strconv.Itoa(in.Limit)}}
	if in.Username != "" {
		q.Set("username", in.Username)
	}
	return q
}

type Event struct {
	Time       string `json:"time"`
	Username   string `json:"username"`
	Type       string `json:"type" jsonschema:"1FA = password, 2FA = second factor"`
	Successful bool   `json:"successful"`
	Banned     bool   `json:"banned,omitempty"`
	RemoteIP   string `json:"remote_ip,omitempty"`
}

type EventsOut struct {
	Events []Event `json:"events"`
}

func (t *Tools) events(ctx context.Context, _ *mcp.CallToolRequest, in LogIn) (*mcp.CallToolResult, EventsOut, error) {
	var r struct {
		Events []Event `json:"events"`
	}
	if err := t.API.get(ctx, "/events", in.query(), &r); err != nil {
		return nil, EventsOut{}, err
	}
	if r.Events == nil {
		r.Events = []Event{}
	}
	return nil, EventsOut{r.Events}, nil
}

type Change struct {
	Time     string         `json:"time"`
	Actor    string         `json:"actor" jsonschema:"who changed it: client:<id> or cli:<user>"`
	Action   string         `json:"action"`
	Username string         `json:"username,omitempty"`
	Group    string         `json:"group,omitempty"`
	Details  map[string]any `json:"details,omitempty"`
}

type AuditOut struct {
	Changes []Change `json:"changes"`
}

func (t *Tools) audit(ctx context.Context, _ *mcp.CallToolRequest, in LogIn) (*mcp.CallToolResult, AuditOut, error) {
	var r struct {
		Entries []Change `json:"entries"`
	}
	if err := t.API.get(ctx, "/audit", in.query(), &r); err != nil {
		return nil, AuditOut{}, err
	}
	if r.Entries == nil {
		r.Entries = []Change{}
	}
	return nil, AuditOut{r.Entries}, nil
}
