package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/igorski89/sgnl-mcp/internal/listmonk"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeListmonk: lists 1 (double opt-in) and 2 (single), subscriber 7,
// campaigns 10 (draft) and 11 (scheduled), template 3 (tx); every call
// that changes something is recorded.
type fakeListmonk struct {
	mu      sync.Mutex
	changes []string
	bodies  map[string]map[string]any
	sent    int // campaign 12 is sending: each read shows more sent
}

func (f *fakeListmonk) serve(t *testing.T) *httptest.Server {
	f.bodies = map[string]map[string]any{}
	data := map[string]string{
		"GET /api/profile":       `{"email":"me@co.example","name":"Me"}`,
		"GET /api/lists/1":       `{"id":1,"name":"News​","optin":"double","type":"public","status":"active","tags":["a"],"subscriber_count":5,"subscriber_statuses":{"confirmed":4,"unconfirmed":1}}`,
		"GET /api/lists/2":       `{"id":2,"name":"Team","optin":"single","type":"private","status":"active","subscriber_count":3,"subscriber_statuses":{"confirmed":1,"unconfirmed":2}}`,
		"GET /api/lists":         `{"results":[{"id":1,"name":"News​","optin":"double","subscriber_statuses":{"confirmed":4,"unconfirmed":1}}],"total":1,"page":1}`,
		"GET /api/subscribers/7": `{"id":7,"email":"ann@example.org","name":"Ann","status":"enabled","attribs":{"city":"Bern"},"lists":[{"id":1,"name":"News","optin":"double","subscription_status":"unconfirmed"}]}`,
		"GET /api/subscribers":   `{"results":[{"id":7,"email":"ann@example.org","name":"Ann","status":"enabled"},{"id":8,"email":"me@co.example","name":"Me","status":"enabled"}],"total":2}`,
		"GET /api/campaigns/10":  `{"id":10,"name":"December","subject":"Our news","status":"draft","content_type":"html","body":"<p>Hello</p>","lists":[{"id":1,"name":"News"},{"id":2,"name":"Team"}],"media":[{"id":5}],"attribs":{"x":1}}`,
		"GET /api/campaigns/11":  `{"id":11,"name":"Later","subject":"S","status":"scheduled","send_at":"2099-01-01T09:00:00Z","lists":[{"id":2,"name":"Team"}]}`,
		"GET /api/templates/3":   `{"id":3,"name":"Receipt","type":"tx","subject":"Your receipt","body":"<p>{{ .Tx.Data.n }}</p>"}`,
		"GET /api/bounces/4":     `{"id":4,"type":"hard","email":"ann@example.org","created_at":"2026-11-01"}`,
		// As dev listmonk answers: objects with nulls in them.
		"GET /api/dashboard/counts":        `{"subscribers":{"total":2,"blocklisted":null,"orphans":0},"lists":{"total":2},"campaigns":{"total":1,"by_status":{"draft":1}},"messages":0}`,
		"GET /api/campaigns/running/stats": `[]`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k := r.Method + " " + r.URL.Path
		if r.Method != "GET" {
			b, _ := io.ReadAll(r.Body)
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			f.mu.Lock()
			f.changes = append(f.changes, k)
			f.bodies[k] = m
			f.mu.Unlock()
			w.Write([]byte(`{"data":{"id":10,"status":"running"}}`))
			return
		}
		if k == "GET /api/campaigns/12" {
			f.mu.Lock()
			f.sent += 7
			n := f.sent
			f.mu.Unlock()
			fmt.Fprintf(w, `{"data":{"id":12,"name":"Now","subject":"S","status":"running","to_send":100,"sent":%d,"views":%d,"updated_at":"t%d","lists":[{"id":2,"name":"Team"}]}}`, n, n/2, n)
			return
		}
		d, ok := data[k]
		if !ok {
			w.WriteHeader(400)
			w.Write([]byte(`{"message":"not found"}`))
			return
		}
		w.Write([]byte(`{"data":` + d + `}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeListmonk) changed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.changes...)
}

func newsSession(t *testing.T, f *fakeListmonk, h elicitFn, mod func(*Tools)) *mcp.ClientSession {
	t.Helper()
	srv := f.serve(t)
	s := mcp.NewServer(&mcp.Implementation{Name: "t"}, nil)
	tl := &Tools{Auth: fakeTokens{}, News: listmonk.New(srv.URL), Access: Access{All: true}, InternalDomains: []string{"co.example"}}
	if mod != nil {
		mod(tl)
	}
	tl.Register(s)
	ct, st := mcp.NewInMemoryTransports()
	if _, err := s.Connect(context.Background(), st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c"}, &mcp.ClientOptions{ElicitationHandler: h}).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) (string, bool) {
	t.Helper()
	r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return resultText(r), r.IsError
}

func answerAs(action string, asked *[]string) elicitFn {
	return func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		*asked = append(*asked, r.Params.Message)
		return &mcp.ElicitResult{Action: action, Content: map[string]any{}}, nil
	}
}

// Every newsletter change but a draft or a new template needs the
// person's yes: an app that can't ask gets reads and drafts only.
func TestNewsletterUntrustedAppGetsReadsAndDrafts(t *testing.T) {
	f := &fakeListmonk{}
	cs := newsSession(t, f, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		t.Error("asked through an untrusted app")
		return &mcp.ElicitResult{Action: "accept"}, nil
	}, func(tl *Tools) { tl.ConfirmClients = []string{"sgnl-mcp-claude-code"} })
	if txt, isErr := call(t, cs, "newsletter_lists", nil); isErr || strings.Contains(txt, "​") || !strings.Contains(txt, "written by others") {
		t.Errorf("lists: %s", txt)
	}
	if txt, isErr := call(t, cs, "newsletter_campaign_draft_create", map[string]any{"name": "n", "subject": "s", "lists": []int{1}, "body": "b"}); isErr {
		t.Errorf("draft: %s", txt)
	}
	// Every newsletter tool that isn't a read or a draft is refused, found
	// from the registered tools (so a new one can't slip through).
	args := map[string]map[string]any{
		"newsletter_campaign_send":        {"id": 10},
		"newsletter_campaign_stop":        {"id": 11, "action": "unschedule"},
		"newsletter_campaign_delete":      {"id": 10},
		"newsletter_test_send":            {"id": 10, "to": []string{"nobody@example.org"}}, // refused for the app, not the address
		"newsletter_tx_send":              {"template_id": 3, "to": []string{"ann@example.org"}},
		"newsletter_list_create":          {"name": "x"},
		"newsletter_list_update":          {"id": 1, "name": "y"},
		"newsletter_list_delete":          {"id": 1},
		"newsletter_subscriber_add":       {"email": "b@example.org", "lists": []int{2}},
		"newsletter_subscriber_update":    {"id": 7, "name": "Anne"},
		"newsletter_subscription_change":  {"id": 7, "action": "unsubscribe", "lists": []int{1}},
		"newsletter_subscriber_blocklist": {"id": 7},
		"newsletter_subscriber_delete":    {"id": 7},
		"newsletter_template_create":      {"name": "t", "type": "tx", "subject": "s", "body": "b"},
		"newsletter_template_update":      {"id": 3, "name": "z"},
		"newsletter_bounce_delete":        {"id": 4},
	}
	lt, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	asked := 0
	for _, tool := range lt.Tools {
		if !strings.HasPrefix(tool.Name, "newsletter_") || tool.Annotations.ReadOnlyHint || newsDrafts[tool.Name] {
			continue
		}
		a, ok := args[tool.Name]
		if !ok {
			t.Errorf("%s changes something without being a draft: give it a confirmation (and arguments here)", tool.Name)
			continue
		}
		asked++
		if txt, isErr := call(t, cs, tool.Name, a); !isErr || !strings.Contains(txt, "isn't trusted yet") {
			t.Errorf("%s: %v %s", tool.Name, isErr, txt)
		}
	}
	if asked != len(args) {
		t.Errorf("%d of %d confirmed tools checked", asked, len(args))
	}
	if got := f.changed(); len(got) != 1 || got[0] != "POST /api/campaigns" {
		t.Errorf("changes: %v", got)
	}
}

// Sending shows the lists, up to how many people and the subject; only a
// yes starts it, and the status is the only thing sent.
func TestNewsletterCampaignSend(t *testing.T) {
	f := &fakeListmonk{}
	var asked []string
	cs := newsSession(t, f, answerAs("decline", &asked), nil)
	if txt, isErr := call(t, cs, "newsletter_campaign_send", map[string]any{"id": 10}); !isErr || !strings.Contains(txt, "declined") {
		t.Errorf("declined: %s", txt)
	}
	if len(asked) != 1 || !strings.Contains(asked[0], `SEND the newsletter campaign "December" NOW to up to 7 people`) ||
		!strings.Contains(asked[0], "Subject: Our news") || !strings.Contains(asked[0], "News (double opt-in, 4 recipient(s)), Team (single opt-in, 3 recipient(s))") {
		t.Errorf("question: %q", asked)
	}
	if len(f.changed()) != 0 {
		t.Fatalf("declined, yet: %v", f.changed())
	}
	cs = newsSession(t, f, answerAs("accept", &asked), nil)
	if txt, isErr := call(t, cs, "newsletter_campaign_send", map[string]any{"id": 10}); isErr {
		t.Fatal(txt)
	}
	if got := f.changed(); len(got) != 1 || got[0] != "PUT /api/campaigns/10/status" || f.bodies[got[0]]["status"] != "running" {
		t.Errorf("changes: %v %v", got, f.bodies)
	}
	// Already scheduled: not sent again.
	if txt, isErr := call(t, cs, "newsletter_campaign_send", map[string]any{"id": 11}); !isErr || !strings.Contains(txt, "already scheduled") {
		t.Errorf("scheduled: %s", txt)
	}
}

// Scheduling: the date goes in with the stored campaign (lists, media,
// attributes kept), then the status.
func TestNewsletterCampaignSchedule(t *testing.T) {
	f := &fakeListmonk{}
	var asked []string
	cs := newsSession(t, f, answerAs("accept", &asked), nil)
	if txt, isErr := call(t, cs, "newsletter_campaign_send", map[string]any{"id": 10, "send_at": "2099-06-01T09:00:00Z"}); isErr {
		t.Fatal(txt)
	}
	if !strings.Contains(asked[0], "at Mon 1 Jun 2099 09:00 UTC") {
		t.Errorf("question: %s", asked[0])
	}
	put := f.bodies["PUT /api/campaigns/10"]
	if put["send_at"] != "2099-06-01T09:00:00Z" || len(put["lists"].([]any)) != 2 || len(put["media"].([]any)) != 1 || put["attribs"] == nil {
		t.Errorf("put: %v", put)
	}
	if f.bodies["PUT /api/campaigns/10/status"]["status"] != "scheduled" {
		t.Errorf("status: %v", f.bodies)
	}
}

// Only drafts change without a question; a scheduled campaign was
// approved as it is.
func TestNewsletterDraftUpdate(t *testing.T) {
	f := &fakeListmonk{}
	cs := newsSession(t, f, nil, nil)
	if txt, isErr := call(t, cs, "newsletter_campaign_draft_update", map[string]any{"id": 11, "subject": "x"}); !isErr || !strings.Contains(txt, "only drafts") {
		t.Errorf("scheduled: %s", txt)
	}
	if txt, isErr := call(t, cs, "newsletter_campaign_draft_update", map[string]any{"id": 10, "subject": "New"}); isErr {
		t.Fatal(txt)
	}
	put := f.bodies["PUT /api/campaigns/10"]
	if put["subject"] != "New" || put["body"] != "<p>Hello</p>" || len(put["lists"].([]any)) != 2 || len(put["media"].([]any)) != 1 {
		t.Errorf("put: %v", put)
	}
}

// A test goes only to existing subscribers, by default the person; an
// unknown address is refused before anyone is asked.
func TestNewsletterTestSend(t *testing.T) {
	f := &fakeListmonk{}
	var asked []string
	cs := newsSession(t, f, answerAs("accept", &asked), nil)
	if txt, isErr := call(t, cs, "newsletter_test_send", map[string]any{"id": 10, "to": []string{"nobody@example.org"}}); !isErr || !strings.Contains(txt, "not subscribers") || len(asked) != 0 {
		t.Errorf("unknown: %s %v", txt, asked)
	}
	if txt, isErr := call(t, cs, "newsletter_test_send", map[string]any{"id": 10}); isErr {
		t.Fatal(txt)
	}
	if !strings.Contains(asked[0], "TEST of the newsletter campaign \"December\" to me@co.example?") {
		t.Errorf("question: %s", asked[0])
	}
	b := f.bodies["POST /api/campaigns/10/test"]
	if s, _ := b["subscribers"].([]any); len(s) != 1 || s[0] != "me@co.example" || b["body"] != "<p>Hello</p>" {
		t.Errorf("test body: %v", b)
	}
	asked = nil
	call(t, cs, "newsletter_test_send", map[string]any{"id": 10, "to": []string{"ann@example.org"}})
	if !strings.Contains(asked[0], "ann@example.org (OUTSIDE the company)") {
		t.Errorf("outside: %s", asked[0])
	}
}

// Adding to a double opt-in list says the opt-in mail goes out; recording
// consent says so, and is sent as preconfirm.
func TestNewsletterSubscriberAddConsent(t *testing.T) {
	f := &fakeListmonk{}
	var asked []string
	cs := newsSession(t, f, answerAs("accept", &asked), nil)
	call(t, cs, "newsletter_subscriber_add", map[string]any{"email": "b@example.org", "name": "Bea", "lists": []int{1}})
	if !strings.Contains(asked[0], "MAILS THEM a request to confirm") || !strings.Contains(asked[0], "OUTSIDE") {
		t.Errorf("opt-in: %s", asked[0])
	}
	if f.bodies["POST /api/subscribers"]["preconfirm_subscriptions"] != false {
		t.Errorf("body: %v", f.bodies)
	}
	call(t, cs, "newsletter_subscriber_add", map[string]any{"email": "b@example.org", "lists": []int{1}, "consented": true})
	if !strings.Contains(asked[1], "RECORDS THAT THEY AGREED") || f.bodies["POST /api/subscribers"]["preconfirm_subscriptions"] != true {
		t.Errorf("consent: %s %v", asked[1], f.bodies)
	}
	// A change to someone with an unconfirmed double opt-in subscription
	// says listmonk mails them again.
	call(t, cs, "newsletter_subscriber_update", map[string]any{"id": 7, "name": "Anne"})
	if !strings.Contains(asked[2], "name: Ann → Anne") || !strings.Contains(asked[2], "MAILS THEM again") {
		t.Errorf("update: %s", asked[2])
	}
	if got := f.changed(); got[len(got)-1] != "PATCH /api/subscribers/7" {
		t.Errorf("changes: %v", got)
	}
}

// A campaign that is sending can be paused: its counters move while the
// question is open, and the answer still counts.
func TestNewsletterPauseWhileSending(t *testing.T) {
	f := &fakeListmonk{}
	var asked []string
	cs := newsSession(t, f, answerAs("accept", &asked), nil)
	if txt, isErr := call(t, cs, "newsletter_campaign_stop", map[string]any{"id": 12, "action": "pause"}); isErr {
		t.Fatalf("pause: %s", txt)
	}
	if f.bodies["PUT /api/campaigns/12/status"]["status"] != "paused" || !strings.Contains(asked[0], "Pause the newsletter campaign \"Now\"") {
		t.Errorf("%v %v", f.bodies, asked)
	}
}

func TestNewsletterStatsTotals(t *testing.T) {
	cs := newsSession(t, &fakeListmonk{}, nil, nil)
	if txt, isErr := call(t, cs, "newsletter_stats", nil); isErr || !strings.Contains(txt, `"blocklisted":null`) {
		t.Errorf("%s", txt)
	}
}

func TestNewsletterSubscriberRead(t *testing.T) {
	f := &fakeListmonk{}
	cs := newsSession(t, f, nil, nil)
	txt, isErr := call(t, cs, "newsletter_subscriber_read", map[string]any{"id": 7})
	if isErr || !strings.Contains(txt, "city = Bern") || !strings.Contains(txt, "written by others") {
		t.Errorf("%s", txt)
	}
}

func TestAttribsCapped(t *testing.T) {
	big := map[string]any{"a": strings.Repeat("x", 500), "deep": map[string]any{"b": map[string]any{"c": map[string]any{"d": 1}}}}
	for i := range 100 {
		big[strings.Repeat("k", 3)+string(rune('A'+i%26))+string(rune('a'+i/26))] = strings.Repeat("y", 50)
	}
	got := strings.Join(attribs(big), "\n")
	if len(got) > 2300 || !strings.Contains(got, "(truncated)") || !strings.Contains(got, "more attributes, not shown") {
		t.Errorf("%d: %s", len(got), got[:200])
	}
}

// A confirmed action costs two of the outward budget, both paid when the
// question is asked; a call that doesn't ask costs one. With one left, a
// send is refused before asking, never after the person's yes.
func TestConfirmedActionsPayWhenAsking(t *testing.T) {
	f := &fakeListmonk{}
	var asked []string
	cs := newsSession(t, f, answerAs("accept", &asked), nil)
	// Refused without asking (already scheduled): one of 20.
	if _, isErr := call(t, cs, "newsletter_campaign_send", map[string]any{"id": 11}); !isErr {
		t.Fatal("a scheduled campaign was sent")
	}
	for i := range 9 {
		if txt, isErr := call(t, cs, "newsletter_campaign_send", map[string]any{"id": 10}); isErr {
			t.Fatalf("send %d: %s", i+1, txt)
		}
	}
	txt, isErr := call(t, cs, "newsletter_campaign_send", map[string]any{"id": 10})
	if !isErr || !strings.Contains(txt, "too many outward") || len(asked) != 9 {
		t.Errorf("with one left: %s, %d questions", txt, len(asked))
	}
	if n := len(f.changed()); n != 9 {
		t.Errorf("%d sends", n)
	}
}
