# The admin connection (for administrators)

Besides the everyday connection (see the [user guide](user-guide.md)), SGNL has a second, separate MCP connection for the instance's administrators: `<admin address>`, which looks like `https://mcp.example.com/admin/mcp`. It lets your AI assistant look after people, groups and the server's health.

- **Only admins:** members of the admin group (usually `admins`) as the login server has them *now*. Someone removed from it loses these tools at once, even with a connection that's still logged in.
- **Read freely, change only with your yes:** looking things up needs no question. Every change shows you a confirmation in your app and happens only if you press Accept. The answer comes from you, not from the assistant: it can't press Accept for you.
- **Everything is logged:** every call is logged with your username, and the login server's own audit (`admin_audit`) shows each change.

## Getting started

Ask whoever runs the instance for `<admin address>` and a short name, e.g. `<name>-admin`. In Claude Code:

```
claude mcp add --transport http --client-id sgnl-mcp-claude-code --callback-port 47621 <name>-admin <admin address>
```

Then `/mcp`, pick `<name>-admin`, **Authenticate**, and log in as usual. Try *"Who has an account, and in which groups?"*

Keep it separate from your everyday connection, and connect it only when you need it.

## At a glance

| The assistant can… | Does it ask you first? |
|---|---|
| List people, groups, recent logins and recent account changes | No |
| See firing alerts, the server's disk/memory/CPU, the apps, backups, certificates | No |
| See the mailboxes: shared ones and their members, people's addresses and aliases, space used; the outgoing mail queue | No (never any mail content) |
| **Add someone to a shared mailbox / remove them** | **Yes**: you see the mailbox, who's in it and what they gain or lose |
| **Set a mailbox's size limit** | **Yes**: you see the limit now, the new one and what it uses |
| **Invite a new person** | **Yes**: you see the name, their future address and where the set-up link goes |
| **Finish an invitation** | **Yes**: you see the address and the groups, with what each opens |
| **Add someone to groups / remove them** | **Yes**: you see what each group opens or what they lose |
| **Create a group** | **Yes** |
| **Email someone a set-up link** (new password and passkey) | **Yes**: you see the address it goes to |
| **Log someone out everywhere** | **Yes** |
| **Disable an account** | **Yes, and you type their username** |
| **Enable an account again** | **Yes** |
| See the file app's project spaces: members and their roles, size limit and use | No (never file contents) |
| **Create a project space** (with a manager; you stay a manager) | **Yes** |
| **Add someone to a project space you manage, change their role, remove them** | **Yes**: you see the space and what they gain or lose |
| **Set a project space's size limit** | **Yes**: you see the limit now, the new one and what it uses |
| Add or remove someone's extra addresses (aliases); delete a person, a group, a mailbox or mail; disable or delete a project space; reset passwords or passkeys, lift a lockout | **Never**: done by whoever runs the instance, on the server, or in the file app |
| Restart apps, run or restore backups, deploy | **Never**: done by whoever runs the instance |

## People and groups

### Looking things up

> "Who has an account? Anyone disabled?"
>
> "Show me Anna: groups, passkey or not, how many sessions."
>
> "Any failed logins in the last day?"
>
> "Who changed Hugo's groups, and when?"

### A new colleague

1. **Invite:** *"Invite Anna Example as anna, set-up link to anna@personal.example."* The confirmation starts with: *Invite "Anna Example" as anna@your-company, set-up link to anna@personal.example (outside)?*
   - The link goes to an address she can read **now**, usually a personal one. Her future company address is refused, because she can't read it before she can log in.
   - Another company mailbox is allowed and marked *(a COMPANY mailbox)*, e.g. when a colleague hands the link over.
   - The link is valid 72 hours. Until you finish the invitation she reaches no app, only her login settings.
   - The mailbox itself isn't made here: ask whoever runs the instance to add it.
2. Tell her on another channel (phone, in person) to expect the email.
3. **Finish:** once she has set a password and a passkey: *"Finish Anna's invitation, with the developers group."* It's refused until both are set. It then sets her company address and gives her access to the apps (`sgnl_users`) plus the groups you name.
4. **Password manager (and, where the instance shares free/busy by default, calendar free/busy):** these follow only after whoever runs the instance runs their sync. Each result reminds you.

If the link expired or got lost: *"Send Anna a new set-up link."*

### Groups

> "Add Hugo to forgejo_users."
>
> "Take Hugo out of developers."

The confirmation lists what each group opens (or what they lose). Adding someone to the admin group says **ADMIN RIGHTS** first. Removing someone from `sgnl_users` says they lose **every** app.

You can't remove *yourself* from the admin group or from `sgnl_users`, so nobody locks themselves out by accident. Ask another admin.

### Someone left, or an account looks compromised

- *"Log Hugo out everywhere."* ends every session and app token now. They can log in again with their own password and passkey (do this after a lost laptop, for example).
- *"Disable Hugo."* ends every session and stops all logins until enabled again. Nothing of theirs is deleted.
  - The confirmation says **they lose EVERY login now** (and that app passwords keep working until the full suspend) and asks you to **type their username**. Only the exact username counts.
- *"Enable Hugo again."* restores access with their groups as before.

