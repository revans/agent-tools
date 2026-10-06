---
name: slack-post
description: Post Markdown messages to Slack with the `notif` CLI. Use whenever the user wants something sent, posted, announced, or shared to Slack — a channel, a workspace, a deploy/release note, an incident update, a standup, a status report, a summary of work just finished, or "tell the team". Also use to list configured Slack accounts/channels/templates, to check Slack credentials, or to create and edit `notif` Markdown templates.
allowed-tools: Bash, Read, Write, Edit, Glob
---

# Posting to Slack with `notif`

`notif` is a Rust CLI that renders a Markdown template and posts it to a named
Slack workspace and channel. Accounts, channels and credentials live in one
external TOML file; nothing is hard-coded and no token is ever passed on the
command line.

Addressing is always **account + channel**, both by friendly name:
`--account gierd` is `gierd.slack.com`, `--channel releases` is whatever id that
account's config maps `releases` to.

## Before the first call

Check the tool is present: `notif --version`.

If it is missing, build and install it from the project:
`cd /home/revans/Work/PersonalLab/notif && cargo install --path . --locked`

If `notif where` reports no config, the user has not set up credentials yet. Run
`notif init`, tell them to put a bot token in the file it created, and stop —
you cannot invent a token.

## Workflow

1. **Find the destination.** Never guess an account or channel name.

   ```bash
   notif accounts --json            # accounts, their workspaces, default channel
   notif channels <account> --json  # channel keys -> ids for one account
   ```

   If the user named a channel that is not in the list, say so and show the
   available ones. Only use `notif discover <account>` (which asks Slack for
   every visible channel) when the user wants to add a channel to the config.

2. **Pick or write the message.**

   ```bash
   notif templates --json   # name, description, required vars, destination
   ```

   Use an existing template when one fits — it carries the house style and the
   right default channel. Pass values with repeated `--var key=value`:

   ```bash
   notif post -t deploy -v service=api -v version=v2.3.1 -v env=staging
   ```

   `notif templates --json` lists each template's `declared_vars` with
   `required` and `default`. Supply every required one; a missing value is an
   error, not a silent blank.

   For a one-off message, write the Markdown yourself with `--text` (or pipe it
   on stdin for anything multi-line — quoting long Markdown in a shell argument
   is where this goes wrong):

   ```bash
   notif post -a gierd -c alerts --text '**Build 412 failed** — see [logs](https://ci/412)'

   notif post -a gierd -c general <<'MSG'
   ## Weekly summary

   - Shipped the new importer
   - Cut p95 latency from 400ms to 120ms
   MSG
   ```

3. **Preview when the destination or wording is at all uncertain.** `--dry-run`
   renders exactly what would be sent and posts nothing:

   ```bash
   notif post -t incident -v title="Checkout 500s" -v summary="..." --dry-run
   ```

   Show the user that output and get a yes before posting. Post straight away
   only when the user already named the channel and the content — "post this to
   #alerts on gierd" is a go; "let the team know when you're done" is a
   dry-run first.

4. **Post, then report the link.** `notif post … --json` returns
   `{"ok":true,"channel":…,"ts":…,"permalink":…}`. Give the user the permalink.
   Keep the `ts` if you may need to reply in-thread: `--thread <ts>`.

Posting is irreversible and visible to other people. Post once; if a command
seems to hang, check Slack before retrying rather than sending a duplicate.

## Writing the Markdown

Write normal Markdown — `notif` translates it to Slack's `mrkdwn` (headings
become bold, `**bold**` becomes `*bold*`, links become `<url|text>`, tables
become aligned code blocks, `@here` becomes a real broadcast). See
`references/mrkdwn.md` for the full mapping and the handful of things that do
not survive.

Keep it short. Lead with the thing that matters, use a heading and a few
bullets, and let the permalink carry the detail. Do not open with "Hi team" or
close with a sign-off unless the user asked for one.

## Creating a template

When the user will send the same shape of message again, write a template
instead of a one-off. Put it in the directory `notif where` reports:

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

Frontmatter is TOML between `---` fences and is optional. `account` and
`channel` there become the defaults for that template; a `--var` on the command
line always wins. A var with no `default` is required. Verify a new template
with `notif render -t <name> -v …` before posting with it.

## When a command fails

`notif` prints the reason and a hint, and exits non-zero. The common ones:

| Message | What to do |
| --- | --- |
| `unknown account` / `unknown channel` | List them and ask; do not guess an id. |
| `missing values for: …` | Supply those `--var`s; ask the user if you cannot derive them. |
| `not_in_channel` | The bot is not in that channel — ask the user to `/invite` it. |
| `invalid_auth` / `token_revoked` | The stored token is bad. Tell the user; never edit a token yourself. |
| `missing_scope` | The named scope must be added to the Slack app and the app reinstalled. |

Config layout, token sources and multi-workspace setup are in
`references/config.md`. Full flag list: `notif post --help`.
