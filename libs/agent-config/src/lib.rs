//! Where a tool's settings live.
//!
//! Every agent-tools CLI reads one shared file, `agent-tools.toml`, with a table
//! per tool, and falls back to its own older per-tool file:
//!
//! ```toml
//! [notif-slack]
//! default_account = "gierd"
//! [notif-slack.accounts.gierd]
//! token_env = "SLACK_TOKEN"
//! ```
//!
//! A tool's table holds exactly what its own file would hold at the top level, so
//! moving between the two is a matter of adding or removing the `notif-slack.`
//! prefix. The Go tools (`libs/apikit`) follow the same rules; keep the two in step.
//!
//! Lookup order, first match wins:
//! 1. `--config PATH`
//! 2. `$TOOL_CONFIG` (`NOTIF_SLACK_CONFIG` for `notif-slack`)
//! 3. `./agent-tools.toml`, if it has the tool's table
//! 4. `~/.config/agent-tools/config.toml`, if it has the tool's table
//! 5. `~/.config/TOOL/config.toml`
//! 6. `./TOOL.toml`
//!
//! A file with a tool's table in it is the shared file. It may hold every tool's
//! credentials, so one that other users can read is refused.

use anyhow::{Context, Result, anyhow, bail};
use serde::de::DeserializeOwned;
use std::path::{Path, PathBuf};

/// The project-level shared file, read from the working directory.
pub const PROJECT_FILE: &str = "agent-tools.toml";

/// Where the pieces of a lookup are found. `from_env` is the real thing; tests
/// build their own so they never touch the real config or each other.
#[derive(Debug, Clone)]
pub struct Locator {
    /// `$XDG_CONFIG_HOME`, or `~/.config`.
    pub config_home: PathBuf,
    /// The directory `./agent-tools.toml` and `./TOOL.toml` are looked for in.
    pub project_dir: PathBuf,
    /// The value of `$TOOL_CONFIG`, if set.
    pub env_path: Option<PathBuf>,
}

/// One located configuration.
#[derive(Debug, Clone)]
pub struct Located {
    /// The file that was read.
    pub path: PathBuf,
    /// True when it is the shared file and the settings came from the tool's table.
    pub unified: bool,
    tool: String,
    raw: String,
    table: toml::Table,
}

impl Located {
    /// Deserializes the tool's settings. For the tool's own file this parses the
    /// text directly, so error messages keep their line numbers.
    pub fn parse<T: DeserializeOwned>(&self) -> Result<T> {
        if self.unified {
            toml::Value::Table(self.table.clone())
                .try_into()
                .with_context(|| format!("the [{}] table in {}", self.tool, self.path.display()))
        } else {
            toml::from_str(&self.raw).with_context(|| format!("parsing {}", self.path.display()))
        }
    }

    /// The directory a tool keeps its own files in (templates, state). Beside its
    /// own config file that is the file's directory. In the shared file it is a
    /// subdirectory named for the tool, so two tools never share a templates folder.
    pub fn dir(&self) -> PathBuf {
        let parent = self
            .path
            .parent()
            .map(Path::to_path_buf)
            .unwrap_or_else(|| PathBuf::from("."));
        if self.unified {
            parent.join(&self.tool)
        } else {
            parent
        }
    }
}

/// The environment variable that overrides the config path for a tool.
pub fn env_var_name(tool: &str) -> String {
    format!("{}_CONFIG", tool.to_uppercase().replace('-', "_"))
}

/// `$XDG_CONFIG_HOME`, or `~/.config`.
pub fn config_home() -> PathBuf {
    if let Ok(x) = std::env::var("XDG_CONFIG_HOME")
        && !x.is_empty()
    {
        return PathBuf::from(x);
    }
    dirs::home_dir()
        .unwrap_or_else(|| PathBuf::from("."))
        .join(".config")
}

/// The shared file, for a given config home.
pub fn user_config_path_in(config_home: &Path) -> PathBuf {
    config_home.join("agent-tools").join("config.toml")
}

