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
| **Invite a new person** | **Yes**: you see the name, their future address and where the set-up link goes |
| **Finish an invitation** | **Yes**: you see the address and the groups, with what each opens |
| **Add someone to groups / remove them** | **Yes**: you see what each group opens or what they lose |
| **Create a group** | **Yes** |
| **Email someone a set-up link** (new password and passkey) | **Yes**: you see the address it goes to |
| **Log someone out everywhere** | **Yes** |
| **Disable an account** | **Yes, and you type their username** |
| **Enable an account again** | **Yes** |
| Delete a person or a group, reset passwords or passkeys, lift a lockout | **Never**: done by whoever runs the instance, on the server |
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
4. **Password manager:** its members follow the groups only after whoever runs the instance runs the sync. Each result reminds you.

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
  - The confirmation says **they lose access to EVERYTHING now** and asks you to **type their username**. Only the exact username counts.
- *"Enable Hugo again."* restores access with their groups as before.

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

- **Read-only, and never mail content:** the assistant sees mailboxes, members, addresses, aliases, space used and the outgoing queue (sender, recipients, status, the receiving server's reply). It never sees what a mail says.
- **"Was it delivered?":** only mail still waiting in the queue shows up, e.g. a server that keeps refusing it and the reason. An empty answer means it was handed on: there's no delivery history. Outgoing mail goes through a relay service, so a bounce comes back later as a mail to the sender.
- **Changing members, aliases or quotas** isn't possible through the assistant yet: ask whoever runs the instance.

## If something goes wrong

- **"these tools are for admins only":** the login server doesn't have you in the admin group (any more).
- **"isn't trusted yet to ask them":** your AI app can't show SGNL's confirmation reliably. Use Claude Code.
- **"the answer came back faster than a person can read the question":** your app answered the confirmation by itself, and nothing was done. Ask again and read the question before pressing Accept.
- **"what it shows has changed since":** the person changed between the question and your answer (e.g. another admin edited them). Ask again.
- **"stopped half-way: done: …":** a multi-step change (finishing an invitation, several groups) failed in the middle. The message says exactly what was done; check with *"Show me …"* and finish by hand.
