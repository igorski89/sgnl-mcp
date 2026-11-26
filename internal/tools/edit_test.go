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

	"github.com/igorski89/sgnl-mcp/internal/opencloud"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeDrive: files by DAV path, each with a version.
type fakeDrive struct {
	mu     sync.Mutex
	files  map[string]string
	ver    map[string]int
	puts   int
	ctypes map[string]string // Content-Type of new files
	copies []string          // COPY src → dst
	shared map[string]bool   // folders shared with others
}

func (f *fakeDrive) set(p, s string) { f.mu.Lock(); f.files[p] = s; f.ver[p]++; f.mu.Unlock() }

func (f *fakeDrive) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		p := r.URL.Path
		etag := fmt.Sprintf(`"v%d"`, f.ver[p])
		_, exists := f.files[p]
		switch {
		case p == "/graph/v1.0/me/drives":
			json.NewEncoder(w).Encode(map[string]any{"value": []map[string]any{
				{"id": "p", "name": "Personal", "driveType": "personal", "root": map[string]string{"webDavUrl": "http://x/remote.php/dav/spaces/p"}},
				{"id": "t", "name": "Team", "driveType": "project", "root": map[string]string{"webDavUrl": "http://x/remote.php/dav/spaces/t"}}}})
		case r.Method == "PUT" && r.Header.Get("If-None-Match") == "*" && !exists:
			b, _ := io.ReadAll(r.Body)
			f.files[p], f.puts = string(b), f.puts+1
			if f.ctypes == nil {
				f.ctypes = map[string]string{}
			}
			f.ctypes[p] = r.Header.Get("Content-Type")
			w.WriteHeader(201)
		case r.Method == "PROPFIND" && strings.HasSuffix(p, "/") || r.Method == "PROPFIND" && f.shared[p]:
			// A folder: shared with others if listed.
			w.WriteHeader(207)
			st := ""
			if f.shared[strings.TrimSuffix(p, "/")] {
				st = `<oc:share-types><oc:share-type>0</oc:share-type></oc:share-types>`
			}
			fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:response><d:propstat><d:status>HTTP/1.1 200 OK</d:status><d:prop>%s</d:prop></d:propstat></d:response></d:multistatus>`, st)
		case r.Method == "COPY" && exists:
			dst := strings.TrimPrefix(r.Header.Get("Destination"), "http://"+r.Host)
			if _, taken := f.files[dst]; taken || r.Header.Get("Overwrite") != "F" {
				w.WriteHeader(412)
				return
			}
			f.files[dst] = f.files[p]
			f.copies = append(f.copies, p+" → "+dst)
			w.WriteHeader(201)
		case !exists:
			w.WriteHeader(404)
		case r.Method == "GET":
			w.Header().Set("ETag", etag)
			io.WriteString(w, f.files[p])
		case r.Method == "PROPFIND":
			w.WriteHeader(207)
			fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:"><d:response><d:propstat><d:prop><d:getetag>%s</d:getetag></d:prop></d:propstat></d:response></d:multistatus>`, etag)
		case r.Method == "PUT" && r.Header.Get("If-Match") == etag:
			b, _ := io.ReadAll(r.Body)
			f.files[p], f.puts = string(b), f.puts+1
			f.ver[p]++
			w.WriteHeader(204)
		case r.Method == "PUT":
			w.WriteHeader(412)
		default:
			w.WriteHeader(405)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

const notes = "/remote.php/dav/spaces/p/notes.md"

func editSession(t *testing.T, f *fakeDrive, h elicitFn) *mcp.ClientSession {
	t.Helper()
	srv := f.serve(t)
	s := mcp.NewServer(&mcp.Implementation{Name: "t"}, nil)
	(&Tools{Auth: fakeTokens{}, Files: opencloud.New(srv.URL), Access: Access{All: true}}).Register(s)
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

func edit(t *testing.T, cs *mcp.ClientSession, p string, edits ...[2]string) *mcp.CallToolResult {
	t.Helper()
	var es []map[string]string
	for _, e := range edits {
		es = append(es, map[string]string{"old_text": e[0], "new_text": e[1]})
	}
	r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "file_edit", Arguments: map[string]any{"path": p, "edits": es}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func newDrive() *fakeDrive {
	f := &fakeDrive{files: map[string]string{}, ver: map[string]int{}}
	f.set(notes, "# Notes\n\nmeet Anna on Monday\n")
	f.set("/remote.php/dav/spaces/t/plan.md", "team plan\n")
	f.set("/remote.php/dav/spaces/p/report.pdf", "%PDF")
	return f
}

func TestEditOnlyWithThePersonsYes(t *testing.T) {
	f := newDrive()
	var asked string
	cs := editSession(t, f, func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = r.Params.Message
		return &mcp.ElicitResult{Action: "decline"}, nil
	})
	if r := edit(t, cs, notes, [2]string{"Monday", "Tuesday"}); !r.IsError || f.puts != 0 {
		t.Fatalf("declined, yet %q, %d puts", resultText(r), f.puts)
	}
	first, rest, _ := strings.Cut(asked, "\n")
	for _, want := range []string{`file "notes.md"`, "replace «Monday» with «Tuesday»"} {
		if !strings.Contains(first, want) {
			t.Errorf("first line lacks %q: %q", want, first)
		}
	}
	if !strings.Contains(rest, "personal space") || !strings.Contains(rest, "1. replace «Monday»") {
		t.Errorf("details not shown: %q", rest)
	}

	f = newDrive()
	cs = editSession(t, f, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "accept"}, nil
	})
	// A short path is the personal space; edits apply in order; one append.
	r := edit(t, cs, "/notes.md", [2]string{"Monday", "Tuesday"}, [2]string{"Anna", "Anna and Ben"}, [2]string{"", "- bring slides\n"})
	if r.IsError || f.files[notes] != "# Notes\n\nmeet Anna and Ben on Tuesday\n- bring slides\n" {
		t.Fatalf("accepted: %q, file %q", resultText(r), f.files[notes])
	}
}