/// The shared user-level file.
pub fn user_config_path() -> PathBuf {
    user_config_path_in(&config_home())
}

/// A tool's own older file.
pub fn legacy_path_in(config_home: &Path, tool: &str) -> PathBuf {
    config_home.join(tool).join("config.toml")
}

pub fn expand_tilde(p: &str) -> String {
    if let Some(rest) = p.strip_prefix("~/")
        && let Some(h) = dirs::home_dir()
    {
        return h.join(rest).display().to_string();
    }
    p.to_string()
}

impl Locator {
    pub fn from_env(tool: &str) -> Self {
        Locator {
            config_home: config_home(),
            project_dir: PathBuf::from("."),
            env_path: std::env::var(env_var_name(tool))
                .ok()
                .map(|p| PathBuf::from(expand_tilde(&p))),
        }
    }

    fn project_file(&self) -> PathBuf {
        self.project_dir.join(PROJECT_FILE)
    }

    /// Every file a lookup consults, in order, for error messages.
    pub fn searched(&self, tool: &str) -> Vec<PathBuf> {
        vec![
            self.project_file(),
            user_config_path_in(&self.config_home),
            legacy_path_in(&self.config_home, tool),
            self.project_dir.join(format!("{tool}.toml")),
        ]
    }

    pub fn locate(&self, tool: &str, explicit: Option<&Path>) -> Result<Located> {
        if let Some(p) = explicit {
            if !p.exists() {
                bail!("config file not found: {}", p.display());
            }
            return load_file(tool, p);
        }
        if let Some(p) = &self.env_path {
            if !p.exists() {
                bail!(
                    "{} points at a missing file: {}",
                    env_var_name(tool),
                    p.display()
                );
            }
            return load_file(tool, p);
        }
        // The shared files count only when they have this tool's table.
        for cand in [self.project_file(), user_config_path_in(&self.config_home)] {
            if cand.exists() {
                let found = load_file(tool, &cand)?;
                if found.unified {
                    return Ok(found);
                }
            }
        }
        for cand in [
            legacy_path_in(&self.config_home, tool),
            self.project_dir.join(format!("{tool}.toml")),
        ] {
            if cand.exists() {
                return load_file(tool, &cand);
            }
        }
        Err(anyhow!(
            "no config file found (looked at {}). Run `{tool} init` to create one.",
            self.searched(tool)
                .iter()
                .map(|c| c.display().to_string())
                .collect::<Vec<_>>()
                .join(", ")
        ))
    }
}

/// Locates a tool's configuration using the real environment.
pub fn locate(tool: &str, explicit: Option<&Path>) -> Result<Located> {
    Locator::from_env(tool).locate(tool, explicit)
}

fn load_file(tool: &str, path: &Path) -> Result<Located> {
    let raw =
        std::fs::read_to_string(path).with_context(|| format!("reading {}", path.display()))?;
    let doc: toml::Table =
        toml::from_str(&raw).with_context(|| format!("parsing {}", path.display()))?;
    match doc.get(tool) {
        Some(toml::Value::Table(t)) => {
            check_private(path)?;
            Ok(Located {
                path: path.to_path_buf(),
                unified: true,
                tool: tool.to_string(),
                raw,
                table: t.clone(),
            })
        }
        Some(_) => bail!(
            "{}: `{tool}` must be a table, as in [{tool}]",
            path.display()
        ),
        None => Ok(Located {
            path: path.to_path_buf(),
            unified: false,
            tool: tool.to_string(),
            raw,
            table: toml::Table::new(),
        }),
    }
}

/// Refuses a shared file that other users can read.
#[cfg(unix)]
pub fn check_private(path: &Path) -> Result<()> {
    use std::os::unix::fs::PermissionsExt;
    let mode = std::fs::metadata(path)
        .with_context(|| format!("reading {}", path.display()))?
        .permissions()
        .mode();
    if mode & 0o077 != 0 {
        bail!(
            "{} is readable by other users (mode {:o}), and it can hold every tool's credentials. Run: chmod 600 {}",
            path.display(),
            mode & 0o777,
            path.display()
        );
    }
    Ok(())
}

