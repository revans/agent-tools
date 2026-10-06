---
name: telegram-messages
description: Send and read Telegram messages with the `notif-tg` CLI. Use whenever the user wants something sent, posted, announced or forwarded to Telegram — a chat, a group, a channel, a deploy or incident note, an alert, a status update, "ping me on Telegram", "message the group". Also use to read or check messages sent to a bot, catch up on a chat, watch for incoming messages, reply to one, or list configured Telegram bots, chats and templates.
allowed-tools: Bash, Read, Write, Edit, Glob
---

# Telegram with `notif-tg`

`notif-tg` is a Rust CLI that renders a Markdown template and sends it to a named
Telegram bot and chat, and reads the messages people send that bot back.
Credentials, bots and chats live in one external TOML file.

Addressing is **account + chat**, both by friendly name: `--account gierd` is one
bot, `--chat alerts` is whatever chat id that account's config maps `alerts` to.

## Before the first call

Check the tool is present: `notif-tg --version`.

If it is missing: `cd /home/revans/Work/PersonalLab/notif-tg && cargo install --path . --locked`

If `notif-tg where` reports no config, the user has not set up a bot yet. Run
`notif-tg init`, tell them to create a bot with @BotFather and paste the token
into the file it created, and stop — you cannot invent a token.

## Sending

1. **Find the destination.** Never guess a chat.

   ```bash
   notif-tg accounts --json         # bots, their default chat
   notif-tg chats <account> --json  # chat keys -> ids
   ```

   A chat the user names that is not configured is an error, not a guess. To add
   one, `notif-tg discover <account>` lists every chat that has messaged the bot
   recently, as TOML to paste.

2. **Pick or write the message.**

   ```bash
   notif-tg templates --json        # name, description, required vars, destination
   notif-tg post -t deploy -v service=api -v version=v2.3.1
   ```

   Supply every variable `declared_vars` marks `required`; a missing one is an
   error, not a silent blank. For a one-off, use `--text`, or pipe Markdown on
   stdin for anything multi-line:

   ```bash
   notif-tg post -c alerts --text '**Build 412 failed** — see [logs](https://ci/412)'

   notif-tg post -c me <<'MSG'
   ## Weekly summary

   - Shipped the new importer
   - Cut p95 latency from 400ms to 120ms
   MSG
   ```

3. **Preview when the destination or wording is uncertain.** `--dry-run` shows
   the exact HTML that would be sent, and how many messages it splits into:

   ```bash
   notif-tg post -t incident -v title="Checkout 500s" -v summary="…" --dry-run
   ```

   Show that to the user and get a yes. Send straight away only when they named
   both the chat and the content.

4. **Send, then report the link.** `--json` returns each `message_id` and a
   `t.me` link where one exists. Keep the `message_id` — replying to it is what
   makes a thread.

Useful flags: `--silent` (no notification sound), `--no-preview` (suppress link
previews), `--topic <id>` (forum topic in a supergroup), `--raw` (send plain
text with no formatting at all).

## Reading

```bash
notif-tg read                 # messages waiting for the default bot
notif-tg read -c alerts -n 50 # one chat, more of them
notif-tg read --json          # for parsing: message_id, chat_id, from, markdown, reply_to
notif-tg read --peek          # look without consuming
```

Read is **destructive by default**: Telegram drops an update once it has been
confirmed, so a plain `read` consumes everything fetched, including messages
from chats `--chat` filtered out of the display. Use `--peek` when the user just
wants a look, or when another process also reads this bot.

`notif-tg watch` streams messages as they arrive and blocks until interrupted —
run it only when the user asked to wait for something, prefer `--once` so it
exits after the first message, and never leave it running in the background
without saying so.

Two facts that explain most "the bot sees nothing" confusion, worth stating to
the user rather than debugging silently:

- A bot only receives messages **sent to it**: direct messages, commands, and
  replies to its own messages. In groups it sees nothing else unless privacy
  mode is off (`notif-tg check` reports this) or it is an admin.
- Only one reader at a time. A second `read`/`watch`, or a webhook, makes
  Telegram return `Conflict` — stop the other one.

## Replying

A Telegram thread is a reply chain. Take the `message_id` from `read` and pass it:

```bash
notif-tg read --json                                   # -> message_id 4242
notif-tg post -c alerts --reply-to 4242 --text 'On it' # threads under that message
```

If the message being replied to is in a different chat than `--chat`, Telegram
rejects it — reply in the chat the message came from (`chat_key` in the JSON).

## Writing the Markdown

Write normal Markdown; `notif-tg` converts it to the HTML subset Telegram
accepts and splits anything over 4096 characters into several messages at
paragraph boundaries. `references/formatting.md` has the full mapping and the
things that do not survive.

Keep it short — Telegram is a phone notification, not a document. Lead with what
matters, a few bullets at most, link out for detail.

## Creating a template

For a message shape that recurs, write a template in the directory
`notif-tg where` reports:

```markdown
---
description = "Deploy announcement for a service"
chat = "releases"

[vars]
service = "Name of the deployed service"
version = "Version or tag that went out"
env = { description = "Target environment", default = "production" }
---
## 🚀 Deployed {{service}} {{version}}

**Environment:** {{env}}
```

Frontmatter is TOML between `---` fences and is optional; `account` and `chat`
there are defaults that a command-line flag overrides. A var with no `default`
is required. Verify with `notif-tg render -t <name> -v …` before sending.

## When a command fails

| Message | What to do |
| --- | --- |
| `unknown account` / `unknown chat` | List them and ask; do not guess an id. |
| `missing values for: …` | Supply those `--var`s. |
| `Unauthorized` | The bot token is wrong or revoked. Tell the user; never edit a token yourself. |
| `chat not found` | The bot was never added to that chat, or the id is wrong. |
| `bot is not a member` / `not enough rights` | Ask the user to add the bot, and make it an admin for channels. |
| `Conflict` | Another reader or a webhook owns the update stream. |
| `Too Many Requests` | Wait the stated retry period. |

Config, token sources and bot setup: `references/config.md`.
Full flag list: `notif-tg post --help`, `notif-tg read --help`.