func TestEditRefusals(t *testing.T) {
	f := newDrive()
	f.set("/remote.php/dav/spaces/p/twice.md", "a a")
	cs := editSession(t, f, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		t.Error("asked, though it should have been refused first")
		return &mcp.ElicitResult{Action: "accept"}, nil
	})
	for name, c := range map[string]struct {
		p string
		e [][2]string
	}{
		"project space": {"/remote.php/dav/spaces/t/plan.md", [][2]string{{"team", "x"}}},
		"not text":      {"/remote.php/dav/spaces/p/report.pdf", [][2]string{{"PDF", "x"}}},
		"not found":     {notes, [][2]string{{"Friday", "x"}}},
		"not unique":    {"/remote.php/dav/spaces/p/twice.md", [][2]string{{"a", "b"}}},
		"no change":     {notes, [][2]string{{"Monday", "Monday"}}},
		"two appends":   {notes, [][2]string{{"", "x"}, {"", "y"}}},
		"no edits":      {notes, nil},
		"unknown":       {"/remote.php/dav/spaces/zz/x.md", [][2]string{{"a", "b"}}},
		"traversal":     {"../t/plan.md", [][2]string{{"team", "x"}}},
		"query":         {"/remote.php/dav/spaces/p/notes.md?x.md", [][2]string{{"Monday", "x"}}},
	} {
		if r := edit(t, cs, c.p, c.e...); !r.IsError {
			t.Errorf("%s: not refused (%q)", name, resultText(r))
		}
	}
	if f.puts != 0 {
		t.Fatalf("%d files written", f.puts)
	}
}

// Names as the person knows them: a short path with a space and an
// umlaut reaches the escaped file, and the dialog shows it unescaped.
func TestEditShowsReadableNames(t *testing.T) {
	f := newDrive()
	f.set("/remote.php/dav/spaces/p/Q4/Müller notes.md", "draft\n") // the server sees the unescaped path
	var asked string
	cs := editSession(t, f, func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = r.Params.Message
		return &mcp.ElicitResult{Action: "accept"}, nil
	})
	long := strings.Repeat("line\n", 50)
	r := edit(t, cs, "Q4/Müller notes.md", [2]string{"", long}, [2]string{"draft", "final"})
	if r.IsError {
		t.Fatal(resultText(r))
	}
	first, _, _ := strings.Cut(asked, "\n")
	if !strings.Contains(first, `"Q4/Müller notes.md"`) || !strings.Contains(first, "(50 lines") || !strings.Contains(first, "and 1 more") {
		t.Errorf("first line: %q", first)
	}
}

// The person is shown one version; if the file changes before they accept,
// nothing is written (the confirmation is bound to the version shown).
func TestEditNotWrittenIfFileChangedWhileAsking(t *testing.T) {
	f := newDrive()
	cs := editSession(t, f, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		f.set(notes, "# Notes\n\nmeet Anna on Monday\nsomeone else's line\n")
		return &mcp.ElicitResult{Action: "accept"}, nil
	})
	r := edit(t, cs, notes, [2]string{"Monday", "Tuesday"})
	if !r.IsError || f.puts != 0 || !strings.Contains(f.files[notes], "someone else's line") {
		t.Fatalf("written over a newer version: %q, %d puts", resultText(r), f.puts)
	}
}

// Live test: file_read dropped the trailing newline and showed the name
// escaped. A text file reads exactly as it is (minus invisible characters),
// so an edit can copy old_text from it.
func TestReadIsExactForEditableText(t *testing.T) {
	f := newDrive()
	f.set("/remote.php/dav/spaces/p/Müller notes.md", "a  \n\n\n​b\n")
	cs := editSession(t, f, nil)
	r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "file_read", Arguments: map[string]any{"path": "/remote.php/dav/spaces/p/M%C3%BCller%20notes.md"}})
	if err != nil || r.IsError {
		t.Fatal(err, resultText(r))
	}
	var out FileReadOut
	b, _ := json.Marshal(r.StructuredContent)
	json.Unmarshal(b, &out)
	if out.Text != "a  \n\n\nb\n" || out.Name != "Müller notes.md" {
		t.Fatalf("read %q as %q", out.Name, out.Text)
	}
}

// Live test: a short personal path (as file_share and file_edit take) was
// refused by file_read.
func TestReadShortPersonalPath(t *testing.T) {
	f := newDrive()
	f.set("/remote.php/dav/spaces/p/Müller notes.md", "hello\n")
	cs := editSession(t, f, nil)
	r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "file_read", Arguments: map[string]any{"path": "/Müller notes.md"}})
	if err != nil || r.IsError || !strings.Contains(resultText(r), `"text":"hello\n"`) {
		t.Fatalf("short path: %v %s", err, resultText(r))
	}
}