#[cfg(not(unix))]
pub fn check_private(_path: &Path) -> Result<()> {
    Ok(())
}

// ---------------------------------------------------------------- nesting

/// Rewrites a tool's own file as the body of its table in the shared file:
/// top-level keys move under `[tool]`, and every `[x]` or `[[x]]` becomes
/// `[tool.x]` or `[[tool.x]]`. Comments and layout are kept. The result is
/// parsed and compared with the input, so a rewrite that changed the meaning is
/// an error and never reaches a file.
pub fn nest(tool: &str, text: &str) -> Result<String> {
    let mut out = String::new();
    let mut in_multiline: Option<&str> = None;
    let mut header_open = false; // the [tool] header for top-level keys has been written
    let mut seen_header = false;
    for line in text.split_inclusive('\n') {
        let trimmed = line.trim_start();
        if let Some(q) = in_multiline {
            out.push_str(line);
            if line.matches(q).count() % 2 == 1 {
                in_multiline = None;
            }
            continue;
        }
        if trimmed.is_empty() || trimmed.starts_with('#') {
            out.push_str(line);
            continue;
        }
        let indent = &line[..line.len() - trimmed.len()];
        if let Some(rest) = trimmed.strip_prefix("[[") {
            out.push_str(&format!("{indent}[[{tool}.{rest}"));
            seen_header = true;
        } else if let Some(rest) = trimmed.strip_prefix('[') {
            out.push_str(&format!("{indent}[{tool}.{rest}"));
            seen_header = true;
        } else {
            if !seen_header && !header_open {
                out.push_str(&format!("[{tool}]\n"));
                header_open = true;
            }
            out.push_str(line);
            for q in ["\"\"\"", "'''"] {
                if line.matches(q).count() % 2 == 1 {
                    in_multiline = Some(q);
                }
            }
        }
    }
    if !out.ends_with('\n') {
        out.push('\n');
    }
    let original: toml::Table = toml::from_str(text).context("the original does not parse")?;
    let nested: toml::Table = toml::from_str(&out).context("the nested copy does not parse")?;
    let mut expected = toml::Table::new();
    if !original.is_empty() {
        expected.insert(tool.to_string(), toml::Value::Table(original));
    }
    if nested != expected {
        bail!("could not move this file into the shared file safely; add a [{tool}] table by hand");
    }
    Ok(out)
}

fn write_private_atomic(path: &Path, contents: &str) -> Result<()> {
    use std::io::Write;
    if let Some(dir) = path.parent() {
        create_private_dir(dir)?;
    }
    let tmp = path.with_extension(format!("tmp{}", std::process::id()));
    let result = (|| -> Result<()> {
        let mut f = open_private(&tmp)?;
        f.write_all(contents.as_bytes())?;
        f.sync_all()?;
        std::fs::rename(&tmp, path)?;
        Ok(())
    })();
    if result.is_err() {
        let _ = std::fs::remove_file(&tmp);
    }
    result.with_context(|| format!("writing {}", path.display()))
}

#[cfg(unix)]
fn open_private(path: &Path) -> std::io::Result<std::fs::File> {
    use std::os::unix::fs::OpenOptionsExt;
    std::fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o600)
        .open(path)
}

#[cfg(not(unix))]
fn open_private(path: &Path) -> std::io::Result<std::fs::File> {
    std::fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .open(path)
}

#[cfg(unix)]
fn create_private_dir(dir: &Path) -> Result<()> {
    use std::os::unix::fs::DirBuilderExt;
    std::fs::DirBuilder::new()
        .recursive(true)
        .mode(0o700)
        .create(dir)
        .with_context(|| format!("creating {}", dir.display()))
}

