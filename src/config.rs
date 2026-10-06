//! Loading and resolution of the external credentials/config TOML.

use anyhow::{Context, Result, anyhow, bail};
use serde::Deserialize;
use std::collections::BTreeMap;
use std::path::{Path, PathBuf};

/// Top level of `config.toml`.
#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Config {
    /// Bot account used when `--account` is omitted. Optional when only one exists.
    #[serde(default)]
    pub default_account: Option<String>,
    /// Where `--template <name>` looks for `<name>.md`. Defaults to `<config dir>/templates`.
    #[serde(default)]
    pub template_dir: Option<String>,
    /// Where `read`/`watch` remember how far they have consumed.
    /// Defaults to `$XDG_STATE_HOME/notif-tg`.
    #[serde(default)]
    pub state_dir: Option<String>,
    #[serde(default)]
    pub accounts: BTreeMap<String, Account>,

    #[serde(skip)]
    pub path: PathBuf,
}

/// One Telegram bot. The account key is how you address it: `--account gierd`.
#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Account {
    /// Purely informational, e.g. "@gierd_notify_bot".
    #[serde(default)]
    pub bot: Option<String>,
    /// Bot token from @BotFather. Exactly one token source is required.
    #[serde(default)]
    pub token: Option<String>,
    /// Name of an environment variable holding the token.
    #[serde(default)]
    pub token_env: Option<String>,
    /// Shell command printing the token on stdout (e.g. `pass show telegram/gierd`).
    #[serde(default)]
    pub token_command: Option<String>,
    /// Chat key used when `--chat` is omitted.
    #[serde(default)]
    pub default_chat: Option<String>,
    /// Friendly key -> chat id (numeric, or `@channelusername`).
    #[serde(default)]
    pub chats: BTreeMap<String, ChatEntry>,
}

/// A chat is either a bare id string or a table with per-chat defaults.
#[derive(Debug, Deserialize)]
#[serde(untagged)]
pub enum ChatEntry {
    Id(String),
    Table(ChatTable),
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ChatTable {
    /// Numeric chat id (supergroups and channels are negative) or `@channelusername`.
    pub id: String,
    #[serde(default)]
    pub description: Option<String>,
    /// Forum topic to post into, for supergroups with topics enabled.
    #[serde(default)]
    pub topic_id: Option<i64>,
    /// Post without a notification sound by default.
    #[serde(default)]
    pub silent: Option<bool>,
    /// Suppress link previews by default.
    #[serde(default)]
    pub no_preview: Option<bool>,
}

impl ChatEntry {
    pub fn id(&self) -> &str {
        match self {
            ChatEntry::Id(s) => s,
            ChatEntry::Table(t) => &t.id,
        }
    }
    pub fn description(&self) -> Option<&str> {
        match self {
            ChatEntry::Id(_) => None,
            ChatEntry::Table(t) => t.description.as_deref(),
        }
    }
}

/// A chat after `--chat` has been matched against the account's chat map.
#[derive(Debug, Clone)]
pub struct ResolvedChat {
    /// What the user typed.
    pub key: String,
    /// What gets sent to Telegram as `chat_id`.
    pub id: String,
    /// True when the key was not in the config and is being passed through verbatim.
    pub ad_hoc: bool,
    pub topic_id: Option<i64>,
    pub silent: Option<bool>,
    pub no_preview: Option<bool>,
}

impl Config {
    /// Resolution order: `--config`, `$NOTIF_TG_CONFIG`, `$XDG_CONFIG_HOME/notif-tg/config.toml`,
    /// `~/.config/notif-tg/config.toml`, then `./notif-tg.toml`.
    pub fn locate(explicit: Option<&Path>) -> Result<PathBuf> {
        if let Some(p) = explicit {
            if !p.exists() {
                bail!("config file not found: {}", p.display());
            }
            return Ok(p.to_path_buf());
        }
        if let Ok(p) = std::env::var("NOTIF_TG_CONFIG") {
            let p = PathBuf::from(expand_tilde(&p));
            if !p.exists() {
                bail!("NOTIF_TG_CONFIG points at a missing file: {}", p.display());
            }
            return Ok(p);
        }
        let candidates = [default_config_path(), PathBuf::from("notif-tg.toml")];
        for c in &candidates {
            if c.exists() {
                return Ok(c.clone());
            }
        }
        Err(anyhow!(
            "no config file found (looked at {}). Run `notif-tg init` to create one.",
            candidates
                .iter()
                .map(|c| c.display().to_string())
                .collect::<Vec<_>>()
                .join(", ")
        ))
    }

