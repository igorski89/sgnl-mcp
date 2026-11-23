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
