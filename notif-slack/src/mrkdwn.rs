//! Markdown -> Slack `mrkdwn` conversion.
//!
//! Slack does not speak CommonMark: it has its own single-asterisk bold, `<url|label>`
//! links and no headings or tables. This module rewrites the common subset so a plain
//! `.md` template renders the way the author meant it.

use regex::{Captures, Regex};
use std::sync::OnceLock;

/// Private-use codepoints, so placeholders can never collide with real content.
const PH_OPEN: char = '\u{E000}';
const PH_CLOSE: char = '\u{E001}';
/// Stand-in for a literal `*` that must survive italic conversion.
const BOLD_MARK: char = '\u{E002}';

/// Compiles each literal pattern once, at its own call site.
macro_rules! re {
    ($pattern:literal) => {{
        static RE: OnceLock<Regex> = OnceLock::new();
        RE.get_or_init(|| Regex::new($pattern).expect("built-in regex must compile"))
    }};
}

/// Text pulled out of the stream before escaping and put back at the very end.
struct Vault(Vec<String>);

impl Vault {
    fn new() -> Self {
        Vault(Vec::new())
    }
    fn stash(&mut self, s: String) -> String {
        self.0.push(s);
        format!("{PH_OPEN}{}{PH_CLOSE}", self.0.len() - 1)
    }
    fn restore(&self, s: &str) -> String {
        if self.0.is_empty() {
            return s.to_string();
        }
        re!(r"\x{E000}(\d+)\x{E001}")
            .replace_all(s, |c: &Captures| {
                let i: usize = c[1].parse().unwrap_or(usize::MAX);
                self.0.get(i).cloned().unwrap_or_default()
            })
            .into_owned()
    }
}

/// Convert a Markdown document to Slack mrkdwn.
pub fn to_mrkdwn(src: &str) -> String {
    let mut vault = Vault::new();

    let s = src.replace("\r\n", "\n");
    let s = tables_to_code_blocks(&s);
    let s = protect_fenced_code(&s, &mut vault);
    let s = protect_inline_code(&s, &mut vault);
    let s = protect_slack_tokens(&s, &mut vault);
    let s = escape(&s);
    let s = inline_spans(&s);
    let s = line_blocks(&s);
    let s = mentions(&s);
    let s = collapse_blank_runs(&s);
    vault.restore(&s)
}

/// Slack's `header` block takes plain text of at most this many characters.
pub const MAX_HEADER_CHARS: usize = 150;
/// A `section` block's text is capped here, far below a plain message's limit.
pub const MAX_SECTION_CHARS: usize = 3000;
/// Slack rejects a message carrying more blocks than this.
pub const MAX_BLOCKS: usize = 50;

/// A message that opens with a top-level `# Heading` as Block Kit blocks: the heading in a
/// `header` block, which Slack sets in larger type, each horizontal rule as a `divider`, and
/// the text between them as mrkdwn `section`s. `None` when there is no leading `# ` or any
/// part is too long for its block, so the caller posts plain text exactly as it always has.
pub fn header_blocks(src: &str) -> Option<serde_json::Value> {
    let heading = re!(r"^#[ \t]+(.*?)[ \t]*#*[ \t]*$");

    let src = src.replace("\r\n", "\n");
    let src = src.trim_start_matches(['\n', ' ', '\t']);
    let (first, rest) = src.split_once('\n').unwrap_or((src, ""));
    let title = plain_heading(&heading.captures(first)?[1]);
    if title.is_empty() || title.chars().count() > MAX_HEADER_CHARS {
        return None;
    }

    let mut blocks = vec![serde_json::json!({
        "type": "header",
        "text": { "type": "plain_text", "text": title, "emoji": true },
    })];
    for (i, part) in split_on_rules(rest).iter().enumerate() {
        if i > 0 {
            blocks.push(serde_json::json!({ "type": "divider" }));
        }
        let body = to_mrkdwn(part);
        let body = body.trim();
        if body.is_empty() {
            continue;
        }
        if body.chars().count() > MAX_SECTION_CHARS {
            return None;
        }
        blocks.push(serde_json::json!({
            "type": "section",
            "text": { "type": "mrkdwn", "text": body },
        }));
    }
    if blocks.len() > MAX_BLOCKS {
        return None;
    }
    Some(serde_json::Value::Array(blocks))
}

