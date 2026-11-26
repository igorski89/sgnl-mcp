package tools

// The audit trail's meta level (the default for people's own calls) is
// default-deny: only these argument fields are stored as they are
// (targets: ids, paths, accounts, dates, flags, counts); every other field
// as SHA-256 and length, since it can hold mail, file or calendar content
// or other people's details. "tool.field" applies to one tool only. A test
// fails when a tool has a field in neither list.
var AuditClear = map[string]bool{
	// what is acted on
	"account": true, "id": true, "ids": true, "path": true, "folder": true, "to_folder": true, "space": true,
	"version": true, "index": true, "occurrence": true, "reply_to": true, "forward_of": true, "attach_files": true,
	"parent": true, "with": true,
	// how
	"access": true, "answer": true, "limit": true, "minutes": true, "start": true, "time_zone": true,
	"read": true, "starred": true, "unread": true, "reply_all": true, "on": true,
	// names of the person's own files and folders
	"file_save.name": true, "folder_create.name": true, "mail_attachment_save.name": true,
	"mail_folder_create.name": true, "mail_folder_rename.name": true,
	"file_copy.new_name": true, "file_move.new_name": true,
	// dates
	"event_list.from": true, "event_list.to": true, "availability_get.from": true, "availability_get.to": true,
	"out_of_office_set.from": true, "out_of_office_set.to": true, "mail_search.since": true,
	// results of changes
	"sent": true, "recipients": true, "outside_company": true, "changed": true, "note": true, "done": true, "size": true,
}

// AuditHashed: the fields deliberately stored only as a hash (listed so
// the test sees every field was considered).
var AuditHashed = map[string]bool{
	"body": true, "subject": true, "content": true, "sheets": true, "edits": true, "append": true, "replace": true,
	"changes": true, "query": true, "description": true, "location": true, "title": true, "attendees": true,
	"people": true, "emails": true, "phones": true, "company": true, "note": true, "name": true, "members": true,
	"add": true, "remove": true, "text": true, "to": true, "cc": true, "bcc": true, "from": true, "new_name": true,
	"label": true, "add_labels": true, "remove_labels": true, "remove_attachments": true, "from_date": true,
}
