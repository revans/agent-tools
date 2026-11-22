//! Loading and resolution of the external credentials/config TOML.

use anyhow::{Context, Result, bail};
use serde::Deserialize;
use std::collections::BTreeMap;
use std::path::{Path, PathBuf};

/// The name of this tool in the shared config file and in error messages.
pub const TOOL: &str = "notif-slack";

/// Top level of `config.toml`.
#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Config {
    /// Account used when `--account` is omitted. Optional when only one account exists.
    #[serde(default)]
    pub default_account: Option<String>,
    /// Where `--template <name>` looks for `<name>.md`. Defaults to `<config dir>/templates`.
    #[serde(default)]
    pub template_dir: Option<String>,
    #[serde(default)]
    pub accounts: BTreeMap<String, Account>,

    #[serde(skip)]
    pub path: PathBuf,
    /// True when the settings came from the shared agent-tools file.
    #[serde(skip)]
    pub unified: bool,
    #[serde(skip)]
    dir: PathBuf,
}

/// One Slack workspace.
#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Account {
    /// e.g. "gierd.slack.com". Informational for posting; `notif-slack read` builds each
    /// message's permalink from it.
    #[serde(default)]
    pub workspace: Option<String>,
    /// Bot/user token (`xoxb-…` / `xoxp-…`). Exactly one token source is required.
    #[serde(default)]
    pub token: Option<String>,
    /// Name of an environment variable holding the token.
    #[serde(default)]
    pub token_env: Option<String>,
    /// Shell command printing the token on stdout (e.g. `pass show slack/gierd`).
    #[serde(default)]
    pub token_command: Option<String>,
    /// Channel key used when `--channel` is omitted.
    #[serde(default)]
    pub default_channel: Option<String>,
    /// Account-wide sender overrides (need the `chat:write.customize` scope).
    #[serde(default)]
    pub username: Option<String>,
    #[serde(default)]
    pub icon_emoji: Option<String>,
    #[serde(default)]
    pub icon_url: Option<String>,
    /// Friendly key -> channel id (or `#name`).
    #[serde(default)]
    pub channels: BTreeMap<String, ChannelEntry>,
}

/// A channel is either a bare id string or a table with per-channel overrides.
#[derive(Debug, Deserialize)]
#[serde(untagged)]
pub enum ChannelEntry {
    Id(String),
    Table(ChannelTable),
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ChannelTable {
    pub id: String,
    #[serde(default)]
    pub description: Option<String>,
    #[serde(default)]
    pub username: Option<String>,
    #[serde(default)]
    pub icon_emoji: Option<String>,
    #[serde(default)]
    pub icon_url: Option<String>,
}

impl ChannelEntry {
    pub fn id(&self) -> &str {
        match self {
            ChannelEntry::Id(s) => s,
            ChannelEntry::Table(t) => &t.id,
        }
    }
    pub fn description(&self) -> Option<&str> {
        match self {
            ChannelEntry::Id(_) => None,
            ChannelEntry::Table(t) => t.description.as_deref(),
        }
    }
}

/// A channel after `--channel` has been matched against the account's channel map.
#[derive(Debug, Clone)]
pub struct ResolvedChannel {
    /// What the user typed.
    pub key: String,
    /// What gets sent to Slack.
    pub id: String,
    /// True when the key was not in the config and is being passed through verbatim.
    pub ad_hoc: bool,
    pub username: Option<String>,
    pub icon_emoji: Option<String>,
    pub icon_url: Option<String>,
}

impl Config {
    /// Finds and reads the settings. Lookup order: `--config`, `$NOTIF_SLACK_CONFIG`,
    /// `./agent-tools.toml` and `~/.config/agent-tools/config.toml` (each only if it
    /// has a `[notif-slack]` table), then `~/.config/notif-slack/config.toml`, then
    /// `./notif-slack.toml`. See the `agent-config` crate.
    pub fn load(explicit: Option<&Path>) -> Result<Config> {
        let located = agent_config::locate(TOOL, explicit)?;
        let mut cfg: Config = located.parse()?;
        cfg.path = located.path.clone();
        cfg.unified = located.unified;
        cfg.dir = located.dir();
        if cfg.accounts.is_empty() {
            bail!("{} defines no [accounts.*] tables", cfg.where_defined());
        }
        if let Some(d) = &cfg.default_account
            && !cfg.accounts.contains_key(d)
        {
            bail!(
                "default_account = \"{d}\" is not defined in {}",
                cfg.where_defined()
            );
        }
        Ok(cfg)
    }

