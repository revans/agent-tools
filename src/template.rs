//! Markdown templates: optional TOML frontmatter plus `{{variable}}` substitution.

use anyhow::{Context, Result, bail};
use regex::{Captures, Regex};
use serde::Deserialize;
use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::sync::OnceLock;

/// Frontmatter: everything between the opening and closing `---` fence, parsed as TOML.
#[derive(Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Meta {
    #[serde(default)]
    pub description: Option<String>,
    /// Defaults used when `--account` / `--channel` are omitted on the command line.
    #[serde(default)]
    pub account: Option<String>,
    #[serde(default)]
    pub channel: Option<String>,
    #[serde(default)]
    pub username: Option<String>,
    #[serde(default)]
    pub icon_emoji: Option<String>,
    #[serde(default)]
    pub icon_url: Option<String>,
    #[serde(default)]
    pub vars: BTreeMap<String, VarSpec>,
}

#[derive(Debug, Deserialize)]
#[serde(untagged)]
pub enum VarSpec {
    /// `service = "a service name"` — shorthand for a described, required variable.
    Description(String),
    Table(VarTable),
}

#[derive(Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct VarTable {
    #[serde(default)]
    pub description: Option<String>,
    #[serde(default)]
    pub default: Option<toml::Value>,
    #[serde(default)]
    pub required: Option<bool>,
}

impl VarSpec {
    pub fn description(&self) -> Option<&str> {
        match self {
            VarSpec::Description(d) => Some(d),
            VarSpec::Table(t) => t.description.as_deref(),
        }
    }
    pub fn default(&self) -> Option<String> {
        match self {
            VarSpec::Description(_) => None,
            VarSpec::Table(t) => t.default.as_ref().map(value_to_string),
        }
    }
    /// Required unless it has a default or says otherwise.
    pub fn required(&self) -> bool {
        match self {
            VarSpec::Description(_) => true,
            VarSpec::Table(t) => t.required.unwrap_or_else(|| t.default.is_none()),
        }
    }
}

fn value_to_string(v: &toml::Value) -> String {
    match v {
        toml::Value::String(s) => s.clone(),
        other => other.to_string(),
    }
}

#[derive(Debug)]
pub struct Template {
    /// Bare name (`deploy`) for templates found in the template dir, else the file stem.
    pub name: String,
    pub path: Option<PathBuf>,
    pub meta: Meta,
    /// Template body with frontmatter stripped.
    pub body: String,
}

fn placeholder_re() -> &'static Regex {
    static R: OnceLock<Regex> = OnceLock::new();
    R.get_or_init(|| Regex::new(r"\{\{\s*([A-Za-z_][A-Za-z0-9_.-]*)\s*\}\}").unwrap())
}

impl Template {
    pub fn parse(name: &str, path: Option<PathBuf>, raw: &str) -> Result<Template> {
        let raw = raw.replace("\r\n", "\n");
        let (meta, body) = split_frontmatter(&raw)
            .with_context(|| format!("template \"{name}\": invalid frontmatter"))?;
        Ok(Template {
            name: name.to_string(),
            path,
            meta,
            body,
        })
    }

    pub fn from_file(path: &Path) -> Result<Template> {
        let raw = std::fs::read_to_string(path)
            .with_context(|| format!("reading template {}", path.display()))?;
        let name = path
            .file_stem()
            .map(|s| s.to_string_lossy().into_owned())
            .unwrap_or_else(|| path.display().to_string());
        Template::parse(&name, Some(path.to_path_buf()), &raw)
    }

    /// `--template` is a path when it looks like one, otherwise a name in `template_dir`.
    pub fn resolve(spec: &str, template_dir: &Path) -> Result<Template> {
        let as_path = PathBuf::from(crate::config::expand_tilde(spec));
        if as_path.is_file() {
            return Template::from_file(&as_path);
        }
        let in_dir = template_dir.join(format!("{spec}.md"));
        if in_dir.is_file() {
            return Template::from_file(&in_dir);
        }
        let in_dir_exact = template_dir.join(spec);
        if in_dir_exact.is_file() {
            return Template::from_file(&in_dir_exact);
        }
        let known = list(template_dir)
            .map(|ts| {
                ts.iter()
                    .map(|t| t.name.clone())
                    .collect::<Vec<_>>()
                    .join(", ")
            })
            .unwrap_or_default();
        bail!(
            "template \"{spec}\" not found (looked at {} and {}); available: {}",
            as_path.display(),
            in_dir.display(),
            if known.is_empty() {
                "none".into()
            } else {
                known
            }
        )
    }

    /// Variable names actually referenced by the body.
    pub fn placeholders(&self) -> Vec<String> {
        let mut names: Vec<String> = placeholder_re()
            .captures_iter(&self.body)
            .map(|c| c[1].to_string())
            .collect();
        names.sort();
        names.dedup();
        names
    }

