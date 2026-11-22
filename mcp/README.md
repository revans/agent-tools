# agent-tools-mcp

One MCP server that gives your AI assistants the agent-tools CLIs: Slack,
Telegram, SFTP deploys (wharf) and DigitalOcean (docli). Anything else with a
command line can be added by writing a short TOML file.

It works with:

| Assistant | Subscription | How it connects | Status |
| --- | --- | --- | --- |
| **Claude Code** | Claude | local, stdio | tested |
| **Codex CLI** | ChatGPT | local, stdio | tested |
| **Grok Build CLI** | SuperGrok / X | local, stdio | registered, **not yet tested** (needs `grok login`) |
| **Claude Code, Codex** | | remote, HTTP + bearer token | tested on loopback |
| **claude.ai, Claude Desktop, mobile, ChatGPT web** | | remote, HTTP + OAuth | built and tested against the SDK's OAuth client; **not yet tried against the real apps** |

> The ChatGPT and Grok *web* apps cannot run a local server. Only their
> terminal agents (Codex, Grok Build) connect over stdio. The web apps need the
> remote setup at the end of this file.

## Contents

1. [How it works](#how-it-works)
2. [Quick start](#quick-start)
3. [Set up the tools' credentials](#set-up-the-tools-credentials)
4. [Connect each assistant](#connect-each-assistant)
5. [Using it](#using-it)
6. [Tool reference](#tool-reference)
7. [Safety](#safety)
8. [Adding your own tools](#adding-your-own-tools)
9. [Remote access: HTTP and OAuth](#remote-access-http-and-oauth)
10. [Troubleshooting](#troubleshooting)
11. [Reference: flags and environment](#reference-flags-and-environment)

## How it works

```
Claude Code / Codex / Grok ──stdio──►  agent-tools-mcp  ──exec──►  notif-slack
claude.ai / ChatGPT web ──HTTPS+OAuth─►  (one Go binary)           notif-tg, wharf, docli
```

The server holds a list of tools. Each is a fixed command line plus a few typed
parameters the model may fill in. When the assistant calls a tool, the server
checks the arguments, runs the CLI (no shell, no stdin), and returns its output.

Two rules shape everything else:

- **It runs each CLI in your current directory.** The assistant launches the
  server from your project directory, and the server never changes directory.
  `.wharf`, `notif-slack.toml` and a docli stack file are found exactly as they
  are from a shell there.
- **It never touches credentials.** Tokens stay in each CLI's own config file.
  No tool takes a password or token as an argument.

## Quick start

You need Go and Rust (`cargo`) installed, and at least one of `claude`, `codex`
or `grok` on your PATH.

```bash
git clone <this repo> && cd agent-tools
make mcp-register
```

That one command:

1. builds and installs `notif-slack`, `notif-tg`, `wharf`, `docli` and
   `agent-tools-mcp` into `~/.cargo/bin` (so make sure that is on your PATH);
2. registers the server with each of `claude` (user scope, so every project),
   `codex` and `grok` that it finds, and says which it skipped.

Check it:

```bash
agent-tools-mcp -list          # the tools it will serve (78; 88 with -allow destructive)
claude mcp list                # agent-tools: ... ✔ Connected
codex mcp list                 # agent-tools ... enabled
grok mcp list                  # agent-tools: ...
```

To undo: `make mcp-unregister`.

## Set up the tools' credentials

The server starts without any of this, but each tool needs its CLI configured.
Do it once, in a terminal, the same way you would without the server.

The Slack, Telegram and Reddit tools keep their accounts in **one shared file**,
`~/.config/agent-tools/config.toml`, with a table per tool. Its location, lookup
order and the rule that it must be mode 0600 are in
[docs/configuration.md](../docs/configuration.md). An existing notif config moves
in with `notif-slack migrate` and `notif-tg migrate`.

| Tools | Set up | Details |
| --- | --- | --- |
| `slack_*` | `notif-slack init` (adds a `[notif-slack]` table to the shared config file), then add a bot token; check with `notif-slack check` | [notif-slack/README.md](../tools/notif-slack/README.md) |
| `tg_*` | `notif-tg init` (adds `[notif-tg]`), add a BotFather token; check with `notif-tg check` | [notif-tg/README.md](../tools/notif-tg/README.md) |
| `wharf_*` | a `.wharf` file at the root of your site repo | [wharf/README.md](../tools/wharf/README.md) |
| `reddit_*` | `reddit init` (adds `[reddit]`), then add a Reddit app's client id and secret; check with `reddit check` | [reddit/README.md](../tools/reddit/README.md) |
| `youtube_*` | `youtube init`, then add a YouTube Data API key; check with `youtube check` | [youtube/README.md](../tools/youtube/README.md) |
| `x_*` | `x init`, then add the four OAuth 1.0a keys from the X developer portal; check with `x check` | [x/README.md](../tools/x/README.md) |
| `linkedin_*` | `linkedin init`, then paste an access token (about 60 days; no login flow yet); check with `linkedin check` | [linkedin/README.md](../tools/linkedin/README.md) |
| `instagram_*` | `instagram init`, then paste a long-lived token (about 60 days; no login flow yet); check with `instagram check` | [instagram/README.md](../tools/instagram/README.md) |
| `facebook_*` | `facebook init`, then add a Page access token and `page_id` (Pages only; see its README); check with `facebook check` | [facebook/README.md](../tools/facebook/README.md) |
| `cloudflare_*` | `cloudflare init`, then add a scoped API token (and `account_id` for tunnels and Pages); check with `cloudflare check` | [cloudflare/README.md](../tools/cloudflare/README.md) |
| `docli_*` | `DIGITALOCEAN_TOKEN`, or a token in `~/.config/docli/token` (mode 0600) | [docli/README.md](../tools/docli/README.md) |

Two of those are per project: **wharf** reads `.wharf` and **docli plan** reads
its stack file from the directory you launch the assistant in. Start your
assistant from the site or infrastructure repo you mean.

The CLIs may be launched with a smaller environment than your shell (Codex can
filter it). If a tool cannot see a token you exported, put it in the CLI's
config file (the notif tools also accept `token_command`), or give the server
the variable: `codex mcp add agent-tools --env KEY=VALUE -- agent-tools-mcp`.

## Connect each assistant

`make mcp-register` does all three. The manual commands are below, for when you
want one client, a different scope, or a different path.

### Claude Code (Claude subscription)

```bash
claude mcp add --scope user agent-tools -- agent-tools-mcp
claude mcp list                  # should say ✔ Connected
```

Use `--scope local` (the default) to limit it to the current project, or
`--scope project` to write a `.mcp.json` your team can commit. Tools appear as
`mcp__agent-tools__<name>`. To skip the permission prompt for the read-only
ones, allow them in `settings.json`, for example `mcp__agent-tools__slack_read`.

### Codex CLI (ChatGPT subscription)

```bash
codex mcp add agent-tools -- agent-tools-mcp
codex mcp list
```

This writes `[mcp_servers.agent-tools]` into `~/.codex/config.toml`. In the
list, `Auth: Unsupported` is normal for a local stdio server.

### Grok Build CLI (SuperGrok / X subscription)

```bash
grok login                       # once; or: grok login --device-code
grok mcp add agent-tools -- agent-tools-mcp
grok mcp list
```

This writes `~/.grok/config.toml`; use `--scope project` for `./.grok/config.toml`.
If Grok answers "Not signed in", the MCP side is fine: run `grok login` first.

### Check that it works

From a directory with no `.wharf`, ask any of them:

> Call the agent-tools tool `wharf_cat` with remote_file "index.html" and show
> me the result.

A correct run ends in an error that names **your current directory**:
`wharf: no .wharf file found in /your/dir or any parent directory`. That error
proves the call went through and ran where you launched the assistant.

## Using it

Just ask in plain language. The assistant picks the tool.

- "Post to #releases on the gierd Slack: v2.3.1 is out." → `slack_post`
- "What was said in #ops since yesterday?" → `slack_read`
- "What would `wharf sync` upload right now?" → `wharf_sync_plan`
- "Diff `index.html` against the server." → `wharf_diff`
- "List my droplets tagged `docli:managed`." → `docli_droplet_list`
- "What would applying my stack file change, and what does it cost?" → `docli_plan`

Tips:

- Ask for a **dry run** first on anything that sends or uploads (`slack_post`,
  `tg_post` and `wharf_copy` take `dry_run`).
- `tg_read` only peeks: it never marks messages read, so you can ask twice.
- If you do not know the account or channel keys, ask the assistant to run
  `slack_accounts` and `slack_channels` first.

## Tool reference

78 tools are on by default, and 88 with `-allow destructive`. **Read** tools
change nothing. **Write** tools change something reversible or additive.
**Destructive** tools do something public or permanent that cannot be undone:
the nine that publish to X, LinkedIn, Instagram and Facebook, and `cloudflare_dns_delete`,
which removes a public DNS record. They are **off by default**.
The server does not register them, so an assistant cannot see them, until you
start it with `-allow destructive`:

```bash
make mcp-register MCP_ALLOW=destructive      # registers the server with -allow destructive
# or by hand:
claude mcp add --scope user agent-tools -- agent-tools-mcp -allow destructive
```

Even then, your assistant's own permission prompt still stands between a request
and a post.

| Group | Tool | Safety | What it does |
| --- | --- | --- | --- |
| Slack | `slack_post` | write | Post Markdown, or a template with variables; supports threads and `dry_run` |
| | `slack_read` | read | Recent messages from a channel, with `since` / `until` |
| | `slack_thread` | read | One thread, by timestamp or permalink |
| | `slack_channels`, `slack_accounts`, `slack_templates` | read | What is configured |
| Telegram | `tg_post` | write | Send a message; supports replies, `silent`, `dry_run` |
| | `tg_read` | read | Messages sent to the bot (peek only) |
| | `tg_chats`, `tg_accounts`, `tg_templates` | read | What is configured |
| wharf | `wharf_sync_plan` | read | What `sync` would upload, without uploading |
| | `wharf_diff`, `wharf_cat` | read | Compare or print a remote file |
| | `wharf_prune_list` | read | Remote files with no local counterpart (never deletes) |
| | `wharf_verify` | read | Request each deployed file's public URL |
| | `wharf_restore_list` | read | Local pre-overwrite backups |
| | `wharf_copy` | write | Upload one file (backs up the old remote file first) |
| | `wharf_pull`, `wharf_mkdir`, `wharf_explore` | write | Download, create a directory, write `WHARF.md` |
| Reddit | `reddit_search`, `reddit_fetch` | read | Search; one post with its best comments |
| | `reddit_read` | read | Call any read-only Reddit endpoint by name, including ones you add under `~/.config/agent-tools/reddit/endpoints.d` |
| | `reddit_endpoints` | read | List endpoints or show one's parameters |
| YouTube | `youtube_search` | read | Search for videos (up to 10: title, channel, URL, snippet, date) |
| | `youtube_read`, `youtube_endpoints` | read | Call any read-only endpoint by name (videos, channels, ...); list endpoints |
| X | `x_search`, `x_timeline`, `x_thread`, `x_profile_metrics`, `x_engagement_baseline` | read | Search recent posts, an account's posts, a post's metrics and replies, your profile counts, average engagement |
| | `x_read`, `x_endpoints` | read | Call any read-only endpoint by name; list endpoints |
| | `x_post`, `x_reply` | **destructive** | Publish a post or reply, with an optional image. Public and permanent |
| LinkedIn | `linkedin_post` | **destructive** | Publish a post with an optional image. Public and permanent |
| | `linkedin_read`, `linkedin_endpoints` | read | Call any read-only endpoint by name; list endpoints |
| Instagram | `instagram_post`, `instagram_comment`, `instagram_reply` | **destructive** | Publish an image post, a comment, or a reply. Public and permanent |
| | `instagram_metrics` | read | One post's likes and comments, and with permission views, reach, saves, shares |
| | `instagram_read`, `instagram_endpoints` | read | Call any read-only endpoint by name; list endpoints |
| Post log | `posts_list`, `posts_show`, `posts_find`, `posts_report`, `posts_where` | read | What the publishing tools have published, searched, and summarised with metrics |
| | `posts_add`, `posts_metrics`, `posts_refresh` | write | Record a post made elsewhere, store a metrics reading, or read fresh metrics. They write to the log and never publish |
| Images | `media_profiles`, `media_info`, `media_check` | read | The platforms known, what an image is, and whether it fits a platform |
| | `media_fit` | write | Write a new copy of an image that satisfies a platform (never overwrites) |
| Facebook (Pages) | `facebook_posts`, `facebook_metrics` | read | A Page's published posts; a post's reactions, comments, shares and (with permission) views and reach |
| | `facebook_read`, `facebook_endpoints` | read | Call any read-only endpoint by name; list endpoints |
| | `facebook_post`, `facebook_photo`, `facebook_comment` | **destructive** | Publish a Page post, a photo, or a comment. Public and permanent |
| Cloudflare | `cloudflare_zones`, `cloudflare_dns_list`, `cloudflare_tunnels`, `cloudflare_check` | read | Your zones, a zone's DNS records (by domain name), tunnels, and the token's validity |
| | `cloudflare_read`, `cloudflare_endpoints` | read | Call any read-only Cloudflare endpoint by name; list endpoints |
| | `cloudflare_dns_add`, `cloudflare_dns_update`, `cloudflare_purge` | write | Create or change a DNS record; purge the cache. They change a live site |
| | `cloudflare_dns_delete` | **destructive** | Remove a public DNS record. Cannot be undone, and can take a site offline |
| docli | `docli_token_check`, `docli_sizes`, `docli_regions`, `docli_images` | read | Account and catalog |
| | `docli_droplet_list`, `docli_droplet_get`, `docli_vpc_list`, `docli_firewall_list`, `docli_ssh_key_list`, `docli_db_list`, `docli_domain_list`, `docli_dns_list` | read | Inventory |
| | `docli_plan` | read | What `apply` would change and cost. Exit code 2 ("there are changes") is a normal result |

**Left out on purpose:** `wharf` sync, delete, move, archive, chmod and
restore, and every `docli` command that changes your account. They ask for
confirmation, and a model cannot answer a prompt. Add them with a reviewed
manifest entry (below) when you want them.

## Safety

- **Safety levels.** Every tool is `read`, `write` or `destructive`. The server
  registers tools up to `-allow` (default `write`); anything above is not
  registered at all, so the model cannot see it. Run `agent-tools-mcp -allow
  read` for a read-only server, or `-allow destructive` to add the tools that
  publish. Clients also receive read-only and destructive hints so they can
  prompt you. A test asserts that the default server offers none of the
  destructive tools, and that each one says it is public and cannot be undone.
- **Every tool's flags are checked against the real CLI.** A test runs each
  built-in tool, with a value for every parameter, against its actual binary and
  fails on any flag or command the CLI does not know, so a typo in a manifest
  cannot reach an assistant.
- **No shell, no injection.** Arguments are checked against the manifest
  (types, enums, required, unknown names rejected). Flags go in as
  `--flag=value` and positionals after `--`, so a value like `-rf` is data, not
  a flag.
- **Nothing can prompt.** The CLIs get no stdin, so one that asks "are you
  sure?" fails instead of hanging.
- **Bounded.** Each call has a timeout (default 60s) and its output is capped at
  256 KB.
- **Your assistant's own permission prompts still apply** on top of this.
- **The risk that remains:** whoever controls a connected assistant can use
  every registered tool with your credentials. Keep `-allow` as low as the job
  needs, and read the consent warning on the OAuth page.

## Adding your own tools

Drop a `.toml` file into `~/.config/agent-tools-mcp/tools.d/` (or any directory
listed in `AGENT_TOOLS_MCP_TOOLS`, separated by `:`) and restart the assistant.
No rebuild. Run `agent-tools-mcp -list` to confirm it loaded; a mistake in the
file stops the server at start with the file and tool named.

```toml
[[tool]]
name = "docli_droplet_reboot"
description = "Reboot a droplet by name or id. It restarts immediately."
bin = "docli"                                   # on PATH, or next to agent-tools-mcp
args = ["droplet", "reboot", "--yes", "--json"] # fixed arguments, always passed
safety = "write"                                # read | write | destructive
timeout = "60s"                                 # optional, default 60s
ok_exit = [0]                                   # optional: exit codes that are not errors

  [[tool.param]]
  name = "droplet"
  type = "string"                               # string | integer | boolean | map
  positional = true                             # passed after "--"
  required = true
  description = "Droplet name or id."
```

How a parameter becomes an argument:

| `type` | In the manifest | Becomes |
| --- | --- | --- |
| `string` | `flag = "--name"` | `--name=value` |
| `string` | `positional = true` | `-- value` (in manifest order) |
| `integer` | `flag = "--limit"` | `--limit=5` |
| `boolean` | `flag = "--dry-run"` | `--dry-run` only when true |
| `map` | `flag = "--var"` | `--var=key=value`, one per entry |

Rules: tool names are unique (a duplicate is an error, never an override);
`description` is required on tools and parameters, because the model reads it to
decide when to call; flags must be long (`--like-this`); `enum = [...]` limits a
string. Only wrap commands that cannot prompt, and only pass `--yes` when the
command is safe to run unattended. The built-in set lives in
[`manifests/`](manifests/) and is a good template.

## Remote access: HTTP and OAuth

Use this to run the server on a cloud box or a PC in a closet, or to connect
the web apps. Everything below is optional; stdio needs none of it.

### Plain HTTP with a bearer token

```bash
agent-tools-mcp -gen-token ~/.config/agent-tools-mcp/token   # random token, mode 0600, never printed
agent-tools-mcp -http 127.0.0.1:8808 \
    -token-file ~/.config/agent-tools-mcp/token \
    -cwd ~/sites/mysite
```

The endpoint is `http://127.0.0.1:8808/mcp`; `/healthz` returns `ok` without a
token. Connect an assistant to it:

```bash
# Claude Code
claude mcp add --transport http --scope user agent-tools-remote https://HOST/mcp \
    --header "Authorization: Bearer $(cat ~/.config/agent-tools-mcp/token)"

# Codex: the token comes from an environment variable, never the command line
export AGENT_TOOLS_MCP_TOKEN="$(cat ~/.config/agent-tools-mcp/token)"
codex mcp add agent-tools-remote --url https://HOST/mcp --bearer-token-env-var AGENT_TOOLS_MCP_TOKEN

# Grok
grok mcp add --transport http agent-tools-remote https://HOST/mcp \
    --header "Authorization: Bearer $(cat ~/.config/agent-tools-mcp/token)"
```

(The Grok command is from `grok mcp add --help` and the xAI docs; it has not
been run against a server.)

What to know:

- **There is no client directory over HTTP.** Nobody launched the server, so
  every CLI runs in `-cwd`, and the credentials and `.wharf` must live on that
  host. Run one server per project directory if you need several.
- **The token** comes from `-token-file` (refused if other users can read it)
  or `AGENT_TOOLS_MCP_TOKEN`, never a flag, which would show in `ps`. At least
  32 characters. It is one shared secret with no rate limit.
- **Binding.** A non-loopback address needs TLS (`-tls-cert`, `-tls-key`) or
  `-insecure-http` on a network you trust; otherwise the server refuses to
  start. The usual setup is to bind `127.0.0.1` and put a TLS-terminating
  reverse proxy or tunnel in front, with `-behind-proxy` so the proxy's public
  `Host` header is accepted.
- **Stateless.** Each request stands alone, so a restart or a second instance
  loses nothing.

Keep it running with a user service. `~/.config/systemd/user/agent-tools-mcp.service`:

```ini
[Service]
ExecStart=%h/.cargo/bin/agent-tools-mcp -http 127.0.0.1:8808 -behind-proxy \
  -token-file %h/.config/agent-tools-mcp/token -cwd %h/sites/mysite
Restart=on-failure
[Install]
WantedBy=default.target
```

```bash
systemctl --user enable --now agent-tools-mcp
loginctl enable-linger "$USER"      # keep it running when you are logged out
```

### OAuth: claude.ai, Claude Desktop and mobile, ChatGPT web

The web apps call your server from their own servers and only accept OAuth, so
the server must be reachable at a public **https** address (a tunnel, or a
domain pointing at the box) and you must tell it that address:

```bash
agent-tools-mcp -http 127.0.0.1:8808 -behind-proxy \
    -token-file ~/.config/agent-tools-mcp/token -cwd ~/sites/mysite \
    -public-url https://mcp.example.com
```

Then in the app, add a **custom connector** with the URL
`https://mcp.example.com/mcp` and leave any client ID and secret blank. The app
discovers the server, registers itself, and opens a consent page on your
server. Type the server secret (the contents of the token file) and press
Approve. The static bearer token keeps working for the CLI clients beside it.

What it implements: discovery (RFC 9728 and RFC 8414, also at
`/.well-known/openid-configuration`), dynamic client registration, the
authorization-code flow with PKCE S256 only, resource indicators (tokens are
bound to `<public-url>/mcp`), and rotating refresh tokens. Reusing a spent
refresh token revokes that whole grant.

Decisions you should know about:

- **One owner, no accounts.** Approving a client needs the server secret. Five
  wrong secrets lock that address out of the consent page for 15 minutes.
- **No Client ID Metadata Documents.** They make the server fetch URLs that
  clients name. Apps that prefer them fall back to registration.
- **Redirect URIs** must be `https`, or `http` on a loopback host, matched
  exactly. Registering is not approving: nothing is granted until you type the
  secret.
- **State** (clients and token *hashes*) is one mode-0600 file,
  `~/.local/state/agent-tools-mcp/oauth.json` or `-oauth-state`. Delete it and
  restart to revoke every client and token. Rotating the token file does **not**
  revoke OAuth grants already issued. Access tokens last 1 hour, refresh tokens
  30 days, and at most 500 clients are kept.
- **Every approved client gets every registered tool.** There are no per-tool
  scopes. Treat the consent page like a login to the machine.

## Troubleshooting

| Symptom | Likely cause and fix |
| --- | --- |
| `claude mcp list` shows ✗ Failed | Run `agent-tools-mcp -list` in a terminal. A manifest error is printed there. Check that `~/.cargo/bin` is on the PATH the assistant sees. |
| Tool returns "`wharf` is not installed: it is not on PATH or next to agent-tools-mcp" | Run `make install`. Binaries are found on PATH, or in the same directory as `agent-tools-mcp`. |
| `wharf: no .wharf file found in /some/dir` | The assistant was started in a directory without a `.wharf`. Start it in the site repo. |
| `no config file found (looked at ./agent-tools.toml, ~/.config/agent-tools/config.toml, ...)` | Run `notif-slack init` (or the tool's own `init`) and add a token. See [credentials](#set-up-the-tools-credentials). |
| A token works in your shell but not through the assistant | The assistant filtered the environment. Put the credential in the CLI's config file, or pass it with `--env`. |
| `docli_plan` reports a result with "exit code 2" | Not an error: it means the plan has changes. |
| `x_post`, `linkedin_post`, an `instagram_*` or `facebook_*` publishing tool or `cloudflare_dns_delete` is missing | They are destructive and off by default. Re-register with `make mcp-register MCP_ALLOW=destructive`, or add `-allow destructive` to the server's command. |
| A tool you added does not appear | `agent-tools-mcp -list`. Check its `safety` is at or below `-allow`, then restart the assistant (tool lists are read at start). |
| `grok`: "Not signed in" | `grok login` (or `--device-code`). |
| Codex shows the call as "(failed)" | The CLI returned an error. The text of the error is in the result; it is not a connection fault. |
| HTTP: 401 | Wrong or missing bearer token. For OAuth clients, the access token may be revoked: reconnect the connector. |
| HTTP: 403 from a tunnel | Add `-behind-proxy`. |
| Server refuses to start: "not a loopback address and there is no TLS" | Bind `127.0.0.1` behind a proxy, add `-tls-cert/-tls-key`, or pass `-insecure-http` on a trusted network. |
| OAuth consent says "This request expired" | You took more than 10 minutes. Start the connection again. |

Run the server by hand to see its log (it writes to stderr, never stdout, which
belongs to the protocol): `agent-tools-mcp -http 127.0.0.1:8808 -token-file ...`.

## Reference: flags and environment

| Flag | Environment | Meaning |
| --- | --- | --- |
| `-allow LEVEL` | `AGENT_TOOLS_MCP_ALLOW` | Highest safety level to register: `read`, `write` (default), `destructive` |
| `-tools DIRS` | `AGENT_TOOLS_MCP_TOOLS` | Extra manifest directories, `:`-separated. `~/.config/agent-tools-mcp/tools.d` is always read |
| `-list` | | Print the tools that would be registered and exit |
| `-version` | | Print the version |
| `-http ADDR` | | Serve over HTTP at `ADDR` instead of stdio |
| `-token-file FILE` | `AGENT_TOOLS_MCP_TOKEN` | Bearer token for HTTP (the variable is a fallback) |
| `-gen-token FILE` | | Write a new random token to `FILE` (mode 0600) and exit |
| `-cwd DIR` | | HTTP only: the directory every CLI runs in |
| `-tls-cert`, `-tls-key` | | HTTP only: serve TLS directly |
| `-behind-proxy` | | HTTP only: a proxy or tunnel on this host terminates TLS |
| `-insecure-http` | | HTTP only: allow plain HTTP on a non-loopback address |
| `-public-url URL` | `AGENT_TOOLS_MCP_PUBLIC_URL` | HTTP only: the public https origin; turns OAuth on |
| `-oauth-state FILE` | | OAuth state file (default `~/.local/state/agent-tools-mcp/oauth.json`) |

Develop: `make test-mcp` runs `go vet` and the tests (`go test -race ./...` too).
`make mcp` builds just this binary into `target/release/`.
