---
name: slack-post
description: Post Markdown messages to Slack, read a channel or thread, and reply in thread with the `notif-slack` CLI. Use whenever the user wants something sent, posted, announced, or shared to Slack — a channel, a workspace, a deploy/release note, an incident update, a standup, a status report, a summary of work just finished, or "tell the team". Also use to read, catch up on or summarise what was said in a Slack channel or thread, to reply to a message in its thread, to list configured Slack accounts/channels/templates, to check Slack credentials, or to create and edit `notif-slack` Markdown templates.
allowed-tools: Bash, Read, Write, Edit, Glob
---

# Posting to and reading Slack with `notif-slack`

`notif-slack` is a Rust CLI that renders a Markdown template and posts it to a named
Slack workspace and channel, and reads messages back out of one. Accounts,
channels and credentials live in one external TOML file; nothing is hard-coded
and no token is ever passed on the command line.

Addressing is always **account + channel**, both by friendly name:
`--account gierd` is `gierd.slack.com`, `--channel releases` is whatever id that
account's config maps `releases` to.

## Before the first call

Check the tool is present: `notif-slack --version`.

If it is missing, build and install it from the project:
`cd /home/revans/Work/PersonalLab/notif-slack && cargo install --path . --locked`

If `notif-slack where` reports no config, the user has not set up credentials yet. Run
`notif-slack init`, tell them to put a bot token in the file it created, and stop —
you cannot invent a token.

## Workflow

1. **Find the destination.** Never guess an account or channel name.

   ```bash
   notif-slack accounts --json            # accounts, their workspaces, default channel
   notif-slack channels <account> --json  # channel keys -> ids for one account
   ```

   If the user named a channel that is not in the list, say so and show the
   available ones. Only use `notif-slack discover <account>` (which asks Slack for
   every visible channel) when the user wants to add a channel to the config.

2. **Pick or write the message.**

   ```bash
   notif-slack templates --json   # name, description, required vars, destination
   ```

   Use an existing template when one fits — it carries the house style and the
   right default channel. Pass values with repeated `--var key=value`:

   ```bash
   notif-slack post -t deploy -v service=api -v version=v2.3.1 -v env=staging
   ```

   `notif-slack templates --json` lists each template's `declared_vars` with
   `required` and `default`. Supply every required one; a missing value is an
   error, not a silent blank.

   For a one-off message, write the Markdown yourself with `--text` (or pipe it
   on stdin for anything multi-line — quoting long Markdown in a shell argument
   is where this goes wrong):

   ```bash
   notif-slack post -a gierd -c alerts --text '**Build 412 failed** — see [logs](https://ci/412)'

   notif-slack post -a gierd -c general <<'MSG'
   ## Weekly summary

   - Shipped the new importer
   - Cut p95 latency from 400ms to 120ms
   MSG
   ```

3. **Preview when the destination or wording is at all uncertain.** `--dry-run`
   renders exactly what would be sent and posts nothing:

   ```bash
   notif-slack post -t incident -v title="Checkout 500s" -v summary="..." --dry-run
   ```

   Show the user that output and get a yes before posting. Post straight away
   only when the user already named the channel and the content — "post this to
   #alerts on gierd" is a go; "let the team know when you're done" is a
   dry-run first.

4. **Post, then report the link.** `notif-slack post … --json` returns
   `{"ok":true,"channel":…,"ts":…,"permalink":…}`. Give the user the permalink.
   Keep the `ts` if you may need to reply in-thread: `--reply-to <ts>`.

Posting is irreversible and visible to other people. Post once; if a command
seems to hang, check Slack before retrying rather than sending a duplicate.

## Reading a channel

`notif-slack read` fetches recent messages. Addressing is exactly the same as posting
— same account keys, same channel keys, same default channel:

```bash
notif-slack read -a gierd -c alerts            # 20 most recent, oldest at the top
notif-slack read -c alerts -n 50               # more of them
notif-slack read -c alerts --since 2025-01-13  # since a date, or an ISO datetime, or a ts
notif-slack read -c alerts --since 2025-01-13T09:00:00Z --until 2025-01-13T17:00:00Z
```

