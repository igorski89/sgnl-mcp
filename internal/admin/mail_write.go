package admin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/igorski89/sgnl-mcp/internal/textx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Mail admin changes: who is in a shared mailbox, a person's extra
// addresses (aliases), a mailbox's size limit. The live mail server is the
// record for these (the instance's plan in git keeps only which accounts
// and groups exist). Through a separate key (Tools.MailWrite) so changes
// can be switched off alone; each change is one x:Account/set with one
// update of memberGroupIds, aliases or quotas, written whole: the server's
// current value with this one change, bound to what the admin was shown.
// Never: creating or deleting accounts, groups or mail; machine accounts.

// The only properties an update may carry, and what an alias may hold.
var (
	setProps = []string{"memberGroupIds", "aliases", "quotas"}
	// description: kept as the server has it on existing aliases (it
	// returns the key, often null); a new alias never gets one.
	aliasProps = []string{"name", "domainId", "enable", "description"}
)

// checkSet: x:Account/set with exactly one update of setProps, each a
// whole object of the expected shape; nothing else (no create, destroy,
// pointer patch).
func checkSet(c mcall) error {
	if c.method != "x:Account/set" {
		return fmt.Errorf("the gateway doesn't call %s", c.method)
	}
	for k := range c.args {
		if k != "update" {
			return fmt.Errorf("x:Account/set: %s is never sent", k)
		}
	}
	up, _ := c.args["update"].(map[string]any)
	if len(up) != 1 {
		return fmt.Errorf("x:Account/set: exactly one update")
	}
	for _, v := range up {
		patch, _ := v.(map[string]any)
		if len(patch) == 0 {
			return fmt.Errorf("x:Account/set: an empty update")
		}
		for k, pv := range patch {
			if !slices.Contains(setProps, k) {
				return fmt.Errorf("x:Account/set: %s is never changed", k)
			}
			obj, ok := pv.(map[string]any)
			if !ok {
				return fmt.Errorf("x:Account/set: %s only as a whole object", k)
			}
			for ok2, ov := range obj {
				switch k {
				case "memberGroupIds":
					if ov != true {
						return fmt.Errorf("x:Account/set: memberGroupIds/%s must be true", short(ok2))
					}
				case "aliases":
					al, ok := ov.(map[string]any)
					if !ok {
						return fmt.Errorf("x:Account/set: an alias that isn't an object")
					}
					for ak, av := range al {
						if !slices.Contains(aliasProps, ak) {
							return fmt.Errorf("x:Account/set: alias property %s", short(ak))
						}
						if ak == "description" && av == nil {
							continue
						}
						if _, s := av.(string); ak != "enable" && !s {
							return fmt.Errorf("x:Account/set: alias %s isn't text", ak)
						}
						if _, b := av.(bool); ak == "enable" && !b {
							return fmt.Errorf("x:Account/set: alias enable isn't true/false")
						}
					}
				case "quotas":
					switch ov.(type) {
					case float64, int64, int, nil:
					default:
						return fmt.Errorf("x:Account/set: quota %s isn't a number", short(ok2))
					}
				}
			}
		}
	}
	return nil
}

// mailDir: the accounts (as the server has them, and as people read them).
type mailDir struct {
	raw     map[string]map[string]any // by id
	domains map[string]string         // id → name
	views   []MailAccount
}

func (t *Tools) mailDir(ctx context.Context) (*mailDir, error) {
	accts, domains, err := directory(ctx, t.MailWrite)
	if err != nil {
		return nil, err
	}
	d := &mailDir{raw: map[string]map[string]any{}, domains: domains, views: accountsFrom(accts, domains)}
	for _, a := range accts {
		d.raw[str(a, "id")] = a
	}
	return d, nil
}

// find: one account by name, address or (enabled) alias.
func (d *mailDir) find(q string) (*MailAccount, map[string]any) {
	want := strings.ToLower(strings.TrimSpace(q))
	for i, a := range d.views {
		if strings.ToLower(a.Name) == want || strings.ToLower(a.Address) == want || slices.ContainsFunc(a.Aliases, func(x string) bool { return strings.ToLower(x) == want }) {
			return &d.views[i], d.raw[a.accountID]
		}
	}
	return nil, nil
}

