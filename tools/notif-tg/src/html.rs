//! Markdown -> Telegram HTML, and splitting into messages Telegram will accept.
//!
//! Telegram's `parse_mode=HTML` understands a small fixed tag set and nothing
//! else; it is not real HTML. It also has no headings and no tables, and caps a
//! message at 4096 UTF-16 code units. This module handles all three.

use regex::{Captures, Regex};
use std::sync::OnceLock;

/// Telegram's per-message limit, in UTF-16 code units.
pub const MAX_MESSAGE_UNITS: usize = 4096;

/// Private-use codepoints, so placeholders can never collide with real content.
const PH_OPEN: char = '\u{E000}';
const PH_CLOSE: char = '\u{E001}';

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

/// Convert a Markdown document to Telegram HTML as a single message.
pub fn to_html(src: &str) -> String {
    let mut vault = Vault::new();

    let s = src.replace("\r\n", "\n");
    let s = tables_to_code_blocks(&s);
    let s = protect_fenced_code(&s, &mut vault);
    let s = protect_inline_code(&s, &mut vault);
    let s = escape(&s);
    let s = inline_spans(&s);
    let s = line_blocks(&s);
    let s = collapse_blank_runs(&s);
    vault.restore(&s)
}

/// Convert and split, so every returned string is a valid, tag-balanced message.
///
/// Splitting happens on the Markdown side — whole paragraphs, then lines, then
/// characters — so a message can never end mid-tag.
pub fn to_messages(src: &str, limit: usize) -> Vec<String> {
    let whole = to_html(src);
    if units(&whole) <= limit {
        return if whole.trim().is_empty() {
            Vec::new()
        } else {
            vec![whole]
        };
    }

    let mut out: Vec<String> = Vec::new();
    let mut pending: Vec<String> = Vec::new();

    let flush = |pending: &mut Vec<String>, out: &mut Vec<String>| {
        if !pending.is_empty() {
            let html = to_html(&pending.join("\n\n"));
            if !html.trim().is_empty() {
                out.push(html);
            }
            pending.clear();
        }
    };

    for block in blocks(&src.replace("\r\n", "\n")) {
        for piece in fit_block(&block, limit) {
            let candidate = if pending.is_empty() {
                piece.clone()
            } else {
                format!("{}\n\n{piece}", pending.join("\n\n"))
            };
            if units(&to_html(&candidate)) <= limit {
                pending.push(piece);
            } else {
                flush(&mut pending, &mut out);
                pending.push(piece);
            }
        }
    }
    flush(&mut pending, &mut out);
    out
}

/// Telegram measures a message in UTF-16 code units, not bytes or chars.
pub fn units(s: &str) -> usize {
    s.encode_utf16().count()
}

/// Paragraphs, with a fenced code block held together as one block.
fn blocks(src: &str) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    let mut current: Vec<&str> = Vec::new();
    let mut in_fence = false;
    for line in src.split('\n') {
        let is_fence = line.trim_start().starts_with("```");
        if is_fence {
            in_fence = !in_fence;
            current.push(line);
            if !in_fence {
                out.push(current.join("\n"));
                current.clear();
            }
            continue;
        }
        if !in_fence && line.trim().is_empty() {
            if !current.is_empty() {
                out.push(current.join("\n"));
                current.clear();
            }
            continue;
        }
        current.push(line);
    }
    if !current.is_empty() {
        out.push(current.join("\n"));
    }
    out
}

