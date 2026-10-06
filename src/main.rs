//! notif — post Markdown templates to any configured Slack workspace and channel.

mod config;
mod mrkdwn;
mod slack;
mod template;

use anyhow::{Context, Result, bail};
use clap::{Args, Parser, Subcommand};
use config::{Account, Config, redact};
use serde_json::json;
use std::collections::BTreeMap;
use std::io::{IsTerminal, Read, Write};
use std::path::{Path, PathBuf};
use template::Template;

/// Slack's hard cap on a single message.
const MAX_MESSAGE_CHARS: usize = 40_000;

#[derive(Parser, Debug)]
#[command(
    name = "notif",
    version,
    about = "Post Markdown templates to Slack",
    long_about = "Post Markdown templates to any Slack workspace and channel listed in an \
                  external TOML config (default: ~/.config/notif/config.toml)."
)]
struct Cli {
    /// Config file to use (else $NOTIF_CONFIG, then ~/.config/notif/config.toml, then ./notif.toml).
    #[arg(long, short = 'C', global = true, value_name = "PATH")]
    config: Option<PathBuf>,

    #[command(subcommand)]
    cmd: Cmd,
}

#[derive(Subcommand, Debug)]
enum Cmd {
    /// Post a message to a channel.
    Post(PostArgs),
    /// Render a template to Slack mrkdwn without posting.
    Render(PostArgs),
    /// List the accounts in the config.
    Accounts {
        #[arg(long)]
        json: bool,
    },
    /// List the channels configured for an account.
    Channels {
        account: Option<String>,
        #[arg(long)]
        json: bool,
    },
    /// List available Markdown templates.
    Templates {
        #[arg(long)]
        json: bool,
    },
    /// Verify a token by calling auth.test (all accounts when none is named).
    Check {
        account: Option<String>,
        #[arg(long)]
        json: bool,
    },
    /// Ask Slack which channels an account's token can see, as TOML to paste into the config.
    Discover {
        account: Option<String>,
        /// Include private channels the token is a member of.
        #[arg(long)]
        private: bool,
        /// Only channels the bot has already joined.
        #[arg(long)]
        member_only: bool,
        #[arg(long)]
        json: bool,
    },
    /// Write a starter config (and example template) if none exists.
    Init {
        /// Overwrite an existing config file.
        #[arg(long)]
        force: bool,
    },
    /// Print the resolved config path and template directory.
    Where,
}

#[derive(Args, Debug)]
struct PostArgs {
    /// Account key, e.g. `gierd` for gierd.slack.com.
    #[arg(long, short = 'a', value_name = "NAME")]
    account: Option<String>,
    /// Channel key from the config, or a raw `C…` id / `#name`.
    #[arg(long, short = 'c', value_name = "NAME")]
    channel: Option<String>,
    /// Template name (from the template dir) or path to a .md file.
    #[arg(long, short = 't', value_name = "NAME|PATH")]
    template: Option<String>,
    /// Markdown file to post as-is.
    #[arg(long, short = 'f', value_name = "PATH")]
    file: Option<PathBuf>,
    /// Literal Markdown to post.
    #[arg(long, short = 'm', value_name = "MARKDOWN")]
    text: Option<String>,
    /// Template variable, repeatable: --var service=api
    #[arg(long, short = 'v', value_name = "KEY=VALUE")]
    var: Vec<String>,
    /// Template variables as a JSON object.
    #[arg(long, value_name = "JSON")]
    vars_json: Option<String>,
    /// Template variables from a JSON file.
    #[arg(long, value_name = "PATH")]
    vars_file: Option<PathBuf>,
    /// Reply inside a thread (parent message `ts`).
    #[arg(long, value_name = "TS")]
    thread: Option<String>,
    /// With --thread, also show the reply in the channel.
    #[arg(long)]
    broadcast: bool,
    /// Suppress link/media previews.
    #[arg(long, conflicts_with = "unfurl")]
    no_unfurl: bool,
    /// Force link/media previews on.
    #[arg(long)]
    unfurl: bool,
    /// Override the sender name (needs chat:write.customize).
    #[arg(long, value_name = "NAME")]
    username: Option<String>,
    /// Override the sender icon, e.g. :rocket:
    #[arg(long, value_name = "EMOJI")]
    icon_emoji: Option<String>,
    /// Leave unknown {{variables}} empty instead of failing.
    #[arg(long)]
    allow_missing: bool,
    /// Send the text exactly as given, skipping Markdown conversion.
    #[arg(long)]
    raw: bool,
    /// Print what would be sent and exit.
    #[arg(long)]
    dry_run: bool,
    /// Machine-readable output.
    #[arg(long)]
    json: bool,
}

