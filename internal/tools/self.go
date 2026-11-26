package tools

import (
	"context"
	"fmt"

	"github.com/igorski89/sgnl-mcp/internal/admin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---- my_sessions / logout_everywhere: the person's own login account.

func (t *Tools) mySessions(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, admin.MySessions, error) {
	out, err := t.Self.Sessions(ctx)
	return nil, out, err
}

type LogoutOut struct {
	Done string `json:"done"`
	Note string `json:"note"`
}

func (t *Tools) logoutEverywhere(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, LogoutOut, error) {
	me, err := t.Self.Username(ctx)
	if err != nil {
		return nil, LogoutOut{}, err
	}
	q := fmt.Sprintf("Log %s out EVERYWHERE now? Every SGNL login session and app token of yours ends: browsers, phone and desktop apps that log in with SGNL, and this AI app too (you'll log in again here).\n\nApp passwords of mail apps keep working. Do this after losing a device, or if you think someone else is logged in as you.", me)
	res, ok, err := t.confirm(ctx, req, q, me)
	if err != nil || res != nil {
		return res, LogoutOut{}, err
	}
	if !ok {
		return nil, LogoutOut{}, fmt.Errorf("the person declined: no one was logged out")
	}
	if err := t.Self.LogoutEverywhere(ctx); err != nil {
		return nil, LogoutOut{}, err
	}
	return nil, LogoutOut{Done: "logged out everywhere",
		Note: "Every session and token ended, this connection's too: the next request here asks the person to log in again. If a device is lost, also ask whoever runs the instance to remove its mail app passwords."}, nil
}
