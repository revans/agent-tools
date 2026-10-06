//! notif-tg — post Markdown templates to Telegram, and read what comes back.

mod config;
mod entities;
mod html;
mod state;
mod telegram;
mod template;

use anyhow::{Context, Result, bail};
use clap::{Args, Parser, Subcommand};
use config::{Account, Config, redact};
use serde_json::json;
use std::collections::BTreeMap;
use std::io::{IsTerminal, Read, Write};
use std::path::{Path, PathBuf};
use template::Template;

#[derive(Parser, Debug)]
#[command(
    name = "notif-tg",
    version,
    about = "Post Markdown templates to Telegram, and read replies",
    long_about = "Post Markdown templates to any Telegram bot and chat listed in an external \
                  TOML config (default: ~/.config/notif-tg/config.toml), and read the messages \
                  sent to those bots."
)]
struct Cli {
    /// Config file to use (else $NOTIF_TG_CONFIG, then ~/.config/notif-tg/config.toml,
    /// then ./notif-tg.toml).
    #[arg(long, short = 'C', global = true, value_name = "PATH")]
    config: Option<PathBuf>,

    #[command(subcommand)]
    cmd: Cmd,
}

#[derive(Subcommand, Debug)]
enum Cmd {
    /// Post a message to a chat.
    Post(PostArgs),
    /// Render a template to Telegram HTML without posting.
    Render(PostArgs),
    /// Read the messages waiting for a bot.
    Read(ReadArgs),
    /// Stream messages as they arrive.
    Watch(WatchArgs),
    /// List the bot accounts in the config.
    Accounts {
        #[arg(long)]
        json: bool,
    },
    /// List the chats configured for an account.
    Chats {
        account: Option<String>,
        #[arg(long)]
        json: bool,
    },
    /// List available Markdown templates.
    Templates {
        #[arg(long)]
        json: bool,
    },
    /// Verify a token with getMe, and every configured chat with getChat.
    Check {
        account: Option<String>,
        #[arg(long)]
        json: bool,
    },
    /// Show the chat ids of everyone who has messaged the bot, as TOML for the config.
    Discover {
        account: Option<String>,
        #[arg(long)]
        json: bool,
    },
    /// Write a starter config (and example templates) if none exists.
    Init {
        #[arg(long)]
        force: bool,
    },
    /// Print the resolved config, template and state paths.
    Where,
}

#[derive(Args, Debug)]
struct PostArgs {
    /// Bot account key, e.g. `gierd`.
    #[arg(long, short = 'a', value_name = "NAME")]
    account: Option<String>,
    /// Chat key from the config, or a raw numeric id / @channelusername.
    /// Group and channel ids are negative, so hyphens are allowed here.
    #[arg(long, short = 'c', value_name = "NAME", allow_hyphen_values = true)]
    chat: Option<String>,
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
    /// Reply to a message — this is what a Telegram thread is.
    #[arg(long, value_name = "MESSAGE_ID")]
    reply_to: Option<i64>,
    /// Forum topic to post into, for supergroups with topics enabled.
    #[arg(long, value_name = "TOPIC_ID")]
    topic: Option<i64>,
    /// Deliver without a notification sound.
    #[arg(long)]
    silent: bool,
    /// Suppress the link preview.
    #[arg(long, conflicts_with = "preview")]
    no_preview: bool,
    /// Force the link preview on.
    #[arg(long)]
    preview: bool,
    /// Leave unknown {{variables}} empty instead of failing.
    #[arg(long)]
    allow_missing: bool,
    /// Send as plain text, with no Markdown conversion and no formatting.
    #[arg(long)]
    raw: bool,
    /// Print what would be sent and exit.
    #[arg(long)]
    dry_run: bool,
    /// Machine-readable output.
    #[arg(long)]
    json: bool,
}