    pub fn load(explicit: Option<&Path>) -> Result<Config> {
        let path = Self::locate(explicit)?;
        let raw = std::fs::read_to_string(&path)
            .with_context(|| format!("reading {}", path.display()))?;
        let mut cfg: Config =
            toml::from_str(&raw).with_context(|| format!("parsing {}", path.display()))?;
        cfg.path = path;
        if cfg.accounts.is_empty() {
            bail!("{} defines no [accounts.*] tables", cfg.path.display());
        }
        if let Some(d) = &cfg.default_account
            && !cfg.accounts.contains_key(d)
        {
            bail!(
                "default_account = \"{d}\" is not defined in {}",
                cfg.path.display()
            );
        }
        Ok(cfg)
    }

    /// Directory holding the config file; templates and relative paths hang off it.
    pub fn dir(&self) -> PathBuf {
        self.path
            .parent()
            .map(Path::to_path_buf)
            .unwrap_or_else(|| PathBuf::from("."))
    }

    pub fn template_dir(&self) -> PathBuf {
        match &self.template_dir {
            Some(t) => {
                let p = PathBuf::from(expand_tilde(t));
                if p.is_absolute() {
                    p
                } else {
                    self.dir().join(p)
                }
            }
            None => self.dir().join("templates"),
        }
    }

    pub fn state_dir(&self) -> PathBuf {
        match &self.state_dir {
            Some(t) => {
                let p = PathBuf::from(expand_tilde(t));
                if p.is_absolute() {
                    p
                } else {
                    self.dir().join(p)
                }
            }
            None => default_state_dir(),
        }
    }

    pub fn account(&self, name: Option<&str>) -> Result<(&str, &Account)> {
        let key = match name {
            Some(n) => n.to_string(),
            None => match &self.default_account {
                Some(d) => d.clone(),
                None if self.accounts.len() == 1 => self.accounts.keys().next().unwrap().clone(),
                None => bail!(
                    "no account given and no default_account set; known accounts: {}",
                    self.account_names().join(", ")
                ),
            },
        };
        match self.accounts.get_key_value(key.as_str()) {
            Some((k, a)) => Ok((k.as_str(), a)),
            None => bail!(
                "unknown account \"{key}\"; known accounts: {}",
                self.account_names().join(", ")
            ),
        }
    }

    pub fn account_names(&self) -> Vec<&str> {
        self.accounts.keys().map(String::as_str).collect()
    }
}

impl Account {
    /// Reads the token from whichever source the account declares.
    pub fn resolve_token(&self, account_name: &str) -> Result<String> {
        let sources = [
            self.token.is_some(),
            self.token_env.is_some(),
            self.token_command.is_some(),
        ]
        .iter()
        .filter(|b| **b)
        .count();
        if sources == 0 {
            bail!("account \"{account_name}\" has no token, token_env or token_command");
        }
        if sources > 1 {
            bail!(
                "account \"{account_name}\" sets more than one of token / token_env / token_command"
            );
        }
        let tok = if let Some(t) = &self.token {
            t.clone()
        } else if let Some(var) = &self.token_env {
            std::env::var(var)
                .with_context(|| format!("account \"{account_name}\": env var {var} is not set"))?
        } else {
            let cmd = self.token_command.as_ref().unwrap();
            let out = std::process::Command::new("sh")
                .arg("-c")
                .arg(cmd)
                .output()
                .with_context(|| format!("account \"{account_name}\": running token_command"))?;
            if !out.status.success() {
                bail!(
                    "account \"{account_name}\": token_command failed ({}): {}",
                    out.status,
                    String::from_utf8_lossy(&out.stderr).trim()
                );
            }
            String::from_utf8(out.stdout).context("token_command produced non-UTF-8 output")?
        };
        let tok = tok.trim().to_string();
        if tok.is_empty() {
            bail!("account \"{account_name}\": resolved token is empty");
        }
        Ok(tok)
    }