#[cfg(not(unix))]
fn create_private_dir(dir: &Path) -> Result<()> {
    std::fs::create_dir_all(dir).with_context(|| format!("creating {}", dir.display()))
}

/// Adds a tool's table to the shared file, creating the file (mode 0600) if it
/// does not exist. Another tool's tables are left exactly as they were. Refuses
/// when the file already has this tool's table.
pub fn add_to_unified(tool: &str, own_format_text: &str, dest: &Path, note: &str) -> Result<()> {
    let nested = nest(tool, own_format_text)?;
    let mut out = match std::fs::read_to_string(dest) {
        Ok(existing) => {
            check_private(dest)?;
            let doc: toml::Table =
                toml::from_str(&existing).with_context(|| format!("parsing {}", dest.display()))?;
            if doc.contains_key(tool) {
                bail!(
                    "{} already has a [{tool}] table; not changing it",
                    dest.display()
                );
            }
            existing
        }
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => String::new(),
        Err(e) => return Err(e).with_context(|| format!("reading {}", dest.display())),
    };
    if !out.is_empty() {
        if !out.ends_with('\n') {
            out.push('\n');
        }
        out.push('\n');
    }
    out.push_str(&format!("# {note}\n{nested}"));
    toml::from_str::<toml::Table>(&out)
        .context("the combined file would not parse; nothing was written")?;
    write_private_atomic(dest, &out)
}

/// Writes a tool's starter config into the shared file.
pub fn init_unified(tool: &str, starter: &str, dest: &Path) -> Result<()> {
    add_to_unified(
        tool,
        starter,
        dest,
        &format!("{tool}: starter settings. Fill in your credentials."),
    )
}

/// Copies a tool's own older file into the shared file. The older file is left
/// alone; once this succeeds the shared file takes precedence over it.
pub fn migrate_into(tool: &str, legacy: &Path, dest: &Path) -> Result<()> {
    let text =
        std::fs::read_to_string(legacy).with_context(|| format!("reading {}", legacy.display()))?;
    let doc: toml::Table =
        toml::from_str(&text).with_context(|| format!("parsing {}", legacy.display()))?;
    if doc.contains_key(tool) {
        bail!(
            "{} is already a shared file (it has a [{tool}] table); nothing to migrate",
            legacy.display()
        );
    }
    add_to_unified(
        tool,
        &text,
        dest,
        &format!("{tool}: moved here from {}", legacy.display()),
    )
}