/// Breaks one oversized block down until each piece converts to something that fits.
///
/// Cost is measured per line and summed, which never underestimates: the
/// constructs that span lines (blockquotes, tables) only ever get shorter when
/// converted together. A final pass verifies and halves anything that slipped
/// through, so the estimate can be cheap without being unsafe.
fn fit_block(block: &str, limit: usize) -> Vec<String> {
    if units(&to_html(block)) <= limit {
        return vec![block.to_string()];
    }
    let fence = block.trim_start().starts_with("```");
    let (prefix, suffix, body) = if fence {
        let mut lines: Vec<&str> = block.split('\n').collect();
        let open = lines.remove(0).to_string();
        if lines
            .last()
            .is_some_and(|l| l.trim_start().starts_with("```"))
        {
            lines.pop();
        }
        (open, "```".to_string(), lines.join("\n"))
    } else {
        (String::new(), String::new(), block.to_string())
    };

    let wrap = |lines: &[String]| -> String {
        if fence {
            format!("{prefix}\n{}\n{suffix}", lines.join("\n"))
        } else {
            lines.join("\n")
        }
    };
    let overhead = if fence {
        units(&to_html(&wrap(&[String::new()])))
    } else {
        0
    };
    let budget = limit.saturating_sub(overhead);

    let mut out: Vec<String> = Vec::new();
    let mut current: Vec<String> = Vec::new();
    let mut used = 0usize;
    for line in body.split('\n') {
        for piece in fit_line(line, budget) {
            let cost = units(&to_html(&piece)) + 1;
            if used + cost > budget && !current.is_empty() {
                out.push(wrap(&current));
                current.clear();
                used = 0;
            }
            current.push(piece);
            used += cost;
        }
    }
    if !current.is_empty() {
        out.push(wrap(&current));
    }
    out.into_iter()
        .flat_map(|chunk| enforce_limit(chunk, limit, fence, &prefix, &suffix))
        .collect()
}

/// Safety net for the rare chunk whose converted form still overshoots.
fn enforce_limit(
    chunk: String,
    limit: usize,
    fence: bool,
    prefix: &str,
    suffix: &str,
) -> Vec<String> {
    if units(&to_html(&chunk)) <= limit {
        return vec![chunk];
    }
    let lines: Vec<&str> = chunk
        .split('\n')
        .filter(|l| !fence || !l.trim_start().starts_with("```"))
        .collect();
    if lines.len() < 2 {
        // Nothing left to divide; hand back what we have rather than loop forever.
        return vec![chunk];
    }
    let mid = lines.len() / 2;
    let rewrap = |part: &[&str]| -> String {
        if fence {
            format!("{prefix}\n{}\n{suffix}", part.join("\n"))
        } else {
            part.join("\n")
        }
    };
    let mut out = enforce_limit(rewrap(&lines[..mid]), limit, fence, prefix, suffix);
    out.extend(enforce_limit(
        rewrap(&lines[mid..]),
        limit,
        fence,
        prefix,
        suffix,
    ));
    out
}

/// Last resort: a single line longer than a whole message, cut on character boundaries.
fn fit_line(line: &str, budget: usize) -> Vec<String> {
    let budget = budget.saturating_sub(16).max(64);
    if units(&to_html(line)) <= budget {
        return vec![line.to_string()];
    }
    let mut out = Vec::new();
    let mut buf = String::new();
    let mut cost = 0usize;
    for ch in line.chars() {
        // Escaping can turn one character into five, so price each one.
        let c = match ch {
            '&' => 5,
            '<' | '>' => 4,
            _ => {
                let mut b = [0u16; 2];
                ch.encode_utf16(&mut b).len()
            }
        };
        if cost + c > budget && !buf.is_empty() {
            out.push(std::mem::take(&mut buf));
            cost = 0;
        }
        buf.push(ch);
        cost += c;
    }
    if !buf.is_empty() {
        out.push(buf);
    }
    out
}

/// Telegram needs these three escaped everywhere; nothing else.
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