// taken: every address the server routes, enabled or not: account names
// and addresses, every alias.
func (d *mailDir) taken(addr string) string {
	addr = strings.ToLower(addr)
	for _, a := range d.raw {
		dn := d.domains[str(a, "domainId")]
		if strings.ToLower(str(a, "emailAddress")) == addr || strings.ToLower(str(a, "name")+"@"+dn) == addr {
			return str(a, "name")
		}
		for _, al := range aliasMap(a) {
			ad := d.domains[str(al, "domainId")]
			if ad == "" {
				ad = dn
			}
			if strings.ToLower(str(al, "name")+"@"+ad) == addr || strings.ToLower(str(al, "name")) == addr {
				return str(a, "name")
			}
		}
	}
	return ""
}

// aliasMap: the account's aliases as the server keeps them (key → object),
// a list read as keys "0", "1", ….
func aliasMap(a map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	switch x := a["aliases"].(type) {
	case map[string]any:
		for k, v := range x {
			if m, ok := v.(map[string]any); ok {
				out[k] = m
			}
		}
	case []any:
		for i, v := range x {
			if m, ok := v.(map[string]any); ok {
				out[strconv.Itoa(i)] = m
			}
		}
	}
	return out
}

func objOf(a map[string]any, k string) map[string]any {
	out := map[string]any{}
	if m, ok := a[k].(map[string]any); ok {
		maps.Copy(out, m)
	}
	return out
}