    /// Substitutes `{{var}}`; missing values are an error unless `allow_missing`.
    pub fn render(&self, vars: &BTreeMap<String, String>, allow_missing: bool) -> Result<String> {
        let mut effective = BTreeMap::new();
        for (k, spec) in &self.meta.vars {
            if let Some(d) = spec.default() {
                effective.insert(k.clone(), d);
            }
        }
        effective.extend(vars.clone());

        let mut missing: Vec<String> = Vec::new();
        for (k, spec) in &self.meta.vars {
            if spec.required() && !effective.contains_key(k) {
                missing.push(k.clone());
            }
        }
        for name in self.placeholders() {
            if !effective.contains_key(&name) && !missing.contains(&name) {
                missing.push(name);
            }
        }
        if !missing.is_empty() && !allow_missing {
            missing.sort();
            missing.dedup();
            bail!(
                "template \"{}\" is missing values for: {} (pass --var name=value, or --allow-missing)",
                self.name,
                missing.join(", ")
            );
        }

        let declared = &self.meta.vars;
        let referenced = self.placeholders();
        for k in vars.keys() {
            if !declared.contains_key(k) && !referenced.contains(k) {
                eprintln!(
                    "notif: warning: --var {k} is not used by template \"{}\"",
                    self.name
                );
            }
        }

        Ok(placeholder_re()
            .replace_all(&self.body, |c: &Captures| {
                effective.get(&c[1]).cloned().unwrap_or_default()
            })
            .into_owned())
    }
}

/// Lists `*.md` templates in `dir`, sorted by name. A missing dir yields an empty list.
pub fn list(dir: &Path) -> Result<Vec<Template>> {
    if !dir.is_dir() {
        return Ok(Vec::new());
    }
    let mut out = Vec::new();
    let mut entries: Vec<PathBuf> = std::fs::read_dir(dir)
        .with_context(|| format!("reading template dir {}", dir.display()))?
        .filter_map(|e| e.ok())
        .map(|e| e.path())
        .filter(|p| {
            p.is_file()
                && p.extension().is_some_and(|e| {
                    e.eq_ignore_ascii_case("md") || e.eq_ignore_ascii_case("markdown")
                })
        })
        .collect();
    entries.sort();
    for p in entries {
        match Template::from_file(&p) {
            Ok(t) => out.push(t),
            Err(e) => eprintln!("notif: warning: skipping {}: {e:#}", p.display()),
        }
    }
    Ok(out)
}

/// Splits a leading `---` … `---` TOML frontmatter block from the body.
fn split_frontmatter(raw: &str) -> Result<(Meta, String)> {
    let Some(rest) = raw.strip_prefix("---\n") else {
        return Ok((Meta::default(), raw.to_string()));
    };
    let Some(end) = find_fence(rest) else {
        bail!("frontmatter opened with `---` but never closed");
    };
    let (front, body) = rest.split_at(end);
    let body = body
        .trim_start_matches("---")
        .trim_start_matches('\n')
        .to_string();
    let meta: Meta = toml::from_str(front).context("frontmatter is not valid TOML")?;
    Ok((meta, body))
}

fn find_fence(s: &str) -> Option<usize> {
    let mut offset = 0usize;
    for line in s.split_inclusive('\n') {
        if line.trim_end() == "---" {
            return Some(offset);
        }
        offset += line.len();
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;

    fn vars(pairs: &[(&str, &str)]) -> BTreeMap<String, String> {
        pairs
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect()
    }

    #[test]
    fn renders_placeholders() {
        let t = Template::parse("t", None, "Hello {{ name }} / {{name}}").unwrap();
        assert_eq!(
            t.render(&vars(&[("name", "world")]), false).unwrap(),
            "Hello world / world"
        );
    }

    #[test]
    fn reports_missing_vars() {
        let t = Template::parse("t", None, "Hi {{name}} {{other}}").unwrap();
        let err = t.render(&BTreeMap::new(), false).unwrap_err().to_string();
        assert!(err.contains("name"), "{err}");
        assert!(err.contains("other"), "{err}");
        assert_eq!(t.render(&BTreeMap::new(), true).unwrap(), "Hi  ");
    }

    #[test]
    fn frontmatter_is_parsed_and_stripped() {
        let raw = "---\ndescription = \"d\"\nchannel = \"alerts\"\n\n[vars]\nenv = { default = \"prod\" }\nsvc = \"the service\"\n---\n{{svc}} on {{env}}\n";
        let t = Template::parse("t", None, raw).unwrap();
        assert_eq!(t.meta.description.as_deref(), Some("d"));
        assert_eq!(t.meta.channel.as_deref(), Some("alerts"));
        assert!(t.meta.vars["svc"].required());
        assert!(!t.meta.vars["env"].required());
        assert_eq!(
            t.render(&vars(&[("svc", "api")]), false).unwrap().trim(),
            "api on prod"
        );
    }

    #[test]
    fn body_without_frontmatter_is_untouched() {
        let t = Template::parse("t", None, "# Hi\n\n---\n\nbye").unwrap();
        assert_eq!(t.body, "# Hi\n\n---\n\nbye");
    }
}
