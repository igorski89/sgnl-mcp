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
	a := Claims{Username: "u", Email: "e", Name: "n", Groups: []string{"x", "y"}}
	b := a
	b.Groups = []string{"y", "x"}
	if d := identityDiff(a, b); d != "" {
		t.Errorf("same identity, group order differs: %q", d)
	}
	for field, mod := range map[string]func(*Claims){
		"groups": func(c *Claims) { c.Groups = []string{"x"} },
		"email":  func(c *Claims) { c.Email = "" },
		"name":   func(c *Claims) { c.Name = "m" },
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
