# notif-tg

Send Markdown to Telegram from the command line, and read what comes back.
Bots, chats and credentials live in one external TOML file, so a message is
addressed by name:

```bash
notif-tg post --account gierd --chat releases --template deploy \
              --var service=api --var version=v2.3.1
```

`gierd` is one bot; `releases` is whatever chat id that account's config maps it
to. Multiple bots and any number of chats per bot are supported.

Companion to [`notif-slack`](../notif-slack), which does the same for Slack.

## Install

```bash
make -C .. install        # or: cargo install --path .
notif-tg init        # writes ~/.config/notif-tg/config.toml (0600) + templates
```

Create a bot with [@BotFather](https://t.me/BotFather), paste the token into the
config, then:

```bash
notif-tg check
```

## Configuration

```toml
default_account = "gierd"

[accounts.gierd]
bot = "@gierd_notify_bot"
token = "123456789:AA…"        # or token_env = "…" / token_command = "…"
default_chat = "me"

[accounts.gierd.chats]
me = "123456789"               # a person: positive id
alerts = "-1001234567890"      # a group or channel: negative id

[accounts.gierd.chats.releases]
id = "-1009876543210"
description = "Deploy and release announcements"
no_preview = true
topic_id = 42                  # forum topic, for supergroups with topics on

[accounts.acme]
token_env = "ACME_TELEGRAM_TOKEN"
default_chat = "oncall"

[accounts.acme.chats]
oncall = "-1002222222222"
```

The bot token travels in the URL path, so it is scrubbed from every error
message and listings only show `123456789:…xyz`. `notif-tg` warns if a config
with inline tokens is group- or world-readable.

Telegram gives a bot no way to list its own chats, so `notif-tg discover` shows
every chat that has recently messaged the bot, as a `[chats]` table ready to
paste. It does not consume those updates.

## Templates

Markdown with optional TOML frontmatter, in `<config dir>/templates/`:

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

`{{var}}` placeholders come from `--var key=value`, `--vars-json` or
`--vars-file`. A variable with no default is required: sending without it fails
rather than leaving a gap.

## Reading

```bash
notif-tg read                  # messages waiting for the default bot
notif-tg read -c alerts -n 50  # one chat, more of them
notif-tg read --peek           # look without consuming
notif-tg watch --once          # block until the next message arrives
notif-tg watch --exec ./handle.sh   # run a command per message
```

Telegram drops an update once it is confirmed, so `read` is consuming by
default; the next unread position is kept per account under `state_dir` and
shown by `notif-tg where`. `--peek` leaves it alone, `--reset` re-reads whatever
Telegram still holds (roughly 24 hours).

Two constraints come from Telegram, not from this tool:

- A bot only receives messages **sent to it** — direct messages, commands, and
  replies to its own messages. In groups it sees nothing else unless privacy
  mode is off or it is an admin. `notif-tg check` reports which.
- Only one consumer per bot. A second reader, or a webhook, gets `Conflict`.

`--exec` runs a command per message with the Markdown on stdin and
`NOTIF_TG_ACCOUNT`, `NOTIF_TG_CHAT_ID`, `NOTIF_TG_CHAT_KEY`,
`NOTIF_TG_CHAT_TITLE`, `NOTIF_TG_MESSAGE_ID`, `NOTIF_TG_FROM`,
`NOTIF_TG_FROM_ID`, `NOTIF_TG_DATE` and `NOTIF_TG_TEXT` in the environment.

## Replying

A Telegram thread is a reply chain. Take a `message_id` from `read` and reply to it:

```bash
notif-tg post -c alerts --reply-to 4242 --text 'On it'
```

Forum supergroups also have topics: `--topic <id>`, or `topic_id` per chat.

## Commands

| Command | Does |
| --- | --- |
| `notif-tg post` | render and send |
| `notif-tg render` | render to Telegram HTML on stdout, send nothing |
| `notif-tg read` | read messages waiting for a bot |
| `notif-tg watch` | stream messages as they arrive |
| `notif-tg accounts` | list configured bots |
| `notif-tg chats [account]` | list that bot's chats |
| `notif-tg templates` | list templates with their variables |
| `notif-tg check [account]` | verify tokens and every configured chat |
| `notif-tg discover [account]` | chat ids seen in recent updates, as TOML |
| `notif-tg init` | write a starter config and templates |
| `notif-tg where` | show config, template and state paths |

Every command takes `--json`. Message sources, in place of `--template`:
`--file <path>`, `--text <markdown>`, or Markdown on stdin. Useful flags:
`--dry-run`, `--reply-to`, `--topic`, `--silent`, `--no-preview`, `--raw`.

## Markdown handling

Telegram renders a small fixed set of HTML tags, so `notif-tg` converts:
headings become bold, `**bold**` becomes `<b>`, links become `<a href>`, bullets
become `•`, task lists become `☑`/`☐`, tables become aligned `<pre>` blocks,
`||spoilers||` become spoilers, and `&`/`<`/`>` are escaped. Code spans and
fenced blocks keep their contents and their language. `snake_case` is left
alone. Anything over Telegram's 4096-character limit is split into several
messages at paragraph boundaries — including long code blocks, which stay
fenced. `--raw` skips conversion entirely.

Reading goes the other way: Telegram sends text plus entity spans whose offsets
are in UTF-16 units, and those are rendered back to Markdown.

See `skills/telegram-messages/references/formatting.md` for the full table.

## Claude Code skill

`skills/telegram-messages/` teaches an agent to use this CLI — look up a bot and
chat, pick a template, preview with `--dry-run`, send, read, and reply in a
thread. Install it with:

```bash
make -C .. skills        # or: ln -s "$PWD/skills/telegram-messages" ~/.claude/skills/telegram-messages
```