fn main() {
    if let Err(e) = run() {
        eprintln!("notif: {e:#}");
        std::process::exit(1);
    }
}

fn run() -> Result<()> {
    let cli = Cli::parse();
    let cfg_path = cli.config.as_deref();
    match cli.cmd {
        Cmd::Post(a) => cmd_post(cfg_path, a, true),
        Cmd::Render(a) => cmd_post(cfg_path, a, false),
        Cmd::Accounts { json } => cmd_accounts(cfg_path, json),
        Cmd::Channels { account, json } => cmd_channels(cfg_path, account.as_deref(), json),
        Cmd::Templates { json } => cmd_templates(cfg_path, json),
        Cmd::Check { account, json } => cmd_check(cfg_path, account.as_deref(), json),
        Cmd::Discover {
            account,
            private,
            member_only,
            json,
        } => cmd_discover(cfg_path, account.as_deref(), private, member_only, json),
        Cmd::Init { force } => cmd_init(cfg_path, force),
        Cmd::Where => cmd_where(cfg_path),
    }
}

// ---------------------------------------------------------------- post / render

fn cmd_post(cfg_path: Option<&Path>, a: PostArgs, send: bool) -> Result<()> {
    let cfg = Config::load(cfg_path)?;
    warn_on_loose_permissions(&cfg);

    let (markdown, tpl) = load_source(&cfg, &a)?;
    let meta = tpl.as_ref().map(|t| &t.meta);

    let account_name = a
        .account
        .clone()
        .or_else(|| meta.and_then(|m| m.account.clone()));
    let (account_key, account) = cfg.account(account_name.as_deref())?;

    let channel_name = a
        .channel
        .clone()
        .or_else(|| meta.and_then(|m| m.channel.clone()));
    let channel = account.resolve_channel(account_key, channel_name.as_deref())?;

    let text = if a.raw {
        markdown.clone()
    } else {
        mrkdwn::to_mrkdwn(&markdown)
    };
    if text.trim().is_empty() {
        bail!("refusing to post an empty message");
    }
    if text.chars().count() > MAX_MESSAGE_CHARS {
        bail!(
            "message is {} characters; Slack's limit is {MAX_MESSAGE_CHARS}",
            text.chars().count()
        );
    }

    let username = a
        .username
        .clone()
        .or_else(|| meta.and_then(|m| m.username.clone()))
        .or_else(|| channel.username.clone());
    let icon_emoji = a
        .icon_emoji
        .clone()
        .or_else(|| meta.and_then(|m| m.icon_emoji.clone()))
        .or_else(|| channel.icon_emoji.clone());
    let icon_url = meta
        .and_then(|m| m.icon_url.clone())
        .or_else(|| channel.icon_url.clone());

    let unfurl = if a.no_unfurl {
        Some(false)
    } else if a.unfurl {
        Some(true)
    } else {
        None
    };

    let post = slack::Post {
        channel: channel.id.clone(),
        text: text.clone(),
        mrkdwn: true,
        thread_ts: a.thread.clone(),
        reply_broadcast: a.broadcast.then_some(true),
        unfurl_links: unfurl,
        unfurl_media: unfurl,
        username,
        icon_emoji,
        icon_url,
    };

    if !send || a.dry_run {
        if a.json {
            println!(
                "{}",
                serde_json::to_string_pretty(&json!({
                    "ok": true,
                    "dry_run": true,
                    "account": account_key,
                    "channel_key": channel.key,
                    "channel": channel.id,
                    "ad_hoc_channel": channel.ad_hoc,
                    "template": tpl.as_ref().map(|t| t.name.clone()),
                    "text": text,
                }))?
            );
        } else {
            let mut out = std::io::stdout().lock();
            if send {
                writeln!(
                    out,
                    "notif: dry run — would post to {account_key}/{} ({})\n",
                    channel.key, channel.id
                )?;
            }
            writeln!(out, "{text}")?;
        }
        return Ok(());
    }

    let token = account.resolve_token(account_key)?;
    let client = slack::Slack::new(token)?;
    let posted = client.post_message(&post)?;

    if a.json {
        println!(
            "{}",
            serde_json::to_string_pretty(&json!({
                "ok": true,
                "account": account_key,
                "channel_key": channel.key,
                "channel": posted.channel,
                "ad_hoc_channel": channel.ad_hoc,
                "ts": posted.ts,
                "permalink": posted.permalink,
                "template": tpl.as_ref().map(|t| t.name.clone()),
            }))?
        );
    } else {
        print!("notif: posted to {account_key}/{}", channel.key);
        match &posted.permalink {
            Some(l) => println!(" — {l}"),
            None => println!(" (ts {})", posted.ts),
        }
    }
    Ok(())
}

