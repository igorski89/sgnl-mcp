// sgnl-mcp-smoke: the gateway's regression smoke test. It calls every user
// tool against the real apps as two dedicated test accounts and answers
// their confirmations: private changes and changes between the two are
// accepted, anything to an outside address is checked and declined.
// Everything it creates is named "[smoke] …" and removed at the end.
//
//	SGNL_SMOKE_URL         the gateway's MCP endpoint (…/mcp)
//	SGNL_SMOKE_ISSUER      the login server's issuer URL
//	SGNL_SMOKE_CLIENT_ID   the login client the tokens were issued to
//	SGNL_SMOKE_A_USER, SGNL_SMOKE_B_USER    the two accounts' usernames
//	SGNL_SMOKE_A_TOKEN, SGNL_SMOKE_B_TOKEN  their token files (JSON with
//	                       refresh_token, mode 600; rewritten on refresh)
//	SGNL_SMOKE_SHARED      a shared mailbox both are members of
//	SGNL_SMOKE_OUTSIDE     an outside address, only ever declined
//	                       (default smoke-outside@example.com)
//
// Flags: -read-only (no changes, no sweep), -only tool,tool, -newsletter
// (also the newsletter steps; off by default, since they mail).
// Exit: 0 all ok, 1 a step failed, 2 the run couldn't start, 3 a re-login
// is needed (the saved login has ended).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/igorski89/sgnl-mcp/internal/smoke"
)

func main() {
	readOnly := flag.Bool("read-only", false, "only the read-only steps: no changes, no sweep")
	only := flag.String("only", "", "only these tools (comma-separated)")
	newsletter := flag.Bool("newsletter", false, "also the newsletter steps: [smoke] items in listmonk, a test and a transactional mail to the second account (dev; not against production unless agreed)")
	flag.Parse()
	need := func(k string) string {
		v := os.Getenv(k)
		if v == "" {
			fmt.Fprintf(os.Stderr, "%s isn't set\n", k)
			os.Exit(2)
		}
		return v
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	hc := &http.Client{Timeout: 60 * time.Second}
	tokURL, err := smoke.TokenURL(ctx, hc, need("SGNL_SMOKE_ISSUER"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "login server:", err)
		os.Exit(2)
	}
	client := need("SGNL_SMOKE_CLIENT_ID")
	acct := func(p string) smoke.Account {
		return smoke.Account{User: need("SGNL_SMOKE_" + p + "_USER"),
			Login: &smoke.Login{Path: need("SGNL_SMOKE_" + p + "_TOKEN"), TokenURL: tokURL, ClientID: client, HTTP: hc}}
	}
	cfg := smoke.Config{URL: need("SGNL_SMOKE_URL"), Accounts: [2]smoke.Account{acct("A"), acct("B")},
		Shared: need("SGNL_SMOKE_SHARED"), Outside: os.Getenv("SGNL_SMOKE_OUTSIDE"), ReadOnly: *readOnly, Newsletter: *newsletter, Log: os.Stderr}
	if *only != "" {
		cfg.Only = strings.Split(*only, ",")
	}
	start := time.Now()
	r, err := smoke.Run(ctx, cfg)
	if r != nil && len(r.Results) > 0 {
		printTable(r.Results)
	}
	switch {
	case errors.Is(err, smoke.ErrRelogin):
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	case err != nil:
		fmt.Fprintln(os.Stderr, "the run stopped:", err)
		os.Exit(2)
	}
	failed, skipped := 0, 0
	for _, x := range r.Results {
		switch x.Status {
		case "failed":
			failed++
		case "skipped":
			skipped++
		}
	}
	fmt.Printf("\n%d steps: %d failed, %d skipped, in %s\n", len(r.Results), failed, skipped, time.Since(start).Round(time.Second))
	if failed > 0 {
		os.Exit(1)
	}
}

func printTable(rs []smoke.Result) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TOOL\tSTEP\tRESULT\tMS\tDETAIL")
	for _, x := range rs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n", x.Tool, x.Step, x.Status, x.MS, x.Detail)
	}
	w.Flush()
}
