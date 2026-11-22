# agent-tools

Command-line tools for agents and the people working with them. Each ships a
Claude Code skill that teaches an agent to drive it.

- **`notif-slack`, `notif-tg`**: Markdown notifications, addressed by name. Two
  CLIs that share a design: write a Markdown template, name an account and a
  destination, send.
- **`wharf`**: SFTP deployment for static sites: explore, sync, move, archive,
  pull, diff, verify, restore, and (carefully) delete. See [tools/wharf/README.md](tools/wharf/README.md).
- **`docli`**: manage DigitalOcean droplets, VPCs, firewalls, DNS and databases from
  the command line, with a plan before every change. See [tools/docli/README.md](tools/docli/README.md).

- **`x`, `youtube`, `linkedin`, `instagram`**: the X, YouTube, LinkedIn and
  Instagram APIs, rebuilt from Mimir's tools on the same library: search and read,
  and publish (posts, replies, comments, with images). See
  [tools/x](tools/x/README.md), [tools/youtube](tools/youtube/README.md),
  [tools/linkedin](tools/linkedin/README.md) and
  [tools/instagram](tools/instagram/README.md).
- **`cloudflare`**: zones, DNS records (by domain name), cache purge, tunnels and
  Pages, on `apikit`, so any other endpoint is a few lines of TOML. Deleting a record
  needs an explicit `--max-safety destructive`. See
  [tools/cloudflare](tools/cloudflare/README.md).
- **`posts`** and **`media`**: the two that tie the publishing tools together.
  `posts` is the ledger of what `x`, `linkedin` and `instagram` have published (they
  record every post, and refuse to publish the same text twice); `media` fits an
  image to a platform's rules before you post it. See
  [tools/posts](tools/posts/README.md) and [tools/media](tools/media/README.md).
- **`reddit`**: search Reddit and read posts with their top comments. Built on
  [`apikit`](libs/apikit/README.md), where a new API endpoint is a few lines of
  TOML. See [tools/reddit/README.md](tools/reddit/README.md).
- **`agent-tools-mcp`**: one MCP server that gives Claude Code, Codex and Grok
  (and, over HTTP with OAuth, claude.ai and ChatGPT web) all of these tools,
  plus `docli` for DigitalOcean. See [mcp/README.md](mcp/README.md).

```bash
notif-slack post --account gierd --channel releases --template deploy \
                 --var service=api --var version=v2.3.1

notif-tg     post --account gierd --chat releases --template deploy \
                  --var service=api --var version=v2.3.1
```

| | | |
| --- | --- | --- |
| [`notif-slack`](tools/notif-slack) | Slack | post, read a channel or thread, reply in thread |
| [`notif-tg`](tools/notif-tg) | Telegram | send, read messages sent to the bot, watch, reply |
| [`wharf`](tools/wharf) | SFTP | explore, sync, deploy, move, archive, pull, diff, verify, restore, prune, delete |
| [`docli`](tools/docli) | DigitalOcean | droplets, VPCs, firewalls, DNS, databases; plan, apply |
| [`reddit`](tools/reddit) | Reddit | search, fetch a post with comments, any endpoint |
| [`cloudflare`](tools/cloudflare) | Cloudflare | zones, DNS records, cache purge, tunnels, Pages |
| [`posts`](tools/posts) | the post log | list, find, report, store and refresh metrics |
| [`media`](tools/media) | images | check and fit an image to Instagram, X or LinkedIn |
| [`x`](tools/x) | X | search, timelines, threads, profile and engagement metrics, post, reply |
| [`youtube`](tools/youtube) | YouTube | search videos, any endpoint |
| [`linkedin`](tools/linkedin) | LinkedIn | publish a post with an image |
| [`instagram`](tools/instagram) | Instagram | publish an image, comment, reply |

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

Every tool that needs credentials reads one shared file, `~/.config/agent-tools/config.toml`,
with a table per tool. See [docs/configuration.md](docs/configuration.md) for the
rules, where each tool looks, and how to move an existing config in.


Each tool writes its own starter config, at mode 0600:

```bash
notif-slack init   # adds [notif-slack] to ~/.config/agent-tools/config.toml — then add a bot token
notif-tg     init  # adds [notif-tg] to the same file                      — then add a BotFather token

notif-slack check  # verify the credentials
notif-tg     check
```

Then see [tools/notif-slack/README.md](tools/notif-slack/README.md) and
[tools/notif-tg/README.md](tools/notif-tg/README.md) for configuration, templates, reading
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
