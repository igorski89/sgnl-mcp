# sgnl-mcp

The MCP gateway of an [SGNL](https://github.com/igorski89/sgnl) instance: one
[Model Context Protocol](https://modelcontextprotocol.io) endpoint over the
instance's apps, so a person can work with their mail, calendar and files
from Claude, Codex or their own client, logged in with their SGNL account.

Status: proof of concept (M2+): reads, private and reversible writes, and
sending a draft after the person confirms it. Nothing is ever deleted, and
nothing is sent without that confirmation.

For people using it: [docs/user-guide.md](docs/user-guide.md) (what to ask, what
needs your click, staying safe).

## Tools

| tool | app | does |
|---|---|---|
| `whoami` | login server | username, name, email, groups |
| `day_overview` | all | today's events, unread mail, files changed today |
| `mail_search` | mail (JMAP) | messages by words / sender / date / folder / unread: id, folder, sender, subject, date, preview |
| `mail_read` | mail (JMAP) | one message as plain text (hidden text removed), attachments listed |
| `mail_thread_read` | mail (JMAP) | a whole conversation |
| `mail_attachment_read` | mail (JMAP) | an attachment as text |
| `mail_attachment_save` | mail → files | an attachment as a new file in the drive (personal space by default; shared space: confirmation) |
| `mailbox_list` | mail (JMAP) | folders with unread counts; also the shared mailboxes the person is a member of |
| `mail_draft_create` | mail (JMAP) | new mail, reply, reply-all or forward, saved in Drafts |
| `mail_draft_update` | mail (JMAP) | change a draft (replaced by a new draft) |
| `mail_flag` | mail (JMAP) | read / unread, flagged |
| `mail_move` | mail (JMAP) | move between folders; never to Trash, Junk, Sent or Drafts |
| `mail_trash` | mail (JMAP) | move mail to Trash (at most 25), **only after the person confirms**; restorable |
| `out_of_office_get` | mail (JMAP) | the automatic reply: on/off, dates, text |
| `out_of_office_set` | mail (JMAP) | switch it on (**only after the person confirms** dates and whole text; plain text) or off |
| `mail_send` | mail (JMAP) | send a draft, **only after the person confirms** |
| `event_list` | calendar (JMAP) | events in a date range |
| `event_read` | calendar (JMAP) | one event in full: organizer, attendees and their answers |
| `event_create` | calendar (JMAP) | a private event, no participants |
| `event_invite` | calendar (JMAP) | a meeting with attendees, who get invitations, **only after the person confirms** |
| `event_import` | mail → calendar | add an invitation received by mail (e.g. from outside) to the calendar, **only after the person confirms**; mails no one |
| `event_respond` | calendar (JMAP) | answer an invitation (accept/decline/maybe); the organizer is mailed, **only after the person confirms** |
| `event_update` | calendar (JMAP) | change time, title, place or text of the person's own event; for a meeting **only after the person confirms**, attendees get the update |
| `event_cancel` | calendar (JMAP) | cancel a meeting the person organizes (attendees get a cancellation) or delete a private event, **only after the person confirms** |
| `availability_get` | calendar (JMAP) | colleagues' free/busy |
| `contact_search` | contacts (JMAP) | address book and company directory |
| `file_search` | files (OpenCloud) | files and folders by name, across all spaces the person can see |
| `file_read` | files (OpenCloud) | a file as text (text, Markdown, CSV, HTML, PDF, .docx) |
| `folder_list` | files (OpenCloud) | spaces, or a folder's contents |
| `file_share` | files (OpenCloud) | share a file or folder in the personal space with one colleague (view/edit), **only after the person confirms**; no links |
| `file_save` | files (OpenCloud) | a new text file, never overwriting; **shared spaces need a confirmation** |
| `file_edit` | files (OpenCloud) | change a text file in the personal space (exact replacements or an append), **only after the person confirms**, and only if the file is still the version shown |

## Admin endpoint

With `SGNL_MCP_ADMIN_CLIENT_ID` / `SGNL_MCP_ADMIN_CLIENT_SECRET` set, the
gateway also serves `<public host>/admin/mcp`, a separate MCP connection with
read-only admin tools (`admin_users`, `admin_user`, `admin_groups`,
`admin_events`, `admin_audit`) over the login server's admin API. It takes
only tokens issued for it (its URL in `aud`), and only members of
`SGNL_MCP_ADMIN_GROUPS` (default `admins`) by the login server's current
groups. The admin client is the one master credential the gateway holds:
read scope only, every call logged with the acting person.

## How it works

- **Login:** the gateway is an OAuth 2.1 resource server. It publishes
  Protected Resource Metadata (RFC 9728) naming the instance's login server;
  MCP clients log the person in there. It accepts only tokens from that
  issuer whose audience names the gateway (`SGNL_MCP_CLIENT_ID`, or its
  public URL as an RFC 8707 resource indicator), with at least 10 s left.
- **No token passthrough:** the gateway trades the person's token for a
  short-lived one of its own (RFC 8693 token exchange) and calls the app with
  that. The apps see the person, with their own permissions; the gateway
  holds no master credentials. The exchange happens when a request arrives:
  if the login server refuses the login (revoked, rotated out, logged out),
  the client gets 401 `invalid_token` and refreshes. The app token is kept in
  memory for that one login token (never another of the same person) and
  app until 30 s before either expires; concurrent calls share one exchange.
  With `SGNL_MCP_MAIL_AUDIENCE` / `SGNL_MCP_FILES_AUDIENCE` set, each app
  gets a token for its own audience, so an app that checks `aud` refuses a
  token meant for the other (the `aud_check` experiment shows it).
- **Key set:** the issuer's signing keys are fetched at most once a minute,
  so junk tokens can't make the gateway hammer the login server; only
  public-key algorithms are accepted.
- **Narrow writes:** the JMAP client allows only the methods the tools need
  (no destroy except replacing a verified draft; mail submission only from
  `mail_send`); the OpenCloud client only reads and creates new files.
- **Identity check:** apps that update the account (and its role) from the
  token they get (OpenCloud) are only called when the gateway's token
  carries exactly the person's username, name, email and groups.
- **Untrusted content:** mail and file content is written by others. The
  tools and server instructions say so, hidden text is removed, and anything
  that leaves the person's own space (sending, saving to a shared space) needs
  their confirmation in the client.
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
needs a confidential client for the gateway with token exchange
(impersonation) allowed, and a client for each MCP client (e.g. Claude Code:
public, PKCE, its access tokens' audience including the gateway's client id).
Both get the scopes `openid profile email groups`, and the identity claims
(`preferred_username`, `email`, `name`, `groups`) must be in the JWT access
tokens themselves. Keycloak and Authelia (with token exchange) both work.
Then, in Claude Code:

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

- Mail: the gateway writes drafts; `mail_send` sends a draft only after
  the person says yes in their app, shown every recipient (Bcc included)
  and what is special about them (outside the company, never written to
  before), plus a warning when outside mail was read shortly before.
  Sender and Reply-To must be the person's own; at most 50 recipients;
  drafts stay in Drafts; nothing is deleted except a draft being replaced.
  `mail_trash` moves mail to Trash (restorable) after the person confirms
  the listed mails; nothing is ever destroyed.
  Shared mailboxes: every mail tool takes an optional `account`, one
  the mail server lists for the person (it enforces who may do what);
  sending from one needs a sending address the server gives the person
  there, and the confirmation's first line says "Send AS <address>".
  The out-of-office reply (it answers every sender) is switched on only
  after the person confirms the dates and the whole text; plain text only,
  through its own path in the JMAP client. Forwarding and filters stay
  impossible.
- Calendar: events with attendees (which send invitations) only through
  `event_invite`, after the person confirms; the mail server makes the
  person the organizer, the gateway can't set organizer or reply address.
  Answers (`event_respond`: only the person's own participation status),
  changes to their own meetings (`event_update`: time, title, place, text,
  never who is invited) and cancellations (`event_cancel`: one event) each
  have their own narrow path in the JMAP client and need the person's yes;
  nothing is mailed about an event that is over, and repeating events
  aren't changed yet. Invitations from outside aren't added by the mail
  server itself; `event_import` adds one from the received mail after the
  person confirms (organizer shown, OUTSIDE marked), only if they are an
  attendee, without alerts or links, and without mailing anyone.
- Files: `file_save` only makes new files; saving into a shared space
  needs the person's confirmation. `file_edit` changes a text file in the
  personal space after the person confirms the changes shown; the gateway
  applies exact replacements to the whole file as it is on the server
  (larger than 2 MB is refused, never cut), and writes only if the file is
  still the version shown: checked just before, and sent as If-Match
  (`Overwrite` is the client's only way to replace a file). No deleting,
  renaming or moving. Sharing: one colleague at a time, from the personal space,
  view or edit, after the person confirms; the client's only POST is that
  invite (no public links, no unsharing).
- Confirmations are asked of the person through MCP elicitation and bound
  to the exact request; the model only sees the outcome. Clients without
  elicitation can't do confirmed actions.
- Mail and file text reaches the model as a person would see it: hidden
  HTML text and invisible characters are dropped.
- Per person: rate limits per kind of call; every call is logged with its
  outcome, never content.

## License

MIT. The container image also carries the license texts of Go and of every library compiled into the
binary (all MIT, BSD or Apache-2.0) in `/licenses`; the image build gathers them and fails when one is missing.
