package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/igorski89/sgnl-mcp/internal/office"
	"github.com/igorski89/sgnl-mcp/internal/textx"
)

func TestOfficeFileFromSaveInput(t *testing.T) {
	b, ct, err := officeFile(FileSaveIn{Name: "Plan.docx", Content: "# Plan\n\n- a\n- b"})
	if err != nil || !strings.Contains(ct, "wordprocessingml") {
		t.Fatal(err, ct)
	}
	if got, _ := textx.Extract("Plan.docx", ct, b, false); !strings.Contains(got, "Plan") || !strings.Contains(got, "b") {
		t.Errorf("docx: %q", got)
	}
	// CSV into one sheet: numbers stay numbers, codes with leading zeros text.
	b, ct, err = officeFile(FileSaveIn{Name: "a.xlsx", Content: "Name,Amount,Code\nRent,1200.5,007\nZero,0,0.5\n"})
	if err != nil || !strings.Contains(ct, "spreadsheetml") {
		t.Fatal(err, ct)
	}
	if got, _ := textx.Extract("a.xlsx", ct, b, false); got != "## Sheet: Sheet1\nName\tAmount\tCode\nRent\t1200.5\t007\nZero\t0\t0.5" {
		t.Errorf("xlsx from csv: %q", got)
	}
	if _, _, err := officeFile(FileSaveIn{Name: "a.xlsx"}); err == nil {
		t.Error("empty xlsx allowed")
	}
	if _, _, err := officeFile(FileSaveIn{Name: "a.docx", Sheets: []office.Sheet{{Name: "x"}}}); err == nil {
		t.Error("sheets for a docx allowed")
	}
	if _, ct, _ := officeFile(FileSaveIn{Name: "a.pptx"}); ct != "" {
		t.Error("pptx made")
	}
}

func TestFileSaveOfficeAndCopyThroughTheTools(t *testing.T) {
	f := newDrive()
	f.shared = map[string]bool{"/remote.php/dav/spaces/p/Shared": true}
	var asked []string
	cs := editSession(t, f, func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = append(asked, r.Params.Message)
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{}}, nil
	})
	// Sheets through the tool's schema: numbers, text, a date, null, true.
	txt, isErr := callTool(t, cs, "file_save", map[string]any{"name": "Budget.xlsx", "sheets": []any{
		map[string]any{"name": "Q4", "rows": []any{[]any{"Item", "Cost", "When"}, []any{"Rent", 1200.5, "2026-01-15"}, []any{"Paid", nil, true}}}}})
	if isErr {
		t.Fatalf("xlsx: %s", txt)
	}
	p := "/remote.php/dav/spaces/p/Budget.xlsx"
	if !strings.Contains(f.ctypes[p], "spreadsheetml") {
		t.Errorf("content type: %q", f.ctypes[p])
	}
	if got, _ := textx.Extract("Budget.xlsx", "", []byte(f.files[p]), false); got != "## Sheet: Q4\nItem\tCost\tWhen\nRent\t1200.5\t2026-01-15\nPaid\t\tTRUE" {
		t.Errorf("saved xlsx: %q", got)
	}
	if _, isErr := callTool(t, cs, "file_save", map[string]any{"name": "Plan.docx", "content": "# Plan\n\nText."}); isErr || !strings.Contains(f.ctypes["/remote.php/dav/spaces/p/Plan.docx"], "wordprocessingml") {
		t.Error("docx not saved")
	}
	if len(asked) != 0 {
		t.Errorf("asked for the personal space: %v", asked)
	}
	// Into a project space: asked.
	if _, isErr := callTool(t, cs, "file_save", map[string]any{"folder": "/remote.php/dav/spaces/t/", "name": "Team.docx", "content": "x"}); isErr || len(asked) != 1 {
		t.Errorf("project space: asked %v", asked)
	}

	// file_copy: in the same folder only with a new name; into a shared folder asked.
	if txt, isErr := callTool(t, cs, "file_copy", map[string]any{"path": "/notes.md"}); !isErr || !strings.Contains(txt, "new_name") {
		t.Errorf("same folder: %s", txt)
	}
	if _, isErr := callTool(t, cs, "file_copy", map[string]any{"path": "/notes.md", "new_name": "notes copy.md"}); isErr || len(asked) != 1 {
		t.Errorf("private copy asked or failed: %v", asked)
	}
	if _, isErr := callTool(t, cs, "file_copy", map[string]any{"path": "/notes.md", "to_folder": "/remote.php/dav/spaces/p/Shared/"}); isErr || len(asked) != 2 ||
		!strings.HasPrefix(asked[1], `Copy the file "notes.md" to "Shared" as "notes.md"?`) {
		t.Errorf("shared copy: %v", asked)
	}
	if _, isErr := callTool(t, cs, "file_copy", map[string]any{"path": "/notes.md", "to_folder": "/remote.php/dav/spaces/t/"}); isErr || len(asked) != 3 {
		t.Errorf("copy to another space: %v", asked)
	}
	if len(f.copies) != 3 || !strings.HasSuffix(f.copies[2], "/remote.php/dav/spaces/t/notes.md") {
		t.Errorf("copies: %v", f.copies)
	}
	// Never over a name.
	if txt, isErr := callTool(t, cs, "file_copy", map[string]any{"path": "/notes.md", "new_name": "notes copy.md"}); !isErr || !strings.Contains(txt, "already exists") {
		t.Errorf("over a name: %s", txt)
	}
}