/// Exactly one of --template / --file / --text / stdin supplies the Markdown.
fn load_source(cfg: &Config, a: &PostArgs) -> Result<(String, Option<Template>)> {
    let chosen = [a.template.is_some(), a.file.is_some(), a.text.is_some()]
        .iter()
        .filter(|b| **b)
        .count();
    if chosen > 1 {
        bail!("--template, --file and --text are mutually exclusive");
    }

    if let Some(spec) = &a.template {
        let t = Template::resolve(spec, &cfg.template_dir())?;
        let rendered = t.render(&collect_vars(a)?, a.allow_missing)?;
        return Ok((rendered, Some(t)));
    }
    if let Some(path) = &a.file {
        let t = Template::from_file(path)?;
        let rendered = t.render(&collect_vars(a)?, a.allow_missing)?;
        return Ok((rendered, Some(t)));
    }
    if let Some(text) = &a.text {
        let t = Template::parse("<inline>", None, text)?;
        let rendered = t.render(&collect_vars(a)?, a.allow_missing)?;
        return Ok((rendered, None));
    }

    if std::io::stdin().is_terminal() {
        bail!("nothing to post: pass --template, --file, --text, or pipe Markdown on stdin");
    }
    let mut buf = String::new();
    std::io::stdin()
        .read_to_string(&mut buf)
        .context("reading Markdown from stdin")?;
    if buf.trim().is_empty() {
        bail!("stdin was empty");
    }
    let t = Template::parse("<stdin>", None, &buf)?;
    let rendered = t.render(&collect_vars(a)?, a.allow_missing)?;
    Ok((rendered, Some(t)))
}

fn collect_vars(a: &PostArgs) -> Result<BTreeMap<String, String>> {
    let mut vars = BTreeMap::new();
    if let Some(path) = &a.vars_file {
        let raw =
            std::fs::read_to_string(path).with_context(|| format!("reading {}", path.display()))?;
        merge_json_vars(&mut vars, &raw).with_context(|| format!("parsing {}", path.display()))?;
    }
    if let Some(raw) = &a.vars_json {
        merge_json_vars(&mut vars, raw).context("parsing --vars-json")?;
    }
    for pair in &a.var {
        let (k, v) = pair
            .split_once('=')
            .with_context(|| format!("--var needs KEY=VALUE, got \"{pair}\""))?;
        let k = k.trim();
        if k.is_empty() {
            bail!("--var has an empty key: \"{pair}\"");
        }
        vars.insert(k.to_string(), v.to_string());
    }
    Ok(vars)
}

