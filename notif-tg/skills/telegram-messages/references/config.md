# `notif-tg` configuration

One TOML file holds every bot, every chat and every credential. `notif-tg where`
prints the paths in use. Resolution order:

1. `--config <path>`
2. `$NOTIF_TG_CONFIG`
3. `$XDG_CONFIG_HOME/notif-tg/config.toml`, else `~/.config/notif-tg/config.toml`
4. `./notif-tg.toml`

`notif-tg init` writes a commented starter file with mode `0600`.

## Shape

```toml
default_account = "gierd"
# template_dir = "~/.config/notif-tg/templates"   # default: <config dir>/templates
# state_dir = "~/.local/state/notif-tg"           # where read/watch keep their place

[accounts.gierd]              # the key is how you address it: --account gierd
bot = "@gierd_notify_bot"     # informational
token = "123456789:AA…"       # exactly one token source, see below
default_chat = "me"

[accounts.gierd.chats]        # friendly name -> chat id
me = "123456789"              # a person: positive
alerts = "-1001234567890"     # a group or channel: negative

[accounts.gierd.chats.releases]   # or a table, for per-chat defaults
id = "-1009876543210"
description = "Deploy and release announcements"
no_preview = true
silent = true
topic_id = 42                 # forum topic, for supergroups with topics enabled

[accounts.acme]               # a second bot
token_env = "ACME_TELEGRAM_TOKEN"
default_chat = "oncall"

[accounts.acme.chats]
oncall = "-1002222222222"
```

## Token sources

Exactly one per account:

| Key | Meaning |
| --- | --- |
| `token` | the literal token in the file (keep it `chmod 600`) |
| `token_env` | name of an environment variable holding the token |
| `token_command` | a shell command that prints the token, e.g. `pass show telegram/gierd` |

The token goes in the request *path*, so `notif-tg` scrubs it out of any error
or log line, and listings only ever show `123456789:…xyz`.

## Bot setup

1. Message [@BotFather](https://t.me/BotFather), `/newbot`, and copy the token.
2. For a group or channel: add the bot to it, and for channels make it an
   administrator — a non-admin bot cannot post to a channel.
3. To receive messages in groups, either turn privacy mode off in BotFather
   (`/setprivacy` → Disable) or make the bot an admin. With privacy mode on, a
   bot only sees commands and replies to itself. `notif-tg check` reports which
   mode is active.

## Finding chat ids

A bot cannot list its own chats — Telegram has no such API. The only way is to
look at who has written to it:

```bash
notif-tg discover gierd     # prints a ready-to-paste [chats] table
```

Message the bot (or post in the group it is in) first. Telegram only holds
undelivered updates for about 24 hours. `discover` deliberately does not consume
anything, so it will not hide messages from `read`.

```bash
notif-tg check              # getMe for every account, getChat for every chat
```

## Reading state

`read` and `watch` store the next unconfirmed `update_id` per account under
`state_dir`. `notif-tg where` shows the file and current position.
`read --reset` forgets it and re-reads whatever Telegram still holds;
`read --peek` leaves it untouched.

Only one consumer per bot: a second `read`/`watch`, or a registered webhook,
gets `Conflict`.
