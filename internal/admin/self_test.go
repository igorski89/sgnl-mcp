package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/igorski89/sgnl-mcp/internal/auth"
)

func asUser(name string) context.Context {
	me := auth.Claims{Sub: "s-" + name, Username: name, Groups: []string{"sgnl_users"}}
	return auth.WithRequest(context.Background(), auth.TokenInfoNow("t", me, me, time.Now().Add(time.Hour)))
}

func TestSelfOnlyEverTheCaller(t *testing.T) {
	f, read, write := newFake(t)
	s := &Self{Read: read, Write: write}
	out, err := s.Sessions(asUser("hugo"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(out)
	if strings.Contains(string(b), "10.9.9.9") || len(out.Logins) != 2 || out.Failed != 1 || out.Logins[0].Step != "second factor (passkey)" {
		t.Errorf("sessions: %s", b)
	}
	if err := s.LogoutEverywhere(asUser("hugo")); err != nil {
		t.Fatal(err)
	}
	ch := f.changed()
	if len(ch) == 0 || !strings.HasSuffix(ch[len(ch)-1], "/users/hugo/logout") {
		t.Errorf("changes: %v", ch)
	}
	// No caller: nothing is done.
	n := len(f.changed())
	if err := s.LogoutEverywhere(context.Background()); err == nil || len(f.changed()) != n {
		t.Error("logged out without knowing who")
	}
	// Without the write client: no logout.
	if err := (&Self{Read: read}).LogoutEverywhere(asUser("hugo")); err == nil {
		t.Error("logout without the write client")
	}
}