fn merge_json_vars(vars: &mut BTreeMap<String, String>, raw: &str) -> Result<()> {
    let v: serde_json::Value = serde_json::from_str(raw)?;
    let obj = v
        .as_object()
        .context("expected a JSON object of variable names to values")?;
    for (k, val) in obj {
        let s = match val {
            serde_json::Value::String(s) => s.clone(),
            serde_json::Value::Null => String::new(),
            other => other.to_string(),
        };
        vars.insert(k.clone(), s);
    }
    Ok(())
}

// ------------------------------------------------------------------- listings

fn cmd_accounts(cfg_path: Option<&Path>, json: bool) -> Result<()> {
    let cfg = Config::load(cfg_path)?;
    if json {
        let rows: Vec<_> = cfg
            .accounts
            .iter()
            .map(|(name, acc)| {
                json!({
                    "account": name,
                    "workspace": acc.workspace,
                    "default": cfg.default_account.as_deref() == Some(name.as_str()),
                    "default_channel": acc.default_channel,
                    "channels": acc.channel_names(),
                    "token_source": token_source(acc),
                })
            })
            .collect();
        println!(
            "{}",
            serde_json::to_string_pretty(&json!({"accounts": rows}))?
        );
        return Ok(());
    }
    for (name, acc) in &cfg.accounts {
        let marker = if cfg.default_account.as_deref() == Some(name.as_str()) {
            " (default)"
        } else {
            ""
        };
        println!("{name}{marker}");
        if let Some(w) = &acc.workspace {
            println!("  workspace: {w}");
        }
        println!("  token: {}", token_source(acc));
        println!(
            "  channels: {}",
            if acc.channels.is_empty() {
                "(none)".to_string()
            } else {
                acc.channel_names().join(", ")
            }
        );
        if let Some(d) = &acc.default_channel {
            println!("  default channel: {d}");
        }
    }
    Ok(())
}

fn token_source(acc: &Account) -> String {
    if let Some(t) = &acc.token {
        format!("inline ({})", redact(t))
    } else if let Some(e) = &acc.token_env {
        format!("env ${e}")
    } else if let Some(c) = &acc.token_command {
        format!("command `{c}`")
    } else {
        "MISSING".to_string()
    }
}

fn cmd_channels(cfg_path: Option<&Path>, account: Option<&str>, json: bool) -> Result<()> {
    let cfg = Config::load(cfg_path)?;
    let (name, acc) = cfg.account(account)?;
    if json {
        let rows: Vec<_> = acc
            .channels
            .iter()
            .map(|(key, entry)| {
                json!({
                    "channel": key,
                    "id": entry.id(),
                    "description": entry.description(),
                    "default": acc.default_channel.as_deref() == Some(key.as_str()),
                })
            })
            .collect();
        println!(
            "{}",
            serde_json::to_string_pretty(&json!({"account": name, "channels": rows}))?
        );
        return Ok(());
    }
    if acc.channels.is_empty() {
        println!("account {name} has no channels configured (try `notif discover {name}`)");
        return Ok(());
    }
    let width = acc.channels.keys().map(String::len).max().unwrap_or(0);
    for (key, entry) in &acc.channels {
        let marker = if acc.default_channel.as_deref() == Some(key.as_str()) {
            "*"
        } else {
            " "
        };
        let desc = entry
            .description()
            .map(|d| format!("  — {d}"))
            .unwrap_or_default();
        println!("{marker} {key:<width$}  {}{desc}", entry.id());
    }
    Ok(())
}

