package tools

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/igorski89/sgnl-mcp/internal/jmap"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---- contact groups: cards of kind "group" in the person's own address
// book, whose members are other cards (by uid). Private: no question.

const maxCards = 2000

type rawCard struct {
	ID      string          `json:"id"`
	UID     string          `json:"uid"`
	Kind    string          `json:"kind"`
	Members map[string]bool `json:"members"`
	Name    struct {
		Full string `json:"full"`
	} `json:"name"`
	Emails map[string]struct{ Address string } `json:"emails"`
}

func (c rawCard) name() string { return oneLine(c.Name.Full, 100) }

func (c rawCard) email() string {
	var es []string
	for _, e := range c.Emails {
		es = append(es, e.Address)
	}
	slices.Sort(es)
	if len(es) == 0 {
		return ""
	}
	return es[0]
}

// allCards: the person's address book (up to maxCards cards).
func (t *Tools) allCards(ctx context.Context, tok string, s *jmap.Session, acct string) ([]rawCard, bool, error) {
	r, err := t.JMAP.Invoke(ctx, tok, s, contactsUsing,
		jmap.Call{Method: "ContactCard/query", Args: map[string]any{"accountId": acct, "limit": maxCards}},
		jmap.Call{Method: "ContactCard/get", Args: map[string]any{"accountId": acct, "#ids": jmap.Ref(0, "ContactCard/query", "/ids"),
			"properties": []string{"id", "uid", "kind", "name", "emails", "members"}}})
	if err != nil {
		return nil, false, err
	}
	var q struct {
		Total int `json:"total"`
		IDs   []string
	}
	_ = json.Unmarshal(r[0], &q)
	var got struct{ List []rawCard }
	if err := json.Unmarshal(r[1], &got); err != nil {
		return nil, false, err
	}
	return got.List, len(q.IDs) >= maxCards, nil
}

type GroupMember struct {
	ID    string `json:"id,omitempty"`
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
}

type ContactGroup struct {
	ID      string        `json:"id" jsonschema:"for contact_group_update (and contact_delete to delete the group, not its members)"`
	Name    string        `json:"name"`
	Members []GroupMember `json:"members"`
	Unknown int           `json:"members_not_in_address_book,omitempty"`
}

type ContactGroupsOut struct {
	Groups []ContactGroup `json:"groups"`
	Note   string         `json:"note,omitempty"`
}