/// Markdown split at each horizontal rule, ignoring `---` inside a fenced code block.
fn split_on_rules(src: &str) -> Vec<String> {
    let mut parts = vec![String::new()];
    let mut fenced = false;
    for line in src.split('\n') {
        let t = line.trim_start();
        if t.starts_with("```") || t.starts_with("~~~") {
            fenced = !fenced;
        }
        if !fenced && is_horizontal_rule(line) {
            parts.push(String::new());
            continue;
        }
        let last = parts.last_mut().expect("parts starts non-empty");
        last.push_str(line);
        last.push('\n');
    }
    parts
}

/// A header block shows its text literally, so Markdown emphasis around a word would print
/// as asterisks; only paired markers are removed, leaving a lone `_` in a name alone.
fn plain_heading(s: &str) -> String {
    let s = re!(r"\*\*(.+?)\*\*").replace_all(s, "$1");
    let s = re!(r"__(.+?)__").replace_all(&s, "$1");
    let s = re!(r"\*([^*\s][^*]*?)\*").replace_all(&s, "$1");
    let s = re!(r"\b_([^_\s][^_]*?)_\b").replace_all(&s, "$1");
    let s = re!(r"`([^`]+)`").replace_all(&s, "$1");
    s.trim().to_string()
}

/// Slack needs these three escaped everywhere; nothing else.
fn escape(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for c in s.chars() {
        match c {
            '&' => out.push_str("&amp;"),
            '<' => out.push_str("&lt;"),
            '>' => out.push_str("&gt;"),
            _ => out.push(c),
        }
    }
    out
}

/// ```lang\n…\n``` -> a Slack code block, content escaped but otherwise untouched.
fn protect_fenced_code(s: &str, vault: &mut Vault) -> String {
    let mut out = String::with_capacity(s.len());
    let mut body: Option<String> = None;
    for line in s.split_inclusive('\n') {
        let trimmed = line.trim_end_matches('\n').trim_end();
        let is_fence = trimmed.trim_start().starts_with("```");
        match (&mut body, is_fence) {
            (None, true) => body = Some(String::new()),
            (Some(b), true) => {
                let block = format!("```\n{}```", escape(b));
                out.push_str(&vault.stash(block));
                out.push('\n');
                body = None;
            }
            (Some(b), false) => b.push_str(line),
            (None, false) => out.push_str(line),
        }
    }
    // Unterminated fence: emit what we have rather than losing it.
    if let Some(b) = body {
        let block = format!("```\n{}\n```", escape(b.trim_end_matches('\n')));
        out.push_str(&vault.stash(block));
    }
    out
}

fn protect_inline_code(s: &str, vault: &mut Vault) -> String {
    re!(r"`([^`\n]+)`")
        .replace_all(s, |c: &Captures| {
            vault.stash(format!("`{}`", escape(&c[1])))
        })
        .into_owned()
}

/// Keep hand-written Slack entities (`<@U…>`, `<#C…|name>`, `<!here>`, `<http…|x>`) intact.
fn protect_slack_tokens(s: &str, vault: &mut Vault) -> String {
    re!(r"<(?:[@#!][A-Za-z0-9_^|&;.,'\-]+|https?://[^<>\s]+(?:\|[^<>]*)?)>")
        .replace_all(s, |c: &Captures| vault.stash(c[0].to_string()))
        .into_owned()
}

