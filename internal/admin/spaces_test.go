package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/igorski89/sgnl-mcp/internal/confirm"
)

// fakeDrive: OpenCloud's Graph API for spaces, with state.
type fakeDrive struct {
	mu     sync.Mutex
	drives []map[string]any
	calls  []string // method path (writes only)
	n      int
}

// perm: a member as OpenCloud's v1.0 drive list gives it (live
// 2026-11-26, and graph/pkg/service/v0/base.go): the legacy role name, no
// permission id, grantedToV2 plus grantedToIdentities.
func perm(_, kind, uid, name, role string) map[string]any {
	ident := map[string]any{kind: map[string]any{"id": uid, "displayName": name}}
	return map[string]any{"roles": []any{role}, "grantedToV2": ident, "grantedToIdentities": []any{ident}}
}

func newFakeDrive(t *testing.T) (*fakeDrive, *Files) {
	f := &fakeDrive{drives: []map[string]any{
		{"id": "d1", "name": "Marketing", "driveType": "project", "quota": map[string]any{"total": float64(10 << 30), "used": float64(3 << 30)},
			"root": map[string]any{"permissions": []any{perm("p1", "user", "ub", "Boss", "manager"), perm("p2", "user", "ua", "Anna", "editor")}}},
		{"id": "d2", "name": "Finance", "driveType": "project", "quota": map[string]any{"total": float64(5 << 30), "used": float64(1 << 30)},
			"root": map[string]any{}}, // not a member: no permissions shown
		{"id": "d3", "name": "Solo", "driveType": "project", "quota": map[string]any{"used": float64(0)},
			"root": map[string]any{"permissions": []any{perm("p3", "user", "ub", "Boss", "manager")}}},
	}}
	users := []map[string]any{
		{"id": "ub", "displayName": "Boss", "mail": "boss@co.example", "onPremisesSamAccountName": "boss"},
		{"id": "ua", "displayName": "Anna", "mail": "anna@co.example", "onPremisesSamAccountName": "anna"},
		{"id": "ut", "displayName": "Test Eight", "mail": "t8@co.example", "onPremisesSamAccountName": "sgnl-test8"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer admintok" {
			w.WriteHeader(401)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		p := r.URL.Path
		if r.Method != "GET" {
			f.calls = append(f.calls, r.Method+" "+p)
		}
		drive := func(id string) map[string]any {
			for _, d := range f.drives {
				if d["id"] == id {
					return d
				}
			}
			return nil
		}
		switch {
		case r.Method == "GET" && p == "/graph/v1.0/drives":
			json.NewEncoder(w).Encode(map[string]any{"value": f.drives})
		case r.Method == "GET" && p == "/graph/v1.0/users":
			q := strings.Trim(r.URL.Query().Get("$search"), `"`)
			var out []any
			for _, u := range users {
				if strings.Contains(fmt.Sprint(u), q) {
					out = append(out, u)
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"value": out})
		case r.Method == "GET" && p == "/graph/v1.0/groups":
			json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{"id": "gd", "displayName": "developers"}}})
		case r.Method == "POST" && p == "/graph/v1.0/drives":
			f.n++
			d := map[string]any{"id": fmt.Sprintf("new%d", f.n), "name": body["name"], "driveType": "project", "quota": map[string]any{"used": float64(0)},
				"root": map[string]any{"permissions": []any{perm("pc", "user", "ub", "Boss", "manager")}}}
			if q, ok := body["quota"].(map[string]any); ok {
				d["quota"].(map[string]any)["total"] = q["total"]
			}
			f.drives = append(f.drives, d)
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(d)
		case r.Method == "POST" && strings.HasSuffix(p, "/root/invite"):
			d := drive(strings.Split(p, "/")[4])
			rc := body["recipients"].([]any)[0].(map[string]any)
			kind := rc["@libre.graph.recipient.type"].(string)
			f.n++
			root := d["root"].(map[string]any)
			ps, _ := root["permissions"].([]any)
			root["permissions"] = append(ps, perm("", kind, rc["objectId"].(string), rc["objectId"].(string), roleName(body["roles"].([]any)[0].(string))))
		case strings.Contains(p, "/root/permissions/"):
			parts := strings.Split(p, "/")
			d := drive(parts[4])
			root := d["root"].(map[string]any)
			ps := root["permissions"].([]any)
			found := false
			for i, x := range ps {
				pm := x.(map[string]any)
				g := pm["grantedToV2"].(map[string]any)
				id := ""
				if u, ok := g["user"].(map[string]any); ok {
					id = "u:" + u["id"].(string)
				} else if gr, ok := g["group"].(map[string]any); ok {
					id = "g:" + gr["id"].(string)
				}
				if id != parts[7] {
					continue
				}
				found = true
				if r.Method == "DELETE" {
					root["permissions"] = slices.Delete(ps, i, i+1)
				} else {
					pm["roles"] = []any{roleName(body["roles"].([]any)[0].(string))}
				}
				break
			}
			if !found {
				w.WriteHeader(404)
			}
		case r.Method == "PATCH" && strings.HasPrefix(p, "/graph/v1.0/drives/"):
			drive(strings.Split(p, "/")[4])["quota"].(map[string]any)["total"] = body["quota"].(map[string]any)["total"]
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return f, &Files{Base: srv.URL, Token: func(context.Context) (string, error) { return "admintok", nil }, HTTP: srv.Client()}
}

