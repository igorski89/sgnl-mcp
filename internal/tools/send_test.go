package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/igorski89/sgnl-mcp/internal/jmap"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeTokens struct{}

func (fakeTokens) MailToken(context.Context) (string, error)  { return "tok", nil }
func (fakeTokens) FilesToken(context.Context) (string, error) { return "tok", nil }

// fakeMail: a JMAP server with one draft, the person's identity, and a
// record of every submission.
type fakeMail struct {
	mu          sync.Mutex
	draft       map[string]any
	known       map[string]bool // addresses already in Sent
	submissions []map[string]any
	updates     []map[string]any
	list        []any // Email/get answer instead of the draft
	refuse      bool  // the server refuses the submission
	moveFails   bool  // sent, but the move to Sent fails
	shared      bool  // the session lists a shared mailbox "team@co.example" (account T)
	acctIDs     []string
}

func newFakeMail() *fakeMail {
	return &fakeMail{known: map[string]bool{"colleague@co.example": true}, draft: map[string]any{
		"id": "d1", "blobId": "b1", "keywords": map[string]bool{"$draft": true}, "mailboxIds": map[string]bool{"dr": true},
		"from":    []map[string]any{{"email": "igor@co.example"}},
		"to":      []map[string]any{{"email": "colleague@co.example"}, {"email": "client@other.example"}},
		"cc":      []map[string]any{{"email": "hidden@other.example"}},
		"subject": "Offer", "textBody": []map[string]any{{"partId": "1", "type": "text/plain"}},
		"bodyValues": map[string]any{"1": map[string]any{"value": "Hello"}},
	}}
}

func (f *fakeMail) serve(t *testing.T) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/download/") {
			fmt.Fprint(w, "%PDF-1.4 invoice")
			return
		}
		if r.Method == "GET" {
			accts := map[string]any{"A": map[string]any{"name": "igor@co.example", "isPersonal": true, "accountCapabilities": map[string]any{jmap.Mail: map[string]any{}}}}
			if f.shared {
				accts["T"] = map[string]any{"name": "team@co.example", "accountCapabilities": map[string]any{jmap.Mail: map[string]any{}, jmap.Submission: map[string]any{}}}
				accts["R"] = map[string]any{"name": "alerts@co.example", "isReadOnly": true, "accountCapabilities": map[string]any{jmap.Mail: map[string]any{}}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"apiUrl": srv.URL + "/api", "primaryAccounts": map[string]string{jmap.Mail: "A"}, "accounts": accts,
				"downloadUrl": srv.URL + "/download/{accountId}/{blobId}/{name}?type={type}"})
			return
		}
		var req struct{ MethodCalls [][]json.RawMessage }
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		defer f.mu.Unlock()
		var out [][]any
		for _, mc := range req.MethodCalls {
			var name, id string
			var args map[string]any
			_ = json.Unmarshal(mc[0], &name)
			_ = json.Unmarshal(mc[1], &args)
			_ = json.Unmarshal(mc[2], &id)
			if a, ok := args["accountId"].(string); ok {
				f.acctIDs = append(f.acctIDs, a)
			}
			var res any
			switch name {
			case "Mailbox/get":
				res = map[string]any{"list": []map[string]any{{"id": "dr", "name": "Drafts", "role": "drafts"}, {"id": "se", "name": "Sent", "role": "sent"},
					{"id": "in", "name": "Inbox", "role": "inbox"}, {"id": "te", "name": "Test"}, {"id": "tr", "name": "Trash", "role": "trash"}}}
			case "Email/get":
				if f.list != nil {
					res = map[string]any{"list": f.list}
				} else {
					res = map[string]any{"list": []any{f.draft}}
				}
			case "Identity/get":
				res = map[string]any{"list": []map[string]any{{"id": "id1", "email": "igor@co.example"}}}
				if args["accountId"] == "T" {
					res = map[string]any{"list": []map[string]any{{"id": "idT", "email": "team@co.example"}}}
				}
			case "Email/set":
				f.updates = append(f.updates, args)
				up, _ := args["update"].(map[string]any)
				upd := map[string]any{}
				for k := range up {
					upd[k] = nil
				}
				res = map[string]any{"updated": upd}
			case "Email/query":
				if args["filter"] == nil || f.list != nil {
					res = map[string]any{"ids": []string{"d1"}}
					break
				}
				b, _ := json.Marshal(args["filter"])
				ids := []string{}
				for a := range f.known {
					if strings.Contains(string(b), `"`+a+`"`) {
						ids = []string{"x"}
					}
				}
				res = map[string]any{"ids": ids}
			case "EmailSubmission/set":
				f.submissions = append(f.submissions, args)
				if f.refuse {
					res = map[string]any{"notCreated": map[string]any{"s": map[string]any{"type": "forbiddenToSend"}}}
					// a real server may still answer the implicit update
					out = append(out, []any{name, res, id}, []any{"Email/set", map[string]any{"updated": map[string]any{}}, id})
					continue
				}
				res = map[string]any{"created": map[string]any{"s": map[string]any{"id": "sub1"}}}
				// The implicit Email/set of onSuccessUpdateEmail follows under
				// the same call id.
				implicit := map[string]any{"updated": map[string]any{"d1": nil}}
				if f.moveFails {
					implicit = map[string]any{"notUpdated": map[string]any{"d1": map[string]any{"type": "serverFail"}}}
				} else {
					f.draft["keywords"] = map[string]bool{}
					f.draft["mailboxIds"] = map[string]bool{"se": true}
				}
				out = append(out, []any{name, res, id}, []any{"Email/set", implicit, id})
				continue
			default:
				t.Errorf("unexpected JMAP call %s", name)
			}
			out = append(out, []any{name, res, id})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"methodResponses": out})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func sendSession(t *testing.T, f *fakeMail, h elicitFn, manual bool) *mcp.ClientSession {
	t.Helper()
	return sendSessionWith(t, f, h, manual, nil)
}