/// Links, emphasis and strikethrough.
fn inline_spans(s: &str) -> String {
    // Images first, so the leading `!` is consumed before the link rule sees them.
    let s = re!(r"!\[([^\]]*)\]\(\s*(\S+?)(?:\s+&quot;[^&]*&quot;)?\s*\)")
        .replace_all(s, |c: &Captures| link(&c[2], &c[1]))
        .into_owned();
    let s = re!(r"\[([^\]]*)\]\(\s*(\S+?)(?:\s+&quot;[^&]*&quot;)?\s*\)")
        .replace_all(&s, |c: &Captures| link(&c[2], &c[1]))
        .into_owned();
    // Bare autolinks written as <https://…> survived escaping as &lt;…&gt;.
    let s = re!(r"&lt;((?:https?|mailto):[^\s|]+?)&gt;")
        .replace_all(&s, "<$1>")
        .into_owned();

    // ***both*** -> *_both_*, **bold** -> *bold*, remaining *x* -> _x_.
    let s = re!(r"\*\*\*([^*\n]+)\*\*\*")
        .replace_all(&s, |c: &Captures| {
            format!("{BOLD_MARK}_{}_{BOLD_MARK}", &c[1])
        })
        .into_owned();
    let s = re!(r"\*\*([^*\n]+)\*\*")
        .replace_all(&s, |c: &Captures| {
            format!("{BOLD_MARK}{}{BOLD_MARK}", &c[1])
        })
        .into_owned();
    let s = re!(r"\*([^*\n]+)\*").replace_all(&s, "_${1}_").into_owned();
    let s = s.replace(BOLD_MARK, "*");

    re!(r"~~([^~\n]+)~~").replace_all(&s, "~$1~").into_owned()
}

fn link(url: &str, label: &str) -> String {
    let url = url.trim();
    let label = label.trim();
    if label.is_empty() || label == url {
        format!("<{url}>")
    } else {
        format!("<{url}|{label}>")
    }
}

/// Line-oriented structure: headings, rules, quotes and bullets.
fn line_blocks(s: &str) -> String {
    let heading = re!(r"^\s{0,3}(#{1,6})\s+(.*?)\s*#*$");
    let heading_bold = re!(r"\*([^*\n]+)\*");
    let quote = re!(r"^(\s*)((?:&gt;\s*)+)(.*)$");
    let bullet = re!(r"^(\s*)[-*+]\s+(.*)$");
    let task = re!(r"^\[([ xX])\]\s+(.*)$");
    let ordered = re!(r"^(\s*)(\d+)([.)])\s+(.*)$");

    let mut out: Vec<String> = Vec::new();
    for line in s.split('\n') {
        if line.starts_with(PH_OPEN) {
            out.push(line.to_string());
            continue;
        }
        let line = line.to_string();

        if let Some(c) = heading.captures(&line) {
            let text = c[2].trim();
            if text.is_empty() {
                out.push(String::new());
            } else {
                // The whole heading is bold, so drop any inner bold markers:
                // `*Hi *there**` is not something Slack renders sensibly.
                let text = heading_bold.replace_all(text, "${1}");
                out.push(format!("*{}*", text.trim()));
            }
            continue;
        }
        if is_horizontal_rule(&line) {
            out.push("────────────────────".to_string());
            continue;
        }
        // Blockquotes survive escaping as `&gt;`.
        if let Some(c) = quote.captures(&line) {
            let depth = c[2].matches("&gt;").count().min(2);
            out.push(format!("{}{}", ">".repeat(depth), {
                let rest = c[3].trim_start();
                if rest.is_empty() {
                    String::new()
                } else {
                    format!(" {rest}")
                }
            }));
            continue;
        }
        if let Some(c) = bullet.captures(&line) {
            let indent = c[1].len() / 2;
            let bullet = match indent {
                0 => "•",
                1 => "◦",
                _ => "▪",
            };
            let mut body = c[2].to_string();
            if let Some(t) = task.captures(&body.clone()) {
                let mark = if t[1].eq_ignore_ascii_case("x") {
                    "☑"
                } else {
                    "☐"
                };
                body = format!("{mark} {}", &t[2]);
            }
            out.push(format!("{}{bullet} {body}", "    ".repeat(indent)));
            continue;
        }
        if let Some(c) = ordered.captures(&line) {
            let indent = c[1].len() / 2;
            out.push(format!("{}{}. {}", "    ".repeat(indent), &c[2], &c[4]));
            continue;
        }
        out.push(line);
    }
    out.join("\n")
}

