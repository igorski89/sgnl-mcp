package tools

import (
	"context"

	"github.com/igorski89/sgnl-mcp/internal/confirm"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// confirm asks the person (see package confirm): the same trusted
// clients and fast-answer rule for every confirmed action.
func (t *Tools) confirm(ctx context.Context, req *mcp.CallToolRequest, message, bind string) (*mcp.CallToolResult, bool, error) {
	return t.Asker().Ask(ctx, req, message, bind)
}

// Asker: who may be asked to confirm (shared with the admin endpoint).
func (t *Tools) Asker() *confirm.Asker {
	return &confirm.Asker{Clients: t.ConfirmClients, FastOK: t.ConfirmFastOK}
}

// ---- confirm_test (experiment: SGNL_MCP_EXPERIMENTS=confirm)

type ConfirmTestOut struct {
	Answer string `json:"answer" jsonschema:"what the person chose: accepted or declined"`
	Note   string `json:"note"`
}

func (t *Tools) confirmTest(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ConfirmTestOut, error) {
	res, ok, err := t.confirm(ctx, req, "SGNL MCP test: approve this test question? Nothing happens either way.", "")
	if err != nil || res != nil {
		return res, ConfirmTestOut{}, err
	}
	a := "declined"
	if ok {
		a = "accepted"
	}
	return nil, ConfirmTestOut{a, "test only: nothing was changed"}, nil
}