Each message prints the author, the local time, its `ts`, the text converted
back to Markdown, and `↳ N replies` when it has a thread. `--raw` prints
Slack's own `mrkdwn` instead and resolves no names.

To work with the messages rather than read them, use `--json`:

```json
{"ok":true,"account":"gierd","channel":"C0100000001","channel_key":"alerts",
 "count":2,"messages":[{"ts":"1718031234.567890","iso_time":"2024-06-10T08:53:54-06:00",
 "user_id":"U01ABCDEF","user_name":"alice","text":"Deployed *api*","markdown":"Deployed **api**",
 "thread_ts":null,"reply_count":3,"permalink":"https://gierd.slack.com/archives/…"}]}
```

`permalink` is only filled in when the account sets `workspace` in the config.

Reading needs the bot to be **in** the channel and the app to have
`channels:history` (`groups:history` for private channels). `users:read` is
what turns `<@U01ABCDEF>` into `@alice`; without it the raw id is shown rather
than a guess.

Summarising a channel: read it with `--json`, then say what happened in your own
words. Do not paste the whole transcript back at the user.

## Replying in a thread

The useful loop is **read → pick a `ts` → `--reply-to`**:

```bash
notif-slack read -c alerts --json                      # find the message, copy its "ts"
notif-slack post -c alerts --reply-to 1718031234.567890 --text 'Looking at it now'
```

A Slack permalink works in place of the `ts`, and carries the channel with it,
so a message the user pasted needs nothing else:

```bash
notif-slack post --reply-to 'https://gierd.slack.com/archives/C0100000001/p1718031234567890' \
           --text 'On it — deploying the fix.'
```

- If `--channel` is also given and points somewhere else, the command fails
  rather than guessing. Drop one of the two.
- Replying to a message that is itself a reply puts your message in that same
  thread, not in a new one.
- Add `--broadcast` to also show the reply in the channel. Use it sparingly —
  it notifies everyone in the channel.
- `--thread <ts>` is the lower-level form and still works; `--reply-to` is the
  one to reach for, because it also takes a permalink.

To read one thread, with the parent message first:

```bash
notif-slack thread -c alerts 1718031234.567890
notif-slack thread 'https://gierd.slack.com/archives/C0100000001/p1718031234567890' --json
```

A permalink to any reply in a thread resolves to the whole thread.

## Writing the Markdown

Write normal Markdown — `notif-slack` translates it to Slack's `mrkdwn` (headings
become bold, `**bold**` becomes `*bold*`, links become `<url|text>`, tables
become aligned code blocks, `@here` becomes a real broadcast). See
`references/mrkdwn.md` for the full mapping and the handful of things that do
not survive.

Keep it short. Lead with the thing that matters, use a heading and a few
bullets, and let the permalink carry the detail. Do not open with "Hi team" or
close with a sign-off unless the user asked for one.

## Creating a template

When the user will send the same shape of message again, write a template
instead of a one-off. Put it in the directory `notif-slack where` reports:

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
with `notif-slack render -t <name> -v …` before posting with it.

## When a command fails

`notif-slack` prints the reason and a hint, and exits non-zero. The common ones:

| Message | What to do |
| --- | --- |
| `unknown account` / `unknown channel` | List them and ask; do not guess an id. |
| `missing values for: …` | Supply those `--var`s; ask the user if you cannot derive them. |
| `not_in_channel` | The bot is not in that channel — ask the user to `/invite` it. Reading needs this too. |
| `message_not_found` / `thread_not_found` | That `ts` is not in that channel. Re-read the channel and take the `ts` from the output. |
| `invalid_auth` / `token_revoked` | The stored token is bad. Tell the user; never edit a token yourself. |
| `missing_scope` | The named scope must be added to the Slack app and the app reinstalled. |

Config layout, token sources, scopes and multi-workspace setup are in
`references/config.md`. Full flag lists: `notif-slack post --help`,
`notif-slack read --help`, `notif-slack thread --help`.