/// `---`, `***`, `___` (optionally space-separated) on a line of their own.
fn is_horizontal_rule(line: &str) -> bool {
    let t = line.trim();
    let Some(marker) = t.chars().next() else {
        return false;
    };
    if !matches!(marker, '-' | '*' | '_') {
        return false;
    }
    let count = t.chars().filter(|c| *c == marker).count();
    count >= 3 && t.chars().all(|c| c == marker || c == ' ')
}

/// `@here` / `@channel` / `@everyone` become real broadcast mentions.
fn mentions(s: &str) -> String {
    re!(r"(?:^|(?P<pre>[^\w<&]))@(?P<who>here|channel|everyone)\b")
        .replace_all(s, |c: &Captures| {
            format!(
                "{}<!{}>",
                c.name("pre").map(|m| m.as_str()).unwrap_or(""),
                &c["who"]
            )
        })
        .into_owned()
}

fn collapse_blank_runs(s: &str) -> String {
    re!(r"\n{3,}")
        .replace_all(s.trim_matches('\n'), "\n\n")
        .into_owned()
}

/// Markdown tables do not render in Slack at all, so align them into a code block.
fn tables_to_code_blocks(s: &str) -> String {
    let lines: Vec<&str> = s.split('\n').collect();
    let mut out: Vec<String> = Vec::new();
    let mut i = 0;
    while i < lines.len() {
        if i + 1 < lines.len() && is_table_row(lines[i]) && is_table_divider(lines[i + 1]) {
            let mut rows = vec![split_row(lines[i])];
            let aligns = split_row(lines[i + 1]);
            let mut j = i + 2;
            while j < lines.len() && is_table_row(lines[j]) {
                rows.push(split_row(lines[j]));
                j += 1;
            }
            out.push(render_table(&rows, &aligns));
            i = j;
            continue;
        }
        out.push(lines[i].to_string());
        i += 1;
    }
    out.join("\n")
}

fn is_table_row(l: &str) -> bool {
    let t = l.trim();
    t.starts_with('|') && t.len() > 1 && t.matches('|').count() >= 2
}

fn is_table_divider(l: &str) -> bool {
    is_table_row(l)
        && split_row(l).iter().all(|c| {
            !c.is_empty() && c.chars().all(|ch| matches!(ch, '-' | ':' | ' ')) && c.contains('-')
        })
}

fn split_row(l: &str) -> Vec<String> {
    let t = l.trim().trim_start_matches('|').trim_end_matches('|');
    t.split('|').map(|c| c.trim().to_string()).collect()
}

fn render_table(rows: &[Vec<String>], aligns: &[String]) -> String {
    let cols = rows.iter().map(Vec::len).max().unwrap_or(0);
    let mut widths = vec![0usize; cols];
    for row in rows {
        for (i, cell) in row.iter().enumerate() {
            widths[i] = widths[i].max(display_width(cell));
        }
    }
    let right: Vec<bool> = (0..cols)
        .map(|i| {
            aligns
                .get(i)
                .is_some_and(|a| a.trim().ends_with(':') && !a.trim().starts_with(':'))
        })
        .collect();

    let mut buf = String::from("```\n");
    for (r, row) in rows.iter().enumerate() {
        let cells: Vec<String> = (0..cols)
            .map(|i| {
                let cell = row.get(i).map(String::as_str).unwrap_or("");
                let pad = widths[i].saturating_sub(display_width(cell));
                if right[i] {
                    format!("{}{cell}", " ".repeat(pad))
                } else {
                    format!("{cell}{}", " ".repeat(pad))
                }
            })
            .collect();
        buf.push_str(cells.join("  ").trim_end());
        buf.push('\n');
        if r == 0 {
            let rule: Vec<String> = widths.iter().map(|w| "─".repeat(*w)).collect();
            buf.push_str(rule.join("  ").trim_end());
            buf.push('\n');
        }
    }
    buf.push_str("```");
    buf
}

/// Strips inline markdown so column widths line up with what Slack will show.
fn display_width(s: &str) -> usize {
    s.chars().filter(|c| !matches!(c, '*' | '`' | '_')).count()
}

// ------------------------------------------------------- mrkdwn -> Markdown

