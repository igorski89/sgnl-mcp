package tools

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/igorski89/sgnl-mcp/internal/auth"
	"golang.org/x/time/rate"
)

// Access: who may use the gateway. Empty lists and All false: nobody.
type Access struct {
	All    bool
	Users  []string // usernames or email addresses, case-insensitive
	Groups []string // any of these groups (the token's "groups" claim)
}

func (a Access) Allows(c auth.Claims) bool {
	if a.All {
		return true
	}
	for _, u := range a.Users {
		if (c.Username != "" && strings.EqualFold(u, c.Username)) || (c.Email != "" && strings.EqualFold(u, c.Email)) {
			return true
		}
	}
	for _, g := range a.Groups {
		g = strings.TrimPrefix(g, "/")
		if slices.ContainsFunc(c.Groups, func(h string) bool { return strings.TrimPrefix(h, "/") == g }) {
			return true
		}
	}
	return false
}

// Kinds of tool call, each with its own per-person budget.
const (
	kindRead    = "read"
	kindWrite   = "write"   // private and reversible
	kindOutward = "outward" // reaches other people (send, share)
)

// Limits per person and kind: a steady rate per minute and a burst.
var limitsPerMinute = map[string]struct {
	perMin float64
	burst  int
}{
	kindRead:  {120, 60},
	kindWrite: {30, 15},
	// Outward calls come in pairs (the question, then the answer): about
	// 10 sends at once, then one every 2 minutes (30 an hour).
	kindOutward: {1, 20},
}

type limiter struct {
	mu sync.Mutex
	m  map[string]*rate.Limiter
}

func (l *limiter) allow(who, kind string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil {
		l.m = map[string]*rate.Limiter{}
	}
	k := who + "\x00" + kind
	r, ok := l.m[k]
	if !ok {
		c := limitsPerMinute[kind]
		r = rate.NewLimiter(rate.Limit(c.perMin/60), c.burst)
		l.m[k] = r
	}
	if !r.Allow() {
		return fmt.Errorf("too many %s actions in a short time: wait a minute and try again", kind)
	}
	return nil
}
