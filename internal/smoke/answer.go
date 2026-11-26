// Package smoke: a regression smoke test that calls the gateway's user
// tools against the real apps as two dedicated test accounts, and answers
// their confirmations. Everything it creates is named "[smoke] …" and
// removed at the end; anything to an outside address is only ever checked
// and declined.
package smoke

import (
	"fmt"
	"regexp"
	"strings"
)

// Dialog: what a step expects of a confirmation.
type Dialog int

const (
	// NoDialog: the step must not ask; a question is declined and fails it.
	NoDialog Dialog = iota
	// Accept: the step must ask, and the question is accepted if it
	// passes every check.
	Accept
	// AcceptIfAsked: the tool asks only in some cases (shared spaces, an
	// existing file…); if it asks, as Accept.
	AcceptIfAsked
	// Decline: the step must ask; the question is checked, then declined.
	Decline
)

// Expect: a step's confirmation.
type Expect struct {
	Dialog Dialog
	// Must: text the question must contain (the run's item, the outside
	// address, "OUTSIDE"…).
	Must []string
	// Allow: addresses besides the two accounts and the shared mailbox
	// that may appear in an accepted question (a test contact's).
	Allow []string
	// From: the sender address an accepted question must name, if it has
	// a "From:" line (default: the acting account's).
	From string
	// Unnamed: an accepted question needn't name a [smoke] item or the run
	// (for a dialog that shows no names).
	Unnamed bool
	// Outward: the question may say OUTSIDE (switching on an automatic
	// reply always does: it answers anyone who writes).
	Outward bool
}

// People: the addresses an accepted question may name, and the run's id.
type People struct {
	A, B, Shared string
	Run          string
}

var reEmail = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

// Decide: whether to accept a question, and what's wrong with it (""
// when it is as expected). Decline is the default: a question is accepted
// only when the step expects one to accept, it names what the step is
// about, says nothing is going OUTSIDE, and every address in it belongs
// to the test accounts (or the step's own allowed ones).
func Decide(msg string, e Expect, p People, actor string) (accept bool, problem string) {
	for _, m := range e.Must {
		if !strings.Contains(msg, m) {
			return false, fmt.Sprintf("the question doesn't mention %q", m)
		}
	}
	switch e.Dialog {
	case NoDialog:
		return false, "a question where none was expected"
	case Decline:
		return false, ""
	}
	if !e.Unnamed && !strings.Contains(msg, "[smoke]") && (p.Run == "" || !strings.Contains(msg, p.Run)) {
		return false, "the question names no [smoke] item"
	}
	if strings.Contains(msg, "OUTSIDE") && !e.Outward {
		return false, "the question says something goes OUTSIDE the company"
	}
	ok := map[string]bool{}
	for _, a := range append([]string{p.A, p.B, p.Shared}, e.Allow...) {
		if a != "" {
			ok[strings.ToLower(a)] = true
		}
	}
	for _, a := range reEmail.FindAllString(msg, -1) {
		if !ok[strings.ToLower(a)] {
			return false, fmt.Sprintf("the question names an address that isn't a test account: %s", a)
		}
	}
	from := e.From
	if from == "" {
		from = actor
	}
	for _, l := range strings.Split(msg, "\n") {
		if v, found := strings.CutPrefix(l, "From: "); found && !strings.EqualFold(strings.TrimSpace(v), from) {
			return false, fmt.Sprintf("the question sends FROM %s, not %s", strings.TrimSpace(v), from)
		}
	}
	return true, ""
}
