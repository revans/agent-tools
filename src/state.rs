//! Where `read` and `watch` remember how far they have consumed.
//!
//! Telegram's `getUpdates` only forgets an update once a later call confirms it
//! by offset, so the last seen `update_id` has to outlive the process.

use anyhow::{Context, Result};
use std::path::{Path, PathBuf};

pub struct Offsets {
    dir: PathBuf,
}

impl Offsets {
    pub fn new(dir: PathBuf) -> Offsets {
        Offsets { dir }
    }

    fn path(&self, account: &str) -> PathBuf {
        // Account keys come from the config, but keep them to a safe file name.
        let safe: String = account
            .chars()
            .map(|c| {
                if c.is_ascii_alphanumeric() || c == '-' || c == '_' {
                    c
                } else {
                    '_'
                }
            })
            .collect();
        self.dir.join(format!("{safe}.offset"))
    }

    /// The next offset to request, i.e. one past the last confirmed update.
    pub fn get(&self, account: &str) -> Option<i64> {
        let raw = std::fs::read_to_string(self.path(account)).ok()?;
        raw.trim().parse::<i64>().ok()
    }

    pub fn set(&self, account: &str, next: i64) -> Result<()> {
        std::fs::create_dir_all(&self.dir)
            .with_context(|| format!("creating {}", self.dir.display()))?;
        let path = self.path(account);
        write_atomic(&path, &format!("{next}\n"))
            .with_context(|| format!("writing {}", path.display()))
    }

    pub fn clear(&self, account: &str) -> Result<()> {
        let path = self.path(account);
        match std::fs::remove_file(&path) {
            Ok(()) => Ok(()),
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(()),
            Err(e) => Err(e).with_context(|| format!("removing {}", path.display())),
        }
    }

    pub fn location(&self, account: &str) -> PathBuf {
        self.path(account)
    }
}

/// Write then rename, so an interrupted run cannot leave a half-written offset.
fn write_atomic(path: &Path, contents: &str) -> Result<()> {
    let tmp = path.with_extension("offset.tmp");
    std::fs::write(&tmp, contents)?;
    std::fs::rename(&tmp, path)?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn round_trips_through_a_file() {
        let dir = std::env::temp_dir().join(format!("notif-tg-test-{}", std::process::id()));
        let o = Offsets::new(dir.clone());
        assert_eq!(o.get("gierd"), None);
        o.set("gierd", 42).unwrap();
        assert_eq!(o.get("gierd"), Some(42));
        o.set("gierd", 43).unwrap();
        assert_eq!(o.get("gierd"), Some(43));
        o.clear("gierd").unwrap();
        assert_eq!(o.get("gierd"), None);
        o.clear("gierd").unwrap();
        let _ = std::fs::remove_dir_all(dir);
    }

    #[test]
    fn account_names_cannot_escape_the_state_dir() {
        let o = Offsets::new(PathBuf::from("/state"));
        assert_eq!(
            o.location("../../etc/passwd"),
            PathBuf::from("/state/______etc_passwd.offset")
        );
    }
}
