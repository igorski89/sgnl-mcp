package auth

import (
	"net/http"
	"strings"
)

// Protect puts the SDK's bearer-token middleware mw in front of h, adding
// error="invalid_token" (RFC 6750 §3.1) to its 401 when a token was
// presented, so clients refresh the token and retry rather than start a new
// login. h gets the plain ResponseWriter (streaming keeps working).
func Protect(mw func(http.Handler) http.Handler, h http.Handler) http.Handler {
	inner := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, ok := w.(*challenge); ok {
			w = c.ResponseWriter
		}
		h.ServeHTTP(w, r)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner.ServeHTTP(&challenge{ResponseWriter: w, token: r.Header.Get("Authorization") != ""}, r)
	})
}

type challenge struct {
	http.ResponseWriter
	token bool
}

func (c *challenge) WriteHeader(code int) {
	if code == http.StatusUnauthorized && c.token {
		h := c.Header()
		if v := h.Get("WWW-Authenticate"); strings.HasPrefix(v, "Bearer ") && !strings.Contains(v, "error=") {
			h.Set("WWW-Authenticate", v+`, error="invalid_token"`)
		}
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *challenge) Unwrap() http.ResponseWriter { return c.ResponseWriter }
