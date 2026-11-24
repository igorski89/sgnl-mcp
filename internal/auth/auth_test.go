package auth

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestAudience(t *testing.T) {
	for in, want := range map[string]int{`{"aud":"a"}`: 1, `{"aud":["a","b"]}`: 2, `{}`: 0} {
		var c Claims
		if err := json.Unmarshal([]byte(in), &c); err != nil || len(c.Aud) != want {
			t.Errorf("%s: %v %v", in, c.Aud, err)
		}
	}
}

func TestPeekClaims(t *testing.T) {
	p := base64.RawURLEncoding.EncodeToString([]byte(`{"azp":"gw","preferred_username":"u"}`))
	if c := PeekClaims("h." + p + ".s"); c.Azp != "gw" || c.Username != "u" {
		t.Errorf("got %+v", c)
	}
	if c := PeekClaims("garbage"); c.Azp != "" {
		t.Errorf("garbage: %+v", c)
	}
}

func TestIdentityDiff(t *testing.T) {
	a := Claims{Sub: "s", Username: "u", Email: "e", Name: "n", Groups: []string{"x", "y"}}
	b := a
	b.Groups, b.Name = []string{"y", "x", "new"}, "N" // newer from the issuer: fine
	if d := identityDiff(a, b); d != "" {
		t.Errorf("same person, newer groups and name: %q", d)
	}
	for field, mod := range map[string]func(*Claims){
		"subject":  func(c *Claims) { c.Sub = "t" },
		"username": func(c *Claims) { c.Username = "v" },
		"email":    func(c *Claims) { c.Email = "" },
	} {
		c := a
		c.Groups = append([]string(nil), a.Groups...)
		mod(&c)
		if d := identityDiff(a, c); d != field {
			t.Errorf("%s: got %q", field, d)
		}
	}
	if identityDiff(Claims{}, Claims{}) != "username" {
		t.Error("empty username must differ")
	}
}

func TestClientFromAzpOrClientID(t *testing.T) {
	if (Claims{Azp: "a", ClientID: "b"}).Client() != "a" || (Claims{ClientID: "b"}).Client() != "b" {
		t.Fatal("Client() should prefer azp, then client_id")
	}
}

func TestLoginEnded(t *testing.T) {
	ended := [][2]string{
		{"invalid_token", ""}, {"", "Invalid token"}, {"invalid_grant", "x"},
		{"invalid_request", "The request is missing a required parameter, includes an invalid parameter value, includes a parameter more than once, or is otherwise malformed. Token is not valid or has expired."},
	}
	for _, e := range ended {
		if !loginEnded(e[0], e[1]) {
			t.Errorf("%q %q should mean the login ended", e[0], e[1])
		}
	}
	config := [][2]string{{"invalid_request", "The request is missing a required parameter."}, {"invalid_scope", ""}, {"invalid_client", ""}, {"unauthorized_client", ""}}
	for _, e := range config {
		if loginEnded(e[0], e[1]) {
			t.Errorf("%q %q is a configuration error, not an ended login", e[0], e[1])
		}
	}
}

func TestScopesFromScpOrScope(t *testing.T) {
	if s := (Claims{Scp: []string{"openid", "groups"}, Scope: "x"}).Scopes(); len(s) != 2 || s[1] != "groups" {
		t.Fatal(s)
	}
	if s := (Claims{Scope: "openid email"}).Scopes(); len(s) != 2 {
		t.Fatal(s)
	}
}
