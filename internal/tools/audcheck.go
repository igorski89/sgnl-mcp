package tools

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"time"

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
	Probes []AudProbe `json:"probes,omitempty" jsonschema:"other apps (SGNL_MCP_PROBES): none of these tokens may work there"`
	Note   string     `json:"note"`
}

// AudProbe: one of the instance's other apps, called with no token (the
// baseline) and with each of the gateway's tokens. A token "works" there
// if the answer differs from the baseline and is a success (2xx).
type AudProbe struct {
	App      string         `json:"app"`
	Baseline int            `json:"baseline" jsonschema:"HTTP status with no token"`
	Status   map[string]int `json:"status" jsonschema:"HTTP status per token (login, mail, files)"`
	OK       bool           `json:"ok" jsonschema:"true if no token got more than the baseline"`
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
	noRedirect := &http.Client{Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	status := func(u, tok string) int {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return 0
		}
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := noRedirect.Do(req)
		if err != nil {
			return 0
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	names := make([]string, 0, len(t.Probes))
	for n := range t.Probes {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		u := t.Probes[n]
		p := AudProbe{App: n, Baseline: status(u, ""), Status: map[string]int{}, OK: true}
		for _, tk := range []string{"login", "mail", "files"} {
			if toks[tk] == "" {
				continue
			}
			st := status(u, toks[tk])
			p.Status[tk] = st
			if st != p.Baseline && st >= 200 && st < 300 {
				p.OK = false
			}
		}
		slog.Info("aud_check probe", "app", n, "baseline", p.Baseline, "status", p.Status, "ok", p.OK)
		out.Probes = append(out.Probes, p)
	}
	for _, c := range out.Checks { // statuses only, so the result is in the log too
		slog.Info("aud_check", "token", c.Token, "app", c.App, "status", c.Status, "want", c.Want, "ok", c.OK)
	}
	return nil, out, nil
}
