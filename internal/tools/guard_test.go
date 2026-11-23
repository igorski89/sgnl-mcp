package tools

import (
	"testing"

	"github.com/igorski89/sgnl-mcp/internal/auth"
)

func TestAccess(t *testing.T) {
	igor := auth.Claims{Username: "igor", Email: "Igor@Example.com", Groups: []string{"/admins", "sgnl_users"}}
	for name, c := range map[string]struct {
		a    Access
		want bool
	}{
		"nobody by default": {Access{}, false},
		"all":               {Access{All: true}, true},
		"by username":       {Access{Users: []string{"IGOR"}}, true},
		"by email":          {Access{Users: []string{"igor@example.com"}}, true},
		"by group":          {Access{Groups: []string{"sgnl_users"}}, true},
		"by /group":         {Access{Groups: []string{"/admins"}}, true},
		"other user":        {Access{Users: []string{"hugo"}}, false},
		"other group":       {Access{Groups: []string{"mcp"}}, false},
	} {
		if got := c.a.Allows(igor); got != c.want {
			t.Errorf("%s: got %v", name, got)
		}
	}
	if (Access{Users: []string{""}}).Allows(auth.Claims{}) {
		t.Error("empty user allowed by an empty entry")
	}
}

func TestLimitsPerPersonAndKind(t *testing.T) {
	var l limiter
	n := 0
	for l.allow("igor", kindOutward) == nil {
		n++
		if n > 100 {
			break
		}
	}
	if n != limitsPerMinute[kindOutward].burst {
		t.Errorf("outward burst: %d", n)
	}
	if l.allow("hugo", kindOutward) != nil || l.allow("igor", kindRead) != nil {
		t.Error("one person's outward budget limited another person or another kind")
	}
}