/// ```lang\n…\n``` -> `<pre><code class="language-lang">…</code></pre>`.
fn protect_fenced_code(s: &str, vault: &mut Vault) -> String {
    let mut out = String::with_capacity(s.len());
    let mut body: Option<(String, String)> = None;
    for line in s.split_inclusive('\n') {
        let trimmed = line.trim_end_matches('\n').trim_end();
        let is_fence = trimmed.trim_start().starts_with("```");
        match (&mut body, is_fence) {
            (None, true) => {
                let lang = trimmed.trim_start().trim_start_matches("```").trim();
                body = Some((sanitize_lang(lang), String::new()));
            }
            (Some((lang, b)), true) => {
                out.push_str(&vault.stash(pre(lang, b)));
                out.push('\n');
                body = None;
            }
            (Some((_, b)), false) => b.push_str(line),
            (None, false) => out.push_str(line),
        }
    }
    // Unterminated fence: emit what we have rather than losing it.
    if let Some((lang, b)) = body {
        out.push_str(&vault.stash(pre(&lang, &b)));
    }
    out
}

fn pre(lang: &str, body: &str) -> String {
    let body = escape(body.trim_end_matches('\n'));
    if lang.is_empty() {
        format!("<pre>{body}</pre>")
    } else {
        format!("<pre><code class=\"language-{lang}\">{body}</code></pre>")
    }
}

/// The language only ever lands in an attribute, so keep it to an identifier.
fn sanitize_lang(lang: &str) -> String {
    lang.chars()
        .take_while(|c| c.is_ascii_alphanumeric() || *c == '-' || *c == '+' || *c == '_')
        .collect::<String>()
        .to_ascii_lowercase()
}

fn protect_inline_code(s: &str, vault: &mut Vault) -> String {
    re!(r"`([^`\n]+)`")
        .replace_all(s, |c: &Captures| {
            vault.stash(format!("<code>{}</code>", escape(&c[1])))
        })
        .into_owned()
}

/// Links, emphasis, strikethrough and spoilers.
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
        .replace_all(&s, "${1}")
        .into_owned();

    let s = re!(r"\|\|([^|\n]+)\|\|")
        .replace_all(&s, "<span class=\"tg-spoiler\">${1}</span>")
        .into_owned();
    let s = re!(r"\*\*\*([^*\n]+)\*\*\*")
        .replace_all(&s, "<b><i>${1}</i></b>")
        .into_owned();
    let s = re!(r"\*\*([^*\n]+)\*\*")
        .replace_all(&s, "<b>${1}</b>")
        .into_owned();
    let s = re!(r"\*([^*\n]+)\*")
        .replace_all(&s, "<i>${1}</i>")
        .into_owned();
    let s = underscore_italics(&s);
    re!(r"~~([^~\n]+)~~")
        .replace_all(&s, "<s>${1}</s>")
        .into_owned()
}

/// `_italic_`, scanned by hand because the rule needs both-side word boundaries
/// and the regex crate has no look-around. Leaves `snake_case_names` alone.
fn underscore_italics(s: &str) -> String {
    let chars: Vec<char> = s.chars().collect();
    let boundary = |c: Option<&char>| c.is_none_or(|c| !c.is_alphanumeric() && *c != '_');

    let mut out = String::with_capacity(s.len());
    let mut i = 0;
    while i < chars.len() {
        if chars[i] != '_' || !boundary(i.checked_sub(1).and_then(|p| chars.get(p))) {
            out.push(chars[i]);
            i += 1;
            continue;
        }
        // Look for a closing underscore on the same line that ends a word.
        let mut j = i + 1;
        let mut close = None;
        while j < chars.len() && chars[j] != '\n' {
            if chars[j] == '_' && boundary(chars.get(j + 1)) {
                close = Some(j);
                break;
            }
            j += 1;
        }
        let body: Option<String> = close.map(|j| chars[i + 1..j].iter().collect());
        match (close, body) {
            (Some(j), Some(body))
                if !body.is_empty()
                    && !body.starts_with(' ')
                    && !body.ends_with(' ')
                    && !body.contains('_') =>
            {
                out.push_str("<i>");
                out.push_str(&body);
                out.push_str("</i>");
                i = j + 1;
            }
            _ => {
                out.push('_');
                i += 1;
            }
        }
    }
    out
}

