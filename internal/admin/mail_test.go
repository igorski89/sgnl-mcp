package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeStalwart: the management JMAP, answering as Stalwart 1.x would, and
// recording every request path and method call.
func fakeStalwart(t *testing.T) (*Stalwart, *[]string) {
	var mu sync.Mutex
	var seen []string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer API_test" {
			w.WriteHeader(401)
			return
		}
		if r.Method == "GET" && r.URL.Path == "/jmap/session" {
			json.NewEncoder(w).Encode(map[string]any{"apiUrl": srv.URL + "/jmap", "primaryAccounts": map[string]string{stalwartCap: "x"}})
			return
		}
		var req struct {
			Using       []string
			MethodCalls [][]json.RawMessage
		}
		json.NewDecoder(r.Body).Decode(&req)
		var out [][]any
		for _, mc := range req.MethodCalls {
			var name, id string
			var args map[string]any
			json.Unmarshal(mc[0], &name)
			json.Unmarshal(mc[1], &args)
			json.Unmarshal(mc[2], &id)
			b, _ := json.Marshal(args["properties"])
			seen = append(seen, name+" "+string(b))
			var res any
			switch name {
			case "x:Account/query":
				res = map[string]any{"ids": []string{"g1", "u1", "u2"}}
			case "x:Account/get":
				res = map[string]any{"list": []any{
					map[string]any{"id": "g1", "@type": "Group", "name": "contact", "domainId": "d1", "quotas": map[string]any{"maxDiskQuota": 1 << 30}, "usedDiskQuota": 5 << 20},
					map[string]any{"id": "u1", "@type": "User", "name": "igor", "domainId": "d1", "memberGroupIds": map[string]any{"g1": true},
						"aliases": map[string]any{"0": map[string]any{"name": "postmaster", "domainId": "d1", "enable": true}, "1": map[string]any{"name": "old", "domainId": "d1", "enable": false}}},
					map[string]any{"id": "u2", "@type": "User", "name": "hugo", "domainId": "d1", "memberGroupIds": map[string]any{"g1": true}, "usedDiskQuota": 3 << 20},
				}}
				// permissions only when asked (offboarding); igor's missing.
				if strings.Contains(string(b), `"permissions"`) {
					res.(map[string]any)["list"].([]any)[2].(map[string]any)["permissions"] = map[string]any{"@type": "Inherit"}
				}
			case "x:Domain/get":
				res = map[string]any{"list": []any{map[string]any{"id": "d1", "name": "co.example"}}}
			case "x:QueuedMessage/query":
				res = map[string]any{"ids": []string{"q1"}}
			case "x:QueuedMessage/get":
				// A server that answers more than asked: blobId must not get through.
				res = map[string]any{"list": []any{map[string]any{"id": "q1", "blobId": "SECRET-BLOB", "returnPath": "igor@co.example", "size": 2048, "createdAt": "2026-11-26T01:00:00Z",
					"recipients": []any{map[string]any{"address": "bob@other.example", "retryCount": 3, "expires": "2026-11-29T01:00:00Z",
						"status": map[string]any{"@type": "TemporaryFailure", "response": "451 4.7.1 try later", "blobId": "SECRET-2"}}}}}}
			}
			out = append(out, []any{name, res, id})
		}
		json.NewEncoder(w).Encode(map[string]any{"methodResponses": out})
	}))
	t.Cleanup(srv.Close)
	return &Stalwart{Base: srv.URL, Key: "API_test", HTTP: srv.Client()}, &seen
}

func TestMailAdminReads(t *testing.T) {
	_, read, _ := newFake(t)
	m, seen := fakeStalwart(t)
	cs, _ := wsession(t, &Tools{API: read, Mail: m}, nil)
	txt, isErr := call(t, cs, "mail_mailboxes", nil)
	if isErr || !strings.Contains(txt, `"members":["hugo","igor"]`) || !strings.Contains(txt, `"address":"contact@co.example"`) ||
		!strings.Contains(txt, `"aliases":["postmaster@co.example"]`) || !strings.Contains(txt, `"quota_mib":1024`) || !strings.Contains(txt, `"quota":"no limit set"`) || strings.Contains(txt, "old@") {
		t.Errorf("mailboxes: %q", txt)
	}
	if txt, isErr := call(t, cs, "mail_mailbox", map[string]any{"name": "postmaster@co.example"}); isErr || !strings.Contains(txt, `"member_of":["contact@co.example"]`) {
		t.Errorf("mailbox: %q", txt)
	}
	txt, isErr = call(t, cs, "mail_delivery", map[string]any{"to": "bob@other.example"})
	if isErr || !strings.Contains(txt, `"status":"temporary failure"`) || !strings.Contains(txt, "451 4.7.1 try later") || !strings.Contains(txt, `"retries":3`) {
		t.Errorf("delivery: %q", txt)
	}
	if strings.Contains(txt, "SECRET") {
		t.Errorf("a blobId got through: %q", txt)
	}
	for _, s := range *seen {
		if strings.Contains(s, "blobId") || strings.Contains(s, "credentials") || strings.Contains(s, "permissions") || strings.Contains(s, "download") || strings.Contains(s, "/set") {
			t.Errorf("asked for: %s", s)
		}
		if strings.HasSuffix(s, "/get null") {
			t.Errorf("a get without properties: %s", s)
		}
	}
}

func TestMailGuard(t *testing.T) {
	for _, c := range []mcall{
		{"x:QueuedMessage/get", map[string]any{"properties": []string{"id", "blobId"}}},
		{"x:QueuedMessage/get", map[string]any{}},
		{"x:Account/get", map[string]any{"properties": []string{"name", "credentials"}}},
		{"x:Account/set", map[string]any{}},
		{"x:ApiKey/get", map[string]any{"properties": []string{"id"}}},
		{"Email/get", map[string]any{"properties": []string{"id"}}},
	} {
		if checkMail(c) == nil {
			t.Errorf("%s %v allowed", c.method, c.args)
		}
	}
}

func TestMailToolsOnlyWithKey(t *testing.T) {
	_, read, _ := newFake(t)
	cs, _ := wsession(t, &Tools{API: read}, nil)
	r, _ := cs.ListTools(context.Background(), nil)
	for _, x := range r.Tools {
		if strings.HasPrefix(x.Name, "mail_") {
			t.Errorf("%s without a key", x.Name)
		}
	}
}
