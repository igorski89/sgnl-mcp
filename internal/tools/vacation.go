package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/igorski89/sgnl-mcp/internal/jmap"
	"github.com/igorski89/sgnl-mcp/internal/textx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---- out_of_office_get / out_of_office_set: the automatic reply. It
// answers every sender, outside the company too, for as long as it is on,
// so switching it on is asked of the person with the whole text and the
// dates; switching it off isn't.

const maxVacationText = 2000

var vacUsing = []string{jmap.Core, jmap.Vacation}

type OutOfOffice struct {
	On      bool   `json:"on"`
	From    string `json:"from,omitempty" jsonschema:"first day, YYYY-MM-DD"`
	To      string `json:"to,omitempty" jsonschema:"last day, YYYY-MM-DD"`
	Subject string `json:"subject,omitempty"`
	Text    string `json:"text,omitempty"`
}

type OutOfOfficeSetIn struct {
	On      bool   `json:"on" jsonschema:"true: switch the automatic reply on; false: off"`
	From    string `json:"from,omitempty" jsonschema:"first day, YYYY-MM-DD (default today)"`
	To      string `json:"to,omitempty" jsonschema:"last day, YYYY-MM-DD; strongly recommended (without it the reply runs until switched off)"`
	Subject string `json:"subject,omitempty" jsonschema:"subject of the reply (default: the mail server's)"`
	Text    string `json:"text,omitempty" jsonschema:"the reply, plain text (at most 2000 characters); required to switch on"`
}

type OutOfOfficeOut struct {
	OutOfOffice
	Note string `json:"note,omitempty"`
}

func (t *Tools) vacSession(ctx context.Context) (string, *jmap.Session, string, error) {
	tok, err := t.Auth.MailToken(ctx)
	if err != nil {
		return "", nil, "", err
	}
	s, err := t.JMAP.Session(ctx, tok)
	if err != nil {
		return "", nil, "", err
	}
	acct := s.PrimaryAccounts[jmap.Vacation]
	if acct == "" {
		return "", nil, "", fmt.Errorf("the mail server offers no automatic reply for this account")
	}
	return tok, s, acct, nil
}

func (t *Tools) outOfOfficeGet(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, OutOfOfficeOut, error) {
	tok, s, acct, err := t.vacSession(ctx)
	if err != nil {
		return nil, OutOfOfficeOut{}, err
	}
	r, err := t.JMAP.Invoke(ctx, tok, s, vacUsing, jmap.Call{Method: "VacationResponse/get", Args: map[string]any{"accountId": acct, "ids": []string{"singleton"}}})
	if err != nil {
		return nil, OutOfOfficeOut{}, err
	}
	var got struct {
		List []struct {
			IsEnabled bool    `json:"isEnabled"`
			FromDate  *string `json:"fromDate"`
			ToDate    *string `json:"toDate"`
			Subject   *string `json:"subject"`
			TextBody  *string `json:"textBody"`
		} `json:"list"`
	}
	if err := json.Unmarshal(r[0], &got); err != nil {
		return nil, OutOfOfficeOut{}, err
	}
	out := OutOfOfficeOut{}
	if len(got.List) > 0 {
		v := got.List[0]
		day := func(p *string) string {
			if p == nil {
				return ""
			}
			if d, err := time.Parse(time.RFC3339, *p); err == nil {
				return d.In(t.loc()).Format("2006-01-02")
			}
			return *p
		}
		str := func(p *string) string {
			if p == nil {
				return ""
			}
			return *p
		}
		out.OutOfOffice = OutOfOffice{On: v.IsEnabled, From: day(v.FromDate), To: day(v.ToDate), Subject: str(v.Subject), Text: str(v.TextBody)}
		if out.On && out.To != "" && out.To < time.Now().In(t.loc()).Format("2006-01-02") {
			out.Note = "Switched on, but its last day has passed, so no one gets it."
		}
	}
	return nil, out, nil
}