    /// Matches `--chat` against the chat map, falling back to a verbatim id/@username.
    pub fn resolve_chat(
        &self,
        account_name: &str,
        requested: Option<&str>,
    ) -> Result<ResolvedChat> {
        let key = match requested {
            Some(c) => c.to_string(),
            None => match &self.default_chat {
                Some(d) => d.clone(),
                None if self.chats.len() == 1 => self.chats.keys().next().unwrap().clone(),
                None => bail!(
                    "no chat given and account \"{account_name}\" has no default_chat; known chats: {}",
                    self.chat_names().join(", ")
                ),
            },
        };
        if let Some(entry) = self.chats.get(&key) {
            let (topic_id, silent, no_preview) = match entry {
                ChatEntry::Id(_) => (None, None, None),
                ChatEntry::Table(t) => (t.topic_id, t.silent, t.no_preview),
            };
            return Ok(ResolvedChat {
                key,
                id: entry.id().to_string(),
                ad_hoc: false,
                topic_id,
                silent,
                no_preview,
            });
        }
        // Not a configured key: allow raw numeric ids and @usernames through.
        if is_raw_chat_id(&key) {
            return Ok(ResolvedChat {
                id: key.clone(),
                key,
                ad_hoc: true,
                topic_id: None,
                silent: None,
                no_preview: None,
            });
        }
        bail!(
            "unknown chat \"{key}\" for account \"{account_name}\"; known chats: {}",
            self.chat_names().join(", ")
        )
    }

    pub fn chat_names(&self) -> Vec<&str> {
        self.chats.keys().map(String::as_str).collect()
    }

    /// Reverse lookup, so a chat id arriving in an update can be shown by its friendly name.
    pub fn key_for_id(&self, id: i64) -> Option<&str> {
        let id = id.to_string();
        self.chats
            .iter()
            .find(|(_, e)| e.id() == id)
            .map(|(k, _)| k.as_str())
    }
}

/// `-1001234567890`, `123456789` or `@channelusername`.
pub fn is_raw_chat_id(s: &str) -> bool {
    if let Some(name) = s.strip_prefix('@') {
        return !name.is_empty() && name.chars().all(|c| c.is_ascii_alphanumeric() || c == '_');
    }
    let digits = s.strip_prefix('-').unwrap_or(s);
    !digits.is_empty() && digits.chars().all(|c| c.is_ascii_digit())
}

pub fn default_config_path() -> PathBuf {
    config_home().join("notif-tg").join("config.toml")
}

fn config_home() -> PathBuf {
    if let Ok(x) = std::env::var("XDG_CONFIG_HOME")
        && !x.is_empty()
    {
        return PathBuf::from(x);
    }
    home().join(".config")
}

pub fn default_state_dir() -> PathBuf {
    if let Ok(x) = std::env::var("XDG_STATE_HOME")
        && !x.is_empty()
    {
        return PathBuf::from(x).join("notif-tg");
    }
    home().join(".local").join("state").join("notif-tg")
}

fn home() -> PathBuf {
    dirs::home_dir().unwrap_or_else(|| PathBuf::from("."))
}

pub fn expand_tilde(p: &str) -> String {
    if let Some(rest) = p.strip_prefix("~/")
        && let Some(h) = dirs::home_dir()
    {
        return h.join(rest).display().to_string();
    }
    p.to_string()
}

/// `123456789:AAEhBOweik…xyz` -> `123456789:…xyz`, for safe display.
/// The numeric half is the public bot id, so only the secret half is hidden.
pub fn redact(token: &str) -> String {
    let (id, secret) = match token.split_once(':') {
        Some((id, secret)) => (id, secret),
        None => ("", token),
    };
    let tail: String = secret
        .chars()
        .skip(secret.chars().count().saturating_sub(3))
        .collect();
    if id.is_empty() {
        format!("…{tail}")
    } else {
        format!("{id}:…{tail}")
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn raw_chat_ids() {
        assert!(is_raw_chat_id("-1001234567890"));
        assert!(is_raw_chat_id("123456789"));
        assert!(is_raw_chat_id("@gierd_news"));
        assert!(!is_raw_chat_id("alerts"));
        assert!(!is_raw_chat_id("@"));
        assert!(!is_raw_chat_id(""));
    }

    #[test]
    fn token_is_redacted_but_identifiable() {
        assert_eq!(redact("123456789:AAEhBOweik6ad6PsVWxyz"), "123456789:…xyz");
    }
}