    /// For messages: the file, and the table inside it when the file is shared.
    pub fn where_defined(&self) -> String {
        if self.unified {
            format!("[{TOOL}] in {}", self.path.display())
        } else {
            self.path.display().to_string()
        }
    }

    /// The directory this tool keeps its own files in. Templates and relative
    /// paths hang off it: beside its own config file, or `<shared dir>/notif-slack`.
    pub fn dir(&self) -> PathBuf {
        self.dir.clone()
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

    /// Matches `--channel` against the channel map, falling back to a verbatim id/#name.
    pub fn resolve_channel(
        &self,
        account_name: &str,
        requested: Option<&str>,
    ) -> Result<ResolvedChannel> {
        let key = match requested {
            Some(c) => c.to_string(),
            None => match &self.default_channel {
                Some(d) => d.clone(),
                None if self.channels.len() == 1 => self.channels.keys().next().unwrap().clone(),
                None => bail!(
                    "no channel given and account \"{account_name}\" has no default_channel; known channels: {}",
                    self.channel_names().join(", ")
                ),
            },
        };
        if let Some(entry) = self.channels.get(&key) {
            let (username, icon_emoji, icon_url) = match entry {
                ChannelEntry::Id(_) => (None, None, None),
                ChannelEntry::Table(t) => {
                    (t.username.clone(), t.icon_emoji.clone(), t.icon_url.clone())
                }
            };
            return Ok(ResolvedChannel {
                key,
                id: entry.id().to_string(),
                ad_hoc: false,
                username: username.or_else(|| self.username.clone()),
                icon_emoji: icon_emoji.or_else(|| self.icon_emoji.clone()),
                icon_url: icon_url.or_else(|| self.icon_url.clone()),
            });
        }
        // Not a configured key: allow raw ids (C…/G…/D…) and #names through.
        let looks_raw = key.starts_with('#')
            || key.starts_with('@')
            || (key.len() >= 9
                && key.starts_with(['C', 'G', 'D'])
                && key[1..].chars().all(|c| c.is_ascii_alphanumeric()));
        if looks_raw {
            return Ok(ResolvedChannel {
                id: key.clone(),
                key,
                ad_hoc: true,
                username: self.username.clone(),
                icon_emoji: self.icon_emoji.clone(),
                icon_url: self.icon_url.clone(),
            });
        }
        bail!(
            "unknown channel \"{key}\" for account \"{account_name}\"; known channels: {}",
            self.channel_names().join(", ")
        )
    }

    /// The friendly key for a channel id, so a message permalink picks up that
    /// channel's configured overrides instead of being treated as an ad-hoc id.
    pub fn channel_key_for_id(&self, id: &str) -> Option<&str> {
        self.channels
            .iter()
            .find(|(_, entry)| entry.id().eq_ignore_ascii_case(id))
            .map(|(key, _)| key.as_str())
    }

    pub fn channel_names(&self) -> Vec<&str> {
        self.channels.keys().map(String::as_str).collect()
    }
}

/// Where `init` writes: the shared agent-tools file.
pub fn default_config_path() -> PathBuf {
    agent_config::user_config_path()
}

/// The tool's own older file, which `migrate` reads by default.
pub fn legacy_config_path() -> PathBuf {
    agent_config::legacy_path_in(&agent_config::config_home(), TOOL)
}

pub fn expand_tilde(p: &str) -> String {
    if let Some(rest) = p.strip_prefix("~/")
        && let Some(h) = dirs::home_dir()
    {
        return h.join(rest).display().to_string();
    }
    p.to_string()
}

/// `xoxb-1234…wxyz` -> `xoxb-…wxyz`, for safe display.
pub fn redact(token: &str) -> String {
    let n = token.len();
    if n <= 8 {
        return "…".to_string();
    }
    let head: String = token.chars().take(5).collect();
    let tail: String = token.chars().skip(n.saturating_sub(4)).collect();
    format!("{head}…{tail}")
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A private scratch directory that cleans up after itself.
    struct Scratch(PathBuf);
    impl Scratch {
        fn new() -> Self {
            use std::sync::atomic::{AtomicU32, Ordering};
            static N: AtomicU32 = AtomicU32::new(0);
            let p = std::env::temp_dir().join(format!(
                "notif-slack-config-test-{}-{}",
                std::process::id(),
                N.fetch_add(1, Ordering::SeqCst)
            ));
            std::fs::create_dir_all(&p).unwrap();
            Scratch(p)
        }
        fn file(&self, name: &str, body: &str) -> PathBuf {
            use std::os::unix::fs::PermissionsExt;
            let p = self.0.join(name);
            std::fs::create_dir_all(p.parent().unwrap()).unwrap();
            std::fs::write(&p, body).unwrap();
            std::fs::set_permissions(&p, std::fs::Permissions::from_mode(0o600)).unwrap();
            p
        }
    }
    impl Drop for Scratch {
        fn drop(&mut self) {
            let _ = std::fs::remove_dir_all(&self.0);
        }
    }

    const OWN: &str = "default_account = \"a\"\n[accounts.a]\ntoken_env = \"T\"\n";
    const SHARED: &str = "[reddit]\nclient_id = \"x\"\n\n[notif-slack]\ndefault_account = \"a\"\n[notif-slack.accounts.a]\ntoken_env = \"T\"\n\n[x.accounts.m]\nanything = 1\n";

    #[test]
    fn loads_from_the_shared_file_beside_other_tools_tables() {
        let s = Scratch::new();
        let path = s.file("agent-tools/config.toml", SHARED);
        let cfg = Config::load(Some(&path)).unwrap();
        assert!(cfg.unified);
        assert_eq!(cfg.account(None).unwrap().0, "a");
        assert_eq!(cfg.dir(), s.0.join("agent-tools/notif-slack"));
        assert_eq!(
            cfg.template_dir(),
            s.0.join("agent-tools/notif-slack/templates")
        );
    }

    #[test]
    fn the_tools_own_file_still_loads_as_before() {
        let s = Scratch::new();
        let path = s.file("notif-slack/config.toml", OWN);
        let cfg = Config::load(Some(&path)).unwrap();
        assert!(!cfg.unified);
        assert_eq!(cfg.dir(), s.0.join("notif-slack"));
        assert_eq!(cfg.template_dir(), s.0.join("notif-slack/templates"));
    }

    #[test]
    fn a_mistake_inside_this_tools_table_is_still_caught() {
        let s = Scratch::new();
        let bad = SHARED.replace("token_env = \"T\"", "token_env = \"T\"\ntypo_field = 1");
        let path = s.file("agent-tools/config.toml", &bad);
        let e = format!("{:#}", Config::load(Some(&path)).unwrap_err());
        assert!(
            e.contains("typo_field") && e.contains("[notif-slack]"),
            "{e}"
        );
    }

    #[test]
    fn errors_name_the_table_when_the_file_is_shared() {
        let s = Scratch::new();
        let bad = SHARED.replace("default_account = \"a\"", "default_account = \"nope\"");
        let path = s.file("agent-tools/config.toml", &bad);
        let e = Config::load(Some(&path)).unwrap_err().to_string();
        assert!(
            e.contains("[notif-slack] in ") && e.contains("config.toml"),
            "{e}"
        );
    }
}
