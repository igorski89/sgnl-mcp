package tools

import (
	"strings"
	"testing"

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