Neither ends what other apps issued themselves: app passwords for mail apps, git tokens in Forgejo and app tokens in the file app keep working. For someone leaving, ask whoever runs the instance for the full suspend; each result reminds you.

You can't log out or disable yourself here.

## The server's health

> "Anything on fire?" *(firing alerts)*
>
> "How's the server: disk, memory, CPU?"
>
> "Which apps are running, which version, any restarts today?"
>
> "Did last night's backup work? When was the last restore drill?"
>
> "When do our certificates expire?"

- **Read-only:** these tools change nothing.
- **Through the monitoring app, as you:** you see the same as in the monitoring dashboards. Each tool runs the same few fixed checks and reads no logs.
- **Backups:**
  - A job whose last run started after its last success either failed or is still running.
  - "Never recorded" or "unknown" means *not OK until shown otherwise*.

## Mailboxes and mail delivery

> "Who's in the contact@ mailbox?"
>
> "Which addresses does Hugo have? How much space does his mailbox use?"
>
> "Did my mail to bob@example.com go out?"
>
> "Is anything stuck in the outgoing mail queue?"

- **Never mail content:** the assistant sees mailboxes, members, addresses, aliases, space used and the outgoing queue (sender, recipients, status, the receiving server's reply). It never sees what a mail says.
- **"Was it delivered?":** only mail still waiting in the queue shows up, e.g. a server that keeps refusing it and the reason. An empty answer means it was handed on: there's no delivery history. Outgoing mail goes through a relay service, so a bounce comes back later as a mail to the sender.

### Changing mailboxes

*These need SGNL's build of the mail server (see the README); on an instance without it the assistant simply doesn't offer them.*

> "Add Hugo to contact@."
>
> "Take Hugo out of alerts@."
>
> "Give the contact@ mailbox a 5 GB limit." / "No limit for Hugo's mailbox."
>
> "Create a shared mailbox sales@ called 'ACME Sales' with Anna and Ben."

- **A new shared mailbox** (where your instance has it switched on): *"Create a shared mailbox sales@ called 'ACME Sales' with Anna and Ben."* The sender name is what recipients see on mail sent as it. Refused for an address already in use (also switched-off aliases), reserved names (postmaster@, abuse@, admin…) and anyone's login name (it would block their mail). If mail to that address now lands in the catch-all mailbox, the confirmation says so. It can't be deleted or renamed here: ask whoever runs the instance.
- Each asks you first. The confirmation starts with the mailbox and the effect, e.g. *Add hugo to contact@your-company: they read and can send as contact@your-company?*
- **Members:** removing someone takes away their access to the shared mailbox and its mail; the mailbox and its mail stay, and they can be added again. The last member can't be removed: add someone else first.
- **Aliases (extra addresses) aren't changed here:** an alias also decides whose account a login lands in, so a wrong one could put someone into another person's account. Ask whoever runs the instance.
- **Size limits:** never below what the mailbox already uses. When a mailbox is full, new mail to it bounces.
- **What's live is what counts:** members are kept on the mail server itself, not in the instance's mail plan.
- Not here: making or deleting mailboxes or shared mailboxes, deleting mail, and the server's own machine accounts. Ask whoever runs the instance.

## Project spaces (file app)

> "Which project spaces are there, and who's in them?"
>
> "Create a project space 'Marketing' with Anna as manager, 50 GB."
>
> "Give the developers group view access to Marketing." / "Make Ben an editor there." / "Take Ben out of Marketing."
>
> "Give Finance 20 GB."

- **As you:** the assistant uses your own login to the file app, where admins have the admin role. Each change asks you first.
- **Members only in spaces you manage:** the file app lets even admins change members only of spaces they manage. Spaces you create here keep you as a manager. For other spaces, ask one of their managers (they're listed).
- **People appear after their first login to the file app:** a new colleague can be added once they've opened it once. Groups (e.g. developers) can be members too.
- **Never the last manager**, and never yourself (so you don't lose the space by accident).
- **Size limits:** never below what the space already uses. When it's full, nobody can add files to it.
- Not here: disabling or deleting a space (that's in the file app), personal spaces, sharing links.

## The audit trail

> "What did Hugo do through the assistant yesterday?"
>
> "Who changed the members of contact@ this week?"
>
> "Show me everything about that call."

Every call through SGNL MCP is recorded: who, which tool, the question the person was asked, what they answered (and how quickly), and what was done. For people's own calls only who, what and the targets are kept, never the content of their mail or files. An admin change that can't be recorded isn't made.

## If something goes wrong

- **"these tools are for admins only":** the login server doesn't have you in the admin group (any more).
- **"isn't trusted yet to ask them":** your AI app can't show SGNL's confirmation reliably. Use Claude Code.
- **"the answer came back faster than a person can read the question":** your app answered the confirmation by itself, and nothing was done. Ask again and read the question before pressing Accept.
- **"what it shows has changed since":** the person changed between the question and your answer (e.g. another admin edited them). Ask again.
- **"stopped half-way: done: …":** a multi-step change (finishing an invitation, several groups) failed in the middle. The message says exactly what was done; check with *"Show me …"* and finish by hand.
