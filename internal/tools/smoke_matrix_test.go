package tools

import (
	"context"
	"testing"

	"github.com/igorski89/sgnl-mcp/internal/admin"
	"github.com/igorski89/sgnl-mcp/internal/listmonk"
	"github.com/igorski89/sgnl-mcp/internal/opencloud"
	"github.com/igorski89/sgnl-mcp/internal/smoke"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Every user tool (every feature and experiment on) is in the smoke
// test's matrix, and the matrix names no tool that doesn't exist.
func TestSmokeMatrixCoversEveryTool(t *testing.T) {
	s := mcp.NewServer(&mcp.Implementation{Name: "u"}, nil)
	(&Tools{Auth: fakeTokens{}, Files: opencloud.New("http://x"), News: listmonk.New("http://x"), Access: Access{All: true},
		Experiments: map[string]bool{"audcheck": true, "confirm": true},
		Self:        admin.SelfForTest(&admin.Client{}, &admin.Client{}, nil)}).Register(s)
	r, err := connect(t, s).ListTools(context.Background(), nil)
	if err != nil || len(r.Tools) < 40 {
		t.Fatalf("%d tools, %v", len(r.Tools), err)
	}
	registered := map[string]bool{}
	for _, tool := range r.Tools {
		registered[tool.Name] = true
		if _, ok := smoke.Matrix[tool.Name]; !ok {
			t.Errorf("%s isn't in the smoke matrix (internal/smoke/matrix.go)", tool.Name)
		}
	}
	for name := range smoke.Matrix {
		if !registered[name] {
			t.Errorf("the smoke matrix names %s, which isn't a tool", name)
		}
	}
}