/// Resolves Slack ids while rendering a message back to Markdown.
///
/// A lookup that returns `None` leaves the raw `<@U…>` in place: a wrong name is worse
/// than an unresolved id, and name lookups are allowed to fail (scope, rate limit).
pub trait Names {
    fn user(&self, id: &str) -> Option<String>;
    fn channel(&self, id: &str) -> Option<String>;
}

/// Resolves nothing; ids stay as Slack wrote them.
#[cfg(test)]
pub struct NoNames;

#[cfg(test)]
impl Names for NoNames {
    fn user(&self, _id: &str) -> Option<String> {
        None
    }
    fn channel(&self, _id: &str) -> Option<String> {
        None
    }
}

/// Convert a Slack message's `text` back to readable Markdown.
pub fn from_mrkdwn(src: &str, names: &dyn Names) -> String {
    let mut vault = Vault::new();

    let s = src.replace("\r\n", "\n");
    let s = unprotect_code(&s, &mut vault);
    let s = entities(&s, names, &mut vault);
    let s = emphasis(&s);
    let s = unline_blocks(&s);
    let s = unescape(&s);
    vault.restore(&s)
}

/// Undoes Slack's three required escapes, in one pass so `&amp;lt;` stays `&lt;`.
fn unescape(s: &str) -> String {
    if !s.contains('&') {
        return s.to_string();
    }
    let mut out = String::with_capacity(s.len());
    let mut rest = s;
    while let Some(i) = rest.find('&') {
        out.push_str(&rest[..i]);
        let tail = &rest[i..];
        if let Some(r) = tail.strip_prefix("&amp;") {
            out.push('&');
            rest = r;
        } else if let Some(r) = tail.strip_prefix("&lt;") {
            out.push('<');
            rest = r;
        } else if let Some(r) = tail.strip_prefix("&gt;") {
            out.push('>');
            rest = r;
        } else {
            out.push('&');
            rest = &tail[1..];
        }
    }
    out.push_str(rest);
    out
}

/// Stashes code spans and fenced blocks whole, so nothing below rewrites their contents.
/// Only the transport escaping is undone inside them — `&lt;` was never source text.
fn unprotect_code(s: &str, vault: &mut Vault) -> String {
    let mut out = String::with_capacity(s.len());
    let mut rest = s;
    while let Some(start) = rest.find("```") {
        out.push_str(&rest[..start]);
        let after = &rest[start + 3..];
        match after.find("```") {
            Some(end) => {
                out.push_str(&vault.stash(format!("```{}```", unescape(&after[..end]))));
                rest = &after[end + 3..];
            }
            // Unterminated fence: leave the rest alone rather than swallowing it.
            None => {
                out.push_str(&rest[start..]);
                rest = "";
                break;
            }
        }
    }
    out.push_str(rest);

    re!(r"`([^`\n]+)`")
        .replace_all(&out, |c: &Captures| {
            vault.stash(format!("`{}`", unescape(&c[1])))
        })
        .into_owned()
}

/// `<@U…>`, `<#C…|name>`, `<!here>` and `<url|text>` back to something a person can read.
fn entities(s: &str, names: &dyn Names, vault: &mut Vault) -> String {
    re!(r"<([^<>\n]*)>")
        .replace_all(s, |c: &Captures| {
            let raw = &c[0];
            let (body, label) = match c[1].split_once('|') {
                Some((b, l)) => (b, Some(l)),
                None => (&c[1], None),
            };
            let label = label.map(unescape).filter(|l| !l.is_empty());
            let out = match body.chars().next() {
                Some('@') => match label.or_else(|| names.user(&body[1..])) {
                    Some(n) => format!("@{}", n.trim_start_matches('@')),
                    None => raw.to_string(),
                },
                Some('#') => match label.or_else(|| names.channel(&body[1..])) {
                    Some(n) => format!("#{}", n.trim_start_matches('#')),
                    None => raw.to_string(),
                },
                Some('!') => match &body[1..] {
                    broadcast @ ("here" | "channel" | "everyone") => format!("@{broadcast}"),
                    _ => match label {
                        Some(l) => l,
                        None => raw.to_string(),
                    },
                },
                _ if is_url(body) => link_markdown(&unescape(body), label),
                // Not a Slack entity after all (a stray angle bracket); leave it be.
                _ => raw.to_string(),
            };
            vault.stash(out)
        })
        .into_owned()
}