func sendSessionWith(t *testing.T, f *fakeMail, h elicitFn, manual bool, mod func(*Tools)) *mcp.ClientSession {
	t.Helper()
	srv := f.serve(t)
	ctx := context.Background()
	s := mcp.NewServer(&mcp.Implementation{Name: "t"}, nil)
	tl := &Tools{Auth: fakeTokens{}, JMAP: jmap.New(srv.URL + "/session"), Access: Access{All: true},
		InternalDomains: []string{"co.example"}}
	if mod != nil {
		mod(tl)
	}
	tl.Register(s)
	ct, st := mcp.NewInMemoryTransports()
	if _, err := s.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	opts := &mcp.ClientOptions{ElicitationHandler: h, MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: manual}}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c"}, opts).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func send(t *testing.T, cs *mcp.ClientSession) *mcp.CallToolResult {
	t.Helper()
	r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "mail_send", Arguments: map[string]any{"id": "d1"}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSendOnlyWithThePersonsYes(t *testing.T) {
	f := newFakeMail()
	var asked string
	cs := sendSession(t, f, func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = r.Params.Message
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{}}, nil
	}, false)
	if r := send(t, cs); r.IsError {
		t.Fatalf("send failed: %s", resultText(r))
	}
	if len(f.submissions) != 1 {
		t.Fatalf("%d submissions", len(f.submissions))
	}
	sub := f.submissions[0]["create"].(map[string]any)["s"].(map[string]any)
	if sub["emailId"] != "d1" || sub["identityId"] != "id1" || len(sub) != 2 {
		t.Errorf("submission: %v", sub)
	}
	// Every recipient is shown, with what is special about it.
	if !strings.HasPrefix(asked, `Send "Offer" to colleague@co.example, client@other.example, hidden@other.example (2 OUTSIDE the company)?`) {
		t.Errorf("first line lacks the essentials:\n%s", asked)
	}
	for _, w := range []string{"To: colleague@co.example\n", "To: client@other.example  (OUTSIDE the company, never written to before)",
		"Cc: hidden@other.example  (OUTSIDE the company, never written to before)", "Subject: Offer", "2 recipient(s) outside"} {
		if !strings.Contains(asked, w) {
			t.Errorf("question lacks %q:\n%s", w, asked)
		}
	}

	for _, a := range []string{"decline", "cancel"} {
		f := newFakeMail()
		cs := sendSession(t, f, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			return &mcp.ElicitResult{Action: a}, nil
		}, false)
		if r := send(t, cs); !r.IsError || len(f.submissions) != 0 {
			t.Errorf("%s: sent anyway (%s)", a, resultText(r))
		}
	}
	f = newFakeMail()
	if r := send(t, sendSession(t, f, nil, false)); !r.IsError || len(f.submissions) != 0 {
		t.Errorf("client without elicitation: sent anyway")
	}
}