func TestFileOfficeEdit(t *testing.T) {
	f := newDrive()
	x, _ := office.Xlsx([]office.Sheet{{Name: "Q1", Rows: [][]any{{"Item", "Cost"}, {"Rent", 1200.0}}}})
	d, _ := office.Docx("# Plan\n\nMeet Anna on Monday.", "")
	f.set("/remote.php/dav/spaces/p/b.xlsx", string(x))
	f.set("/remote.php/dav/spaces/p/plan.docx", string(d))
	f.set("/remote.php/dav/spaces/p/m.xlsm", string(x))
	var asked []string
	answer := "accept"
	cs := editSession(t, f, func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = append(asked, r.Params.Message)
		return &mcp.ElicitResult{Action: answer, Content: map[string]any{}}, nil
	})
	txt, isErr := callTool(t, cs, "file_office_edit", map[string]any{"path": "/b.xlsx", "changes": []any{
		map[string]any{"sheet": "Q1", "set_cells": map[string]any{"B2": 1300}, "append_rows": []any{[]any{"Power", 80, "2026-02-01"}}}}})
	if isErr {
		t.Fatalf("xlsx: %s", txt)
	}
	if len(asked) != 1 || !strings.HasPrefix(asked[0], `Change "b.xlsx": Sheet "Q1": add 1 row(s) after the last one (rows 3–3)`) || !strings.Contains(asked[0], `Sheet "Q1", B2: 1200 → 1300`) {
		t.Errorf("question: %q", asked)
	}
	if got, _ := textx.Extract("b.xlsx", "", []byte(f.files["/remote.php/dav/spaces/p/b.xlsx"]), false); got != "## Sheet: Q1\nItem\tCost\nRent\t1300\nPower\t80\t2026-02-01" {
		t.Errorf("after: %q", got)
	}
	if _, isErr := callTool(t, cs, "file_office_edit", map[string]any{"path": "/plan.docx", "append": "## Next\n\n- one", "replace": []any{map[string]any{"old": "Monday", "new": "Tuesday"}}}); isErr {
		t.Fatal("docx")
	}
	if got, _ := textx.Extract("plan.docx", "", []byte(f.files["/remote.php/dav/spaces/p/plan.docx"]), false); !strings.Contains(got, "Meet Anna on Tuesday.") || !strings.Contains(got, "• one") {
		t.Errorf("docx after: %q", got)
	}
	// Declined: nothing written. Refusals: nothing asked.
	answer = "decline"
	before := f.files["/remote.php/dav/spaces/p/b.xlsx"]
	if _, isErr := callTool(t, cs, "file_office_edit", map[string]any{"path": "/b.xlsx", "changes": []any{map[string]any{"sheet": "Q1", "set_cells": map[string]any{"A1": "x"}}}}); !isErr || f.files["/remote.php/dav/spaces/p/b.xlsx"] != before {
		t.Error("declined but written")
	}
	n := len(asked)
	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"path": "/m.xlsm", "changes": []any{map[string]any{"sheet": "Q1", "set_cells": map[string]any{"A1": 1}}}}, "macros"},
		{map[string]any{"path": "/notes.md", "append": "x"}, "only .xlsx and .docx"},
		{map[string]any{"path": "/b.xlsx", "append": "x"}, "for .xlsx give changes"},
		{map[string]any{"path": "/plan.docx", "replace": []any{map[string]any{"old": "nowhere", "new": "x"}}}, "isn't in the document"},
	} {
		if txt, isErr := callTool(t, cs, "file_office_edit", c.args); !isErr || !strings.Contains(txt, c.want) {
			t.Errorf("%v: %s, want %q", c.args, txt, c.want)
		}
	}
	if len(asked) != n {
		t.Error("asked for a refused edit")
	}
}