/// User ids mentioned in a message, so a read can warm the name cache in one call.
pub fn mentioned_user_ids(s: &str) -> Vec<String> {
    re!(r"<@([UW][A-Z0-9]+)")
        .captures_iter(s)
        .map(|c| c[1].to_string())
        .collect()
}

fn is_url(s: &str) -> bool {
    s.contains("://") || s.starts_with("mailto:") || s.starts_with("tel:")
}

fn link_markdown(url: &str, label: Option<String>) -> String {
    match label {
        // Slack labels a bare link with the link itself; that is not a Markdown link.
        Some(l) if l == url || Some(l.as_str()) == url.strip_prefix("mailto:") => l,
        Some(l) => format!("[{l}]({url})"),
        None => url.to_string(),
    }
}

/// `*bold*` -> `**bold**` and `~struck~` -> `~~struck~~`; `_italic_` already is Markdown.
fn emphasis(s: &str) -> String {
    let s = re!(r"\*([^\s*][^*\n]*[^\s*]|[^\s*])\*")
        .replace_all(s, "**$1**")
        .into_owned();
    re!(r"~([^\s~][^~\n]*[^\s~]|[^\s~])~")
        .replace_all(&s, "~~$1~~")
        .into_owned()
}

/// Bullets, task marks and the drawn horizontal rule, back to their Markdown spelling.
fn unline_blocks(s: &str) -> String {
    let bullet = re!(r"^(\s*)([\x{2022}\x{25E6}\x{25AA}])\s+(.*)$");
    let task = re!(r"^([\x{2611}\x{2610}])\s+(.*)$");

    let mut out: Vec<String> = Vec::new();
    for line in s.split('\n') {
        if line.starts_with(PH_OPEN) {
            out.push(line.to_string());
            continue;
        }
        if let Some(c) = bullet.captures(line) {
            let depth = match &c[2] {
                "\u{2022}" => 0,
                "\u{25E6}" => 1,
                _ => 2,
            };
            let mut body = c[3].to_string();
            if let Some(t) = task.captures(&body.clone()) {
                let mark = if &t[1] == "\u{2611}" { "x" } else { " " };
                body = format!("[{mark}] {}", &t[2]);
            }
            out.push(format!("{}- {body}", "  ".repeat(depth)));
            continue;
        }
        if is_drawn_rule(line) {
            out.push("---".to_string());
            continue;
        }
        out.push(line.to_string());
    }
    out.join("\n")
}