fn cmd_templates(cfg_path: Option<&Path>, json: bool) -> Result<()> {
    let cfg = Config::load(cfg_path)?;
    let dir = cfg.template_dir();
    let templates = template::list(&dir)?;
    if json {
        let rows: Vec<_> = templates
            .iter()
            .map(|t| {
                let vars: Vec<_> = t
                    .meta
                    .vars
                    .iter()
                    .map(|(name, spec)| {
                        json!({
                            "name": name,
                            "description": spec.description(),
                            "required": spec.required(),
                            "default": spec.default(),
                        })
                    })
                    .collect();
                json!({
                    "name": t.name,
                    "path": t.path.as_ref().map(|p| p.display().to_string()),
                    "description": t.meta.description,
                    "account": t.meta.account,
                    "channel": t.meta.channel,
                    "declared_vars": vars,
                    "placeholders": t.placeholders(),
                })
            })
            .collect();
        println!(
            "{}",
            serde_json::to_string_pretty(&json!({
                "template_dir": dir.display().to_string(),
                "templates": rows
            }))?
        );
        return Ok(());
    }
    if templates.is_empty() {
        println!("no templates in {}", dir.display());
        return Ok(());
    }
    for t in &templates {
        println!("{}", t.name);
        if let Some(d) = &t.meta.description {
            println!("  {d}");
        }
        let vars = t.placeholders();
        if !vars.is_empty() {
            println!("  vars: {}", vars.join(", "));
        }
        match (&t.meta.account, &t.meta.channel) {
            (Some(a), Some(c)) => println!("  posts to: {a}/{c}"),
            (None, Some(c)) => println!("  posts to channel: {c}"),
            _ => {}
        }
    }
    Ok(())
}

// --------------------------------------------------------------- network ops

fn cmd_check(cfg_path: Option<&Path>, account: Option<&str>, json: bool) -> Result<()> {
    let cfg = Config::load(cfg_path)?;
    warn_on_loose_permissions(&cfg);
    let names: Vec<String> = match account {
        Some(a) => vec![cfg.account(Some(a))?.0.to_string()],
        None => cfg.account_names().iter().map(|s| s.to_string()).collect(),
    };

    let mut rows = Vec::new();
    let mut failed = false;
    for name in &names {
        let acc = &cfg.accounts[name];
        let result = acc
            .resolve_token(name)
            .and_then(|t| slack::Slack::new(t)?.auth_test());
        match result {
            Ok(id) => {
                rows.push(json!({
                    "account": name, "ok": true, "team": id.team, "team_id": id.team_id,
                    "user": id.user, "user_id": id.user_id, "bot_id": id.bot_id, "url": id.url,
                }));
                if !json {
                    println!("{name}: ok — {} as {} ({})", id.team, id.user, id.user_id);
                }
            }
            Err(e) => {
                failed = true;
                rows.push(json!({"account": name, "ok": false, "error": format!("{e:#}")}));
                if !json {
                    println!("{name}: FAILED — {e:#}");
                }
            }
        }
    }
    if json {
        println!(
            "{}",
            serde_json::to_string_pretty(&json!({"ok": !failed, "accounts": rows}))?
        );
    }
    if failed {
        std::process::exit(1);
    }
    Ok(())
}

fn cmd_discover(
    cfg_path: Option<&Path>,
    account: Option<&str>,
    private: bool,
    member_only: bool,
    json: bool,
) -> Result<()> {
    let cfg = Config::load(cfg_path)?;
    let (name, acc) = cfg.account(account)?;
    let token = acc.resolve_token(name)?;
    let channels: Vec<_> = slack::Slack::new(token)?
        .conversations(private)?
        .into_iter()
        .filter(|c| !member_only || c.is_member)
        .collect();

    if json {
        let rows: Vec<_> = channels
            .iter()
            .map(|c| {
                json!({"name": c.name, "id": c.id, "private": c.is_private, "is_member": c.is_member})
            })
            .collect();
        println!(
            "{}",
            serde_json::to_string_pretty(&json!({"account": name, "channels": rows}))?
        );
        return Ok(());
    }
    println!("# paste under [accounts.{name}.channels]");
    for c in &channels {
        let note = match (c.is_private, c.is_member) {
            (true, _) => "  # private",
            (false, false) => "  # bot not in channel",
            _ => "",
        };
        println!("{} = \"{}\"{note}", toml_key(&c.name), c.id);
    }
    Ok(())
}

