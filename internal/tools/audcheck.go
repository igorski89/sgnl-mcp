package tools

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/igorski89/sgnl-mcp/internal/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---- aud_check (experiment: SGNL_MCP_EXPERIMENTS=audcheck)
//
// Checks that each app accepts only tokens meant for it: it calls the mail
// server and OpenCloud with the person's login token and with each app's
// token, and reports the HTTP status of each, never a token. Read-only: a
// JMAP session GET and OpenCloud's /graph/v1.0/me.

type AudCheck struct {
	Token  string `json:"token" jsonschema:"which token: login, mail or files"`
	App    string `json:"app" jsonschema:"which app it was sent to: mail or files"`
	Status int    `json:"status" jsonschema:"HTTP status (0: no answer)"`
	Want   string `json:"want" jsonschema:"accepted or refused"`
	OK     bool   `json:"ok" jsonschema:"whether the app behaved as wanted"`
}

type AudCheckOut struct {
	Checks []AudCheck `json:"checks"`
	Note   string     `json:"note"`
}

func (t *Tools) audCheck(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, AudCheckOut, error) {
	login := auth.Token(ctx)
	mail, err := t.Auth.MailToken(ctx)
	if err != nil {
		return nil, AudCheckOut{}, err
	}
	toks := map[string]string{"login": login, "mail": mail}
	apps := map[string]string{"mail": t.JMAP.SessionURL}
	if t.Files != nil {
		files, err := t.Auth.FilesToken(ctx)
		if err != nil {
			return nil, AudCheckOut{}, err
		}
		toks["files"] = files
		apps["files"] = t.Files.Base + "/graph/v1.0/me"
	}
	out := AudCheckOut{Checks: []AudCheck{}, Note: "statuses only; 401/403 = refused"}
	for _, tk := range []string{"login", "mail", "files"} {
		for _, app := range []string{"mail", "files"} {
			tok, u := toks[tk], apps[app]
			if tok == "" || u == "" {
				continue
			}
			c := AudCheck{Token: tk, App: app, Want: "refused"}
			if tk == app {
				c.Want = "accepted"
			}
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
			req.Header.Set("Authorization", "Bearer "+tok)
			if resp, err := http.DefaultClient.Do(req); err == nil {
				c.Status = resp.StatusCode
				resp.Body.Close()
			}
			refused := c.Status == 401 || c.Status == 403
			c.OK = (c.Want == "refused") == refused && c.Status != 0
			out.Checks = append(out.Checks, c)
		}
	}
	for _, c := range out.Checks { // statuses only, so the result is in the log too
		slog.Info("aud_check", "token", c.Token, "app", c.App, "status", c.Status, "want", c.Want, "ok", c.OK)
	}
	return nil, out, nil
}
