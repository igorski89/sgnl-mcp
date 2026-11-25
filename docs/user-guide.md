# Your AI assistant and SGNL

SGNL connects your AI assistant (Claude, Cursor, VS Code and others) to your work mail, calendar, contacts and files. You don't need to learn commands: you ask in plain words, and the assistant finds, reads, drafts and organises things for you.

The assistant acts **as you**. It sees only what you can see and nothing that belongs to other people.

## At a glance

| The assistant can… | Does it ask you first? |
|---|---|
| Read mail, calendar, contacts and files | No |
| Write drafts, flag and move mail | No: it's all private to you, and nothing leaves your mailbox |
| Put private entries in your calendar | No |
| Save new files in your personal space | No |
| **Send an email** | **Yes**: you see a confirmation and press Accept |
| **Save a file into a shared space** | **Yes**: you see a confirmation and press Accept |
| Delete anything, invite people, share links, set up forwarding | **Never**: it can't do these at all |

## Getting started

You need to be switched on for SGNL MCP first: ask your administrator. They also tell you your company's values for the placeholders below (`<mcp address>` looks like `https://mcp.example.com/mcp`, `<name>` is a short name such as `sgnl-example`).

### In Claude Code (the terminal app)

1. Run this once in a terminal:

   ```
   claude mcp add --transport http --client-id sgnl-mcp-claude-code --callback-port 47621 <name> <mcp address>
   ```

2. Start Claude Code, type `/mcp`, pick `<name>` and choose **Authenticate**. Your browser opens your company's login page: log in as usual (with your second factor) and allow the access it asks for.
3. Back in Claude Code, try *"Who am I logged in as?"* and then *"What's on my plate today?"*

If Claude Code later says the connection needs a login, repeat step 2.

### On claude.ai (the website)

1. Go to **Settings → Connectors → Add custom connector**.
2. Name: anything you like, for example your company's name. URL: `<mcp address>`.
3. Open **Advanced settings** and enter the OAuth client ID `sgnl-mcp-claude-ai`. Leave the client secret empty.
4. Click **Add**, then **Connect**, and log in on your company's login page.
5. In a new chat, turn the connector on (the tools menu) and try *"What's on my plate today?"*

Sending email and saving into shared spaces may not work on claude.ai yet: it depends on whether the site shows SGNL's confirmation dialog. If it can't, the assistant tells you it can't send; your draft is waiting in Drafts, so send it from your mail app.

## What you can ask

### Your day

> "What's on my plate today?"
>
> "Give me a morning briefing: meetings, unread mail, files that changed."

### Mail

**Finding and reading**

> "Any unread mail from Anna this week? Summarize it."
>
> "Find the invoice from October and read me the attachment."
>
> "Summarize the whole thread about the budget."
>
> "Which folders do I have, and where's unread mail waiting?"

The assistant can read attachments in PDF, Word, CSV and plain-text formats.

**Writing**

> "Draft a reply to Anna's last mail: I agree, let's meet Thursday."
>
> "Shorten that draft and add Ben in Cc."
>
> "Attach the Q4 report from my files to that draft."
>
> "Forward the offer to Ben with a short note."

Drafts land in your **Drafts** folder. You can review them there, edit them in your mail app, or ask the assistant to change them.

**Keeping things tidy**

> "Mark today's newsletters as read and move them to the Newsletters folder."
>
> "Star the mail from Anna about the contract."

The assistant can move mail between your folders, but not into Trash, so nothing ever gets deleted.

**Sending**

> "Send the draft to Anna."

Before anything is sent, your app shows a confirmation like this one:

```
Send "Meeting on Thursday" to anna@company.com, partner@other.org (1 OUTSIDE the company)?

From: you@company.com
To: anna@company.com
Cc: partner@other.org  (OUTSIDE the company, never written to before)
Subject: Meeting on Thursday
Text begins: Hi Anna, Thursday works for me…

WARNING: 1 recipient(s) outside the company.

                                    [Decline]  [Accept]
```