func (t *Tools) outOfOfficeSet(ctx context.Context, req *mcp.CallToolRequest, in OutOfOfficeSetIn) (*mcp.CallToolResult, OutOfOfficeOut, error) {
	if !in.On {
		tok, s, acct, err := t.vacSession(ctx)
		if err != nil {
			return nil, OutOfOfficeOut{}, err
		}
		r, err := t.JMAP.Invoke(ctx, tok, s, vacUsing, jmap.Call{Method: "VacationResponse/set", Vacation: true,
			Args: map[string]any{"accountId": acct, "update": map[string]any{"singleton": map[string]any{"isEnabled": false}}}})
		if err != nil {
			return nil, OutOfOfficeOut{}, err
		}
		if err := jmap.SetErrors(r[0]); err != nil {
			return nil, OutOfOfficeOut{}, err
		}
		return nil, OutOfOfficeOut{Note: "The automatic reply is off."}, nil
	}

	text := strings.TrimSpace(textx.StripHidden(in.Text))
	if text == "" {
		return nil, OutOfOfficeOut{}, fmt.Errorf("text: give the reply's text")
	}
	if len([]rune(text)) > maxVacationText {
		return nil, OutOfOfficeOut{}, fmt.Errorf("text: at most %d characters", maxVacationText)
	}
	subject := oneLine(in.Subject, 200)
	loc := t.loc()
	today := time.Now().In(loc)
	from := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc)
	if in.From != "" {
		d, err := time.ParseInLocation("2006-01-02", in.From, loc)
		if err != nil {
			return nil, OutOfOfficeOut{}, fmt.Errorf("from: use YYYY-MM-DD")
		}
		from = d
	}
	var to *time.Time
	if in.To != "" {
		d, err := time.ParseInLocation("2006-01-02", in.To, loc)
		if err != nil {
			return nil, OutOfOfficeOut{}, fmt.Errorf("to: use YYYY-MM-DD")
		}
		if d.Before(from) {
			return nil, OutOfOfficeOut{}, fmt.Errorf("to: before from")
		}
		if d.Before(from.AddDate(0, 0, -1)) || d.Before(time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc)) {
			return nil, OutOfOfficeOut{}, fmt.Errorf("to: that day has passed")
		}
		end := d.AddDate(0, 0, 1).Add(-time.Second) // the whole last day
		to = &end
	}

	// The question: dates on the first line, the whole text below.
	period := "from " + from.Format("Mon 2 Jan 2006")
	if to != nil {
		period += " to " + to.Format("Mon 2 Jan 2006")
	} else {
		period += " with NO end date (until you switch it off)"
	}
	first := fmt.Sprintf("Switch on your automatic reply %s? Everyone who mails you gets it, people OUTSIDE the company too.", period)
	var b strings.Builder
	b.WriteString(first + "\n\n")
	if subject != "" {
		b.WriteString("Subject: " + subject + "\n")
	}
	b.WriteString("Text:\n" + text + "\n")
	patch := map[string]any{"isEnabled": true, "fromDate": from.UTC().Format(time.RFC3339), "toDate": nil, "textBody": text, "htmlBody": nil}
	if to != nil {
		patch["toDate"] = to.UTC().Format(time.RFC3339)
	}
	if subject != "" {
		patch["subject"] = subject
	} else {
		patch["subject"] = nil
	}
	res, ok, err := t.confirm(ctx, req, b.String(), fmt.Sprint(patch))
	if err != nil || res != nil {
		return res, OutOfOfficeOut{}, err
	}
	if !ok {
		return nil, OutOfOfficeOut{}, fmt.Errorf("the person declined: the automatic reply wasn't switched on")
	}
	tok, s, acct, err := t.vacSession(ctx)
	if err != nil {
		return nil, OutOfOfficeOut{}, err
	}
	r, err := t.JMAP.Invoke(ctx, tok, s, vacUsing, jmap.Call{Method: "VacationResponse/set", Vacation: true,
		Args: map[string]any{"accountId": acct, "update": map[string]any{"singleton": patch}}})
	if err != nil {
		return nil, OutOfOfficeOut{}, err
	}
	if err := jmap.SetErrors(r[0]); err != nil {
		return nil, OutOfOfficeOut{}, err
	}
	out := OutOfOfficeOut{OutOfOffice: OutOfOffice{On: true, From: from.Format("2006-01-02"), Subject: subject, Text: text},
		Note: "The automatic reply is on, with the person's confirmation."}
	if to != nil {
		out.To = to.Format("2006-01-02")
	}
	return nil, out, nil
}
