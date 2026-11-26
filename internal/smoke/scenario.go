package smoke

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Step: one tool call in the scenario.
type Step struct {
	Name  string
	Who   string // "A" or "B"
	Tool  string
	Write bool     // changes something (not run with ReadOnly)
	Needs []string // values earlier steps found; missing: skipped
	Args  func(r *Runner) map[string]any
	// Expect: the step's confirmation (nil: none may be asked).
	Expect func(r *Runner) Expect
	// WantErr: the call must fail with this (a declined question).
	WantErr string
	// OKErr: errors that count as success (e.g. "already").
	OKErr []string
	// Check: whether the answer is right; may record values.
	Check func(r *Runner, o callOut) error
	// Poll: repeat until Check passes (delivery, indexing), at most this.
	Poll time.Duration
	// Skip: a reason to skip (e.g. the permanent folder exists).
	Skip func(r *Runner) string
}

const tag = "[smoke]"

// errMissing: what Check returns while a polled item hasn't arrived.
var errMissing = errors.New("not there (yet)")

func accept(must ...string) func(*Runner) Expect {
	return func(*Runner) Expect { return Expect{Dialog: Accept, Must: must} }
}

// mustAsk: the tool always asks here (seen in the live runs), and the
// question is accepted if it passes every check; no question fails the step.
func mustAsk(must ...string) func(*Runner) Expect {
	return func(*Runner) Expect { return Expect{Dialog: Accept, Must: must} }
}

func ifAsked(must ...string) func(*Runner) Expect {
	return func(*Runner) Expect { return Expect{Dialog: AcceptIfAsked, Must: must} }
}

func (r *Runner) v(k string) string { return r.vars[k] }

func (r *Runner) day(n int) string { return r.cfg.Now().AddDate(0, 0, n).Format("2006-01-02") }

func set(k, field string) func(*Runner, callOut) error {
	return func(r *Runner, o callOut) error {
		s := gstr(o.out, field)
		if s == "" {
			return fmt.Errorf("no %s in the answer", field)
		}
		r.vars[k] = s
		return nil
	}
}

// findMail: a message in a search answer by subject (not in Trash).
func findMail(k, subject string) func(*Runner, callOut) error {
	return func(r *Runner, o callOut) error {
		for _, m := range glist(o.out, "messages") {
			if strings.Contains(gstr(m, "subject"), subject) && !strings.Contains(strings.ToLower(gstr(m, "folder")), "trash") {
				if k != "" {
					r.vars[k] = gstr(m, "id")
				}
				return nil
			}
		}
		return errMissing
	}
}

func contains(field string, subs ...string) func(*Runner, callOut) error {
	return func(r *Runner, o callOut) error {
		var s string
		if field == "" {
			s = fmt.Sprint(o.out)
		} else {
			s = fmt.Sprint(get(o.out, field))
		}
		for _, x := range subs {
			if !strings.Contains(s, x) {
				return fmt.Errorf("%s doesn't contain %q", field, x)
			}
		}
		return nil
	}
}

// Steps: the scenario, in order. Run is the run's id (in every name and
// subject); people's addresses come from whoami.
// readBlock: how many steps lead the scenario before its first change.
// Read-only runs only those: every later read looks for what the run itself
// made, and would fail or be skipped without it.
func readBlock(steps []Step) int {
	for i, s := range steps {
		if s.Write {
			return i
		}
	}
	return len(steps)
}