/// Channel names are already TOML-safe in practice, but quote anything unusual.
fn toml_key(name: &str) -> String {
    if !name.is_empty()
        && name
            .chars()
            .all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_')
    {
        name.to_string()
    } else {
        format!("\"{}\"", name.replace('\\', "\\\\").replace('"', "\\\""))
    }
}

// ------------------------------------------------------------------- scaffold

fn cmd_where(cfg_path: Option<&Path>) -> Result<()> {
    match Config::load(cfg_path) {
        Ok(cfg) => {
            println!("config:    {}", cfg.path.display());
            println!("templates: {}", cfg.template_dir().display());
            println!("accounts:  {}", cfg.account_names().join(", "));
        }
        Err(e) => {
            println!("config:    not found ({e:#})");
            println!("expected:  {}", config::default_config_path().display());
        }
    }
    Ok(())
}

fn cmd_init(cfg_path: Option<&Path>, force: bool) -> Result<()> {
    let path = match cfg_path {
        Some(p) => p.to_path_buf(),
        None => config::default_config_path(),
    };
    if path.exists() && !force {
        bail!(
            "{} already exists (use --force to overwrite)",
            path.display()
        );
    }
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent)
            .with_context(|| format!("creating {}", parent.display()))?;
    }
    write_private(&path, EXAMPLE_CONFIG)?;

    let tpl_dir = path.parent().unwrap_or(Path::new(".")).join("templates");
    std::fs::create_dir_all(&tpl_dir).with_context(|| format!("creating {}", tpl_dir.display()))?;
    for (name, body) in EXAMPLE_TEMPLATES {
        let dest = tpl_dir.join(name);
        if !dest.exists() {
            std::fs::write(&dest, body).with_context(|| format!("writing {}", dest.display()))?;
        }
    }

    println!("notif: wrote {}", path.display());
    println!("notif: templates live in {}", tpl_dir.display());
    println!("notif: edit the config, then run `notif check`");
    Ok(())
}

/// Writes with mode 0600 so a token never lands in a world-readable file.
#[cfg(unix)]
fn write_private(path: &Path, contents: &str) -> Result<()> {
    use std::os::unix::fs::OpenOptionsExt;
    let mut f = std::fs::OpenOptions::new()
        .write(true)
        .create(true)
        .truncate(true)
        .mode(0o600)
        .open(path)
        .with_context(|| format!("writing {}", path.display()))?;
    f.write_all(contents.as_bytes())
        .with_context(|| format!("writing {}", path.display()))
}

#[cfg(not(unix))]
fn write_private(path: &Path, contents: &str) -> Result<()> {
    std::fs::write(path, contents).with_context(|| format!("writing {}", path.display()))
}

/// A config holding inline tokens should not be readable by anyone else.
fn warn_on_loose_permissions(cfg: &Config) {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let has_inline_token = cfg.accounts.values().any(|a| a.token.is_some());
        if !has_inline_token {
            return;
        }
        if let Ok(md) = std::fs::metadata(&cfg.path) {
            let mode = md.permissions().mode() & 0o077;
            if mode != 0 {
                eprintln!(
                    "notif: warning: {} holds inline tokens and is group/world accessible; run: chmod 600 {}",
                    cfg.path.display(),
                    cfg.path.display()
                );
            }
        }
    }
    #[cfg(not(unix))]
    let _ = cfg;
}

const EXAMPLE_CONFIG: &str = include_str!("../examples/config.toml");

/// Shipped with the binary so `notif init` produces something usable immediately.
const EXAMPLE_TEMPLATES: &[(&str, &str)] = &[
    ("deploy.md", include_str!("../examples/templates/deploy.md")),
    (
        "incident.md",
        include_str!("../examples/templates/incident.md"),
    ),
    (
        "standup.md",
        include_str!("../examples/templates/standup.md"),
    ),
];