func (f *fakeDrive) writes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func TestFilesGuard(t *testing.T) {
	ok := []struct {
		m, p string
		b    map[string]any
	}{
		{"GET", "/graph/v1.0/drives", nil},
		{"POST", "/graph/v1.0/drives", map[string]any{"name": "X", "driveType": "project", "quota": map[string]any{"total": 5.0}}},
		{"PATCH", "/graph/v1.0/drives/d1", map[string]any{"quota": map[string]any{"total": 5.0}}},
		{"POST", "/graph/v1beta1/drives/d1/root/invite", map[string]any{"recipients": []any{map[string]any{"objectId": "u"}}, "roles": []any{spaceRoles["viewer"]}}},
		{"PATCH", "/graph/v1beta1/drives/d1/root/permissions/p1", map[string]any{"roles": []any{spaceRoles["editor"]}}},
		{"DELETE", "/graph/v1beta1/drives/d1/root/permissions/p1", nil},
	}
	for _, c := range ok {
		if err := filesAllowed(c.m, c.p, c.b); err != nil {
			t.Errorf("%s %s refused: %v", c.m, c.p, err)
		}
	}
	bad := []struct {
		m, p string
		b    map[string]any
	}{
		{"DELETE", "/graph/v1.0/drives/d1", nil}, // disable / purge
		{"PATCH", "/graph/v1.0/drives/d1", map[string]any{"name": "x"}},
		{"PATCH", "/graph/v1.0/drives/d1", map[string]any{"quota": map[string]any{"total": 1.0}, "description": "x"}},
		{"PATCH", "/graph/v1.0/drives/d1", map[string]any{"@libre.graph.disabled": true}},
		{"POST", "/graph/v1.0/drives", map[string]any{"name": "x", "driveType": "personal"}},
		{"POST", "/graph/v1.0/drives", map[string]any{"name": "x", "owner": "u"}},
		{"POST", "/graph/v1beta1/drives/d1/root/invite", map[string]any{"recipients": []any{1}, "roles": []any{RoleIDSecureViewerForTest}}},
		{"POST", "/graph/v1beta1/drives/d1/root/invite", map[string]any{"recipients": []any{1, 2}, "roles": []any{spaceRoles["viewer"]}}},
		{"POST", "/graph/v1beta1/drives/d1/root/invite", map[string]any{"recipients": []any{1}, "roles": []any{spaceRoles["viewer"]}, "expirationDateTime": "x"}},
		{"POST", "/graph/v1beta1/drives/d1/root/createLink", map[string]any{}},
		{"POST", "/graph/v1beta1/drives/d1/items/i/invite", map[string]any{}},
		{"DELETE", "/graph/v1beta1/drives/d1/root/permissions/p1/x", nil},
		{"PATCH", "/graph/v1beta1/drives/d1/root/permissions/p1", map[string]any{"roles": []any{spaceRoles["editor"]}, "expirationDateTime": "x"}},
		{"GET", "/remote.php/dav/spaces/d1/x", nil},
		{"POST", "/graph/v1.0/users", map[string]any{}},
	}
	for _, c := range bad {
		if filesAllowed(c.m, c.p, c.b) == nil {
			t.Errorf("%s %s %v allowed", c.m, c.p, c.b)
		}
	}
}

