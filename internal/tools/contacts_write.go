package tools

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/mail"
	"strings"

	"github.com/igorski89/sgnl-mcp/internal/jmap"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---- contact_create / contact_update / contact_delete: the person's own
// address book. Creating and changing is private and needs no question;
// deleting can't be undone, so the person confirms, seeing the card.

type ContactWriteIn struct {
	Name    string   `json:"name,omitempty" jsonschema:"full name"`
	Emails  []string `json:"emails,omitempty" jsonschema:"email addresses (replace the existing ones on update)"`
	Phones  []string `json:"phones,omitempty" jsonschema:"phone numbers (replace the existing ones on update)"`
	Company string   `json:"company,omitempty"`
	Title   string   `json:"title,omitempty" jsonschema:"job title"`
	Note    string   `json:"note,omitempty"`
}

type ContactCreateIn struct {
	ContactWriteIn
}

type ContactUpdateIn struct {
	ID string `json:"id" jsonschema:"the contact's id from contact_search (address book entries only)"`
	ContactWriteIn
}

type ContactDeleteIn struct {
	ID string `json:"id" jsonschema:"the contact's id from contact_search"`
}

type ContactOut struct {
	ID   string `json:"id"`
	Note string `json:"note"`
}

var contactsUsing = []string{jmap.Core, jmap.Contacts}

// fields: the card properties for what was given ("" and empty lists:
// not given), each value checked.
func (in ContactWriteIn) fields() (map[string]any, error) {
	f := map[string]any{}
	plain := func(what, v string, max int) (string, error) {
		v = strings.TrimSpace(v)
		if len([]rune(v)) > max || oneLine(v, max) != v {
			return "", fmt.Errorf("%s %q isn't plain text of at most %d characters", what, oneLine(v, 40), max)
		}
		return v, nil
	}
	if in.Name != "" {
		n, err := plain("name", in.Name, 100)
		if err != nil {
			return nil, err
		}
		given, sur, _ := strings.Cut(n, " ")
		comps := []map[string]any{{"kind": "given", "value": given}}
		if sur != "" {
			comps = append(comps, map[string]any{"kind": "surname", "value": sur})
		}
		f["name"] = map[string]any{"full": n, "components": comps}
	}
	if len(in.Emails) > 0 {
		if len(in.Emails) > 10 {
			return nil, fmt.Errorf("at most 10 email addresses")
		}
		m := map[string]any{}
		for i, e := range in.Emails {
			e = strings.TrimSpace(e)
			a, err := mail.ParseAddress(e)
			if err != nil || a.Address != e || len(e) > 200 {
				return nil, fmt.Errorf("%q isn't one plain email address", oneLine(e, 60))
			}
			m[fmt.Sprintf("e%d", i+1)] = map[string]any{"address": e}
		}
		f["emails"] = m
	}
	if len(in.Phones) > 0 {
		if len(in.Phones) > 10 {
			return nil, fmt.Errorf("at most 10 phone numbers")
		}
		m := map[string]any{}
		for i, p := range in.Phones {
			p, err := plain("phone", p, 40)
			if err != nil {
				return nil, err
			}
			m[fmt.Sprintf("p%d", i+1)] = map[string]any{"number": p}
		}
		f["phones"] = m
	}
	for _, x := range []struct{ prop, key, what, v string }{
		{"organizations", "name", "company", in.Company}, {"titles", "name", "title", in.Title}} {
		if x.v == "" {
			continue
		}
		v, err := plain(x.what, x.v, 100)
		if err != nil {
			return nil, err
		}
		f[x.prop] = map[string]any{"1": map[string]any{x.key: v}}
	}
	if in.Note != "" {
		n := strings.TrimSpace(in.Note)
		if len([]rune(n)) > 2000 {
			return nil, fmt.Errorf("the note is longer than 2000 characters")
		}
		f["notes"] = map[string]any{"1": map[string]any{"note": n}}
	}
	return f, nil
}

func (t *Tools) contactsSession(ctx context.Context) (string, *jmap.Session, string, error) {
	tok, err := t.Auth.MailToken(ctx)
	if err != nil {
		return "", nil, "", err
	}
	s, err := t.JMAP.Session(ctx, tok)
	if err != nil {
		return "", nil, "", err
	}
	acct := s.PrimaryAccounts[jmap.Contacts]
	if acct == "" {
		return "", nil, "", fmt.Errorf("no address book for this account")
	}
	return tok, s, acct, nil
}

func (t *Tools) setCard(ctx context.Context, tok string, s *jmap.Session, call jmap.Call) (json.RawMessage, error) {
	r, err := t.JMAP.Invoke(ctx, tok, s, contactsUsing, call)
	if err != nil {
		return nil, err
	}
	return r[0], jmap.SetErrors(r[0])
}

