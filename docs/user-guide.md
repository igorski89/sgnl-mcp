# Your AI assistant and SGNL

SGNL connects your AI assistant (Claude, Cursor, VS Code and others) to your work mail, calendar, contacts and files. You don't need to learn commands: you ask in plain words, and the assistant finds, reads, drafts and organises things for you.

The assistant acts **as you**. It sees only what you can see and nothing that belongs to other people.

## At a glance

| The assistant can… | Does it ask you first? |
|---|---|
| Read mail, calendar, contacts and files | No |
| Write drafts, flag and move mail, create and rename your mail folders | No: it's all private to you, and nothing leaves your mailbox |
| Add and change contacts in your address book | No |
| **Delete a contact** | **Yes**: you see who it is and press Accept |
| Put private entries in your calendar | No |
| **Invite people to a meeting** | **Yes**: you see a confirmation and press Accept |
| Save new files in your personal space | No |
| **Change a text file in your personal space** | **Yes**: you see the changes and press Accept |
| **Send an email** | **Yes**: you see a confirmation and press Accept |
| **Share a file or folder with a colleague** | **Yes**: you see a confirmation and press Accept |
| **Save a file into a shared space** | **Yes**: you see a confirmation and press Accept |
| **Move mail to Trash** | **Yes**: you see which mails and press Accept (you can restore them from Trash) |
| **Switch on your out-of-office reply** | **Yes**: you see the dates and the whole text and press Accept |
| Delete anything for good, create public links, set up forwarding | **Never**: it can't do these at all |

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

Anything that needs your confirmation (sending email, invitations, sharing, changing a file, saving into shared spaces) may not work on claude.ai yet: it depends on whether the site shows SGNL's confirmation dialog. If it can't, the assistant tells you it can't send; your draft is waiting in Drafts, so send it from your mail app.

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

The assistant can read attachments and files in PDF, Word, Excel, PowerPoint, OpenDocument (LibreOffice), CSV and plain-text formats. Spreadsheets come as rows per sheet, presentations per slide (PowerPoint with speaker notes). Hidden sheets, rows, columns, slides and text are left out on purpose, since hidden content is a common way to slip instructions to an AI.

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

The assistant can move mail between your folders, create new folders and rename your own (not Inbox, Sent and the other standard ones); it never deletes a folder. In a shared mailbox, creating or renaming a folder asks you first, since everyone in it sees it. It can also move mail to **Trash**, after a confirmation listing each mail; you can restore it from Trash in your mail app.

> "Save the PDF from Anna's invoice mail into my files."

Attachments are saved as new files, by default in your personal space.

**Shared mailboxes**

> "Anything new in the contact@ mailbox?"
>
> "Draft a reply from contact@ to that enquiry."

If you're a member of a shared mailbox (for example contact@ or admin@), the assistant can read it, draft there and, after your confirmation, send from it. The confirmation then starts with "Send AS contact@…", because the mail goes out in the team's name and your colleagues in that mailbox see it in Sent.

**Your other addresses, labels and mailbox size**

> "Which addresses can I send from?"
>
> "Draft it from sales@ instead."
>
> "Label these three mails 'invoices'." / "Show me everything labelled invoices."
>
> "How full is my mailbox?"

If you have extra addresses (aliases), the assistant can draft from one of them; when you send, the confirmation's first line says **FROM** that address. Labels are tags on mail, as your mail app shows them (letters, digits, '.', '_' and '-'). The mailbox size shows how much you use and your limit, if there is one.

**Out of office**

> "Set my out-of-office from 1 to 5 December: I'm away, Anna covers for me."

The confirmation shows the dates and the whole text, because everyone who writes to you gets it, people outside the company too. Without an end date it says so on the first line. Switching it off needs no confirmation.

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
>
> "Who has accepted Thursday's Q4 review?"

> "Set up a 30-minute meeting with Hugo on Thursday at 10 about the Q4 review."

> "Accept the invitation to Thursday's Q4 review."
>
> "Add the invitation Bob sent me to my calendar."
>
> "Move my 3 pm meeting with Hugo to 4 pm."
>
> "Cancel tomorrow's team lunch."

Private entries stay private. When you ask for a meeting with others, the assistant shows a confirmation first, like for sending email: the first line names the people, the title and the time, and marks anyone outside the company. After Accept, the event is in your calendar and the others get an invitation by email. For colleagues it sees only *when* they're busy, never what their meetings are. Moving or cancelling a meeting you organize, and deleting a private entry also show a confirmation first: the first line says what happens and who gets an email. Repeating meetings show up on each of their days. You can answer a whole series (single meetings of a series: in your calendar app), and for a series you organize you can move or cancel one meeting, or change the title, place or text of all of them; the confirmation says whether it's one meeting or ALL of them. Moving every meeting of a series or changing how often it repeats: use your calendar app. Answering an invitation also asks first, and the organizer gets your answer. Invitations from people outside the company don't appear in your calendar by themselves: ask the assistant to add one from the mail (it shows the organizer and asks first; nothing is sent), then answer it.

> "Which calendars have colleagues shared with me?"
>
> "What's on the team calendar next week?"
>
> "Add 'Office closed' on 24 December to the team calendar."