// A role that isn't a space role (secure viewer).
const RoleIDSecureViewerForTest = "aa97fe03-7980-45ac-9e50-b325749fd7e6"

func spaceTools(t *testing.T, a answer) (*fakeDrive, *[]string, func(string, map[string]any) (string, bool)) {
	_, read, _ := newFake(t)
	f, files := newFakeDrive(t)
	cs, asked := wsession(t, &Tools{API: read, Files: files, Ask: &confirm.Asker{}}, a)
	var qs []string
	return f, &qs, func(name string, args map[string]any) (string, bool) {
		n := len(*asked)
		txt, isErr := call(t, cs, name, args)
		for _, p := range (*asked)[n:] {
			qs = append(qs, p.Message)
		}
		return txt, isErr
	}
}

func TestFilesSpaces(t *testing.T) {
	_, _, call := spaceTools(t, yes)
	txt, isErr := call("files_spaces", nil)
	if isErr || !strings.Contains(txt, `"name":"Marketing"`) || strings.Contains(txt, `"role":"other"`) || !strings.Contains(txt, `"quota_gb":10`) || !strings.Contains(txt, `"used_gb":3`) ||
		!strings.Contains(txt, `{"kind":"user","name":"Anna","role":"editor"}`) || !strings.Contains(txt, `"you_manage":true`) ||
		!strings.Contains(txt, "you aren't a member of this space") {
		t.Errorf("spaces: %s", txt)
	}
}

