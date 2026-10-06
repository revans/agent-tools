# notif-slack

Post Markdown to Slack from the command line, read a channel back, and reply in
thread. Accounts, channels and credentials live in one external TOML file, so
you address a message by name:

```bash
notif-slack post --account gierd --channel releases --template deploy \
           --var service=api --var version=v2.3.1
```

`gierd` is `gierd.slack.com`; `releases` is whatever channel id that account's
config maps it to. Multiple workspaces and any number of channels per workspace
are supported.

## Install

```bash
cargo install --path .
notif-slack init          # writes ~/.config/notif-slack/config.toml (mode 0600) + templates
```

Then put a bot token in the config and check it:

```bash
notif-slack check
```

## Configuration

`~/.config/notif-slack/config.toml` (or `--config`, `$NOTIF_SLACK_CONFIG`, `./notif-slack.toml`):

```toml
default_account = "gierd"

[accounts.gierd]
workspace = "gierd.slack.com"
token = "xoxb-…"                 # or token_env = "…" / token_command = "…"
default_channel = "general"

[accounts.gierd.channels]
general = "C0100000000"
alerts  = "C0100000001"

[accounts.gierd.channels.releases]
id = "C0100000002"
description = "Deploy and release announcements"
icon_emoji = ":rocket:"

[accounts.acme]
token_env = "ACME_SLACK_TOKEN"
default_channel = "standup"

[accounts.acme.channels]
standup = "C0200000000"
```

A token can be inline, read from an environment variable (`token_env`), or
produced by a command (`token_command = "pass show slack/gierd"`). Tokens are
never echoed — listings show `xoxb-…AB12` — and `notif-slack` warns if a config
holding inline tokens is group- or world-readable.

`notif-slack discover gierd` asks Slack for every channel the token can see and
prints a `[channels]` table ready to paste.

The Slack app needs `chat:write` (plus `chat:write.customize` if you override
the sender name or icon, `channels:read`/`groups:read` for `discover`, and
`channels:history`/`groups:history` plus `users:read` for `read` and `thread`),
and must be invited to each channel it posts to or reads.

## Templates

A template is a Markdown file with optional TOML frontmatter, in
`<config dir>/templates/`:

```markdown
---
description = "Deploy announcement for a service"
channel = "releases"
icon_emoji = ":rocket:"

[vars]
service = "Name of the deployed service"
version = "Version or tag that went out"
env = { description = "Target environment", default = "production" }
---
## Deployed {{service}} {{version}}

**Environment:** {{env}}
```

`{{var}}` placeholders are filled from `--var key=value`, `--vars-json` or
`--vars-file`. A variable with no default is required: posting without it fails
rather than leaving a gap. `account` and `channel` in the frontmatter are
defaults that a command-line flag overrides.

## Commands

| Command | Does |
| --- | --- |
| `notif-slack post` | render and send |
| `notif-slack render` | render to Slack mrkdwn on stdout, send nothing |
| `notif-slack read` | recent messages from a channel, newest last |
| `notif-slack thread` | one thread, parent message first |
| `notif-slack accounts` | list configured workspaces |
| `notif-slack channels [account]` | list that account's channels |
| `notif-slack templates` | list templates with their variables |
| `notif-slack check [account]` | verify tokens via `auth.test` |
| `notif-slack discover [account]` | channel ids from Slack, as TOML |
| `notif-slack init` | write a starter config and templates |
| `notif-slack where` | show the config and template paths in use |

Every listing command takes `--json`. `notif-slack post --json` returns the message
`ts`, `thread_ts` and permalink.

Message sources, in place of `--template`: `--file <path>`, `--text <markdown>`,
or Markdown piped on stdin. Useful flags: `--dry-run`,
`--reply-to <permalink|ts>`, `--thread <ts>`, `--broadcast`, `--no-unfurl`,
`--username`, `--icon-emoji`, `--raw`.

## Reading and replying

```bash
notif-slack read -a gierd -c alerts                     # 20 most recent, oldest first
notif-slack read -c alerts -n 50 --since 2025-01-13     # a ts or an ISO-8601 date/datetime
notif-slack read -c alerts --json                       # ts, iso_time, author, markdown, permalink
notif-slack thread -c alerts 1718031234.567890          # one thread, parent first
```

A read prints each message's author, local time, `ts`, reply count and text
converted back to Markdown. `--raw` prints Slack's `mrkdwn` verbatim and
resolves no user ids; `--since` / `--until` bound the range.

Replying needs only something the user can see — a `ts` from a read, or a
permalink, which also says which channel it is in:

```bash
notif-slack post -c alerts --reply-to 1718031234.567890 --text 'On it'
notif-slack post --reply-to 'https://gierd.slack.com/archives/C0100000001/p1718031234567890' \
           --text 'On it'
```

`--reply-to` follows a message to its thread, so replying to a reply lands in
the same thread rather than starting a new one. It cannot be combined with
`--thread`, and it fails rather than guessing if `--channel` disagrees with the
permalink.

## Markdown handling

Slack does not render Markdown, so `notif-slack` converts it: headings become bold,
`**bold**` becomes `*bold*`, links become `<url|text>`, bullets become `•`,
task lists become `☑`/`☐`, tables become aligned code blocks, `@here` becomes a
real broadcast, and `&`/`<`/`>` are escaped. Code spans and fenced blocks are
passed through untouched, as are hand-written Slack entities like `<@U123>`.
`--raw` skips conversion entirely.

Reading runs the same mapping backwards: `*bold*` comes back as `**bold**`,
`<url|text>` as `[text](url)`, `•` as `-`, and `<@U01ABCDEF>` as `@alice` (ids
that cannot be resolved stay as they are). Code spans and blocks are untouched
in both directions.

See `skills/slack-post/references/mrkdwn.md` for the full table.

## Claude Code skill

`skills/slack-post/` is a skill that teaches an agent to use this CLI — how to
look up accounts and channels, pick a template, preview with `--dry-run`, report
the permalink, read a channel and reply in thread. Install it with:

```bash
ln -s "$PWD/skills/slack-post" ~/.claude/skills/slack-post
```
