package auth

import (
	"bytes"
	"io"
	"net/http"
	"sync"
	"time"
)

// jwksCooldown fetches the issuer's key set at most once per `every`;
// within that, it answers from the last good copy. go-oidc refetches the
// key set whenever a signature doesn't verify (an unknown key id, or any
// forged token), so without this every junk token sent to a public
// gateway would cost the login server a request. Key rotation is still
// picked up, at most `every` late.
type jwksCooldown struct {
	next  http.RoundTripper
	url   string
	every time.Duration
	now   func() time.Time

	mu      sync.Mutex
	last    time.Time
	body    []byte
	header  http.Header
	fetches int
}

func (j *jwksCooldown) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet || req.URL.String() != j.url {
		return j.next.RoundTrip(req)
	}
	j.mu.Lock()
	defer j.mu.Unlock() // one fetch at a time; the others then get the copy
	if j.body != nil && j.now().Sub(j.last) < j.every {
		return j.cached(req), nil
	}
	resp, err := j.next.RoundTrip(req)
	j.fetches++
	if err != nil || resp.StatusCode != http.StatusOK {
		if j.body != nil {
			if resp != nil {
				resp.Body.Close()
			}
			return j.cached(req), nil
		}
		return resp, err
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	j.body, j.header, j.last = b, resp.Header.Clone(), j.now()
	return j.cached(req), nil
}

func (j *jwksCooldown) cached(req *http.Request) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: j.header.Clone(), Body: io.NopCloser(bytes.NewReader(j.body)), ContentLength: int64(len(j.body)), Request: req}
}
