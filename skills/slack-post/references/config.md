# `notif` configuration

One TOML file holds every workspace, every channel and every credential.
`notif where` prints the path in use. Resolution order:

1. `--config <path>`
2. `$NOTIF_CONFIG`
3. `$XDG_CONFIG_HOME/notif/config.toml`, else `~/.config/notif/config.toml`
4. `./notif.toml`

`notif init` writes a commented starter file with mode `0600`.

## Shape

```toml
default_account = "gierd"
# template_dir = "~/.config/notif/templates"   # default: <config dir>/templates

[accounts.gierd]                 # the key is how you address it: --account gierd
workspace = "gierd.slack.com"    # also builds permalinks for `notif read`
token = "xoxb-…"                 # exactly one token source, see below
default_channel = "general"

[accounts.gierd.channels]        # friendly name -> channel id
general = "C0100000000"
alerts  = "C0100000001"

[accounts.gierd.channels.releases]   # or a table, for per-channel extras
id = "C0100000002"
description = "Deploy and release announcements"
icon_emoji = ":rocket:"
username = "release-bot"

[accounts.acme]                  # a second workspace, acme.slack.com
token_env = "ACME_SLACK_TOKEN"
default_channel = "standup"

[accounts.acme.channels]
standup = "C0200000000"
```

Add as many `[accounts.*]` tables as there are workspaces, and as many channels
per account as you like.

## Token sources

Exactly one per account:

| Key | Meaning |
| --- | --- |
| `token` | the literal token in the file (keep it `chmod 600`) |
| `token_env` | name of an environment variable holding the token |
| `token_command` | a shell command that prints the token, e.g. `pass show slack/gierd` |

`notif` warns when a config with inline tokens is group- or world-readable, and
never prints a token: `notif accounts` shows `xoxb-…AB12`.

## Slack app setup

The token is a bot token (`xoxb-…`) from a Slack app installed in that
workspace. Scopes:

- `chat:write` — required, to post.
- `chat:write.customize` — only if you set `username` / `icon_emoji`.
- `channels:read` (plus `groups:read` for private) — only for `notif discover`.
- `channels:history` — for `notif read` and `notif thread`, and for `--reply-to`
  to tell whether the message it was given is itself a reply.
- `groups:history` — the same, for private channels (`im:history` /
  `mpim:history` for DMs and group DMs).
- `users:read` — turns `<@U01ABCDEF>` into `@alice` when reading. Without it
  reading still works and the raw ids are left in place.

The bot must be a member of each channel it posts to **or reads**:
`/invite @your-app`. Reading a channel the bot is not in fails with
`not_in_channel`, not with an empty list.

After adding a scope, reinstall the app in the workspace — an existing token
does not pick up new scopes.

## Filling in channel ids

```bash
notif discover gierd              # prints a ready-to-paste [channels] table
notif discover gierd --private --member-only
notif check                       # auth.test every account
```

Ids are stabler than names — a renamed channel keeps its id — which is why the
config maps your own short name onto an id.

`workspace = "gierd.slack.com"` is informational for posting, but `notif read`
uses it to build each message's permalink locally, so it is worth setting.
