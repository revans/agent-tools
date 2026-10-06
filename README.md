# notif

Markdown notifications, addressed by name. Two CLIs that share a design:
write a Markdown template, name an account and a destination, send.

```bash
notif-slack post --account gierd --channel releases --template deploy \
                 --var service=api --var version=v2.3.1

notif-tg     post --account gierd --chat releases --template deploy \
                  --var service=api --var version=v2.3.1
```

| | | |
| --- | --- | --- |
| [`notif-slack`](notif-slack) | Slack | post, read a channel or thread, reply in thread |
| [`notif-tg`](notif-tg) | Telegram | send, read messages sent to the bot, watch, reply |

Each keeps its own external TOML file holding every account, every destination
and every credential, so nothing is hard-coded and no token is ever passed on
the command line. Each ships Markdown templates with TOML frontmatter and typed
variables, and converts Markdown into whatever the platform actually renders —
Slack's `mrkdwn`, Telegram's HTML subset — rather than hoping it passes through.

Each also ships a Claude Code skill that teaches an agent to drive it.

## Build

```bash
make              # build both binaries in release mode
make install      # and put them on your PATH
make skills       # symlink both Claude Code skills into ~/.claude/skills
make check        # fmt, clippy and tests
make help         # every target
```

`PREFIX` (default `~/.cargo`) and `SKILLS_DIR` (default `~/.claude/skills`)
override where `install` and `skills` put things.

## Set up

Each tool writes its own starter config, at mode 0600:

```bash
notif-slack init   # ~/.config/notif-slack/config.toml   — then add a bot token
notif-tg     init  # ~/.config/notif-tg/config.toml      — then add a BotFather token

notif-slack check  # verify the credentials
notif-tg     check
```

Then see [notif-slack/README.md](notif-slack/README.md) and
[notif-tg/README.md](notif-tg/README.md) for configuration, templates, reading
and the full command list.

## Layout

```
notif/
├── Makefile           build, test, install, skills
├── Cargo.toml         workspace: shared dependency versions and release profile
├── notif-slack/       the Slack CLI, its templates and its skill
└── notif-tg/          the Telegram CLI, its templates and its skill
```

One workspace, one lockfile, one `target/`, so the two crates cannot drift apart
on a dependency version.