#[derive(Args, Debug)]
struct ReadArgs {
    #[arg(long, short = 'a', value_name = "NAME")]
    account: Option<String>,
    /// Show only messages from this chat. Other chats' messages are still consumed.
    #[arg(long, short = 'c', value_name = "NAME", allow_hyphen_values = true)]
    chat: Option<String>,
    /// Maximum messages to fetch (1-100).
    #[arg(long, short = 'n', default_value_t = 20, value_name = "N")]
    limit: u32,
    /// Do not advance the stored offset, so the same messages can be read again.
    #[arg(long)]
    peek: bool,
    /// Forget the stored offset first and re-read everything Telegram still holds.
    #[arg(long)]
    reset: bool,
    /// Print Telegram's raw text instead of rendering entities as Markdown.
    #[arg(long)]
    raw: bool,
    #[arg(long)]
    json: bool,
}

#[derive(Args, Debug)]
struct WatchArgs {
    #[arg(long, short = 'a', value_name = "NAME")]
    account: Option<String>,
    /// Show only messages from this chat. Other chats' messages are still consumed.
    #[arg(long, short = 'c', value_name = "NAME", allow_hyphen_values = true)]
    chat: Option<String>,
    /// Run this command for each message, with the Markdown on its stdin.
    #[arg(long, value_name = "COMMAND")]
    exec: Option<String>,
    /// Exit after the first batch that contains a message.
    #[arg(long)]
    once: bool,
    /// Seconds each long poll waits for something to arrive.
    #[arg(long, default_value_t = 25, value_name = "SECONDS")]
    poll: u32,
    /// Print Telegram's raw text instead of rendering entities as Markdown.
    #[arg(long)]
    raw: bool,
    /// One JSON object per message, one per line.
    #[arg(long)]
    json: bool,
}

fn main() {
    if let Err(e) = run() {
        eprintln!("notif-tg: {e:#}");
        std::process::exit(1);
    }
}