func Steps() []Step {
	A, B := "A", "B"
	return []Step{
		// ---- reading
		{Name: "my sessions", Who: A, Tool: "my_sessions"},
		{Name: "mailboxes", Who: A, Tool: "mailbox_list", Check: func(r *Runner, o callOut) error {
			if len(glist(o.out, "folders")) == 0 {
				return errors.New("no folders")
			}
			for _, a := range glist(o.out, "accounts") {
				if strings.Contains(strings.ToLower(fmt.Sprint(a)), strings.ToLower(strings.Split(r.people.Shared, "@")[0])) {
					return nil
				}
			}
			return fmt.Errorf("the shared mailbox %s isn't among the accounts", r.people.Shared)
		}},
		{Name: "shared mailbox folders", Who: A, Tool: "mailbox_list", Args: func(r *Runner) map[string]any { return map[string]any{"account": r.people.Shared} }},
		{Name: "mailbox size", Who: A, Tool: "mailbox_size"},
		{Name: "identities", Who: A, Tool: "identity_list", Check: contains("identities", "")},
		{Name: "shared identities", Who: A, Tool: "identity_list", Args: func(r *Runner) map[string]any { return map[string]any{"account": r.people.Shared} }},
		{Name: "calendars", Who: A, Tool: "calendar_list"},
		{Name: "day overview", Who: A, Tool: "day_overview"},
		{Name: "out of office", Who: A, Tool: "out_of_office_get"},
		{Name: "contact groups", Who: A, Tool: "contact_groups"},
		{Name: "contacts", Who: A, Tool: "contact_search", Args: func(*Runner) map[string]any { return map[string]any{"query": "smoke"} }},
		{Name: "spaces", Who: A, Tool: "folder_list"},
		{Name: "spaces (B first touches files)", Who: B, Tool: "folder_list"},
		{Name: "shares", Who: A, Tool: "file_shares"},
		{Name: "file trash", Who: A, Tool: "file_trash_list"},
		{Name: "events", Who: A, Tool: "event_list"},
		{Name: "mail search", Who: A, Tool: "mail_search", Args: func(*Runner) map[string]any { return map[string]any{"limit": 5} }},

		// ---- mail between the two
		{Name: "draft A→B", Who: A, Tool: "mail_draft_create", Write: true, Args: func(r *Runner) map[string]any {
			return map[string]any{"to": []string{r.people.B}, "subject": tag + " hello " + r.run, "body": "Smoke test " + r.run + ", line one."}
		}, Check: set("a_draft", "id")},
		{Name: "update draft", Who: A, Tool: "mail_draft_update", Write: true, Needs: []string{"a_draft"}, Args: func(r *Runner) map[string]any {
			return map[string]any{"id": r.v("a_draft"), "body": "Smoke test " + r.run + ", updated."}
		}, Check: set("a_draft", "id")}, // an updated draft is a new message with a new id
		{Name: "send A→B", Who: A, Tool: "mail_send", Write: true, Needs: []string{"a_draft"}, Args: func(r *Runner) map[string]any { return map[string]any{"id": r.v("a_draft")} },
			Expect: func(r *Runner) Expect { return Expect{Dialog: Accept, Must: []string{r.run, r.people.B}} }},
		{Name: "B finds it", Who: B, Tool: "mail_search", Poll: 90 * time.Second, Args: func(r *Runner) map[string]any { return map[string]any{"query": r.run} },
			Check: func(r *Runner, o callOut) error { return findMail("b_msg", tag+" hello "+r.run)(r, o) }},
		{Name: "B reads it", Who: B, Tool: "mail_read", Needs: []string{"b_msg"}, Args: func(r *Runner) map[string]any { return map[string]any{"id": r.v("b_msg")} },
			Check: contains("text", "updated")},
		{Name: "B reads the thread", Who: B, Tool: "mail_thread_read", Needs: []string{"b_msg"}, Args: func(r *Runner) map[string]any { return map[string]any{"id": r.v("b_msg")} }},
		{Name: "B flags it", Who: B, Tool: "mail_flag", Write: true, Needs: []string{"b_msg"}, Expect: ifAsked(tag), Args: func(r *Runner) map[string]any {
			return map[string]any{"ids": []string{r.v("b_msg")}, "read": true, "starred": true, "add_labels": []string{"smoke"}}
		}},
		{Name: "B finds it by label", Who: B, Tool: "mail_search", Needs: []string{"b_msg"}, Poll: 30 * time.Second, Args: func(*Runner) map[string]any { return map[string]any{"label": "smoke"} },
			Check: func(r *Runner, o callOut) error { return findMail("", tag+" hello "+r.run)(r, o) }},
		{Name: "B unflags it", Who: B, Tool: "mail_flag", Write: true, Needs: []string{"b_msg"}, Expect: ifAsked(), Args: func(r *Runner) map[string]any {
			return map[string]any{"ids": []string{r.v("b_msg")}, "starred": false, "remove_labels": []string{"smoke"}}
		}},
		{Name: "B drafts a reply", Who: B, Tool: "mail_draft_create", Write: true, Needs: []string{"b_msg"}, Args: func(r *Runner) map[string]any {
			return map[string]any{"reply_to": r.v("b_msg"), "body": "Reply " + r.run}
		}, Check: set("b_reply", "id")},
		{Name: "B replies", Who: B, Tool: "mail_send", Write: true, Needs: []string{"b_reply"}, Args: func(r *Runner) map[string]any { return map[string]any{"id": r.v("b_reply")} },
			Expect: func(r *Runner) Expect { return Expect{Dialog: Accept, Must: []string{r.run, r.people.A}} }},
		{Name: "B drafts a forward", Who: B, Tool: "mail_draft_create", Write: true, Needs: []string{"b_msg"}, Args: func(r *Runner) map[string]any {
			return map[string]any{"forward_of": r.v("b_msg"), "to": []string{r.people.A}, "body": "Forward " + r.run}
		}, Check: set("b_fwd", "id")},
		{Name: "B forwards", Who: B, Tool: "mail_send", Write: true, Needs: []string{"b_fwd"}, Args: func(r *Runner) map[string]any { return map[string]any{"id": r.v("b_fwd")} },
			Expect: func(r *Runner) Expect { return Expect{Dialog: Accept, Must: []string{r.run, r.people.A}} }},
		{Name: "A gets the reply", Who: A, Tool: "mail_search", Poll: 90 * time.Second, Args: func(r *Runner) map[string]any { return map[string]any{"query": r.run} },
			Check: func(r *Runner, o callOut) error {
				re, fw := false, false
				for _, m := range glist(o.out, "messages") {
					s := strings.ToLower(gstr(m, "subject"))
					re = re || strings.HasPrefix(s, "re:")
					fw = fw || strings.HasPrefix(s, "fwd:") || strings.HasPrefix(s, "fw:")
				}
				if !re || !fw {
					return errMissing
				}
				return nil
			}},

		// ---- the permanent mail folder
		{Name: "B's folders", Who: B, Tool: "mailbox_list", Check: func(r *Runner, o callOut) error {
			if find(glist(o.out, "folders"), "name", tag+" folder") != nil {
				for _, f := range glist(o.out, "folders") {
					if gstr(f, "name") == tag+" folder" {
						r.vars["b_folder_exists"] = "yes"
					}
				}
			}
			return nil
		}},
		{Name: "B creates the [smoke] folder", Who: B, Tool: "mail_folder_create", Write: true, Expect: ifAsked(tag),
			Skip: func(r *Runner) string {
				if r.v("b_folder_exists") != "" {
					return "exists from an earlier run (no tool deletes folders)"
				}
				return ""
			},
			Args: func(*Runner) map[string]any { return map[string]any{"name": tag + " folder"} }},
		{Name: "B renames the folder", Who: B, Tool: "mail_folder_rename", Write: true, Expect: ifAsked(tag), Args: func(r *Runner) map[string]any {
			return map[string]any{"folder": tag + " folder", "name": tag + " folder " + r.run}
		}, Check: func(r *Runner, _ callOut) error { r.vars["b_renamed"] = "yes"; return nil }},
		{Name: "B moves the mail there", Who: B, Tool: "mail_move", Write: true, Needs: []string{"b_msg", "b_renamed"}, Expect: ifAsked(), Args: func(r *Runner) map[string]any {
			return map[string]any{"ids": []string{r.v("b_msg")}, "folder": tag + " folder " + r.run}
		}},
		{Name: "B renames it back", Who: B, Tool: "mail_folder_rename", Write: true, Needs: []string{"b_renamed"}, Expect: ifAsked(tag), Args: func(r *Runner) map[string]any {
			return map[string]any{"folder": tag + " folder " + r.run, "name": tag + " folder"}
		}},

		// ---- files (A's personal space)
		{Name: "folder", Who: A, Tool: "folder_create", Write: true, Expect: ifAsked(), Args: func(r *Runner) map[string]any { return map[string]any{"name": tag + " " + r.run} },
			Check: set("a_dir", "path")},
		{Name: "save .md", Who: A, Tool: "file_save", Write: true, Needs: []string{"a_dir"}, Expect: ifAsked(), Args: func(r *Runner) map[string]any {
			return map[string]any{"folder": r.v("a_dir"), "name": "note " + r.run + ".md", "content": "line one\nzq" + r.run + " is the word\n"}
		}, Check: set("a_note", "path")},
		{Name: "read .md", Who: A, Tool: "file_read", Needs: []string{"a_note"}, Args: func(r *Runner) map[string]any { return map[string]any{"path": r.v("a_note")} },
			Check: contains("text", "line one")},
		{Name: "edit .md", Who: A, Tool: "file_edit", Write: true, Needs: []string{"a_note"}, Expect: mustAsk(), Args: func(r *Runner) map[string]any {
			return map[string]any{"path": r.v("a_note"), "edits": []map[string]any{{"old_text": "line one", "new_text": "line 1"}}}
		}},
		{Name: "versions", Who: A, Tool: "file_versions", Needs: []string{"a_note"}, Poll: 20 * time.Second, Args: func(r *Runner) map[string]any { return map[string]any{"path": r.v("a_note")} },
			Check: func(r *Runner, o callOut) error {
				vs := glist(o.out, "versions")
				if len(vs) == 0 {
					return errMissing
				}
				r.vars["a_ver"] = gstr(vs[0], "id")
				return nil
			}},
		{Name: "restore a version", Who: A, Tool: "file_version_restore", Write: true, Needs: []string{"a_note", "a_ver"}, Expect: mustAsk(), Args: func(r *Runner) map[string]any {
			return map[string]any{"path": r.v("a_note"), "version": r.v("a_ver")}
		}},
		{Name: "read the restored", Who: A, Tool: "file_read", Needs: []string{"a_note", "a_ver"}, Args: func(r *Runner) map[string]any { return map[string]any{"path": r.v("a_note")} },
			Check: contains("text", "line one")},
		{Name: "copy", Who: A, Tool: "file_copy", Write: true, Needs: []string{"a_note"}, Expect: ifAsked(), Args: func(r *Runner) map[string]any {
			return map[string]any{"path": r.v("a_note"), "new_name": "copy " + r.run + ".md"}
		}, Check: set("a_copy", "path")},
		{Name: "rename", Who: A, Tool: "file_move", Write: true, Needs: []string{"a_copy"}, Expect: ifAsked(), Args: func(r *Runner) map[string]any {
			return map[string]any{"path": r.v("a_copy"), "new_name": "moved " + r.run + ".md"}
		}, Check: set("a_moved", "path")},
		{Name: "search by name", Who: A, Tool: "file_search", Poll: 90 * time.Second, Args: func(r *Runner) map[string]any { return map[string]any{"query": "moved " + r.run} },
			Check: func(r *Runner, o callOut) error {
				if find(glist(o.out, "files"), "name", "moved "+r.run) == nil {
					return errMissing
				}
				return nil
			}},
		{Name: "search by content", Who: A, Tool: "file_search", Poll: 120 * time.Second, Args: func(r *Runner) map[string]any { return map[string]any{"content": "zq" + r.run} },
			Check: func(r *Runner, o callOut) error {
				if find(glist(o.out, "files"), "name", "note "+r.run) == nil {
					return errMissing
				}
				return nil
			}},
		{Name: "save .docx ending in a table", Who: A, Tool: "file_save", Write: true, Needs: []string{"a_dir"}, Expect: ifAsked(), Args: func(r *Runner) map[string]any {
			return map[string]any{"folder": r.v("a_dir"), "name": "doc " + r.run + ".docx",
				"content": "# Smoke " + r.run + "\n\nSome **bold** text.\n\n| item | n |\n|---|---|\n| a | 1 |\n"}
		}, Check: set("a_docx", "path")},
		{Name: "read .docx", Who: A, Tool: "file_read", Needs: []string{"a_docx"}, Args: func(r *Runner) map[string]any { return map[string]any{"path": r.v("a_docx")} },
			Check: contains("text", "bold", "item")},
		{Name: "edit .docx", Who: A, Tool: "file_office_edit", Write: true, Needs: []string{"a_docx"}, Expect: mustAsk(), Args: func(r *Runner) map[string]any {
			return map[string]any{"path": r.v("a_docx"), "append": "## Added " + r.run + "\n\nMore text.",
				"replace": []map[string]any{{"old": "Some ", "new": "Changed "}}}
		}},
		{Name: "read the edited .docx", Who: A, Tool: "file_read", Needs: []string{"a_docx"}, Args: func(r *Runner) map[string]any { return map[string]any{"path": r.v("a_docx")} },
			Check: func(r *Runner, o callOut) error {
				return contains("text", "Changed", "Added "+r.run, "More text.")(r, o)
			}},
		{Name: "save .xlsx", Who: A, Tool: "file_save", Write: true, Needs: []string{"a_dir"}, Expect: ifAsked(), Args: func(r *Runner) map[string]any {
			return map[string]any{"folder": r.v("a_dir"), "name": "sheet " + r.run + ".xlsx",
				"sheets": []map[string]any{{"name": "S", "rows": [][]any{{"item", "n"}, {"a", 1}, {"b", 2}}}}}
		}, Check: set("a_xlsx", "path")},
		{Name: "edit .xlsx", Who: A, Tool: "file_office_edit", Write: true, Needs: []string{"a_xlsx"}, Expect: mustAsk(), Args: func(r *Runner) map[string]any {
			return map[string]any{"path": r.v("a_xlsx"), "changes": []map[string]any{
				{"sheet": "S", "append_rows": [][]any{{"zz", 3}}}, {"sheet": "S", "set_cells": map[string]any{"B2": 10}}}}
		}},
		{Name: "read the edited .xlsx", Who: A, Tool: "file_read", Needs: []string{"a_xlsx"}, Args: func(r *Runner) map[string]any { return map[string]any{"path": r.v("a_xlsx")} },
			Check: contains("text", "zz", "10")},
		{Name: "trash a file", Who: A, Tool: "file_trash", Write: true, Needs: []string{"a_moved"}, Expect: accept(), Args: func(r *Runner) map[string]any { return map[string]any{"path": r.v("a_moved")} }},
		{Name: "find it in the trash", Who: A, Tool: "file_trash_list", Needs: []string{"a_moved"}, Poll: 20 * time.Second, Check: func(r *Runner, o callOut) error {
			it := find(glist(o.out, "items"), "name", "moved "+r.run)
			if it == nil {
				return errMissing
			}
			r.vars["a_trash_id"] = gstr(it, "id")
			return nil
		}},
		{Name: "restore it", Who: A, Tool: "file_restore", Write: true, Needs: []string{"a_trash_id"}, Expect: ifAsked(), Args: func(r *Runner) map[string]any { return map[string]any{"id": r.v("a_trash_id")} }},

		// ---- sharing between the two
		{Name: "share with B", Who: A, Tool: "file_share", Write: true, Needs: []string{"a_dir"}, Expect: accept(), Args: func(r *Runner) map[string]any {
			return map[string]any{"path": r.v("a_dir"), "with": r.p["B"].user, "access": "view"}
		}},
		{Name: "A's shares", Who: A, Tool: "file_shares", Needs: []string{"a_dir"}, Poll: 20 * time.Second, Check: func(r *Runner, o callOut) error {
			it := find(glist(o.out, "shared_by_me"), "name", r.run)
			if it == nil {
				return errMissing
			}
			for _, w := range glist(it, "shared_with") {
				if get(w, "link") != true {
					r.vars["a_share_who"] = gstr(w, "who")
				}
			}
			if r.v("a_share_who") == "" {
				return errors.New("shared, but with nobody")
			}
			return nil
		}},
		{Name: "B sees the share", Who: B, Tool: "file_shares", Needs: []string{"a_share_who"}, Poll: 30 * time.Second, Check: func(r *Runner, o callOut) error {
			if find(glist(o.out, "shared_with_me"), "name", r.run) == nil {
				return errMissing
			}
			return nil
		}},
		{Name: "unshare", Who: A, Tool: "file_unshare", Write: true, Needs: []string{"a_dir", "a_share_who"}, Expect: accept(), Args: func(r *Runner) map[string]any {
			return map[string]any{"path": r.v("a_dir"), "with": r.v("a_share_who")}
		}},

		// ---- an attachment
		{Name: "draft with a file", Who: A, Tool: "mail_draft_create", Write: true, Needs: []string{"a_note"}, Args: func(r *Runner) map[string]any {
			return map[string]any{"to": []string{r.people.B}, "subject": tag + " attachment " + r.run, "body": "Attached.", "attach_files": []string{r.v("a_note")}}
		}, Check: set("a_att_draft", "id")},
		{Name: "send it", Who: A, Tool: "mail_send", Write: true, Needs: []string{"a_att_draft"}, Args: func(r *Runner) map[string]any { return map[string]any{"id": r.v("a_att_draft")} },
			Expect: func(r *Runner) Expect { return Expect{Dialog: Accept, Must: []string{r.run, "attachment"}} }},
		{Name: "B finds the attachment mail", Who: B, Tool: "mail_search", Needs: []string{"a_att_draft"}, Poll: 90 * time.Second, Args: func(r *Runner) map[string]any { return map[string]any{"query": r.run} },
			Check: func(r *Runner, o callOut) error { return findMail("b_att", tag+" attachment "+r.run)(r, o) }},
		{Name: "B reads it", Who: B, Tool: "mail_read", Needs: []string{"b_att"}, Args: func(r *Runner) map[string]any { return map[string]any{"id": r.v("b_att")} },
			Check: func(r *Runner, o callOut) error {
				as := glist(o.out, "attachments")
				if len(as) == 0 {
					return errors.New("no attachment")
				}
				r.vars["b_att_idx"] = fmt.Sprint(get(as[0], "index"))
				return nil
			}},
		{Name: "B reads the attachment", Who: B, Tool: "mail_attachment_read", Needs: []string{"b_att", "b_att_idx"}, Args: func(r *Runner) map[string]any {
			return map[string]any{"id": r.v("b_att"), "index": atoi(r.v("b_att_idx"))}
		}},
		{Name: "B saves the attachment", Who: B, Tool: "mail_attachment_save", Write: true, Needs: []string{"b_att", "b_att_idx"}, Expect: ifAsked(), Args: func(r *Runner) map[string]any {
			return map[string]any{"id": r.v("b_att"), "index": atoi(r.v("b_att_idx")), "name": tag + " saved " + r.run + ".md"}
		}, Check: set("b_saved", "path")},

		// ---- the shared mailbox
		{Name: "draft as the shared mailbox", Who: A, Tool: "mail_draft_create", Write: true, Args: func(r *Runner) map[string]any {
			return map[string]any{"account": r.people.Shared, "to": []string{r.people.B}, "subject": tag + " as shared " + r.run, "body": "From the team."}
		}, Check: set("a_sh_draft", "id")},
		{Name: "send AS the shared mailbox", Who: A, Tool: "mail_send", Write: true, Needs: []string{"a_sh_draft"}, Args: func(r *Runner) map[string]any {
			return map[string]any{"account": r.people.Shared, "id": r.v("a_sh_draft")}
		}, Expect: func(r *Runner) Expect {
			return Expect{Dialog: Accept, Must: []string{r.run, "Send AS " + r.people.Shared}, From: r.people.Shared}
		}},
		{Name: "B gets it", Who: B, Tool: "mail_search", Needs: []string{"a_sh_draft"}, Poll: 90 * time.Second, Args: func(r *Runner) map[string]any { return map[string]any{"query": r.run} },
			Check: func(r *Runner, o callOut) error { return findMail("", tag+" as shared "+r.run)(r, o) }},
		{Name: "search the shared mailbox", Who: A, Tool: "mail_search", Args: func(r *Runner) map[string]any { return map[string]any{"account": r.people.Shared, "limit": 5} }},

		// ---- outside: checked, then declined
		{Name: "draft to outside", Who: A, Tool: "mail_draft_create", Write: true, Args: func(r *Runner) map[string]any {
			return map[string]any{"to": []string{r.cfg.Outside}, "subject": tag + " outside " + r.run, "body": "Never sent."}
		}, Check: set("a_out_draft", "id")},
		{Name: "send to outside: declined", Who: A, Tool: "mail_send", Write: true, Needs: []string{"a_out_draft"}, WantErr: "declined",
			Args: func(r *Runner) map[string]any { return map[string]any{"id": r.v("a_out_draft")} },
			Expect: func(r *Runner) Expect {
				return Expect{Dialog: Decline, Must: []string{"OUTSIDE", r.cfg.Outside, "From: " + r.people.A, r.run}}
			}},
		{Name: "invite outside: declined", Who: A, Tool: "event_invite", Write: true, WantErr: "declined", Args: func(r *Runner) map[string]any {
			return map[string]any{"title": tag + " outside " + r.run, "start": r.day(3) + "T09:00", "attendees": []string{r.cfg.Outside}}
		}, Expect: func(r *Runner) Expect {
			return Expect{Dialog: Decline, Must: []string{"OUTSIDE", r.cfg.Outside, r.run}}
		}},

		// ---- calendar
		{Name: "private event", Who: B, Tool: "event_create", Write: true, Expect: ifAsked(), Args: func(r *Runner) map[string]any {
			return map[string]any{"title": tag + " private " + r.run, "start": r.day(3) + "T10:00", "minutes": 30}
		}, Check: set("a_ev", "id")},
		{Name: "read it", Who: B, Tool: "event_read", Needs: []string{"a_ev"}, Args: func(r *Runner) map[string]any { return map[string]any{"id": r.v("a_ev")} },
			Check: func(r *Runner, o callOut) error { return contains("", "private "+r.run)(r, o) }},
		{Name: "move it", Who: B, Tool: "event_update", Write: true, Needs: []string{"a_ev"}, Expect: ifAsked(), Args: func(r *Runner) map[string]any {
			return map[string]any{"id": r.v("a_ev"), "title": tag + " private " + r.run + " moved", "minutes": 45}
		}},
		{Name: "list it", Who: B, Tool: "event_list", Needs: []string{"a_ev"}, Args: func(r *Runner) map[string]any { return map[string]any{"from": r.day(3), "to": r.day(3)} },
			Check: func(r *Runner, o callOut) error {
				if find(glist(o.out, "events"), "title", "private "+r.run+" moved") == nil {
					return errors.New("the moved event isn't listed")
				}
				return nil
			}},
		{Name: "cancel it", Who: B, Tool: "event_cancel", Write: true, Needs: []string{"a_ev"}, Expect: mustAsk(), Args: func(r *Runner) map[string]any { return map[string]any{"id": r.v("a_ev")} }},
		{Name: "invite B", Who: A, Tool: "event_invite", Write: true, Args: func(r *Runner) map[string]any {
			return map[string]any{"title": tag + " meeting " + r.run, "start": r.day(3) + "T11:00", "minutes": 30, "attendees": []string{r.people.B}}
		}, Expect: func(r *Runner) Expect { return Expect{Dialog: Accept, Must: []string{r.run, r.people.B}} }, Check: set("a_inv", "id")},
		{Name: "B's free/busy", Who: A, Tool: "availability_get", Args: func(r *Runner) map[string]any {
			return map[string]any{"people": []string{r.people.B}, "from": r.day(3), "to": r.day(3)}
		}, Check: func(r *Runner, o callOut) error {
			ps := glist(o.out, "people")
			if len(ps) != 1 || gstr(ps[0], "error") != "" {
				return fmt.Errorf("no free/busy for B: %s", gstr(get(ps, 0), "error"))
			}
			return nil
		}},
		{Name: "B gets the invitation", Who: B, Tool: "mail_search", Needs: []string{"a_inv"}, Poll: 90 * time.Second, Args: func(r *Runner) map[string]any { return map[string]any{"query": r.run} },
			Check: func(r *Runner, o callOut) error { return findMail("b_inv_mail", "meeting "+r.run)(r, o) }},
		{Name: "B imports it", Who: B, Tool: "event_import", Write: true, Needs: []string{"b_inv_mail"}, Expect: mustAsk(), OKErr: []string{"already"},
			Args: func(r *Runner) map[string]any { return map[string]any{"id": r.v("b_inv_mail")} }},
		{Name: "B lists it", Who: B, Tool: "event_list", Needs: []string{"a_inv"}, Poll: 60 * time.Second, Args: func(r *Runner) map[string]any { return map[string]any{"from": r.day(3), "to": r.day(3)} },
			Check: func(r *Runner, o callOut) error {
				ev := find(glist(o.out, "events"), "title", "meeting "+r.run)
				if ev == nil {
					return errMissing
				}
				r.vars["b_ev"] = gstr(ev, "id")
				return nil
			}},
		{Name: "B accepts", Who: B, Tool: "event_respond", Write: true, Needs: []string{"b_ev"}, Expect: mustAsk(), Args: func(r *Runner) map[string]any {
			return map[string]any{"id": r.v("b_ev"), "answer": "accept"}
		}},
		{Name: "A moves the meeting", Who: A, Tool: "event_update", Write: true, Needs: []string{"a_inv"}, Expect: mustAsk(), Args: func(r *Runner) map[string]any {
			return map[string]any{"id": r.v("a_inv"), "minutes": 45}
		}},
		{Name: "A cancels the meeting", Who: A, Tool: "event_cancel", Write: true, Needs: []string{"a_inv"}, Expect: accept(), Args: func(r *Runner) map[string]any { return map[string]any{"id": r.v("a_inv")} }},

		// ---- out of office: a window a month ahead, then off
		{Name: "out of office on", Who: B, Tool: "out_of_office_set", Write: true, Expect: func(*Runner) Expect {
			return Expect{Dialog: Accept, Must: []string{"automatic reply"}, Outward: true, Unnamed: true}
		}, Args: func(r *Runner) map[string]any {
			return map[string]any{"on": true, "from": r.day(30), "to": r.day(31), "subject": tag + " away " + r.run, "text": "Away (smoke test " + r.run + ")."}
		}},
		{Name: "read it", Who: B, Tool: "out_of_office_get", Check: func(r *Runner, o callOut) error { return contains("text", r.run)(r, o) }},
		{Name: "out of office off", Who: B, Tool: "out_of_office_set", Write: true, Expect: func(*Runner) Expect { return Expect{Dialog: AcceptIfAsked, Unnamed: true} }, Args: func(*Runner) map[string]any { return map[string]any{"on": false} }},

		// ---- contacts
		{Name: "contact", Who: A, Tool: "contact_create", Write: true, Expect: ifAsked(), Args: func(r *Runner) map[string]any {
			return map[string]any{"name": tag + " Contact " + r.run, "emails": []string{r.contactAddr()}, "company": "Smoke"}
		}, Check: set("a_ct", "id")},
		{Name: "find it", Who: A, Tool: "contact_search", Needs: []string{"a_ct"}, Poll: 20 * time.Second, Args: func(r *Runner) map[string]any { return map[string]any{"query": "Contact " + r.run} },
			Check: func(r *Runner, o callOut) error {
				if find(glist(o.out, "contacts"), "id", r.v("a_ct")) == nil {
					return errMissing
				}
				return nil
			}},
		{Name: "update it", Who: A, Tool: "contact_update", Write: true, Needs: []string{"a_ct"}, Expect: ifAsked(), Args: func(r *Runner) map[string]any {
			return map[string]any{"id": r.v("a_ct"), "title": "Tester"}
		}},
		{Name: "group", Who: A, Tool: "contact_group_create", Write: true, Needs: []string{"a_ct"}, Expect: ifAsked(), Args: func(r *Runner) map[string]any {
			return map[string]any{"name": tag + " group " + r.run, "members": []string{r.v("a_ct")}}
		}, Check: func(r *Runner, o callOut) error {
			if id := gstr(o.out, "group", "id"); id != "" {
				r.vars["a_grp"] = id
				return nil
			}
			return errors.New("no group id")
		}},
		{Name: "rename the group, take the contact out", Who: A, Tool: "contact_group_update", Write: true, Needs: []string{"a_grp", "a_ct"}, Expect: ifAsked(), Args: func(r *Runner) map[string]any {
			return map[string]any{"id": r.v("a_grp"), "name": tag + " group " + r.run + " 2", "remove": []string{r.v("a_ct")}}
		}},
		{Name: "groups", Who: A, Tool: "contact_groups", Needs: []string{"a_grp"}, Check: func(r *Runner, o callOut) error {
			if find(glist(o.out, "groups"), "name", "group "+r.run+" 2") == nil {
				return errors.New("the renamed group isn't listed")
			}
			return nil
		}},
		{Name: "delete the group", Who: A, Tool: "contact_delete", Write: true, Needs: []string{"a_grp"}, Expect: accept(), Args: func(r *Runner) map[string]any { return map[string]any{"id": r.v("a_grp")} }},
		{Name: "delete the contact", Who: A, Tool: "contact_delete", Write: true, Needs: []string{"a_ct"}, Args: func(r *Runner) map[string]any { return map[string]any{"id": r.v("a_ct")} },
			Expect: func(r *Runner) Expect {
				return Expect{Dialog: Accept, Must: []string{r.run}, Allow: []string{r.contactAddr()}}
			}},

		// ---- last: logging out everywhere, declined
		{Name: "log out everywhere: declined", Who: A, Tool: "logout_everywhere", Write: true, WantErr: "declined",
			Expect: func(r *Runner) Expect { return Expect{Dialog: Decline, Must: []string{"EVERYWHERE"}} }},
	}
}

