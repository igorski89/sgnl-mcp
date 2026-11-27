package smoke

// Mode: how the smoke test covers a tool.
type Mode string

const (
	Read       Mode = "read"                          // read-only, called for real
	Accepted   Mode = "accepted for real"             // a private change, confirmed
	Both       Mode = "needs both accounts"           // between the two test accounts, confirmed
	Declined   Mode = "dialog checked, then declined" // outward: the question is checked, never accepted
	NotCovered Mode = "not covered"
)

// Entry: a tool's coverage.
type Entry struct {
	Modes []Mode
	Note  string
}

const news = "newsletter, as B (one listmonk for all)"

func e(note string, m ...Mode) Entry { return Entry{m, note} }

// Matrix: every user tool and how the smoke test covers it. A unit test
// fails when a registered tool is missing here (and when a tool here
// isn't registered, or a covered tool has no step).
var Matrix = map[string]Entry{
	"whoami":        e("checks the run is on the two test accounts", Read),
	"my_sessions":   e("", Read),
	"mailbox_list":  e("own and the shared mailbox", Read),
	"mailbox_size":  e("", Read),
	"identity_list": e("own and the shared mailbox", Read),
	"calendar_list": e("", Read),
	"day_overview":  e("", Read),

	"mail_draft_create":    e("new, reply, forward, to the shared mailbox's name, to outside", Accepted),
	"mail_draft_update":    e("", Accepted),
	"mail_send":            e("A→B, B→A (reply, forward), AS the shared mailbox; to outside: declined", Both, Declined),
	"mail_search":          e("", Read),
	"mail_read":            e("", Read),
	"mail_thread_read":     e("", Read),
	"mail_flag":            e("read, star, label on and off", Accepted),
	"mail_move":            e("into the [smoke] folder", Accepted),
	"mail_trash":           e("everything the run sent or received", Accepted),
	"mail_folder_create":   e("one permanent \"[smoke] folder\" per account (there's no tool to delete a folder): created on the first run only", Accepted),
	"mail_folder_rename":   e("renamed and back", Accepted),
	"mail_attachment_read": e("A's attachment, read by B", Both),
	"mail_attachment_save": e("into B's personal space", Accepted),

	"event_list":       e("", Read),
	"event_read":       e("", Read),
	"event_create":     e("a private event", Accepted),
	"event_update":     e("a private event, and A's invitation (B gets the update)", Accepted, Both),
	"event_cancel":     e("the private event, and A's invitation (B gets the cancellation)", Accepted, Both),
	"event_invite":     e("A invites B; inviting outside: declined", Both, Declined),
	"event_respond":    e("B accepts A's invitation", Both),
	"event_import":     e("B imports A's invitation from its email (or is told it's already there)", Both),
	"availability_get": e("A asks for B's free/busy", Both),

	"contact_create":       e("", Accepted),
	"contact_update":       e("", Accepted),
	"contact_delete":       e("the contact and the group", Accepted),
	"contact_search":       e("", Read),
	"contact_groups":       e("", Read),
	"contact_group_create": e("", Accepted),
	"contact_group_update": e("rename, add, remove", Accepted),

	"folder_list":          e("", Read),
	"folder_create":        e("", Accepted),
	"file_save":            e(".md, .docx ending in a table, .xlsx", Accepted),
	"file_read":            e("text, .docx, .xlsx", Read),
	"file_search":          e("by name and by content", Read),
	"file_edit":            e("", Accepted),
	"file_office_edit":     e(".docx append, .xlsx rows and cells", Accepted),
	"file_versions":        e("", Read),
	"file_version_restore": e("", Accepted),
	"file_copy":            e("", Accepted),
	"file_move":            e("rename", Accepted),
	"file_trash":           e("", Accepted),
	"file_trash_list":      e("", Read),
	"file_restore":         e("", Accepted),
	"file_share":           e("A shares a folder with B", Both),
	"file_unshare":         e("", Both),
	"file_shares":          e("A's shared by me, B's shared with me", Read, Both),

	"out_of_office_get": e("", Read),
	"out_of_office_set": e("on for a window a month ahead (nobody gets a reply), then off", Accepted),

	"logout_everywhere": e("it would end the runner's own login", Declined),

	// The newsletter tools (the smoke accounts are listmonk users in dev).
	"newsletter_lists":                 e(news, Read),
	"newsletter_subscribers":           e(news, Read),
	"newsletter_subscriber_read":       e(news, Read),
	"newsletter_campaigns":             e(news, Read),
	"newsletter_campaign_read":         e(news, Read),
	"newsletter_stats":                 e(news, Read),
	"newsletter_templates":             e(news, Read),
	"newsletter_template_read":         e("newsletter: covered by its unit tests only", NotCovered),
	"newsletter_bounces":               e(news, Read),
	"newsletter_campaign_draft_create": e(news, Accepted),
	"newsletter_campaign_draft_update": e(news, Accepted),
	"newsletter_campaign_send":         e("newsletter: scheduled and unscheduled; send now declined", Accepted, Declined),
	"newsletter_campaign_stop":         e(news, Accepted),
	"newsletter_campaign_delete":       e(news, Accepted),
	"newsletter_test_send":             e("newsletter: to B (Mailpit in dev)", Accepted),
	"newsletter_tx_send":               e("newsletter: to B (Mailpit in dev)", Accepted),
	"newsletter_list_create":           e(news, Accepted),
	"newsletter_list_update":           e(news, Accepted),
	"newsletter_list_delete":           e(news, Accepted),
	"newsletter_subscriber_add":        e("newsletter: B to the [smoke] list; an outside address declined", Accepted, Declined),
	"newsletter_subscriber_update":     e(news, Accepted),
	"newsletter_subscription_change":   e(news, Accepted),
	"newsletter_subscriber_blocklist":  e(news, Accepted),
	"newsletter_subscriber_delete":     e(news, Accepted),
	"newsletter_template_create":       e("newsletter: the permanent [smoke] tx template, once", Accepted),
	"newsletter_template_update":       e(news, Accepted),
	"newsletter_bounce_delete":         e("newsletter: no bounces to delete in dev", NotCovered),

	"aud_check":    e("experiment, off in production", NotCovered),
	"confirm_test": e("experiment, off in production", NotCovered),
}

// ByRun: tools the run itself calls outside the steps (the account check
// and the sweep), and where.
var ByRun = map[string]string{
	"whoami":     "the account check before anything else",
	"mail_trash": "the sweep at the start and the clean-up at the end",
}