func groupsOf(cards []rawCard) []ContactGroup {
	byUID := map[string]rawCard{}
	for _, c := range cards {
		if c.UID != "" {
			byUID[c.UID] = c
		}
	}
	out := []ContactGroup{}
	for _, c := range cards {
		if c.Kind != "group" {
			continue
		}
		g := ContactGroup{ID: c.ID, Name: c.name(), Members: []GroupMember{}}
		for uid, on := range c.Members {
			if !on {
				continue
			}
			if m, ok := byUID[uid]; ok {
				g.Members = append(g.Members, GroupMember{m.ID, m.name(), m.email()})
			} else {
				g.Unknown++
			}
		}
		slices.SortFunc(g.Members, func(a, b GroupMember) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
		out = append(out, g)
	}
	slices.SortFunc(out, func(a, b ContactGroup) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out
}

func (t *Tools) contactGroups(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ContactGroupsOut, error) {
	tok, s, acct, err := t.contactsSession(ctx)
	if err != nil {
		return nil, ContactGroupsOut{}, err
	}
	cards, more, err := t.allCards(ctx, tok, s, acct)
	if err != nil {
		return nil, ContactGroupsOut{}, err
	}
	out := ContactGroupsOut{Groups: groupsOf(cards)}
	if more {
		out.Note = fmt.Sprintf("Only the first %d address book entries were looked at.", maxCards)
	}
	return nil, out, nil
}

// memberUIDs: each given contact (an id from contact_search, or an email
// address in the address book) as its uid.
func memberUIDs(cards []rawCard, refs []string) ([]string, error) {
	var out []string
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		var hit []rawCard
		for _, c := range cards {
			if c.Kind == "group" {
				continue
			}
			if c.ID == ref {
				hit = []rawCard{c}
				break
			}
			for _, e := range c.Emails {
				if strings.EqualFold(e.Address, ref) {
					hit = append(hit, c)
					break
				}
			}
		}
		switch {
		case len(hit) == 0:
			return nil, fmt.Errorf("%q isn't in your address book: add it with contact_create first", oneLine(ref, 80))
		case len(hit) > 1:
			return nil, fmt.Errorf("%q matches %d contacts: use the id from contact_search", oneLine(ref, 80), len(hit))
		case hit[0].UID == "":
			return nil, fmt.Errorf("the contact %q can't be put in a group (it has no uid)", hit[0].name())
		}
		if !slices.Contains(out, hit[0].UID) {
			out = append(out, hit[0].UID)
		}
	}
	return out, nil
}

type ContactGroupCreateIn struct {
	Name    string   `json:"name" jsonschema:"the group's name, e.g. Board"`
	Members []string `json:"members,omitempty" jsonschema:"contacts: ids from contact_search, or email addresses in your address book"`
}

type ContactGroupOut struct {
	Group ContactGroup `json:"group"`
	Note  string       `json:"note"`
}

func (t *Tools) groupAfter(ctx context.Context, tok string, s *jmap.Session, acct, id string) ContactGroup {
	cards, _, err := t.allCards(ctx, tok, s, acct)
	if err == nil {
		for _, g := range groupsOf(cards) {
			if g.ID == id {
				return g
			}
		}
	}
	return ContactGroup{ID: id, Members: []GroupMember{}}
}

func (t *Tools) contactGroupCreate(ctx context.Context, _ *mcp.CallToolRequest, in ContactGroupCreateIn) (*mcp.CallToolResult, ContactGroupOut, error) {
	f, err := ContactWriteIn{Name: in.Name}.fields()
	if err != nil {
		return nil, ContactGroupOut{}, err
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, ContactGroupOut{}, fmt.Errorf("name is required")
	}
	if len(in.Members) > 200 {
		return nil, ContactGroupOut{}, fmt.Errorf("at most 200 members at once")
	}
	tok, s, acct, err := t.contactsSession(ctx)
	if err != nil {
		return nil, ContactGroupOut{}, err
	}
	cards, _, err := t.allCards(ctx, tok, s, acct)
	if err != nil {
		return nil, ContactGroupOut{}, err
	}
	for _, g := range groupsOf(cards) {
		if strings.EqualFold(g.Name, strings.TrimSpace(in.Name)) {
			return nil, ContactGroupOut{}, fmt.Errorf("you already have a group %q (id %s): change it with contact_group_update", g.Name, g.ID)
		}
	}
	uids, err := memberUIDs(cards, in.Members)
	if err != nil {
		return nil, ContactGroupOut{}, err
	}
	book, err := t.defaultBook(ctx, tok, s, acct)
	if err != nil {
		return nil, ContactGroupOut{}, err
	}
	members := map[string]any{}
	for _, u := range uids {
		members[u] = true
	}
	u := make([]byte, 16)
	_, _ = rand.Read(u)
	f["name"] = map[string]any{"full": strings.TrimSpace(in.Name)} // a group's name isn't a person's
	f["@type"], f["version"], f["kind"], f["members"] = "Card", "1.0", "group", members
	f["uid"] = fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:])
	f["addressBookIds"] = map[string]bool{book: true}
	raw, err := t.setCard(ctx, tok, s, jmap.Call{Method: "ContactCard/set", Args: map[string]any{"accountId": acct, "create": map[string]any{"c": f}}})
	if err != nil {
		return nil, ContactGroupOut{}, err
	}
	var got struct {
		Created map[string]struct{ ID string } `json:"created"`
	}
	_ = json.Unmarshal(raw, &got)
	id := got.Created["c"].ID
	if id == "" {
		return nil, ContactGroupOut{}, fmt.Errorf("the address book didn't add the group")
	}
	return nil, ContactGroupOut{t.groupAfter(ctx, tok, s, acct, id), "Group added to the person's address book."}, nil
}