func (r *Runner) contactAddr() string { return "contact-" + r.run + "@example.invalid" }

func atoi(s string) int {
	n := 0
	fmt.Sscan(s, &n)
	return n
}

func newRunID(now time.Time) string {
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return "smk" + now.Format("0102t1504") + hex.EncodeToString(b)
}

// ---- running

// Run: the guard, the sweep, the steps, the clean-up. The error is for
// what stops a run (no login, the wrong accounts); failed steps are in
// Results.
func Run(ctx context.Context, cfg Config) (*Runner, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Outside == "" {
		cfg.Outside = "smoke-outside@example.com"
	}
	r := &Runner{cfg: cfg, p: map[string]*person{}, vars: map[string]string{}, run: newRunID(cfg.Now())}
	sweep := !cfg.ReadOnly
	if len(cfg.Only) > 0 {
		sweep = false // only what was asked for; a full run sweeps
		for _, s := range Steps() {
			sweep = sweep || (s.Write && slices.Contains(cfg.Only, s.Tool))
		}
	}
	for i, k := range []string{"A", "B"} {
		a := cfg.Accounts[i]
		if err := a.Login.Load(); err != nil {
			return r, err
		}
		if _, err := a.Login.Access(ctx); err != nil {
			return r, err
		}
		r.p[k] = &person{key: k, user: a.User, login: a.Login}
		if err := r.connect(ctx, r.p[k]); err != nil {
			return r, err
		}
	}
	defer func() {
		for _, p := range r.p {
			if p.cs != nil {
				p.cs.Close()
			}
		}
	}()
	if err := r.guard(ctx); err != nil {
		return r, err
	}
	cfg.logf("run %s as %s and %s", r.run, r.p["A"].user, r.p["B"].user)
	offered, err := r.offered(ctx)
	if err != nil {
		return r, err
	}
	r.offer = offered
	if sweep {
		r.sweep(ctx, "sweep (leftovers)")
	}
	steps := Steps()
	if cfg.ReadOnly {
		steps = steps[:readBlock(steps)]
	}
	for _, s := range steps {
		if len(cfg.Only) > 0 && !slices.Contains(cfg.Only, s.Tool) {
			continue
		}
		if !offered[s.Tool] {
			r.add(s.Name, s.Tool, "skipped", 0, "the gateway doesn't offer it")
			continue
		}
		r.step(ctx, s)
	}
	if sweep {
		r.sweep(ctx, "clean-up")
		r.leftovers(ctx)
	}
	return r, nil
}

