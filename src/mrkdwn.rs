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

#[cfg(test)]
mod tests {
    use super::*;

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
}
