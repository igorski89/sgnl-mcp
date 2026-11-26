package admin

// The admin connector records at the full level by default; at the meta
// level every admin argument is stored as it is: admin calls name
// accounts, groups, mailboxes and spaces, never anyone's content.
var AuditClear = map[string]bool{
	"username": true, "groups": true, "email": true, "hours": true, "name": true, "limit": true,
	"description": true, "manager": true, "quota_gb": true, "member": true, "role": true, "space": true,
	"to": true, "from": true, "members": true, "sender_name": true, "mailbox": true, "person": true,
	"tool": true, "since": true, "until": true, "target": true, "call": true,
	// results
	"done": true, "note": true, "count": true,
}