fn run() -> Result<()> {
    let cli = Cli::parse();
    let p = cli.config.as_deref();
    match cli.cmd {
        Cmd::Post(a) => cmd_post(p, a, true),
        Cmd::Render(a) => cmd_post(p, a, false),
        Cmd::Read(a) => cmd_read(p, a),
        Cmd::Watch(a) => cmd_watch(p, a),
        Cmd::Accounts { json } => cmd_accounts(p, json),
        Cmd::Chats { account, json } => cmd_chats(p, account.as_deref(), json),
        Cmd::Templates { json } => cmd_templates(p, json),
        Cmd::Check { account, json } => cmd_check(p, account.as_deref(), json),
        Cmd::Discover { account, json } => cmd_discover(p, account.as_deref(), json),
        Cmd::Init { force } => cmd_init(p, force),
        Cmd::Where => cmd_where(p),
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

    let chat_name = a.chat.clone().or_else(|| meta.and_then(|m| m.chat.clone()));
    let chat = account.resolve_chat(account_key, chat_name.as_deref())?;

    let messages = if a.raw {
        if markdown.trim().is_empty() {
            bail!("refusing to post an empty message");
        }
        vec![markdown.clone()]
    } else {
        html::to_messages(&markdown, html::MAX_MESSAGE_UNITS)
    };
    if messages.is_empty() {
        bail!("refusing to post an empty message");
    }
    if a.raw && html::units(&markdown) > html::MAX_MESSAGE_UNITS {
        bail!(
            "--raw message is {} characters; Telegram's limit is {}",
            html::units(&markdown),
            html::MAX_MESSAGE_UNITS
        );
    }

    let no_preview = if a.no_preview {
        true
    } else if a.preview {
        false
    } else {
        chat.no_preview.unwrap_or(false)
    };
    let silent = a.silent || chat.silent.unwrap_or(false);
    let topic = a.topic.or(chat.topic_id);

    if !send || a.dry_run {
        if a.json {
            println!(
                "{}",
                serde_json::to_string_pretty(&json!({
                    "ok": true,
                    "dry_run": true,
                    "account": account_key,
                    "chat_key": chat.key,
                    "chat_id": chat.id,
                    "ad_hoc_chat": chat.ad_hoc,
                    "template": tpl.as_ref().map(|t| t.name.clone()),
                    "messages": messages,
                }))?
            );
        } else {
            let mut out = std::io::stdout().lock();
            if send {
                writeln!(
                    out,
                    "notif-tg: dry run — would post to {account_key}/{} ({}){}\n",
                    chat.key,
                    chat.id,
                    if messages.len() > 1 {
                        format!(" as {} messages", messages.len())
                    } else {
                        String::new()
                    }
                )?;
            }
            for (i, m) in messages.iter().enumerate() {
                if i > 0 {
                    writeln!(out, "\n--- message {} ---", i + 1)?;
                }
                writeln!(out, "{m}")?;
            }
        }
        return Ok(());
    }

    let token = account.resolve_token(account_key)?;
    let client = telegram::Telegram::new(token)?;

    let mut sent = Vec::new();
    for (i, body) in messages.iter().enumerate() {
        if i > 0 {
            // Telegram throttles bursts into a group; pace the continuation messages.
            std::thread::sleep(std::time::Duration::from_millis(400));
        }
        let req = telegram::Send {
            chat_id: chat.id.clone(),
            html: body.clone(),
            topic_id: topic,
            // Only the first part answers the message being replied to.
            reply_to: if i == 0 { a.reply_to } else { None },
            silent,
            no_preview,
        };
        let s = client.send_message(&req).with_context(|| {
            if i == 0 {
                format!("posting to {account_key}/{}", chat.key)
            } else {
                format!(
                    "posting part {} of {} to {account_key}/{} (earlier parts were sent)",
                    i + 1,
                    messages.len(),
                    chat.key
                )
            }
        })?;
        sent.push(s);
    }

    if a.json {
        println!(
            "{}",
            serde_json::to_string_pretty(&json!({
                "ok": true,
                "account": account_key,
                "chat_key": chat.key,
                "chat_id": sent[0].chat_id,
                "template": tpl.as_ref().map(|t| t.name.clone()),
                "messages": sent.iter().map(|s| json!({
                    "message_id": s.message_id,
                    "link": s.link,
                })).collect::<Vec<_>>(),
            }))?
        );
    } else {
        let first = &sent[0];
        print!("notif-tg: posted to {account_key}/{}", chat.key);
        if sent.len() > 1 {
            print!(" in {} messages", sent.len());
        }
        match &first.link {
            Some(l) => println!(" — {l}"),
            None => println!(" (message {})", first.message_id),
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

// -------------------------------------------------------------------- reading

fn cmd_read(cfg_path: Option<&Path>, a: ReadArgs) -> Result<()> {
    let cfg = Config::load(cfg_path)?;
    let (name, account) = cfg.account(a.account.as_deref())?;
    let filter = match &a.chat {
        Some(c) => Some(account.resolve_chat(name, Some(c))?),
        None => None,
    };

    let offsets = state::Offsets::new(cfg.state_dir());
    if a.reset {
        offsets.clear(name)?;
    }
    let token = account.resolve_token(name)?;
    let client = telegram::Telegram::new(token)?;
    let msgs = client.get_updates(offsets.get(name), a.limit, 0)?;

    if let Some(last) = msgs.last()
        && !a.peek
    {
        offsets.set(name, last.update_id + 1)?;
    }

    let shown: Vec<&telegram::Msg> = msgs
        .iter()
        .filter(|m| match &filter {
            Some(f) => m.chat_id.to_string() == f.id,
            None => true,
        })
        .collect();

    if a.json {
        println!(
            "{}",
            serde_json::to_string_pretty(&json!({
                "ok": true,
                "account": name,
                "peeked": a.peek,
                "fetched": msgs.len(),
                "messages": shown.iter().map(|m| msg_json(m, account, a.raw)).collect::<Vec<_>>(),
            }))?
        );
        return Ok(());
    }
    if shown.is_empty() {
        println!("no new messages");
        if !msgs.is_empty() {
            println!("({} message(s) in other chats were consumed)", msgs.len());
        }
        return Ok(());
    }
    let mut out = std::io::stdout().lock();
    for m in shown {
        print_message(&mut out, m, account, a.raw)?;
    }
    Ok(())
}

fn cmd_watch(cfg_path: Option<&Path>, a: WatchArgs) -> Result<()> {
    let cfg = Config::load(cfg_path)?;
    let (name, account) = cfg.account(a.account.as_deref())?;
    let filter = match &a.chat {
        Some(c) => Some(account.resolve_chat(name, Some(c))?),
        None => None,
    };

    let offsets = state::Offsets::new(cfg.state_dir());
    let token = account.resolve_token(name)?;
    let client = telegram::Telegram::new(token)?;

    if !a.json {
        eprintln!(
            "notif-tg: watching {name}{} — Ctrl-C to stop",
            filter
                .as_ref()
                .map(|f| format!("/{}", f.key))
                .unwrap_or_default()
        );
    }

    let mut backoff = 1u64;
    loop {
        let msgs = match client.get_updates(offsets.get(name), 100, a.poll) {
            Ok(m) => {
                backoff = 1;
                m
            }
            Err(e) => {
                if telegram::api_error(&e).is_some_and(telegram::ApiError::is_fatal) {
                    return Err(e);
                }
                // A dropped long poll is normal; keep going, but slow down if it persists.
                eprintln!("notif-tg: {e:#}");
                std::thread::sleep(std::time::Duration::from_secs(backoff));
                backoff = (backoff * 2).min(60);
                continue;
            }
        };
        if let Some(last) = msgs.last() {
            offsets.set(name, last.update_id + 1)?;
        }

        let mut saw = false;
        for m in msgs.iter().filter(|m| match &filter {
            Some(f) => m.chat_id.to_string() == f.id,
            None => true,
        }) {
            saw = true;
            if a.json {
                println!("{}", serde_json::to_string(&msg_json(m, account, a.raw))?);
            } else {
                let mut out = std::io::stdout().lock();
                print_message(&mut out, m, account, a.raw)?;
            }
            std::io::stdout().flush().ok();
            if let Some(cmd) = &a.exec {
                run_handler(cmd, m, name, account)?;
            }
        }
        if saw && a.once {
            return Ok(());
        }
    }
}

/// Runs `--exec` with the message on stdin and its fields in the environment.
fn run_handler(cmd: &str, m: &telegram::Msg, account: &str, acc: &Account) -> Result<()> {
    use std::process::{Command, Stdio};
    let mut child = Command::new("sh")
        .arg("-c")
        .arg(cmd)
        .env("NOTIF_TG_ACCOUNT", account)
        .env("NOTIF_TG_CHAT_ID", m.chat_id.to_string())
        .env("NOTIF_TG_CHAT_KEY", acc.key_for_id(m.chat_id).unwrap_or(""))
        .env("NOTIF_TG_CHAT_TITLE", &m.chat_title)
        .env("NOTIF_TG_MESSAGE_ID", m.message_id.to_string())
        .env("NOTIF_TG_FROM", &m.from)
        .env(
            "NOTIF_TG_FROM_ID",
            m.from_id.map(|i| i.to_string()).unwrap_or_default(),
        )
        .env("NOTIF_TG_DATE", m.date.to_string())
        .env("NOTIF_TG_TEXT", &m.text)
        .stdin(Stdio::piped())
        .spawn()
        .with_context(|| format!("running --exec command: {cmd}"))?;
    if let Some(mut stdin) = child.stdin.take() {
        stdin.write_all(m.markdown.as_bytes()).ok();
    }
    let status = child.wait().context("waiting for --exec command")?;
    if !status.success() {
        eprintln!("notif-tg: --exec command exited with {status}");
    }
    Ok(())
}

fn msg_json(m: &telegram::Msg, acc: &Account, raw: bool) -> serde_json::Value {
    json!({
        "update_id": m.update_id,
        "kind": m.kind,
        "message_id": m.message_id,
        "date": m.date,
        "iso_time": iso_utc(m.date),
        "chat_id": m.chat_id,
        "chat_key": acc.key_for_id(m.chat_id),
        "chat_type": m.chat_type,
        "chat_title": m.chat_title,
        "from": m.from,
        "from_id": m.from_id,
        "from_username": m.from_username,
        "text": m.text,
        "markdown": if raw { m.text.clone() } else { m.markdown.clone() },
        "reply_to": m.reply_to,
        "topic_id": m.topic_id,
        "attachment": m.attachment,
    })
}

fn print_message(out: &mut impl Write, m: &telegram::Msg, acc: &Account, raw: bool) -> Result<()> {
    let chat = match acc.key_for_id(m.chat_id) {
        Some(k) => k.to_string(),
        None => format!("{} [{}]", m.chat_title, m.chat_id),
    };
    writeln!(
        out,
        "┌ {} in {chat} · {} · msg {}{}",
        m.from,
        iso_utc(m.date),
        m.message_id,
        m.reply_to.map(|r| format!(" ↩ {r}")).unwrap_or_default()
    )?;
    let body = if raw { &m.text } else { &m.markdown };
    if body.trim().is_empty() {
        writeln!(out, "│ ({})", m.attachment.as_deref().unwrap_or("no text"))?;
    } else {
        for line in body.split('\n') {
            writeln!(out, "│ {line}")?;
        }
        if let Some(att) = &m.attachment {
            writeln!(out, "│ [{att}]")?;
        }
    }
    writeln!(out, "└")?;
    Ok(())
}

/// Unix epoch -> `YYYY-MM-DD HH:MM:SS UTC`.
///
/// UTC rather than local time: no timezone database, and an unambiguous
/// timestamp beats a plausible wrong one.
fn iso_utc(epoch: i64) -> String {
    let days = epoch.div_euclid(86_400);
    let secs = epoch.rem_euclid(86_400);
    let (y, m, d) = civil_from_days(days);
    format!(
        "{y:04}-{m:02}-{d:02} {:02}:{:02}:{:02} UTC",
        secs / 3600,
        (secs % 3600) / 60,
        secs % 60
    )
}

/// Howard Hinnant's days-from-civil, inverted.
fn civil_from_days(z: i64) -> (i64, u32, u32) {
    let z = z + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z.rem_euclid(146_097);
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let y = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = (doy - (153 * mp + 2) / 5 + 1) as u32;
    let m = if mp < 10 { mp + 3 } else { mp - 9 } as u32;
    (if m <= 2 { y + 1 } else { y }, m, d)
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
                    "bot": acc.bot,
                    "default": cfg.default_account.as_deref() == Some(name.as_str()),
                    "default_chat": acc.default_chat,
                    "chats": acc.chat_names(),
                    "token_source": token_source(acc),
                })
            })
            .collect();
        println!(
            "{}",
            serde_json::to_string_pretty(&json!({ "accounts": rows }))?
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
        if let Some(b) = &acc.bot {
            println!("  bot: {b}");
        }
        println!("  token: {}", token_source(acc));
        println!(
            "  chats: {}",
            if acc.chats.is_empty() {
                "(none)".to_string()
            } else {
                acc.chat_names().join(", ")
            }
        );
        if let Some(d) = &acc.default_chat {
            println!("  default chat: {d}");
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

fn cmd_chats(cfg_path: Option<&Path>, account: Option<&str>, json: bool) -> Result<()> {
    let cfg = Config::load(cfg_path)?;
    let (name, acc) = cfg.account(account)?;
    if json {
        let rows: Vec<_> = acc
            .chats
            .iter()
            .map(|(key, entry)| {
                json!({
                    "chat": key,
                    "id": entry.id(),
                    "description": entry.description(),
                    "default": acc.default_chat.as_deref() == Some(key.as_str()),
                })
            })
            .collect();
        println!(
            "{}",
            serde_json::to_string_pretty(&json!({"account": name, "chats": rows}))?
        );
        return Ok(());
    }
    if acc.chats.is_empty() {
        println!("account {name} has no chats configured (try `notif-tg discover {name}`)");
        return Ok(());
    }
    let width = acc.chats.keys().map(String::len).max().unwrap_or(0);
    for (key, entry) in &acc.chats {
        let marker = if acc.default_chat.as_deref() == Some(key.as_str()) {
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
                    "chat": t.meta.chat,
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
        match (&t.meta.account, &t.meta.chat) {
            (Some(a), Some(c)) => println!("  posts to: {a}/{c}"),
            (None, Some(c)) => println!("  posts to chat: {c}"),
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
        let client = acc
            .resolve_token(name)
            .and_then(telegram::Telegram::new)
            .and_then(|c| c.get_me().map(|id| (c, id)));
        match client {
            Ok((client, id)) => {
                if !json {
                    println!("{name}: ok — @{} ({})", id.username, id.name);
                    if !id.reads_all_group_messages {
                        println!(
                            "  note: privacy mode is on, so in groups the bot only sees commands and replies to it"
                        );
                    }
                }
                let mut chats = Vec::new();
                for (key, entry) in &acc.chats {
                    match client.get_chat(entry.id()) {
                        Ok(v) => {
                            let title = v
                                .get("title")
                                .or_else(|| v.get("username"))
                                .or_else(|| v.get("first_name"))
                                .and_then(serde_json::Value::as_str)
                                .unwrap_or("")
                                .to_string();
                            if !json {
                                println!("  {key}: ok — {title}");
                            }
                            chats.push(json!({"chat": key, "ok": true, "title": title}));
                        }
                        Err(e) => {
                            failed = true;
                            if !json {
                                println!("  {key}: FAILED — {e:#}");
                            }
                            chats
                                .push(json!({"chat": key, "ok": false, "error": format!("{e:#}")}));
                        }
                    }
                }
                rows.push(json!({
                    "account": name, "ok": true, "bot": id.username, "bot_id": id.id,
                    "reads_all_group_messages": id.reads_all_group_messages, "chats": chats,
                }));
            }
            Err(e) => {
                failed = true;
                if !json {
                    println!("{name}: FAILED — {e:#}");
                }
                rows.push(json!({"account": name, "ok": false, "error": format!("{e:#}")}));
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

/// Telegram gives a bot no way to list its chats, so the only way to learn a
/// chat id is to look at who has written to it.
fn cmd_discover(cfg_path: Option<&Path>, account: Option<&str>, json: bool) -> Result<()> {
    let cfg = Config::load(cfg_path)?;
    let (name, acc) = cfg.account(account)?;
    let token = acc.resolve_token(name)?;
    let client = telegram::Telegram::new(token)?;
    // Deliberately does not advance the stored offset: discovering must not
    // swallow messages that `read` has not seen yet.
    let msgs = client.get_updates(None, 100, 0)?;

    let mut seen: BTreeMap<i64, (String, String)> = BTreeMap::new();
    for m in &msgs {
        seen.entry(m.chat_id)
            .or_insert((m.chat_title.clone(), m.chat_type.clone()));
    }

    if json {
        let rows: Vec<_> = seen
            .iter()
            .map(|(id, (title, kind))| {
                json!({
                    "id": id, "title": title, "type": kind,
                    "configured_as": acc.key_for_id(*id),
                })
            })
            .collect();
        println!(
            "{}",
            serde_json::to_string_pretty(&json!({"account": name, "chats": rows}))?
        );
        return Ok(());
    }
    if seen.is_empty() {
        println!("no chats seen.");
        println!(
            "Send the bot a message (or add it to a group and post there), then run this again."
        );
        println!("Telegram only keeps undelivered updates for about 24 hours.");
        return Ok(());
    }
    println!("# paste under [accounts.{name}.chats]");
    for (id, (title, kind)) in &seen {
        let existing = acc
            .key_for_id(*id)
            .map(|k| format!("  # already configured as {k}"))
            .unwrap_or_default();
        println!(
            "{} = \"{id}\"  # {kind}: {title}{existing}",
            toml_key(title)
        );
    }
    Ok(())
}

/// Chat titles are free text, so make a usable key and quote anything unusual.
fn toml_key(title: &str) -> String {
    let mut slug = String::new();
    for c in title.to_lowercase().chars() {
        if c.is_ascii_alphanumeric() {
            slug.push(c);
        } else if !slug.ends_with('-') {
            slug.push('-');
        }
    }
    let slug = slug.trim_matches('-');
    if slug.is_empty() {
        "chat".to_string()
    } else {
        slug.to_string()
    }
}

// ------------------------------------------------------------------- scaffold

fn cmd_where(cfg_path: Option<&Path>) -> Result<()> {
    match Config::load(cfg_path) {
        Ok(cfg) => {
            println!("config:    {}", cfg.path.display());
            println!("templates: {}", cfg.template_dir().display());
            println!("state:     {}", cfg.state_dir().display());
            let offsets = state::Offsets::new(cfg.state_dir());
            for name in cfg.account_names() {
                let at = offsets.location(name);
                let pos = match offsets.get(name) {
                    Some(n) => format!("next update {n}"),
                    None => "nothing read yet".to_string(),
                };
                println!("  {name}: {pos} ({})", at.display());
            }
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

    println!("notif-tg: wrote {}", path.display());
    println!("notif-tg: templates live in {}", tpl_dir.display());
    println!("notif-tg: add a bot token from @BotFather, then run `notif-tg check`");
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
        if !cfg.accounts.values().any(|a| a.token.is_some()) {
            return;
        }
        if let Ok(md) = std::fs::metadata(&cfg.path)
            && md.permissions().mode() & 0o077 != 0
        {
            eprintln!(
                "notif-tg: warning: {} holds inline tokens and is group/world accessible; run: chmod 600 {}",
                cfg.path.display(),
                cfg.path.display()
            );
        }
    }
    #[cfg(not(unix))]
    let _ = cfg;
}

const EXAMPLE_CONFIG: &str = include_str!("../examples/config.toml");

/// Shipped with the binary so `notif-tg init` produces something usable immediately.
const EXAMPLE_TEMPLATES: &[(&str, &str)] = &[
    ("deploy.md", include_str!("../examples/templates/deploy.md")),
    (
        "incident.md",
        include_str!("../examples/templates/incident.md"),
    ),
    ("reply.md", include_str!("../examples/templates/reply.md")),
];

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn epoch_formats_as_utc() {
        assert_eq!(iso_utc(0), "1970-01-01 00:00:00 UTC");
        assert_eq!(iso_utc(1_700_000_000), "2023-11-14 22:13:20 UTC");
        assert_eq!(iso_utc(1_759_000_000), "2025-09-27 19:06:40 UTC");
        // Leap day, and a pre-epoch timestamp.
        assert_eq!(iso_utc(1_709_164_800), "2024-02-29 00:00:00 UTC");
        assert_eq!(iso_utc(-1), "1969-12-31 23:59:59 UTC");
    }

    #[test]
    fn chat_titles_become_usable_toml_keys() {
        assert_eq!(toml_key("Release Alerts"), "release-alerts");
        assert_eq!(toml_key("  "), "chat");
        assert_eq!(toml_key("Ops / On-call"), "ops-on-call");
    }
}
