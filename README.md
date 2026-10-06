# notif

Post Markdown to Slack from the command line. Accounts, channels and
credentials live in one external TOML file, so you address a message by name:

```bash
notif post --account gierd --channel releases --template deploy \
           --var service=api --var version=v2.3.1
```

`gierd` is `gierd.slack.com`; `releases` is whatever channel id that account's
config maps it to. Multiple workspaces and any number of channels per workspace
are supported.

## Install

```bash
cargo install --path .
notif init          # writes ~/.config/notif/config.toml (mode 0600) + templates
```

Then put a bot token in the config and check it:

```bash
notif check
```

## Configuration

`~/.config/notif/config.toml` (or `--config`, `$NOTIF_CONFIG`, `./notif.toml`):

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
never echoed — listings show `xoxb-…AB12` — and `notif` warns if a config
holding inline tokens is group- or world-readable.

`notif discover gierd` asks Slack for every channel the token can see and
prints a `[channels]` table ready to paste.

The Slack app needs `chat:write` (plus `chat:write.customize` if you override
the sender name or icon, and `channels:read`/`groups:read` for `discover`), and
must be invited to each channel.

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
| `notif post` | render and send |
| `notif render` | render to Slack mrkdwn on stdout, send nothing |
| `notif accounts` | list configured workspaces |
| `notif channels [account]` | list that account's channels |
| `notif templates` | list templates with their variables |
| `notif check [account]` | verify tokens via `auth.test` |
| `notif discover [account]` | channel ids from Slack, as TOML |
| `notif init` | write a starter config and templates |
| `notif where` | show the config and template paths in use |

Every listing command takes `--json`. `notif post --json` returns the message
`ts` and permalink.

Message sources, in place of `--template`: `--file <path>`, `--text <markdown>`,
or Markdown piped on stdin. Useful flags: `--dry-run`, `--thread <ts>`,
`--broadcast`, `--no-unfurl`, `--username`, `--icon-emoji`, `--raw`.

## Markdown handling

Slack does not render Markdown, so `notif` converts it: headings become bold,
`**bold**` becomes `*bold*`, links become `<url|text>`, bullets become `•`,
task lists become `☑`/`☐`, tables become aligned code blocks, `@here` becomes a
real broadcast, and `&`/`<`/`>` are escaped. Code spans and fenced blocks are
passed through untouched, as are hand-written Slack entities like `<@U123>`.
`--raw` skips conversion entirely.

See `skills/slack-post/references/mrkdwn.md` for the full table.

## Claude Code skill

`skills/slack-post/` is a skill that teaches an agent to use this CLI — how to
look up accounts and channels, pick a template, preview with `--dry-run` and
report the permalink. Install it with:

```bash
ln -s "$PWD/skills/slack-post" ~/.claude/skills/slack-post
```