fn link(url: &str, label: &str) -> String {
    let url = url.trim();
    let label = label.trim();
    // The url sits inside an attribute; a stray quote would break the tag.
    let href = url.replace('"', "%22");
    if label.is_empty() {
        format!("<a href=\"{href}\">{url}</a>")
    } else {
        format!("<a href=\"{href}\">{label}</a>")
    }
}

/// Line-oriented structure: headings, rules, quotes and bullets.
fn line_blocks(s: &str) -> String {
    let heading = re!(r"^\s{0,3}(#{1,6})\s+(.*?)\s*#*$");
    let heading_tags = re!(r"</?[bi]>");
    let quote = re!(r"^(\s*)((?:&gt;\s*)+)(.*)$");
    let bullet = re!(r"^(\s*)[-*+]\s+(.*)$");
    let task = re!(r"^\[([ xX])\]\s+(.*)$");
    let ordered = re!(r"^(\s*)(\d+)([.)])\s+(.*)$");

    let mut out: Vec<String> = Vec::new();
    let mut quoted: Vec<String> = Vec::new();

    let flush_quote = |quoted: &mut Vec<String>, out: &mut Vec<String>| {
        if !quoted.is_empty() {
            out.push(format!("<blockquote>{}</blockquote>", quoted.join("\n")));
            quoted.clear();
        }
    };

    for line in s.split('\n') {
        if let Some(c) = quote.captures(line) {
            quoted.push(c[3].trim_start().to_string());
            continue;
        }
        flush_quote(&mut quoted, &mut out);

        if line.starts_with(PH_OPEN) {
            out.push(line.to_string());
            continue;
        }
        if let Some(c) = heading.captures(line) {
            let text = c[2].trim();
            if text.is_empty() {
                out.push(String::new());
            } else {
                // The whole heading is bold, so drop inner emphasis rather than nest it.
                let text = heading_tags.replace_all(text, "");
                out.push(format!("<b>{}</b>", text.trim()));
            }
            continue;
        }
        if is_horizontal_rule(line) {
            out.push("────────────────────".to_string());
            continue;
        }
        if let Some(c) = bullet.captures(line) {
            let indent = c[1].len() / 2;
            let marker = match indent {
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
            out.push(format!("{}{marker} {body}", "    ".repeat(indent)));
            continue;
        }
        if let Some(c) = ordered.captures(line) {
            let indent = c[1].len() / 2;
            out.push(format!("{}{}. {}", "    ".repeat(indent), &c[2], &c[4]));
            continue;
        }
        out.push(line.to_string());
    }
    flush_quote(&mut quoted, &mut out);
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

fn collapse_blank_runs(s: &str) -> String {
    re!(r"\n{3,}")
        .replace_all(s.trim_matches('\n'), "\n\n")
        .into_owned()
}

/// Markdown tables do not render in Telegram, so align them into a code block.
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

/// Strips inline markdown so column widths line up with what Telegram will show.
fn display_width(s: &str) -> usize {
    s.chars().filter(|c| !matches!(c, '*' | '`' | '_')).count()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn emphasis() {
        assert_eq!(to_html("**bold**"), "<b>bold</b>");
        assert_eq!(to_html("*italic*"), "<i>italic</i>");
        assert_eq!(to_html("_italic_"), "<i>italic</i>");
        assert_eq!(to_html("***both***"), "<b><i>both</i></b>");
        assert_eq!(to_html("~~gone~~"), "<s>gone</s>");
        assert_eq!(
            to_html("||secret||"),
            "<span class=\"tg-spoiler\">secret</span>"
        );
    }

    #[test]
    fn underscores_inside_words_are_left_alone() {
        assert_eq!(
            to_html("call some_function_name now"),
            "call some_function_name now"
        );
        assert_eq!(to_html("_a_ and _b_"), "<i>a</i> and <i>b</i>");
        assert_eq!(to_html("a _ b _ c"), "a _ b _ c");
        assert_eq!(to_html("MAX_UNITS"), "MAX_UNITS");
    }

    #[test]
    fn headings_become_bold() {
        assert_eq!(to_html("# Title\n\nbody"), "<b>Title</b>\n\nbody");
        assert_eq!(to_html("## Hi **there**"), "<b>Hi there</b>");
    }

    #[test]
    fn links() {
        assert_eq!(
            to_html("[docs](https://x.dev)"),
            "<a href=\"https://x.dev\">docs</a>"
        );
        assert_eq!(to_html("<https://x.dev>"), "https://x.dev");
    }

    #[test]
    fn escapes_markup_characters() {
        assert_eq!(to_html("a < b & c > d"), "a &lt; b &amp; c &gt; d");
        assert_eq!(to_html("`a <b> &`"), "<code>a &lt;b&gt; &amp;</code>");
    }

    #[test]
    fn code_blocks_keep_their_language() {
        assert_eq!(
            to_html("```rust\nlet x = a < b;\n```"),
            "<pre><code class=\"language-rust\">let x = a &lt; b;</code></pre>"
        );
        assert_eq!(to_html("```\nplain\n```"), "<pre>plain</pre>");
    }

    #[test]
    fn lists_and_quotes() {
        assert_eq!(to_html("- one\n- two"), "• one\n• two");
        assert_eq!(to_html("- [x] done\n- [ ] todo"), "• ☑ done\n• ☐ todo");
        assert_eq!(to_html("  - nested"), "    ◦ nested");
        assert_eq!(to_html("1. first"), "1. first");
        assert_eq!(to_html("> a\n> b"), "<blockquote>a\nb</blockquote>");
    }

    #[test]
    fn tables_align_inside_a_code_block() {
        assert_eq!(
            to_html("| a | bb |\n|---|---:|\n| 1 | 2 |"),
            "<pre>a  bb\n─  ──\n1   2</pre>"
        );
    }

    #[test]
    fn short_message_is_not_split() {
        let msgs = to_messages("hello **world**", MAX_MESSAGE_UNITS);
        assert_eq!(msgs, vec!["hello <b>world</b>".to_string()]);
    }

    #[test]
    fn long_message_splits_into_valid_chunks() {
        let para = "Lorem ipsum dolor sit amet, consectetur adipiscing elit. ".repeat(20);
        let src = (0..20)
            .map(|i| format!("## Section {i}\n\n{para}"))
            .collect::<Vec<_>>()
            .join("\n\n");
        let msgs = to_messages(&src, MAX_MESSAGE_UNITS);
        assert!(msgs.len() > 1);
        for m in &msgs {
            assert!(
                units(m) <= MAX_MESSAGE_UNITS,
                "chunk too long: {}",
                units(m)
            );
            assert_eq!(m.matches("<b>").count(), m.matches("</b>").count());
        }
        assert!(msgs.iter().any(|m| m.contains("Section 19")));
    }

    #[test]
    fn long_code_block_splits_but_stays_fenced() {
        let src = format!("```rust\n{}\n```", "let x = 1;\n".repeat(600));
        let msgs = to_messages(&src, MAX_MESSAGE_UNITS);
        assert!(msgs.len() > 1);
        for m in &msgs {
            assert!(units(m) <= MAX_MESSAGE_UNITS);
            assert_eq!(m.matches("<pre>").count(), m.matches("</pre>").count());
            assert!(m.starts_with("<pre>"));
        }
    }

    #[test]
    fn one_absurdly_long_line_still_fits() {
        let src = "&".repeat(5000);
        let msgs = to_messages(&src, MAX_MESSAGE_UNITS);
        assert!(msgs.len() > 1);
        for m in &msgs {
            assert!(units(m) <= MAX_MESSAGE_UNITS, "{}", units(m));
        }
    }

    #[test]
    fn counts_in_utf16_units() {
        // An emoji outside the BMP is two UTF-16 units but one char.
        assert_eq!(units("🚀"), 2);
    }
}
