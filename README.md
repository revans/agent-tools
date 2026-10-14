# agent-tools

Command-line tools for agents and the people working with them. Each ships a
Claude Code skill that teaches an agent to drive it.

- **`notif-slack`, `notif-tg`**: Markdown notifications, addressed by name. Two
  CLIs that share a design: write a Markdown template, name an account and a
  destination, send.
- **`wharf`**: SFTP deployment for static sites: explore, sync, move, archive,
  pull, diff, verify, restore, and (carefully) delete. See [wharf/README.md](wharf/README.md).

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
| [`wharf`](wharf) | SFTP | explore, sync, deploy, move, archive, pull, diff, verify, restore, prune, delete |

Each keeps its own external TOML file holding every account, every destination
and every credential, so nothing is hard-coded and no token is ever passed on
the command line. Each ships Markdown templates with TOML frontmatter and typed
variables, and converts Markdown into whatever the platform actually renders —
Slack's `mrkdwn`, Telegram's HTML subset — rather than hoping it passes through.

Each also ships a Claude Code skill that teaches an agent to drive it.

## Build

```bash
make              # build every binary in release mode
make install      # and put them on your PATH
make skills       # symlink the Claude Code skills into ~/.claude/skills
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
agent-tools/
├── Makefile           build, test, install, skills
├── Cargo.toml         workspace: shared dependency versions and release profile
├── notif-slack/       the Slack CLI, its templates and its skill
├── notif-tg/          the Telegram CLI, its templates and its skill
└── wharf/             the SFTP deploy CLI (Go) and its skill
```

The two Rust crates share one workspace, one lockfile and one `target/`, so they
cannot drift apart on a dependency version. `wharf` is a separate Go module; the
Makefile builds it into the same `target/release/`.