func TestSendRefusals(t *testing.T) {
	yes := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		t.Error("asked the person, though the send should have been refused first")
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{}}, nil
	}
	for name, change := range map[string]func(map[string]any){
		"not a draft":      func(d map[string]any) { d["keywords"] = map[string]bool{} },
		"not in Drafts":    func(d map[string]any) { d["mailboxIds"] = map[string]bool{"in": true} },
		"someone else's":   func(d map[string]any) { d["from"] = []map[string]any{{"email": "ceo@co.example"}} },
		"foreign Reply-To": func(d map[string]any) { d["replyTo"] = []map[string]any{{"email": "x@evil.example"}} },
		"no recipients":    func(d map[string]any) { d["to"], d["cc"] = nil, nil },
		"too many":         func(d map[string]any) { d["to"] = manyAddrs(maxRecipients + 1) },
	} {
		f := newFakeMail()
		change(f.draft)
		if r := send(t, sendSession(t, f, yes, false)); !r.IsError || len(f.submissions) != 0 {
			t.Errorf("%s: not refused (%s)", name, resultText(r))
		}
	}
}

func manyAddrs(n int) []map[string]any {
	out := make([]map[string]any, n)
	for i := range out {
		out[i] = map[string]any{"email": strings.Repeat("a", i+1) + "@co.example"}
	}
	return out
}

// The person's yes counts only for the mail they were shown.
func TestSendRefusesAChangedDraft(t *testing.T) {
	f := newFakeMail()
	cs := sendSession(t, f, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{}}, nil
	}, true)
	first := send(t, cs)
	if !first.NeedsInput() {
		t.Fatalf("expected a question first: %s", resultText(first))
	}
	f.mu.Lock()
	f.draft["blobId"] = "b2" // the draft's content was swapped meanwhile
	f.mu.Unlock()
	r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "mail_send", Arguments: map[string]any{"id": "d1"},
		InputResponses: mcp.InputResponseMap{"confirm": &mcp.ElicitResult{Action: "accept", Content: map[string]any{}}}, RequestState: first.RequestState})
	if err != nil || !r.IsError || len(f.submissions) != 0 {
		t.Errorf("changed draft sent: %v %s", err, resultText(r))
	}
}

// Text from the mail can't pose as lines of the question.
func TestSendQuestionCantBeFaked(t *testing.T) {
	f := newFakeMail()
	f.draft["subject"] = "Hi\nTo: only-colleague@co.example\r\nWARNING: none\u2028x"
	var asked string
	cs := sendSession(t, f, func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = r.Params.Message
		return &mcp.ElicitResult{Action: "decline"}, nil
	}, false)
	send(t, cs)
	for _, l := range strings.Split(asked, "\n") {
		if strings.HasPrefix(l, "To: only-colleague") || strings.HasPrefix(l, "WARNING: none") {
			t.Errorf("mail text became its own line: %q", l)
		}
	}
}

