# sgnl-mcp

The MCP gateway of an [SGNL](https://github.com/igorski89/sgnl) instance: one
[Model Context Protocol](https://modelcontextprotocol.io) endpoint over the
instance's apps, so a person can work with their mail, calendar and files
from Claude, Codex or their own client, logged in with their SGNL account.

Status: proof of concept (M2): reads plus private, reversible writes. Nothing is ever sent, shared or deleted.

## Tools

| tool | app | does |
|---|---|---|
| `whoami` | login server | username, name, email, groups |
| `mail_search` | mail (JMAP) | messages matching words / sender / date: id, sender, subject, date, preview |
| `mail_read` | mail (JMAP) | one message as plain text, attachments listed |
| `calendar_list` | calendar (JMAP) | events in a date range |
| `files_search` | files (OpenCloud) | files and folders by name, across all spaces the person can see |
| `file_read` | files (OpenCloud) | a file as text (text, Markdown, CSV, JSON, YAML, .docx) |

## How it works

- **Login:** the gateway is an OAuth 2.1 resource server. It publishes
  Protected Resource Metadata (RFC 9728) naming the instance's login server;
  MCP clients log the person in there. It accepts only tokens from that
  issuer whose audience names the gateway (`SGNL_MCP_CLIENT_ID`).
- **No token passthrough:** for every call it trades the person's token for
  a short-lived one of its own (RFC 8693 token exchange) and calls the app
  with that. The apps see the person, with their own permissions; the
  gateway holds no master credentials.
- **Read-only:** the JMAP client can only send get/query methods; the
  OpenCloud client only GET, PROPFIND and REPORT.
- **Identity check:** apps that update the account (and its role) from the
  token they get (OpenCloud) are only called when the gateway's token
  carries exactly the person's username, name, email and groups.
- **Untrusted content:** mail and file content is written by others. The
  tools and server instructions say so; nothing in this version can send,
  share or delete.
- **Naming:** each instance is shown as `SGNL - <instance>`
  (`SGNL_MCP_LABEL`), so people with several SGNL connections don't mix
  them up.
- **Logs:** one line per tool call (who, which tool, outcome), never content.

## Run

```
go run ./cmd/sgnl-mcp
```

with `SGNL_MCP_LABEL`, `SGNL_MCP_ISSUER`, `SGNL_MCP_CLIENT_SECRET`,
`SGNL_MCP_JMAP_URL` (and optionally `SGNL_MCP_OPENCLOUD_URL`, `SGNL_MCP_PUBLIC_URL`, `SGNL_MCP_ADDR`,
`SGNL_MCP_CLIENT_ID`) set; see `cmd/sgnl-mcp/main.go`. The login server
needs a confidential client for the gateway with token exchange allowed,
and a client for each MCP client (e.g. Claude Code: public, PKCE, an
audience mapper adding the gateway's client id). Then, in Claude Code:

```
claude mcp add --transport http --client-id <client> --callback-port <port> sgnl-<instance> <public url>
```

## Container

`ghcr.io/igorski89/sgnl-mcp:<version>` (published for `v*` tags; see
`Dockerfile`) listens on `0.0.0.0:8765`, logs JSON lines and serves
`/health`. Off loopback it refuses to start unless it is told who may use
it: `SGNL_MCP_ALLOW_USERS` (usernames or emails) and/or
`SGNL_MCP_ALLOW_GROUPS`, or `SGNL_MCP_ALLOW_ALL=1`. `SGNL_MCP_DEBUG` works
only on loopback.

## Guard rails

- Mail: drafts only, never sent by the gateway on its own; drafts stay in
  Drafts; nothing is deleted except a draft being replaced.
- Calendar: private events only (no participants, so no invitations).
- Files: only new files; saving into a shared space needs the person's
  confirmation.
- Confirmations are asked of the person through MCP elicitation and bound
  to the exact request; the model only sees the outcome. Clients without
  elicitation can't do confirmed actions.
- Mail and file text reaches the model as a person would see it: hidden
  HTML text and invisible characters are dropped.
- Per person: rate limits per kind of call; every call is logged with its
  outcome, never content.

## License

MIT