type ContactGroupUpdateIn struct {
	ID     string   `json:"id" jsonschema:"the group's id from contact_groups"`
	Name   string   `json:"name,omitempty" jsonschema:"a new name"`
	Add    []string `json:"add,omitempty" jsonschema:"contacts to add: ids from contact_search, or email addresses in your address book"`
	Remove []string `json:"remove,omitempty" jsonschema:"members to take out (ids or email addresses); they stay in the address book"`
}

func (t *Tools) contactGroupUpdate(ctx context.Context, _ *mcp.CallToolRequest, in ContactGroupUpdateIn) (*mcp.CallToolResult, ContactGroupOut, error) {
	if in.Name == "" && len(in.Add) == 0 && len(in.Remove) == 0 {
		return nil, ContactGroupOut{}, fmt.Errorf("nothing to change: give name, add or remove")
	}
	if len(in.Add)+len(in.Remove) > 200 {
		return nil, ContactGroupOut{}, fmt.Errorf("at most 200 members at once")
	}
	tok, s, acct, err := t.contactsSession(ctx)
	if err != nil {
		return nil, ContactGroupOut{}, err
	}
	cards, _, err := t.allCards(ctx, tok, s, acct)
	if err != nil {
		return nil, ContactGroupOut{}, err
	}
	var g *rawCard
	for i := range cards {
		if cards[i].ID == strings.TrimSpace(in.ID) && cards[i].Kind == "group" {
			g = &cards[i]
		}
	}
	if g == nil {
		return nil, ContactGroupOut{}, fmt.Errorf("no group %q in your address book (see contact_groups)", oneLine(in.ID, 60))
	}
	patch := map[string]any{}
	if in.Name != "" {
		if _, err := (ContactWriteIn{Name: in.Name}).fields(); err != nil { // the same checks as a contact's name
			return nil, ContactGroupOut{}, err
		}
		patch["name"] = map[string]any{"full": strings.TrimSpace(in.Name)}
	}
	if len(in.Add) > 0 || len(in.Remove) > 0 {
		add, err := memberUIDs(cards, in.Add)
		if err != nil {
			return nil, ContactGroupOut{}, err
		}
		rem, err := memberUIDs(cards, in.Remove)
		if err != nil {
			return nil, ContactGroupOut{}, err
		}
		members := map[string]any{}
		for u, on := range g.Members {
			if on {
				members[u] = true // members not in the address book are kept
			}
		}
		for _, u := range rem {
			if _, ok := members[u]; !ok {
				return nil, ContactGroupOut{}, fmt.Errorf("a contact to remove isn't in the group %q", g.name())
			}
			delete(members, u)
		}
		for _, u := range add {
			if slices.Contains(rem, u) {
				return nil, ContactGroupOut{}, fmt.Errorf("a contact is both added and removed")
			}
			members[u] = true
		}
		patch["members"] = members
	}
	if _, err := t.setCard(ctx, tok, s, jmap.Call{Method: "ContactCard/set", Args: map[string]any{"accountId": acct, "update": map[string]any{g.ID: patch}}}); err != nil {
		return nil, ContactGroupOut{}, err
	}
	return nil, ContactGroupOut{t.groupAfter(ctx, tok, s, acct, g.ID), "Group changed in the person's address book; no contact was deleted."}, nil
}

// defaultBook: the address book new cards go into.
func (t *Tools) defaultBook(ctx context.Context, tok string, s *jmap.Session, acct string) (string, error) {
	r, err := t.JMAP.Invoke(ctx, tok, s, contactsUsing, jmap.Call{Method: "AddressBook/get", Args: map[string]any{"accountId": acct}})
	if err != nil {
		return "", err
	}
	var books struct {
		List []struct {
			ID        string `json:"id"`
			IsDefault bool   `json:"isDefault"`
		}
	}
	_ = json.Unmarshal(r[0], &books)
	book := ""
	for _, b := range books.List {
		if b.IsDefault || book == "" {
			book = b.ID
		}
	}
	if book == "" {
		return "", fmt.Errorf("no address book to add to")
	}
	return book, nil
}
