# Configuration

Every agent-tools tool that needs credentials or accounts reads one shared file,
`agent-tools.toml`, with a table per tool. One file to secure, back up and move
to a new machine.

```toml
# ~/.config/agent-tools/config.toml   (mode 0600)

[notif-slack]
default_account = "gierd"
[notif-slack.accounts.gierd]
workspace = "gierd.slack.com"
token_command = "pass show slack/gierd"
[notif-slack.accounts.gierd.channels]
releases = "C0123456789"

[notif-tg]
default_account = "gierd"
[notif-tg.accounts.gierd]
token_env = "TELEGRAM_TOKEN"

[reddit]
default = "main"
[reddit.accounts.main]
client_id_env = "REDDIT_CLIENT_ID"
client_secret_command = "pass show reddit/secret"
```

A tool's table holds exactly what its own file would hold at the top level. Moving
a tool in or out is adding or removing the `[tool]` prefix on its headers, and
`TOOL migrate` does it for you.

## Which tools use it

| Tool | Reads |
| --- | --- |
| `notif-slack`, `notif-tg` (Rust), `reddit` and the other API tools (Go) | the shared file, with their own older file as a fallback |
| `wharf` | its own `.wharf` in each site repo. That is per-project on purpose: a site's credentials belong to its repo |
| `docli` | `DIGITALOCEAN_TOKEN`, or `~/.config/docli/token` |
| `agent-tools-mcp` | `AGENT_TOOLS_MCP_TOKEN` or `-token-file`, plus the OAuth state file. It holds the server's own secrets, not the tools' |

## Where it looks

First match wins:

1. `--config PATH`
2. `$TOOL_CONFIG` (for example `NOTIF_SLACK_CONFIG`, `REDDIT_CONFIG`)
3. `./agent-tools.toml`, **if it has the tool's table**: settings for one project
4. `~/.config/agent-tools/config.toml`, **if it has the tool's table**
   (`$XDG_CONFIG_HOME` is honored)
5. `~/.config/TOOL/config.toml`: the tool's own older file
6. `./TOOL.toml`

"If it has the tool's table" means a project file that configures only `reddit`
does not hide your Slack settings in the user file. A file given with `--config`
or `$TOOL_CONFIG` can be either kind; the tool tells which by looking for its
table. A shared file that does not parse is an error, never skipped.

`TOOL where` shows which file was used, whether it is shared, and every place it
looked.

## Tools' own files and directories

A tool keeps templates and similar files next to its config. In the shared file
each tool gets a subdirectory, so two tools never share a templates folder:

```
~/.config/agent-tools/
├── config.toml
├── notif-slack/templates/deploy.md
├── notif-tg/templates/deploy.md
└── reddit/endpoints.d/my-endpoints.toml
```

A relative `template_dir` or `state_dir` in a tool's table resolves against that
subdirectory. Extra API endpoints for the Go tools go in
`~/.config/agent-tools/TOOL/endpoints.d/` (or `~/.config/TOOL/endpoints.d/`, or a
directory in `$TOOL_ENDPOINTS`).

## Secrets

Any credential can be written three ways, and exactly one may be present:

```toml
client_id         = "literal value"
client_id_env     = "NAME_OF_ENV_VAR"
client_id_command = "pass show service/key"     # stdout is the value
```

The file may hold every tool's credentials, so **a shared file that other users
can read is refused** (`chmod 600` it). That is stricter than a tool's own file,
which keeps its old behavior. Preferring `_env` and `_command` keeps the file
free of secrets altogether, and is the safer choice for a file that gathers
everything in one place.

An assistant may start a tool with a smaller environment than your shell (Codex
can filter it), so a credential in `_env` that you only export in your shell may
not be there. A `_command`, or a literal value, does not depend on that.

## Moving to the shared file

```bash
notif-slack migrate     # adds [notif-slack] to ~/.config/agent-tools/config.toml
notif-tg migrate
```

`migrate` copies the tool's own file into the shared file (comments kept, meaning
checked), and copies its templates to `~/.config/agent-tools/TOOL/templates`.
It leaves the old file where it is, and says so. From then on the shared file wins;
delete the old file once `TOOL check` passes. It refuses, changing nothing, if the
shared file already has the table.

`TOOL init` writes a new tool's starter into the shared file the same way,
creating it at mode 0600 if needed. `TOOL init --config FILE` still writes a
tool's own file.

## One set of rules, two implementations

The Rust tools use `libs/agent-config` and the Go tools use `libs/apikit`. They
are separate code that follow these same rules, and their tests run the same
scenarios, so a change to the rules means changing both.