func (r *Runner) add(step, tool, status string, ms int64, detail string) {
	r.Results = append(r.Results, Result{step, tool, status, ms, detail})
	r.cfg.logf("%-7s %-22s %-40s %5d ms %s", status, tool, step, ms, detail)
}

// guard: both connections are the two test accounts (never an admin, never
// the same twice), before anything is changed.
func (r *Runner) guard(ctx context.Context) error {
	for _, k := range []string{"A", "B"} {
		p := r.p[k]
		start := time.Now()
		o, err := r.call(ctx, p, "whoami", nil, Expect{})
		if err != nil || o.err != "" {
			return fmt.Errorf("whoami as %s: %v %s", p.user, err, o.err)
		}
		got := gstr(o.out, "username")
		if got != p.user {
			return fmt.Errorf("the token file for %s logs in as %q: stopping before any change", p.user, got)
		}
		for _, g := range glist2(o.out, "groups") {
			if strings.Contains(strings.ToLower(g), "admin") {
				return fmt.Errorf("%s is in %s: test accounts must not be admins", p.user, g)
			}
		}
		p.email = gstr(o.out, "email")
		if p.email == "" {
			return fmt.Errorf("%s has no email address", p.user)
		}
		r.add("who is "+k, "whoami", "ok", time.Since(start).Milliseconds(), p.user)
	}
	if r.p["A"].user == r.p["B"].user || strings.EqualFold(r.p["A"].email, r.p["B"].email) {
		return errors.New("both token files are the same account")
	}
	r.people = People{A: r.p["A"].email, B: r.p["B"].email, Shared: r.cfg.Shared, Run: r.run}
	return nil
}