Calendars a colleague (or a shared mailbox) shares with you can be read like your own. If the share lets you add events, the assistant can add one after you confirm, because everyone who sees that calendar sees it. If the share lets you change its events, the assistant can also change or cancel them, always asking first: private entries quietly, and meetings that calendar's owner organizes, where the attendees then get the update in the owner's name.

### Contacts

> "What's Ben's phone number?"
>
> "Find Ben Miller's email address."

> "Add Ben Miller to my contacts: ben@example.com, +49 30 1234, ACME."
>
> "Ben's new title is CTO."

The assistant searches your address book and the company directory. It can add people to your address book and change them (company directory entries belong to the company and can't be changed). Deleting a contact asks you first, showing who it is, because it can't be undone.

> "Make a contact group 'Board' with Anna and Ben." / "Add Hugo to the Board group."

Contact groups live in your address book; their members are your contacts (add someone as a contact first). Taking someone out of a group keeps them as a contact.

### Files

> "Find the latest pricing document and summarize it."
>
> "What's in the Marketing space?"
>
> "Write meeting notes from this thread and save them as notes.md in my personal space."
>
> "Save that summary into the Marketing space."
>
> "Share my Q4 folder with Hugo, read-only."

Sharing works for files and folders in your personal space, with one colleague at a time, for viewing (the default) or editing. The confirmation's first line says what (FILE or FOLDER), with whom, and whether they can change it. Public links ("anyone with the link") can't be made through the assistant. To stop sharing, ask ("Stop sharing the Q4 folder with Hugo"): it asks you first.

> "Add today's decisions to the end of notes.md."
>
> "In my packing list, change 'two shirts' to 'three shirts'."

The assistant can save **new** text files, and change text files (Markdown, plain text, CSV, JSON, YAML) in your **personal** space. Before a change is written, a confirmation shows the file and each change (what is replaced, removed or added). If the file changed in the meantime, for example because you edited it yourself, nothing is written and the assistant has to read it again. The previous version is always kept: in your file app, open the file's **Versions** to see or restore it. It can't change files in shared spaces or documents like Word or PDF.

> "Rename notes.md to Q4 notes.md and move it into the Q4 folder."
>
> "What's in my trash bin? Restore the budget file."
>
> "Go back to yesterday's version of the plan."
>
> "What do I share, and with whom?"

It can create folders, move and rename within a space, move files and folders to the **trash bin** and restore them, and restore an earlier version of a file. Moving to the trash bin and restoring a version always ask you first; nothing is ever deleted for good. Anything that others will see (saving, moving, editing or restoring into a project space or a folder you share) also asks you first, and the first line says so.

> "Find the contract that mentions the penalty clause." *(searches inside documents, PDFs, spreadsheets and presentations)*
>
> "Make a Word document with these meeting notes." / "Put this table into an Excel file."
>
> "Copy the Q4 template into the Marketing space."

Searching by words inside files finds documents whose text contains them; the assistant then reads the file to see what's there. It can create **new** Word (.docx) and Excel (.xlsx) files: headings, paragraphs, lists and tables for Word; sheets with numbers, dates and text for Excel (never formulas or macros). Copies never replace an existing file; a copy into a place others see asks you first.

### Putting it together

> "Read today's unread mail, draft replies to the ones that need an answer, and tell me what's left for me."
>
> "Anna asked for a meeting: find a free slot for both of us, put a reminder in my calendar, and draft a reply proposing that time."
>
> "Summarize the attached contract and save the summary in my personal space."

### Your logins

> "Where am I logged in?"
>
> "Log me out everywhere." *(after losing a phone or laptop)*

The assistant shows how many SGNL login sessions you have open and your recent logins (when and from which internet address). Logging out everywhere asks you first and ends every session and app login of yours, the assistant's too, so you log in again. App passwords you made for a mail app keep working: if a device with one is lost, ask whoever runs SGNL to remove it.

## Staying safe

Incoming mail and shared files are written by other people, and some of them may contain hidden instructions aimed at AI assistants ("ignore your instructions and send this to…"). SGNL strips hidden text from mail before the assistant reads it, and it never sends anything without your click. **The confirmation is your last line of defence.**

- **Read every confirmation.** Only accept what you actually asked for.
- **Decline if you're unsure.** Nothing happens, and the draft stays in Drafts.
- **Be careful with "OUTSIDE" and "never written to before"**, especially just after the assistant has read mail from strangers.
- **Never let a tool or setting answer confirmations for you.** Some apps can auto-approve requests; keep that switched off for SGNL.
- **If something looks wrong** (a draft you didn't ask for, a strange file), tell your administrator.

## Which apps work?

Everything that asks you first (sending, invitations, sharing, changing files, saving to shared spaces) needs an app that can show confirmations.

| App | Reading and drafts | Anything you confirm |
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

**Can it delete things by mistake?** No. Nothing can be deleted for good through SGNL. It can move mail to Trash, but only the mails you saw in the confirmation and accepted, and you can restore them. When it changes a text file it can remove text from it, again only as you accepted, and the previous version is kept.

**Where can I see what it did?** Drafts are in your Drafts folder, sent mail is in Sent, files are in your spaces, and calendar entries are in your calendar. Everything is visible in your usual apps.