func (t *Tools) contactCreate(ctx context.Context, _ *mcp.CallToolRequest, in ContactCreateIn) (*mcp.CallToolResult, ContactOut, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, ContactOut{}, fmt.Errorf("name is required")
	}
	f, err := in.fields()
	if err != nil {
		return nil, ContactOut{}, err
	}
	tok, s, acct, err := t.contactsSession(ctx)
	if err != nil {
		return nil, ContactOut{}, err
	}
	r, err := t.JMAP.Invoke(ctx, tok, s, contactsUsing, jmap.Call{Method: "AddressBook/get", Args: map[string]any{"accountId": acct}})
	if err != nil {
		return nil, ContactOut{}, err
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
		return nil, ContactOut{}, fmt.Errorf("no address book to add to")
	}
	u := make([]byte, 16)
	_, _ = rand.Read(u)
	f["@type"], f["version"], f["kind"] = "Card", "1.0", "individual"
	f["uid"] = fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:])
	f["addressBookIds"] = map[string]bool{book: true}
	raw, err := t.setCard(ctx, tok, s, jmap.Call{Method: "ContactCard/set", Args: map[string]any{"accountId": acct, "create": map[string]any{"c": f}}})
	if err != nil {
		return nil, ContactOut{}, err
	}
	var got struct {
		Created map[string]struct{ ID string } `json:"created"`
	}
	_ = json.Unmarshal(raw, &got)
	if got.Created["c"].ID == "" {
		return nil, ContactOut{}, fmt.Errorf("the address book didn't add the contact")
	}
	return nil, ContactOut{got.Created["c"].ID, "Added to the person's address book."}, nil
}

// card: one card of the person's address book, as contact_search shows it.
func (t *Tools) card(ctx context.Context, tok string, s *jmap.Session, acct, id string) (*Contact, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("id is required (from contact_search)")
	}
	r, err := t.JMAP.Invoke(ctx, tok, s, contactsUsing, jmap.Call{Method: "ContactCard/get", Args: map[string]any{"accountId": acct, "ids": []string{id},
		"properties": []string{"id", "name", "emails", "phones", "organizations", "titles"}}})
	if err != nil {
		return nil, err
	}
	cs := cardsOf(r[0], "address book")
	if len(cs) == 0 {
		return nil, fmt.Errorf("no contact %q in your address book (company directory entries can't be changed)", oneLine(id, 60))
	}
	return &cs[0], nil
}

func (t *Tools) contactUpdate(ctx context.Context, _ *mcp.CallToolRequest, in ContactUpdateIn) (*mcp.CallToolResult, ContactOut, error) {
	f, err := in.fields()
	if err != nil {
		return nil, ContactOut{}, err
	}
	if len(f) == 0 {
		return nil, ContactOut{}, fmt.Errorf("nothing to change")
	}
	tok, s, acct, err := t.contactsSession(ctx)
	if err != nil {
		return nil, ContactOut{}, err
	}
	if _, err := t.card(ctx, tok, s, acct, in.ID); err != nil {
		return nil, ContactOut{}, err
	}
	if _, err := t.setCard(ctx, tok, s, jmap.Call{Method: "ContactCard/set", Args: map[string]any{"accountId": acct, "update": map[string]any{in.ID: f}}}); err != nil {
		return nil, ContactOut{}, err
	}
	return nil, ContactOut{in.ID, "Changed in the person's address book (the fields given replace the old ones)."}, nil
}

func (t *Tools) contactDelete(ctx context.Context, req *mcp.CallToolRequest, in ContactDeleteIn) (*mcp.CallToolResult, ContactOut, error) {
	tok, s, acct, err := t.contactsSession(ctx)
	if err != nil {
		return nil, ContactOut{}, err
	}
	c, err := t.card(ctx, tok, s, acct, in.ID)
	if err != nil {
		return nil, ContactOut{}, err
	}
	who := oneLine(c.Name, 80)
	if len(c.Emails) > 0 {
		who += " <" + oneLine(c.Emails[0], 100) + ">"
	}
	q := fmt.Sprintf("Delete the contact %s from your address book? It can't be undone.", who)
	if len(c.Emails) > 1 || len(c.Phones) > 0 || c.Company != "" {
		q += fmt.Sprintf("\n\nIt also has: %d more email address(es), %d phone number(s)", max(len(c.Emails)-1, 0), len(c.Phones))
		if c.Company != "" {
			q += ", company " + oneLine(c.Company, 60)
		}
		q += "."
	}
	b, _ := json.Marshal(c)
	res, ok, err := t.confirm(ctx, req, q, string(b))
	if err != nil || res != nil {
		return res, ContactOut{}, err
	}
	if !ok {
		return nil, ContactOut{}, fmt.Errorf("the person declined: nothing was deleted")
	}
	if _, err := t.setCard(ctx, tok, s, jmap.Call{Method: "ContactCard/set", DeleteContact: true, Args: map[string]any{"accountId": acct, "destroy": []string{in.ID}}}); err != nil {
		return nil, ContactOut{}, err
	}
	return nil, ContactOut{in.ID, "Deleted from the person's address book, with their confirmation."}, nil
}