func glist2(m any, k string) []string {
	l, _ := get(m, k).([]any)
	var out []string
	for _, x := range l {
		out = append(out, fmt.Sprint(x))
	}
	return out
}

// offered: the gateway's tools; any not in the matrix fails the run's
// table (a new tool without smoke coverage).
func (r *Runner) offered(ctx context.Context) (map[string]bool, error) {
	res, err := r.p["A"].cs.ListTools(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, t := range res.Tools {
		out[t.Name] = true
		if _, ok := Matrix[t.Name]; !ok {
			r.add("in the matrix", t.Name, "failed", 0, "offered by the gateway but not in the smoke matrix")
		}
	}
	return out, nil
}

// step: one step, polled if it says so.
func (r *Runner) step(ctx context.Context, s Step) {
	for _, n := range s.Needs {
		if r.vars[n] == "" {
			r.add(s.Name, s.Tool, "skipped", 0, "needs "+n+" from an earlier step")
			return
		}
	}
	if s.Skip != nil {
		if why := s.Skip(r); why != "" {
			r.add(s.Name, s.Tool, "skipped", 0, why)
			return
		}
	}
	var args map[string]any
	if s.Args != nil {
		args = s.Args(r)
	}
	exp := Expect{}
	if s.Expect != nil {
		exp = s.Expect(r)
	}
	start := time.Now()
	deadline := start.Add(s.Poll)
	for {
		o, err := r.call(ctx, r.p[s.Who], s.Tool, args, exp)
		ms := time.Since(start).Milliseconds()
		switch {
		case errors.Is(err, errLimited):
			r.add(s.Name, s.Tool, "rate-limited", ms, "the gateway's per-person limit: runs need about 20 minutes between them")
			return
		case err != nil:
			r.add(s.Name, s.Tool, "failed", ms, err.Error()+tail(o.err))
			return
		case s.WantErr != "":
			if !strings.Contains(o.err, s.WantErr) {
				r.add(s.Name, s.Tool, "failed", ms, "expected an error with "+strconvQ(s.WantErr)+", got: "+short(o.err))
				return
			}
			r.add(s.Name, s.Tool, "ok", ms, "")
			return
		case o.err != "":
			for _, ok := range s.OKErr {
				if strings.Contains(o.err, ok) {
					r.add(s.Name, s.Tool, "ok", ms, short(o.err))
					return
				}
			}
			r.add(s.Name, s.Tool, "failed", ms, short(o.err))
			return
		}
		var cerr error
		if s.Check != nil {
			cerr = s.Check(r, o)
		}
		if cerr == nil {
			r.add(s.Name, s.Tool, "ok", ms, "")
			return
		}
		if errors.Is(cerr, errMissing) && time.Now().Before(deadline) {
			select {
			case <-time.After(5 * time.Second):
				continue
			case <-ctx.Done():
				r.add(s.Name, s.Tool, "failed", ms, ctx.Err().Error())
				return
			}
		}
		r.add(s.Name, s.Tool, "failed", ms, cerr.Error())
		return
	}
}

func tail(s string) string {
	if s == "" {
		return ""
	}
	return ": " + short(s)
}

func short(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

func strconvQ(s string) string { return `"` + s + `"` }
