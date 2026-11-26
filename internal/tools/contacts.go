package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/igorski89/sgnl-mcp/internal/jmap"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ContactSearchIn struct {
	Query string `json:"query" jsonschema:"part of a name, email address, company or phone number"`
	Limit int    `json:"limit,omitempty" jsonschema:"1-50 (default 10)"`
}

type Contact struct {
	ID      string   `json:"id,omitempty" jsonschema:"address book entries: for contact_update and contact_delete"`
	Name    string   `json:"name"`
	Emails  []string `json:"emails,omitempty"`
	Phones  []string `json:"phones,omitempty"`
	Company string   `json:"company,omitempty"`
	Title   string   `json:"title,omitempty"`
	Source  string   `json:"source" jsonschema:"address book or company directory"`
}

type ContactSearchOut struct {
	Contacts []Contact `json:"contacts"`
	Note     string    `json:"note,omitempty"`
}

func (t *Tools) contactSearch(ctx context.Context, _ *mcp.CallToolRequest, in ContactSearchIn) (*mcp.CallToolResult, ContactSearchOut, error) {
	if strings.TrimSpace(in.Query) == "" {
		return nil, ContactSearchOut{}, fmt.Errorf("query is required")
	}
	if in.Limit < 1 || in.Limit > 50 {
		in.Limit = 10
	}
	tok, err := t.Auth.MailToken(ctx)
	if err != nil {
		return nil, ContactSearchOut{}, err
	}
	s, err := t.JMAP.Session(ctx, tok)
	if err != nil {
		return nil, ContactSearchOut{}, err
	}
	out := ContactSearchOut{Contacts: []Contact{}}
	// The person's own address book (JSContact cards).
	if acct := s.PrimaryAccounts[jmap.Contacts]; acct != "" {
		r, err := t.JMAP.Invoke(ctx, tok, s, []string{jmap.Core, jmap.Contacts},
			jmap.Call{Method: "ContactCard/query", Args: map[string]any{"accountId": acct, "filter": map[string]any{"text": in.Query}, "limit": in.Limit}},
			jmap.Call{Method: "ContactCard/get", Args: map[string]any{"accountId": acct, "#ids": jmap.Ref(0, "ContactCard/query", "/ids"),
				"properties": []string{"id", "name", "emails", "phones", "organizations", "titles"}}})
		if err == nil {
			out.Contacts = append(out.Contacts, cardsOf(r[1], "address book")...)
		}
	}
	// The company directory (colleagues, groups).
	acct := s.PrimaryAccounts[jmap.Principals]
	if acct == "" {
		acct = s.PrimaryAccounts[jmap.Mail]
	}
	ps, err := t.findPrincipals(ctx, tok, s, acct, in.Query, in.Limit)
	if err != nil {
		// Said, not swallowed: Stalwart refuses directory queries unless the
		// administrator allows them.
		slog.Warn("contact_search: company directory not searched", "error", err.Error())
		out.Note = "The company directory couldn't be searched (" + oneLine(err.Error(), 160) + "); only your address book was. Ask whoever runs the instance to allow directory queries on the mail server."
	} else {
		for _, p := range ps {
			name := p.Description
			if name == "" {
				name = p.Name
			}
			ct := Contact{Name: name, Source: "company directory (" + p.Type + ")"}
			if p.Email != "" {
				ct.Emails = []string{p.Email}
			}
			out.Contacts = append(out.Contacts, ct)
		}
	}
	return nil, out, nil
}

// cardsOf: the cards of a ContactCard/get answer.
func cardsOf(raw json.RawMessage, source string) []Contact {
	var got struct {
		List []struct {
			ID   string `json:"id"`
			Name struct {
				Full       string `json:"full"`
				Components []struct {
					Value string `json:"value"`
				} `json:"components"`
			} `json:"name"`
			Emails        map[string]struct{ Address string } `json:"emails"`
			Phones        map[string]struct{ Number string }  `json:"phones"`
			Organizations map[string]struct{ Name string }    `json:"organizations"`
			Titles        map[string]struct{ Name string }    `json:"titles"`
		}
	}
	_ = json.Unmarshal(raw, &got)
	out := []Contact{}
	for _, c := range got.List {
		ct := Contact{ID: c.ID, Name: c.Name.Full, Source: source}
		if ct.Name == "" {
			parts := []string{}
			for _, p := range c.Name.Components {
				parts = append(parts, p.Value)
			}
			ct.Name = strings.Join(parts, " ")
		}
		for _, e := range c.Emails {
			ct.Emails = append(ct.Emails, e.Address)
		}
		for _, p := range c.Phones {
			ct.Phones = append(ct.Phones, p.Number)
		}
		for _, o := range c.Organizations {
			ct.Company = o.Name
		}
		for _, ti := range c.Titles {
			ct.Title = ti.Name
		}
		slices.Sort(ct.Emails)
		slices.Sort(ct.Phones)
		out = append(out, ct)
	}
	return out
}
