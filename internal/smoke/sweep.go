package smoke

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// sweep: every "[smoke]" item of both accounts (and the shared mailbox)
// removed, any run's: at the start for a failed run's leftovers, at the
// end for this run's. Out-of-office is switched off; the permanent mail
// folder gets its name back. Each action is a row in the results.
func (r *Runner) sweep(ctx context.Context, label string) {
	allow := []string{r.cfg.Outside}
	sw := func(who, tool string, args map[string]any, what string, e Expect) {
		if !r.offer[tool] {
			return
		}
		e.Allow = append(e.Allow, allow...)
		start := time.Now()
		o, err := r.call(ctx, r.p[who], tool, args, e)
		ms := time.Since(start).Milliseconds()
		switch {
		case err != nil:
			r.add(label+": "+what, tool, "failed", ms, err.Error()+tail(o.err))
		case o.err != "":
			r.add(label+": "+what, tool, "failed", ms, short(o.err))
		default:
			r.add(label+": "+what, tool, "ok", ms, "")
		}
	}
	read := func(who, tool string, args map[string]any) map[string]any {
		if !r.offer[tool] {
			return nil
		}
		o, err := r.call(ctx, r.p[who], tool, args, Expect{})
		if err != nil || o.err != "" {
			r.add(label+": look", tool, "failed", 0, fmt.Sprint(err)+tail(o.err))
			return nil
		}
		return o.out
	}
	ifAsk := Expect{Dialog: AcceptIfAsked}

	for _, who := range []string{"A", "B"} {
		p := r.p[who]
		// Out of office.
		if o := read(who, "out_of_office_get", nil); o != nil && get(o, "on") == true &&
			(strings.Contains(gstr(o, "subject"), tag) || strings.Contains(gstr(o, "text"), "smoke test")) {
			sw(who, "out_of_office_set", map[string]any{"on": false}, p.user+": out of office off", ifAsk)
		}
		// Mail: own and (A) the shared mailbox; not what's already in Trash.
		accounts := []string{""}
		if who == "A" && r.cfg.Shared != "" {
			accounts = append(accounts, r.cfg.Shared)
		}
		for _, acct := range accounts {
			args := map[string]any{}
			if acct != "" {
				args["account"] = acct
			}
			fl := read(who, "mailbox_list", args)
			trash := map[string]bool{}
			for _, f := range glist(fl, "folders") {
				if gstr(f, "role") == "trash" {
					trash[gstr(f, "name")] = true
				}
				// The permanent folder, if a run stopped while it was renamed.
				if n := gstr(f, "name"); strings.HasPrefix(n, tag+" folder ") && acct == "" && !hasName(glist(fl, "folders"), tag+" folder") {
					sw(who, "mail_folder_rename", map[string]any{"folder": n, "name": tag + " folder"}, p.user+": folder name back", ifAsk)
				}
			}
			q := map[string]any{"query": "smoke", "limit": 50}
			if acct != "" {
				q["account"] = acct
			}
			var ids []string
			for _, m := range glist(read(who, "mail_search", q), "messages") {
				if strings.Contains(gstr(m, "subject"), tag) && !trash[gstr(m, "folder")] {
					ids = append(ids, gstr(m, "id"))
				}
			}
			for len(ids) > 0 {
				n := min(25, len(ids))
				a := map[string]any{"ids": ids[:n]}
				if acct != "" {
					a["account"] = acct
				}
				sw(who, "mail_trash", a, fmt.Sprintf("%s: %d mail(s) to Trash", strings.TrimPrefix(p.user+" "+acct, " "), n), Expect{Dialog: Accept})
				ids = ids[n:]
			}
		}
		// Events from yesterday to six weeks ahead.
		for _, ev := range glist(read(who, "event_list", map[string]any{"from": r.day(-1), "to": r.day(42)}), "events") {
			if strings.Contains(gstr(ev, "title"), tag) {
				a := map[string]any{"id": gstr(ev, "id")}
				sw(who, "event_cancel", a, p.user+": event "+gstr(ev, "title"), ifAsk)
			}
		}
		// Contacts and groups (contact_delete takes both).
		for _, g := range glist(read(who, "contact_groups", nil), "groups") {
			if strings.HasPrefix(gstr(g, "name"), tag) {
				sw(who, "contact_delete", map[string]any{"id": gstr(g, "id")}, p.user+": group "+gstr(g, "name"), Expect{Dialog: Accept})
			}
		}
		for _, c := range glist(read(who, "contact_search", map[string]any{"query": "smoke", "limit": 50}), "contacts") {
			if strings.HasPrefix(gstr(c, "name"), tag) && gstr(c, "id") != "" {
				emails := glist2(c, "emails")
				sw(who, "contact_delete", map[string]any{"id": gstr(c, "id")}, p.user+": contact "+gstr(c, "name"), Expect{Dialog: Accept, Allow: emails})
			}
		}
		// Shares, then the files themselves (top-level [smoke] items).
		for _, s := range glist(read(who, "file_shares", nil), "shared_by_me") {
			if !strings.HasPrefix(gstr(s, "name"), tag) {
				continue
			}
			for _, w := range glist(s, "shared_with") {
				with := gstr(w, "who")
				if get(w, "link") == true {
					with = "link"
				}
				sw(who, "file_unshare", map[string]any{"path": "/" + gstr(s, "name"), "with": with}, p.user+": unshare "+gstr(s, "name"), ifAsk)
			}
		}
		var gone []string
		for _, f := range glist(read(who, "file_search", map[string]any{"query": "smoke", "limit": 50}), "files") {
			path := gstr(f, "path")
			if !strings.HasPrefix(gstr(f, "name"), tag) || slices.ContainsFunc(gone, func(g string) bool { return strings.HasPrefix(path, g+"/") }) {
				continue
			}
			sw(who, "file_trash", map[string]any{"path": path}, p.user+": file "+gstr(f, "name"), ifAsk)
			gone = append(gone, path)
		}
	}
}

func hasName(items []map[string]any, name string) bool {
	for _, it := range items {
		if gstr(it, "name") == name {
			return true
		}
	}
	return false
}