fn is_drawn_rule(line: &str) -> bool {
    let t = line.trim();
    t.chars().count() >= 3 && t.chars().all(|c| c == '\u{2500}')
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A lookup with a couple of known ids, for the mention tests.
    struct TestNames;

    impl Names for TestNames {
        fn user(&self, id: &str) -> Option<String> {
            (id == "U01ABCDEF").then(|| "alice".to_string())
        }
        fn channel(&self, id: &str) -> Option<String> {
            (id == "C0123").then(|| "dev".to_string())
        }
    }

    fn back(s: &str) -> String {
        from_mrkdwn(s, &NoNames)
    }

    #[test]
    fn reverse_emphasis() {
        assert_eq!(back("*bold*"), "**bold**");
        assert_eq!(back("_italic_"), "_italic_");
        assert_eq!(back("~gone~"), "~~gone~~");
        assert_eq!(back("*_both_*"), "**_both_**");
        assert_eq!(back("2 * 3 * 4"), "2 * 3 * 4");
    }

    #[test]
    fn reverse_links_and_escapes() {
        assert_eq!(back("<https://x.dev|docs>"), "[docs](https://x.dev)");
        assert_eq!(back("<https://x.dev>"), "https://x.dev");
        assert_eq!(back("<https://x.dev|https://x.dev>"), "https://x.dev");
        assert_eq!(back("<mailto:a@b.dev|a@b.dev>"), "a@b.dev");
        assert_eq!(back("a &lt; b &amp; c &gt; d"), "a < b & c > d");
        assert_eq!(back("&amp;lt; stays"), "&lt; stays");
    }

    #[test]
    fn reverse_mentions() {
        assert_eq!(
            from_mrkdwn("hi <@U01ABCDEF> in <#C0123|dev>", &TestNames),
            "hi @alice in #dev"
        );
        assert_eq!(from_mrkdwn("hi <#C0123>", &TestNames), "hi #dev");
        assert_eq!(from_mrkdwn("<!here> look", &TestNames), "@here look");
        assert_eq!(
            from_mrkdwn("<!subteam^S1|@eng> look", &TestNames),
            "@eng look"
        );
        // Unknown ids degrade to what Slack sent rather than guessing a name.
        assert_eq!(back("hi <@U01ABCDEF>"), "hi <@U01ABCDEF>");
        assert_eq!(back("hi <@U9|alice>"), "hi @alice");
    }

    #[test]
    fn reverse_lists_and_rules() {
        assert_eq!(back("• one\n• two"), "- one\n- two");
        assert_eq!(back("    ◦ nested"), "  - nested");
        assert_eq!(back("• ☑ done\n• ☐ todo"), "- [x] done\n- [ ] todo");
        assert_eq!(back("────────────────────"), "---");
        assert_eq!(back("> quoted"), "> quoted");
        assert_eq!(back("&gt; quoted"), "> quoted");
    }

    #[test]
    fn reverse_leaves_code_alone() {
        assert_eq!(back("`a *b* &lt;c&gt;`"), "`a *b* <c>`");
        assert_eq!(
            back("```\n• not a bullet\nlet x = a &lt; b;\n```"),
            "```\n• not a bullet\nlet x = a < b;\n```"
        );
        assert_eq!(back("before ```*x*``` after"), "before ```*x*``` after");
        assert_eq!(back("unclosed ``` *x*"), "unclosed ``` **x**");
    }

    #[test]
    fn round_trips() {
        for src in [
            "**bold** and _soft_ and ~~gone~~",
            "- one\n- two\n- [x] done",
            "a < b & c > d",
            "[docs](https://x.dev) then more",
            "```\nlet x = a < b;\n```",
            "> quoted line",
            "1. first\n2. second",
        ] {
            assert_eq!(back(&to_mrkdwn(src)), src, "round trip of {src:?}");
        }
    }

    #[test]
    fn bold_italic_and_strike() {
        assert_eq!(to_mrkdwn("**bold**"), "*bold*");
        assert_eq!(to_mrkdwn("*italic*"), "_italic_");
        assert_eq!(to_mrkdwn("_italic_"), "_italic_");
        assert_eq!(to_mrkdwn("***both***"), "*_both_*");
        assert_eq!(to_mrkdwn("~~gone~~"), "~gone~");
    }

    #[test]
    fn headings_become_bold() {
        assert_eq!(to_mrkdwn("# Title\n\nbody"), "*Title*\n\nbody");
        assert_eq!(to_mrkdwn("### Sub ###"), "*Sub*");
        assert_eq!(to_mrkdwn("## Hi **there**"), "*Hi there*");
        assert_eq!(to_mrkdwn("## _soft_ Hi"), "*_soft_ Hi*");
    }

    #[test]
    fn links_and_images() {
        assert_eq!(to_mrkdwn("[docs](https://x.dev)"), "<https://x.dev|docs>");
        assert_eq!(
            to_mrkdwn("![](https://x.dev/a.png)"),
            "<https://x.dev/a.png>"
        );
        assert_eq!(to_mrkdwn("<https://x.dev>"), "<https://x.dev>");
    }

    #[test]
    fn escapes_html_entities() {
        assert_eq!(to_mrkdwn("a < b & c > d"), "a &lt; b &amp; c &gt; d");
    }

    #[test]
    fn code_is_left_alone() {
        assert_eq!(to_mrkdwn("`a **b** <c>`"), "`a **b** &lt;c&gt;`");
        assert_eq!(
            to_mrkdwn("```rust\nlet x = a < b;\n```"),
            "```\nlet x = a &lt; b;\n```"
        );
    }

    #[test]
    fn lists_and_quotes() {
        assert_eq!(to_mrkdwn("- one\n- two"), "• one\n• two");
        assert_eq!(to_mrkdwn("- [x] done\n- [ ] todo"), "• ☑ done\n• ☐ todo");
        assert_eq!(to_mrkdwn("  - nested"), "    ◦ nested");
        assert_eq!(to_mrkdwn("> quoted"), "> quoted");
        assert_eq!(to_mrkdwn("1. first"), "1. first");
    }

    #[test]
    fn slack_entities_survive() {
        assert_eq!(
            to_mrkdwn("hi <@U123> in <#C456|dev>"),
            "hi <@U123> in <#C456|dev>"
        );
        assert_eq!(to_mrkdwn("ping @here now"), "ping <!here> now");
        assert_eq!(to_mrkdwn("email a@here.com"), "email a@here.com");
    }

    #[test]
    fn tables_align_inside_code_block() {
        let got = to_mrkdwn("| a | bb |\n|---|---:|\n| 1 | 2 |");
        assert_eq!(got, "```\na  bb\n─  ──\n1   2\n```");
    }

    #[test]
    fn horizontal_rule() {
        assert_eq!(to_mrkdwn("a\n\n---\n\nb"), "a\n\n────────────────────\n\nb");
    }

    #[test]
    fn a_leading_h1_becomes_a_header_block_and_the_rest_a_section() {
        let b = header_blocks("# Weekly status update\n\n_Product flow · 2026-10-05_\n\n- **Built** 10").unwrap();
        assert_eq!(b[0]["type"], "header");
        assert_eq!(b[0]["text"]["type"], "plain_text");
        assert_eq!(b[0]["text"]["text"], "Weekly status update");
        assert_eq!(b[1]["type"], "section");
        assert_eq!(b[1]["text"]["text"], "_Product flow · 2026-10-05_\n\n• *Built* 10");
        assert_eq!(b.as_array().unwrap().len(), 2);
    }

    #[test]
    fn only_a_top_level_heading_on_the_first_line_counts() {
        assert!(header_blocks("## Smaller heading\nbody").is_none());
        assert!(header_blocks("Intro line\n# Late heading").is_none());
        assert!(header_blocks("#hashtag, not a heading").is_none());
        assert!(header_blocks("\n\n# After blank lines\nbody").is_some());
    }

    #[test]
    fn a_heading_alone_is_just_a_header_block() {
        let b = header_blocks("# Just this\n\n").unwrap();
        assert_eq!(b.as_array().unwrap().len(), 1);
    }

    #[test]
    fn header_text_loses_paired_emphasis_but_keeps_lone_underscores() {
        let b = header_blocks("# **Deploy** of `api` _today_ for snake_case_name").unwrap();
        assert_eq!(b[0]["text"]["text"], "Deploy of api today for snake_case_name");
    }

    #[test]
    fn too_long_for_a_block_falls_back_to_plain_text() {
        let long_title = format!("# {}", "x".repeat(MAX_HEADER_CHARS + 1));
        assert!(header_blocks(&long_title).is_none());
        let long_body = format!("# Title\n{}", "y".repeat(MAX_SECTION_CHARS + 1));
        assert!(header_blocks(&long_body).is_none());
    }

    #[test]
    fn horizontal_rules_become_dividers_between_sections() {
        let b = header_blocks("# Title\n**2026-10-05**\n\n---\n\nBody\n\n---\nFooter").unwrap();
        let kinds: Vec<&str> = b.as_array().unwrap().iter().map(|x| x["type"].as_str().unwrap()).collect();
        assert_eq!(kinds, ["header", "section", "divider", "section", "divider", "section"]);
        assert_eq!(b[1]["text"]["text"], "*2026-10-05*");
        assert_eq!(b[5]["text"]["text"], "Footer");
    }

    #[test]
    fn a_rule_inside_a_code_fence_is_not_a_divider() {
        let b = header_blocks("# Title\n```\n---\n```").unwrap();
        assert_eq!(b.as_array().unwrap().len(), 2);
        assert!(b[1]["text"]["text"].as_str().unwrap().contains("---"));
    }
}