/// Copies the `*.md` files of one directory into another, skipping any that
/// already exist there, and returns how many were copied. Used to move a tool's
/// templates when its config moves.
pub fn copy_markdown_files(from: &Path, to: &Path) -> Result<usize> {
    let entries = match std::fs::read_dir(from) {
        Ok(e) => e,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(0),
        Err(e) => return Err(e).with_context(|| format!("reading {}", from.display())),
    };
    let mut copied = 0;
    for entry in entries {
        let path = entry?.path();
        if path.extension().and_then(|e| e.to_str()) != Some("md") || !path.is_file() {
            continue;
        }
        let dest = to.join(path.file_name().unwrap());
        if dest.exists() {
            continue;
        }
        std::fs::create_dir_all(to).with_context(|| format!("creating {}", to.display()))?;
        std::fs::copy(&path, &dest).with_context(|| format!("copying {}", path.display()))?;
        copied += 1;
    }
    Ok(copied)
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde::Deserialize;
    use std::sync::atomic::{AtomicU32, Ordering};

    struct Tmp(PathBuf);
    impl Tmp {
        fn new() -> Self {
            static N: AtomicU32 = AtomicU32::new(0);
            let p = std::env::temp_dir().join(format!(
                "agent-config-test-{}-{}",
                std::process::id(),
                N.fetch_add(1, Ordering::SeqCst)
            ));
            std::fs::create_dir_all(&p).unwrap();
            Tmp(p)
        }
        fn write(&self, rel: &str, body: &str, mode: u32) -> PathBuf {
            let p = self.0.join(rel);
            std::fs::create_dir_all(p.parent().unwrap()).unwrap();
            std::fs::write(&p, body).unwrap();
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                std::fs::set_permissions(&p, std::fs::Permissions::from_mode(mode)).unwrap();
            }
            p
        }
        fn locator(&self) -> Locator {
            Locator {
                config_home: self.0.join("home"),
                project_dir: self.0.join("proj"),
                env_path: None,
            }
        }
    }
    impl Drop for Tmp {
        fn drop(&mut self) {
            let _ = std::fs::remove_dir_all(&self.0);
        }
    }

    #[derive(Debug, Deserialize, PartialEq)]
    #[serde(deny_unknown_fields)]
    struct Settings {
        #[serde(default)]
        default_account: Option<String>,
        #[serde(default)]
        accounts: std::collections::BTreeMap<String, Acct>,
    }
    #[derive(Debug, Deserialize, PartialEq)]
    #[serde(deny_unknown_fields)]
    struct Acct {
        token_env: Option<String>,
    }

    const NESTED: &str = "[notif-slack]\ndefault_account = \"a\"\n[notif-slack.accounts.a]\ntoken_env = \"X\"\n[reddit.accounts.m]\nclient_id = \"1\"\n";
    const LEGACY: &str = "default_account = \"b\"\n[accounts.b]\ntoken_env = \"Y\"\n";

    #[test]
    fn shared_file_is_found_and_parsed() {
        let t = Tmp::new();
        t.write("home/agent-tools/config.toml", NESTED, 0o600);
        let l = t.locator().locate("notif-slack", None).unwrap();
        assert!(l.unified);
        let s: Settings = l.parse().unwrap();
        assert_eq!(s.default_account.as_deref(), Some("a"));
        assert_eq!(l.dir(), t.0.join("home/agent-tools/notif-slack"));
        // another tool's table is invisible, and does not trip deny_unknown_fields
        assert!(t.locator().locate("reddit", None).unwrap().unified);
    }

    #[test]
    fn lookup_order() {
        let t = Tmp::new();
        t.write("home/notif-slack/config.toml", LEGACY, 0o600);
        t.write(
            "proj/notif-slack.toml",
            "default_account = \"p\"\n[accounts.p]\ntoken_env=\"Z\"\n",
            0o600,
        );
        let l = t.locator().locate("notif-slack", None).unwrap();
        assert!(
            !l.unified && l.path.ends_with("home/notif-slack/config.toml"),
            "the user's own file comes before ./TOOL.toml"
        );
        assert_eq!(l.dir(), t.0.join("home/notif-slack"));

        t.write("home/agent-tools/config.toml", NESTED, 0o600);
        let l = t.locator().locate("notif-slack", None).unwrap();
        assert!(
            l.unified && l.path.ends_with("home/agent-tools/config.toml"),
            "the shared file beats the tool's own"
        );

        t.write("proj/agent-tools.toml", "[notif-slack]\ndefault_account = \"proj\"\n[notif-slack.accounts.proj]\ntoken_env=\"Q\"\n", 0o600);
        let l = t.locator().locate("notif-slack", None).unwrap();
        assert_eq!(
            l.parse::<Settings>().unwrap().default_account.as_deref(),
            Some("proj"),
            "a project file beats the user file"
        );

        // a project file with no table for this tool does not hide the user file
        t.write("proj/agent-tools.toml", "[other]\nx = 1\n", 0o600);
        let l = t.locator().locate("notif-slack", None).unwrap();
        assert_eq!(
            l.parse::<Settings>().unwrap().default_account.as_deref(),
            Some("a")
        );
    }

    #[test]
    fn explicit_and_env_paths_win_and_are_detected_by_content() {
        let t = Tmp::new();
        t.write("home/agent-tools/config.toml", NESTED, 0o600);
        let own = t.write("elsewhere/mine.toml", LEGACY, 0o600);
        let l = t.locator().locate("notif-slack", Some(&own)).unwrap();
        assert!(!l.unified);
        assert_eq!(
            l.parse::<Settings>().unwrap().default_account.as_deref(),
            Some("b")
        );
        let shared = t.write("elsewhere/shared.toml", NESTED, 0o600);
        assert!(
            t.locator()
                .locate("notif-slack", Some(&shared))
                .unwrap()
                .unified
        );
        let mut loc = t.locator();
        loc.env_path = Some(own.clone());
        assert!(loc.locate("notif-slack", None).unwrap().path == own);
        loc.env_path = Some(t.0.join("missing.toml"));
        assert!(
            loc.locate("notif-slack", None)
                .unwrap_err()
                .to_string()
                .contains("NOTIF_SLACK_CONFIG points at a missing file")
        );
        assert!(
            t.locator()
                .locate("notif-slack", Some(&t.0.join("nope")))
                .is_err()
        );
    }

    #[cfg(unix)]
    #[test]
    fn a_shared_file_other_users_can_read_is_refused_but_an_own_file_is_not() {
        let t = Tmp::new();
        t.write("home/agent-tools/config.toml", NESTED, 0o644);
        let e = t
            .locator()
            .locate("notif-slack", None)
            .unwrap_err()
            .to_string();
        assert!(
            e.contains("readable by other users") && e.contains("chmod 600"),
            "{e}"
        );
        let t = Tmp::new();
        t.write("home/notif-slack/config.toml", LEGACY, 0o644);
        assert!(
            t.locator().locate("notif-slack", None).is_ok(),
            "a tool's own file keeps its old behavior"
        );
    }

    #[test]
    fn not_found_names_every_place_looked() {
        let t = Tmp::new();
        let e = t
            .locator()
            .locate("notif-tg", None)
            .unwrap_err()
            .to_string();
        for want in [
            "agent-tools.toml",
            "agent-tools/config.toml",
            "notif-tg/config.toml",
            "notif-tg.toml",
            "notif-tg init",
        ] {
            assert!(e.contains(want), "{want} missing from: {e}");
        }
    }

    #[test]
    fn a_broken_shared_file_is_an_error_not_a_skip() {
        let t = Tmp::new();
        t.write("proj/agent-tools.toml", "[notif-slack\nbroken", 0o600);
        t.write("home/notif-slack/config.toml", LEGACY, 0o600);
        assert!(
            t.locator()
                .locate("notif-slack", None)
                .unwrap_err()
                .to_string()
                .contains("parsing")
        );
        let t = Tmp::new();
        t.write("home/agent-tools/config.toml", "notif-slack = 3\n", 0o600);
        assert!(
            t.locator()
                .locate("notif-slack", None)
                .unwrap_err()
                .to_string()
                .contains("must be a table")
        );
    }

    fn assert_nests(text: &str) -> String {
        let nested = nest("tool", text).unwrap_or_else(|e| panic!("{e:#}\n---\n{text}"));
        let orig: toml::Table = toml::from_str(text).unwrap();
        let got: toml::Table = toml::from_str(&nested).unwrap();
        let mut want = toml::Table::new();
        if !orig.is_empty() {
            want.insert("tool".into(), toml::Value::Table(orig));
        }
        assert_eq!(got, want, "{nested}");
        nested
    }

    #[test]
    fn nesting_keeps_meaning_and_comments() {
        let n = assert_nests(
            "# my settings\ndefault_account = \"a\"\n\n# the account\n[accounts.a]\ntoken_env = \"T\" # trailing\n[accounts.a.channels]\nops = \"C123\"\n",
        );
        assert!(
            n.contains("# my settings")
                && n.contains("# trailing")
                && n.contains("[tool.accounts.a.channels]")
        );
        assert_nests("[accounts.\"odd.name\"]\nx = 1\n");
        assert_nests("[[servers]]\nname = \"a\"\n[[servers]]\nname = \"b\"\n");
        assert_nests("a = 1\nb = \"two\"\n");
        assert_nests("");
        assert_nests("# only a comment\n");
        assert_nests("  [ accounts . a ]\n  x = 1\n");
        // a multi-line string that contains header-looking lines is left alone
        let n = assert_nests(
            "note = \"\"\"\n[not.a.header]\n[[nor.this]]\n\"\"\"\n[accounts.a]\nx = 1\n",
        );
        assert!(n.contains("\n[not.a.header]\n") && !n.contains("[tool.not"));
        assert_nests("note = '''\n[x]\n'''\n");
        assert_nests("no_trailing_newline = true");
    }

    #[test]
    fn migrating_and_initialising() {
        let t = Tmp::new();
        let dest = t.0.join("home/agent-tools/config.toml");
        init_unified(
            "notif-slack",
            "# starter\ndefault_account = \"a\"\n[accounts.a]\ntoken_env = \"X\"\n",
            &dest,
        )
        .unwrap();
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            assert_eq!(
                std::fs::metadata(&dest).unwrap().permissions().mode() & 0o777,
                0o600
            );
            assert_eq!(
                std::fs::metadata(dest.parent().unwrap())
                    .unwrap()
                    .permissions()
                    .mode()
                    & 0o777,
                0o700
            );
        }
        let legacy = t.write(
            "old/tg.toml",
            "default_account = \"z\"\n[accounts.z]\ntoken_env = \"Q\"\n",
            0o600,
        );
        migrate_into("notif-tg", &legacy, &dest).unwrap();
        let doc: toml::Table = toml::from_str(&std::fs::read_to_string(&dest).unwrap()).unwrap();
        assert!(
            doc.contains_key("notif-slack") && doc.contains_key("notif-tg"),
            "both tools' tables, side by side"
        );
        assert!(
            std::fs::read_to_string(&dest)
                .unwrap()
                .contains("moved here from")
        );
        assert!(legacy.exists(), "the old file is left alone");

        let before = std::fs::read_to_string(&dest).unwrap();
        assert!(
            migrate_into("notif-tg", &legacy, &dest)
                .unwrap_err()
                .to_string()
                .contains("already has a [notif-tg] table")
        );
        assert!(init_unified("notif-slack", "x = 1\n", &dest).is_err());
        assert_eq!(
            std::fs::read_to_string(&dest).unwrap(),
            before,
            "a refusal changes nothing"
        );
        assert!(
            migrate_into("notif-slack", &dest, &t.0.join("other.toml"))
                .unwrap_err()
                .to_string()
                .contains("already a shared file")
        );
        assert!(t.locator().locate("notif-tg", Some(&dest)).unwrap().unified);
    }

    #[test]
    fn templates_are_copied_without_overwriting() {
        let t = Tmp::new();
        t.write("old/templates/a.md", "A", 0o644);
        t.write("old/templates/b.md", "B", 0o644);
        t.write("old/templates/notes.txt", "ignored", 0o644);
        t.write("new/templates/b.md", "mine", 0o644);
        assert_eq!(
            copy_markdown_files(&t.0.join("old/templates"), &t.0.join("new/templates")).unwrap(),
            1
        );
        assert_eq!(
            std::fs::read_to_string(t.0.join("new/templates/b.md")).unwrap(),
            "mine"
        );
        assert!(
            t.0.join("new/templates/a.md").exists()
                && !t.0.join("new/templates/notes.txt").exists()
        );
        assert_eq!(
            copy_markdown_files(&t.0.join("nowhere"), &t.0.join("new/templates")).unwrap(),
            0
        );
    }

    #[cfg(unix)]
    #[test]
    fn appending_to_a_loose_shared_file_is_refused() {
        let t = Tmp::new();
        let dest = t.write("home/agent-tools/config.toml", "[other]\nx = 1\n", 0o644);
        assert!(
            init_unified("notif-slack", "x = 1\n", &dest)
                .unwrap_err()
                .to_string()
                .contains("readable by other users")
        );
    }
}