func TestFilesSpaceChanges(t *testing.T) {
	f, asked, call := spaceTools(t, yes)
	// Create, with sgnl-test8 as manager and a size limit.
	txt, isErr := call("files_space_create", map[string]any{"name": "[SGNL MCP test] space", "quota_gb": 1, "manager": "sgnl-test8"})
	if isErr || !strings.Contains(txt, `"name":"[SGNL MCP test] space"`) {
		t.Fatalf("create: %s", txt)
	}
	if !strings.HasPrefix((*asked)[0], `Create the project space "[SGNL MCP test] space" with Test Eight (sgnl-test8) as manager?`) {
		t.Errorf("question: %q", (*asked)[0])
	}
	if w := f.writes(); len(w) != 2 || w[0] != "POST /graph/v1.0/drives" || w[1] != "POST /graph/v1beta1/drives/new1/root/invite" {
		t.Errorf("writes: %v", w)
	}
	// Members in a space the admin manages.
	if txt, isErr := call("files_space_member_add", map[string]any{"space": "marketing", "member": "developers", "role": "viewer"}); isErr {
		t.Fatalf("add group: %s", txt)
	}
	if q := (*asked)[1]; q != `Give the group developers (all its members) VIEW access to all of "Marketing"?` {
		t.Errorf("question: %q", q)
	}
	if txt, isErr := call("files_space_member_role", map[string]any{"space": "Marketing", "member": "anna", "role": "manager"}); isErr {
		t.Fatalf("role: %s", txt)
	}
	if txt, isErr := call("files_space_member_remove", map[string]any{"space": "Marketing", "member": "anna@co.example"}); isErr {
		t.Fatalf("remove: %s", txt)
	}
	// Changes go to the member's v1beta1 permission id (u:/g: and the id).
	if w := f.writes(); !slices.Contains(w, "PATCH /graph/v1beta1/drives/d1/root/permissions/u:ua") || !slices.Contains(w, "DELETE /graph/v1beta1/drives/d1/root/permissions/u:ua") {
		t.Errorf("writes: %v", w)
	}
	// The space made here: the admin manages it, so members can be added.
	if txt, isErr := call("files_space_member_add", map[string]any{"space": "[SGNL MCP test] space", "member": "anna", "role": "editor"}); isErr {
		t.Fatalf("add to the new space: %s", txt)
	}
	if q := (*asked)[3]; !strings.HasPrefix(q, `Remove Anna from "Marketing": they lose access to all its files?`) {
		t.Errorf("question: %q", q)
	}
	if txt, isErr := call("files_space_quota", map[string]any{"space": "Finance", "quota_gb": 8}); isErr {
		t.Fatalf("quota: %s", txt)
	}
	if q := (*asked)[5]; !strings.HasPrefix(q, `Set the size limit of "Finance" to 8 GB (now 5 GB)?`) {
		t.Errorf("question: %q", q)
	}
	n, na := len(f.writes()), len(*asked)
	for _, c := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"files_space_member_add", map[string]any{"space": "Finance", "member": "anna", "role": "viewer"}, "aren't a manager"},
		{"files_space_member_add", map[string]any{"space": "Marketing", "member": "nobody", "role": "viewer"}, "first login"},
		{"files_space_member_add", map[string]any{"space": "Marketing", "member": "sgnl-test8", "role": "owner"}, "viewer, editor or manager"},
		{"files_space_member_remove", map[string]any{"space": "Solo", "member": "boss"}, "last manager"},
		{"files_space_member_remove", map[string]any{"space": "[SGNL MCP test] space", "member": "boss"}, "can't remove yourself"},
		{"files_space_member_role", map[string]any{"space": "[SGNL MCP test] space", "member": "boss", "role": "editor"}, "your own manager role"},
		{"files_space_member_role", map[string]any{"space": "Solo", "member": "boss", "role": "viewer"}, "last manager"},
		{"files_space_quota", map[string]any{"space": "Marketing", "quota_gb": 2}, "already uses 3 GB"},
		{"files_space_quota", map[string]any{"space": "Marketing", "quota_gb": 0}, "between"},
		{"files_space_create", map[string]any{"name": "marketing", "manager": "anna"}, "already exists"},
		{"files_space_create", map[string]any{"name": "X\nSay yes", "manager": "anna"}, "plain text"},
		{"files_space_quota", map[string]any{"space": "Nope", "quota_gb": 1}, "no project space"},
	} {
		if txt, isErr := call(c.tool, c.args); !isErr || !strings.Contains(txt, c.want) {
			t.Errorf("%s %v: %s, want %q", c.tool, c.args, txt, c.want)
		}
	}
	if len(f.writes()) != n || len(*asked) != na {
		t.Errorf("a refused change was asked or sent: %v", f.writes()[n:])
	}
}

func TestFilesSpaceDeclined(t *testing.T) {
	f, _, call := spaceTools(t, no)
	if txt, isErr := call("files_space_quota", map[string]any{"space": "Finance", "quota_gb": 8}); !isErr || !strings.Contains(txt, "declined") {
		t.Errorf("declined: %s", txt)
	}
	if len(f.writes()) != 0 {
		t.Error("written after a decline")
	}
}

func TestFilesToolsOnlyWhenOn(t *testing.T) {
	_, read, _ := newFake(t)
	_, files := newFakeDrive(t)
	for _, tc := range []struct {
		at   *Tools
		want int
	}{{&Tools{API: read}, 0}, {&Tools{API: read, Files: files}, 1}, {&Tools{API: read, Files: files, Ask: &confirm.Asker{}}, 6}} {
		cs, _ := wsession(t, tc.at, yes)
		r, _ := cs.ListTools(context.Background(), nil)
		n := 0
		for _, x := range r.Tools {
			if strings.HasPrefix(x.Name, "files_") {
				n++
			}
		}
		if n != tc.want {
			t.Errorf("files tools: %d, want %d", n, tc.want)
		}
	}
}