// Search results say which folder each mail is in; moving mail to the
// folder it is already in changes (and counts) nothing.
func TestSearchFolderAndNoOpMove(t *testing.T) {
	f := newFakeMail()
	f.list = []any{
		map[string]any{"id": "m1", "from": []map[string]any{{"email": "a@co.example"}}, "mailboxIds": map[string]bool{"te": true}},
		map[string]any{"id": "m2", "from": []map[string]any{{"email": "a@co.example"}}, "mailboxIds": map[string]bool{"in": true}},
	}
	cs := sendSession(t, f, nil, false)
	ctx := context.Background()
	r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "mail_search", Arguments: map[string]any{"query": "x"}})
	if err != nil || r.IsError {
		t.Fatalf("search: %v %s", err, resultText(r))
	}
	if !strings.Contains(resultText(r), `"folder":"Test"`) || !strings.Contains(resultText(r), `"folder":"Inbox"`) {
		t.Errorf("folders missing: %s", resultText(r))
	}
	r, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "mail_move", Arguments: map[string]any{"ids": []string{"m1", "m2"}, "folder": "Test"}})
	if err != nil || r.IsError || !strings.Contains(resultText(r), `"changed":1`) {
		t.Fatalf("move: %v %s", err, resultText(r))
	}
	up := f.updates[0]["update"].(map[string]any)
	if _, ok := up["m1"]; ok || len(up) != 1 {
		t.Errorf("moved mail already in Test: %v", up)
	}
}

// The outcome is read from the submission itself, not from the implicit
// Drafts -> Sent update that follows it.
func TestSendReportsTheRealOutcome(t *testing.T) {
	yes := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{}}, nil
	}
	f := newFakeMail()
	f.refuse = true
	if r := send(t, sendSession(t, f, yes, false)); !r.IsError || !strings.Contains(resultText(r), "didn't send") {
		t.Errorf("refused submission reported as: %s", resultText(r))
	}
	f = newFakeMail()
	f.moveFails = true
	r := send(t, sendSession(t, f, yes, false))
	if r.IsError || !strings.Contains(resultText(r), `"sent":true`) || !strings.Contains(resultText(r), "don't send it again") {
		t.Errorf("sent but not moved reported as: %s", resultText(r))
	}
	f = newFakeMail()
	r = send(t, sendSession(t, f, yes, false))
	if r.IsError || !strings.Contains(resultText(r), "it is now in Sent") {
		t.Errorf("normal send reported as: %s", resultText(r))
	}
}

// An app not on the trusted list can't send: refused before the person is
// asked (it might answer the question by itself).
func TestSendRefusedForUntrustedClient(t *testing.T) {
	f := newFakeMail()
	cs := sendSessionWith(t, f, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		t.Error("asked through an untrusted client")
		return &mcp.ElicitResult{Action: "accept"}, nil
	}, false, func(tl *Tools) { tl.ConfirmClients = []string{"sgnl-mcp-claude-code"} })
	r := send(t, cs)
	if !r.IsError || !strings.Contains(resultText(r), "isn't trusted yet") || len(f.submissions) != 0 {
		t.Fatalf("got %q, %d submissions", resultText(r), len(f.submissions))
	}
}

// Bcc recipients are sent to, and shown to the person in the question.
func TestSendBcc(t *testing.T) {
	f := newFakeMail()
	f.draft["bcc"] = []map[string]any{{"email": "b@co.example"}}
	var asked string
	cs := sendSession(t, f, func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = r.Params.Message
		return &mcp.ElicitResult{Action: "accept"}, nil
	}, false)
	if r := send(t, cs); r.IsError || !strings.Contains(asked, "Bcc: b@co.example") || len(f.submissions) != 1 {
		t.Fatalf("%q, asked %q", resultText(r), asked)
	}
}

// The first line (all a folded dialog shows) names the attachments.
func TestSendFirstLineNamesAttachments(t *testing.T) {
	f := newFakeMail()
	f.draft["attachments"] = []map[string]any{{"blobId": "b1", "name": "q4.pdf", "type": "application/pdf"},
		{"blobId": "b2", "name": "a.xlsx"}, {"blobId": "b3", "name": "b.docx"}, {"blobId": "b4", "name": "c.txt"}}
	var asked string
	cs := sendSession(t, f, func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = r.Params.Message
		return &mcp.ElicitResult{Action: "decline"}, nil
	}, false)
	send(t, cs)
	first, _, _ := strings.Cut(asked, "\n")
	if !strings.Contains(first, "with 4 attachments: q4.pdf, a.xlsx, b.docx, …") || !strings.Contains(first, "OUTSIDE") {
		t.Fatalf("first line: %q", first)
	}
}