// fp: what the admin was shown of an account, for binding the answer.
func fp(a map[string]any, keys ...string) string {
	h := sha256.New()
	fmt.Fprint(h, str(a, "id"), "\x00", str(a, "name"), "\x00")
	for _, k := range keys {
		b, _ := json.Marshal(a[k]) // map keys sorted
		h.Write(b)
		h.Write([]byte{0})
	}
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

func (t *Tools) protected() []string {
	if len(t.MailProtected) == 0 {
		return []string{"mcp-gateway", "ses-relay"}
	}
	return t.MailProtected
}

// person: a person's mail account; never a machine account (protected, or
// without a login of the same name).
func (t *Tools) person(ctx context.Context, d *mailDir, q string) (*MailAccount, map[string]any, error) {
	v, raw := d.find(q)
	if v == nil {
		return nil, nil, fmt.Errorf("no mailbox %q (see mail_mailboxes)", short(q))
	}
	if v.Kind != "person" {
		return nil, nil, fmt.Errorf("%s is a shared mailbox, not a person", v.Address)
	}
	if err := t.notMachine(ctx, v); err != nil {
		return nil, nil, err
	}
	return v, raw, nil
}

func (t *Tools) notMachine(ctx context.Context, v *MailAccount) error {
	if slices.Contains(t.protected(), strings.ToLower(v.Name)) {
		return fmt.Errorf("%s is a machine account: it isn't changed here (ask whoever runs the instance)", v.Name)
	}
	if v.Kind == "person" {
		if _, err := t.lookup(ctx, v.Name); err != nil {
			if strings.HasPrefix(err.Error(), "no account") || strings.Contains(err.Error(), "isn't valid") {
				return fmt.Errorf("%s has no login account, so it's likely a machine account: it isn't changed here (ask whoever runs the instance)", v.Name)
			}
			return err
		}
	}
	return nil
}

func (t *Tools) shared(d *mailDir, q string) (*MailAccount, error) {
	v, _ := d.find(q)
	if v == nil {
		return nil, fmt.Errorf("no mailbox %q (see mail_mailboxes)", short(q))
	}
	if v.Kind != "shared" {
		return nil, fmt.Errorf("%s is a person's mailbox, not a shared one", v.Address)
	}
	return v, nil
}

// setAccount: one update, refused unless the server reports it done.
func (t *Tools) setAccount(ctx context.Context, id string, patch map[string]any) error {
	r, err := t.MailWrite.call(ctx, mcall{"x:Account/set", map[string]any{"update": map[string]any{id: patch}}})
	if err != nil {
		return err
	}
	if len(r) == 1 {
		if up, ok := r[0]["updated"].(map[string]any); ok {
			if _, ok := up[id]; ok {
				return nil
			}
		}
		if nu, ok := r[0]["notUpdated"].(map[string]any); ok {
			if e, ok := nu[id].(map[string]any); ok {
				d, _ := e["description"].(string)
				if r := []rune(textx.StripHidden(d)); len(r) > 400 {
					d = string(r[:400]) + "…"
				}
				return fmt.Errorf("the mail server refused the change: %s %s", str(e, "type"), strings.Map(func(r rune) rune {
					if unicode.IsControl(r) {
						return ' '
					}
					return r
				}, d))
			}
		}
	}
	return fmt.Errorf("the mail server didn't confirm the change: check with mail_mailbox")
}

// afterChange: the account as the server has it now.
func (t *Tools) afterChange(ctx context.Context, name string) MailAccount {
	d, err := t.mailDir(ctx)
	if err != nil {
		return MailAccount{Name: name}
	}
	if v, _ := d.find(name); v != nil {
		return *v
	}
	return MailAccount{Name: name}
}

type MailChangeOut struct {
	Done    string      `json:"done"`
	Mailbox MailAccount `json:"mailbox" jsonschema:"the changed mailbox as the server has it now"`
	Note    string      `json:"note,omitempty"`
}

func (t *Tools) askMail(ctx context.Context, req *mcp.CallToolRequest, q, bind string) (*mcp.CallToolResult, error) {
	res, ok, err := t.Ask.Ask(ctx, req, q, bind)
	if err != nil || res != nil {
		return res, err
	}
	if !ok {
		return nil, errDecl
	}
	return nil, nil
}

func memberList(v *MailAccount) string {
	if len(v.Members) == 0 {
		return "nobody"
	}
	return strings.Join(v.Members, ", ")
}

// ---- mail_group_member_add / _remove

type MemberIn struct {
	Mailbox string `json:"mailbox" jsonschema:"the shared mailbox: name or address, e.g. contact@…"`
	Person  string `json:"person" jsonschema:"the person: username or address"`
}

func (t *Tools) memberChange(ctx context.Context, req *mcp.CallToolRequest, in MemberIn, add bool) (*mcp.CallToolResult, MailChangeOut, error) {
	d, err := t.mailDir(ctx)
	if err != nil {
		return nil, MailChangeOut{}, err
	}
	g, err := t.shared(d, in.Mailbox)
	if err != nil {
		return nil, MailChangeOut{}, err
	}
	p, raw, err := t.person(ctx, d, in.Person)
	if err != nil {
		return nil, MailChangeOut{}, err
	}
	in_ := slices.Contains(g.Members, p.Name)
	gids := objOf(raw, "memberGroupIds")
	var q, done string
	if add {
		if in_ {
			return nil, MailChangeOut{}, fmt.Errorf("%s is already in %s", p.Name, g.Address)
		}
		gids[g.accountID] = true
		q = fmt.Sprintf("Add %s to %s: they read and can send as %s?\n\nMembers now: %s.", p.Name, g.Address, g.Address, memberList(g))
		done = "added"
	} else {
		if !in_ {
			return nil, MailChangeOut{}, fmt.Errorf("%s isn't in %s", p.Name, g.Address)
		}
		if len(g.Members) <= 1 {
			return nil, MailChangeOut{}, fmt.Errorf("%s is the last member of %s: add someone else first, or nobody would read it", p.Name, g.Address)
		}
		delete(gids, g.accountID)
		left := slices.DeleteFunc(slices.Clone(g.Members), func(m string) bool { return m == p.Name })
		q = fmt.Sprintf("Remove %s from %s: they lose access to %s and its mail?\n\nMembers left: %s. The mailbox and its mail stay; they can be added again.", p.Name, g.Address, g.Address, strings.Join(left, ", "))
		done = "removed"
	}
	if res, err := t.askMail(ctx, req, q, fp(raw, "memberGroupIds")+"|"+g.accountID+"|"+strings.Join(g.Members, ",")); err != nil || res != nil {
		return res, MailChangeOut{}, err
	}
	if err := t.setAccount(ctx, str(raw, "id"), map[string]any{"memberGroupIds": gids}); err != nil {
		return nil, MailChangeOut{}, err
	}
	return nil, MailChangeOut{Done: done + " " + p.Name, Mailbox: t.afterChange(ctx, g.Name),
		Note: "Done with the admin's confirmation. Mail apps show the change at their next refresh."}, nil
}

func (t *Tools) memberAdd(ctx context.Context, req *mcp.CallToolRequest, in MemberIn) (*mcp.CallToolResult, MailChangeOut, error) {
	return t.memberChange(ctx, req, in, true)
}

func (t *Tools) memberRemove(ctx context.Context, req *mcp.CallToolRequest, in MemberIn) (*mcp.CallToolResult, MailChangeOut, error) {
	return t.memberChange(ctx, req, in, false)
}

// ---- mail_alias_add / _remove

type AliasAddIn struct {
	Person string `json:"person" jsonschema:"the person: username or address"`
	Alias  string `json:"alias" jsonschema:"the extra address, e.g. sales@<company domain>"`
}

var reLocal = regexp.MustCompile(`^[a-z0-9][a-z0-9._+-]{0,63}$`)

func (t *Tools) aliasAdd(ctx context.Context, req *mcp.CallToolRequest, in AliasAddIn) (*mcp.CallToolResult, MailChangeOut, error) {
	addr, err := address(strings.ToLower(strings.TrimSpace(in.Alias)))
	if err != nil {
		return nil, MailChangeOut{}, err
	}
	local, dom, _ := strings.Cut(addr, "@")
	if !reLocal.MatchString(local) {
		return nil, MailChangeOut{}, fmt.Errorf("%q: the part before @ may only have lowercase letters, digits, '.', '_', '+' or '-'", short(local))
	}
	d, err := t.mailDir(ctx)
	if err != nil {
		return nil, MailChangeOut{}, err
	}
	did := ""
	for id, n := range d.domains {
		if strings.EqualFold(n, dom) {
			did = id
		}
	}
	if did == "" || (len(t.domains()) > 0 && !t.company(addr)) {
		return nil, MailChangeOut{}, fmt.Errorf("%s isn't one of the company's mail domains", dom)
	}
	if who := d.taken(addr); who != "" {
		return nil, MailChangeOut{}, fmt.Errorf("%s is already in use (by %s)", addr, who)
	}
	p, raw, err := t.person(ctx, d, in.Person)
	if err != nil {
		return nil, MailChangeOut{}, err
	}
	als := map[string]any{}
	next := 0
	for k, v := range aliasMap(raw) {
		als[k] = v
		if n, err := strconv.Atoi(k); err == nil && n >= next {
			next = n + 1
		}
	}
	als[strconv.Itoa(next)] = map[string]any{"name": local, "domainId": did, "enable": true}
	q := fmt.Sprintf("Give %s the extra address %s: mail to it lands in %s's mailbox?\n\nTheir addresses now: %s.", p.Name, addr, p.Name, strings.Join(append([]string{p.Address}, p.Aliases...), ", "))
	if res, err := t.askMail(ctx, req, q, fp(raw, "aliases")+"|"+addr); err != nil || res != nil {
		return res, MailChangeOut{}, err
	}
	if err := t.setAccount(ctx, str(raw, "id"), map[string]any{"aliases": als}); err != nil {
		return nil, MailChangeOut{}, err
	}
	return nil, MailChangeOut{Done: "added " + addr, Mailbox: t.afterChange(ctx, p.Name),
		Note: "Done with the admin's confirmation. Mail to " + addr + " is delivered to " + p.Name + " from now on."}, nil
}

type AliasRemoveIn struct {
	Alias string `json:"alias" jsonschema:"the extra address to remove (see mail_mailbox)"`
}

func (t *Tools) aliasRemove(ctx context.Context, req *mcp.CallToolRequest, in AliasRemoveIn) (*mcp.CallToolResult, MailChangeOut, error) {
	addr, err := address(strings.ToLower(strings.TrimSpace(in.Alias)))
	if err != nil {
		return nil, MailChangeOut{}, err
	}
	d, err := t.mailDir(ctx)
	if err != nil {
		return nil, MailChangeOut{}, err
	}
	v, _ := d.find(addr)
	if v == nil || !slices.Contains(v.Aliases, addr) {
		if v != nil {
			return nil, MailChangeOut{}, fmt.Errorf("%s is %s's own address, not an alias: it isn't removed here", addr, v.Name)
		}
		return nil, MailChangeOut{}, fmt.Errorf("no alias %s (see mail_mailbox)", short(addr))
	}
	if v.Kind != "person" {
		return nil, MailChangeOut{}, fmt.Errorf("%s belongs to the shared mailbox %s: it isn't changed here", addr, v.Address)
	}
	p, raw, err := t.person(ctx, d, v.Name)
	if err != nil {
		return nil, MailChangeOut{}, err
	}
	als := map[string]any{}
	dn := d.domains[str(raw, "domainId")]
	removed := 0
	for k, al := range aliasMap(raw) {
		ad := d.domains[str(al, "domainId")]
		if ad == "" {
			ad = dn
		}
		if strings.ToLower(str(al, "name")+"@"+ad) == addr {
			removed++
			continue
		}
		als[k] = al
	}
	if removed != 1 {
		return nil, MailChangeOut{}, fmt.Errorf("%s is kept in an unexpected way on the mail server: ask whoever runs the instance", addr)
	}
	q := fmt.Sprintf("Remove the address %s from %s: mail to it no longer reaches %s?\n\nTheir other addresses stay: %s. Mail already received stays.", addr, p.Name, p.Name,
		strings.Join(append([]string{p.Address}, slices.DeleteFunc(slices.Clone(p.Aliases), func(a string) bool { return a == addr })...), ", "))
	if res, err := t.askMail(ctx, req, q, fp(raw, "aliases")+"|"+addr); err != nil || res != nil {
		return res, MailChangeOut{}, err
	}
	if err := t.setAccount(ctx, str(raw, "id"), map[string]any{"aliases": als}); err != nil {
		return nil, MailChangeOut{}, err
	}
	return nil, MailChangeOut{Done: "removed " + addr, Mailbox: t.afterChange(ctx, p.Name),
		Note: "Done with the admin's confirmation. Mail to " + addr + " now bounces, or lands in the domain's catch-all mailbox if there is one."}, nil
}

// ---- mail_quota_set

type QuotaIn struct {
	Mailbox string `json:"mailbox" jsonschema:"a person's or shared mailbox: name or address"`
	Limit   string `json:"limit" jsonschema:"the new size limit, e.g. 500 MB, 5 GB, or 'no limit'"`
}

var reSize = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*(mb|mib|gb|gib|tb|tib)$`)

// parseLimit: bytes (binary units, as mail_mailbox shows them); 0 = no limit.
func parseLimit(s string) (int64, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "no limit", "none", "unlimited", "0":
		return 0, nil
	}
	m := reSize.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("limit %q: give a size like 500 MB or 5 GB, or 'no limit'", short(s))
	}
	f, _ := strconv.ParseFloat(m[1], 64)
	unit := map[byte]float64{'m': 1 << 20, 'g': 1 << 30, 't': 1 << 40}[m[2][0]]
	b := int64(math.Round(f * unit))
	if b < 10<<20 || b > 10<<40 {
		return 0, fmt.Errorf("limit: between 10 MB and 10 TB, or 'no limit'")
	}
	return b, nil
}

func sizeText(b float64) string {
	switch {
	case b >= 1<<30:
		return strconv.FormatFloat(round(b/(1<<30), 1), 'f', -1, 64) + " GB"
	default:
		return strconv.FormatFloat(round(b/(1<<20), 1), 'f', -1, 64) + " MB"
	}
}

func (t *Tools) quotaSet(ctx context.Context, req *mcp.CallToolRequest, in QuotaIn) (*mcp.CallToolResult, MailChangeOut, error) {
	b, err := parseLimit(in.Limit)
	if err != nil {
		return nil, MailChangeOut{}, err
	}
	d, err := t.mailDir(ctx)
	if err != nil {
		return nil, MailChangeOut{}, err
	}
	v, raw := d.find(in.Mailbox)
	if v == nil {
		return nil, MailChangeOut{}, fmt.Errorf("no mailbox %q (see mail_mailboxes)", short(in.Mailbox))
	}
	if err := t.notMachine(ctx, v); err != nil {
		return nil, MailChangeOut{}, err
	}
	used, _ := raw["usedDiskQuota"].(float64)
	if b > 0 && float64(b) < used {
		return nil, MailChangeOut{}, fmt.Errorf("%s already uses %s, more than %s: new mail would bounce. Choose a larger limit", v.Address, sizeText(used), sizeText(float64(b)))
	}
	qs := objOf(raw, "quotas")
	now := "no limit"
	if cur, _ := qs["maxDiskQuota"].(float64); cur > 0 {
		now = sizeText(cur)
	}
	want := "no limit"
	if b > 0 {
		want = sizeText(float64(b))
	}
	if want == now {
		return nil, MailChangeOut{}, fmt.Errorf("%s's limit is already %s", v.Address, now)
	}
	if b > 0 {
		qs["maxDiskQuota"] = b
	} else {
		delete(qs, "maxDiskQuota") // no limit: no key (the server keeps 0 as a 0-byte cap)
	}
	q := fmt.Sprintf("Set the size limit of %s to %s (now %s)?\n\nIt uses %s. When it's full, new mail to it bounces.", v.Address, want, now, sizeText(used))
	if b == 0 {
		q = fmt.Sprintf("Set the size limit of %s to no limit (now %s)?\n\nIt uses %s; it can then grow until the server's disk is full.", v.Address, now, sizeText(used))
	}
	if res, err := t.askMail(ctx, req, q, fp(raw, "quotas")+"|"+strconv.FormatInt(b, 10)); err != nil || res != nil {
		return res, MailChangeOut{}, err
	}
	if err := t.setAccount(ctx, str(raw, "id"), map[string]any{"quotas": qs}); err != nil {
		return nil, MailChangeOut{}, err
	}
	return nil, MailChangeOut{Done: "limit set to " + want, Mailbox: t.afterChange(ctx, v.Name), Note: "Done with the admin's confirmation."}, nil
}