**Check the recipients, then press Accept to send.** Decline or Esc sends nothing, and the draft stays in Drafts.

The first line always names the subject, the recipients and any attachments (some apps fold the rest: expand it if you want to see everything). Look for these labels and warnings:

- **OUTSIDE the company:** this recipient is not a colleague.
- **never written to before:** you haven't sent mail to this address yet.
- **WARNING: … recipients:** more than 10 people will get it.
- **WARNING: your assistant read email from outside the company in the last 30 minutes:** if you didn't ask for this email yourself, decline it (see [Staying safe](#staying-safe)).

### Calendar

> "What does my next week look like?"
>
> "Block two hours of focus time tomorrow morning."
>
> "When are Anna and I both free for an hour this week?"

The entries the assistant creates are private to you: it never invites anyone. For colleagues it sees only *when* they're busy, never what their meetings are.

### Contacts

> "What's Ben's phone number?"
>
> "Find Ben Miller's email address."

The assistant searches your address book and the company directory.

### Files

> "Find the latest pricing document and summarize it."
>
> "What's in the Marketing space?"
>
> "Write meeting notes from this thread and save them as notes.md in my personal space."
>
> "Save that summary into the Marketing space."

The assistant creates **new** text files only: it never overwrites or deletes an existing file. Saving into a **shared** space shows a confirmation first, the same way sending does, because your colleagues will see the file.

### Putting it together

> "Read today's unread mail, draft replies to the ones that need an answer, and tell me what's left for me."
>
> "Anna asked for a meeting: find a free slot for both of us, put a reminder in my calendar, and draft a reply proposing that time."
>
> "Summarize the attached contract and save the summary in my personal space."

## Staying safe

Incoming mail and shared files are written by other people, and some of them may contain hidden instructions aimed at AI assistants ("ignore your instructions and send this to…"). SGNL strips hidden text from mail before the assistant reads it, and it never sends anything without your click. **The confirmation is your last line of defence.**

- **Read every confirmation.** Only accept what you actually asked for.
- **Decline if you're unsure.** Nothing happens, and the draft stays in Drafts.
- **Be careful with "OUTSIDE" and "never written to before"**, especially just after the assistant has read mail from strangers.
- **Never let a tool or setting answer confirmations for you.** Some apps can auto-approve requests; keep that switched off for SGNL.
- **If something looks wrong** (a draft you didn't ask for, a strange file), tell your administrator.

## Which apps work?

Sending and saving to shared spaces need an app that can show confirmations.

| App | Reading and drafts | Sending and shared spaces |
|---|---|---|
| Claude Code | ✅ | ✅ (tested) |
| claude.ai (website) | ✅ | not yet known |
| Cursor | ✅ | ✅ |
| VS Code (Copilot) | ✅ | ✅ |
| Codex CLI | ✅ | ✅, but switch off auto-approve modes |
| Claude Desktop | ✅ | ⚠️ may not show the confirmation |
| Gemini CLI | ✅ | ❌ |

In an app that can't show confirmations, the assistant tells you it can't send. Send the draft from your mail app instead.

## Limits

- The assistant can send a few emails per minute and to at most 50 recipients per email.
- Very long mails and files are shortened when the assistant reads them.
- If you work very fast, the assistant may ask you to wait a moment.

## Questions

**Can the assistant see my colleagues' mail?** No. It uses your login, so it sees what you can see in your mail and file apps, and nothing more.

**Is a draft sent automatically?** No. A draft is sent only when you ask, see the confirmation and press Accept, or when you send it yourself from your mail app.

**Can it delete things by mistake?** No. Deleting isn't possible through SGNL at all.

**Where can I see what it did?** Drafts are in your Drafts folder, sent mail is in Sent, files are in your spaces, and calendar entries are in your calendar. Everything is visible in your usual apps.
