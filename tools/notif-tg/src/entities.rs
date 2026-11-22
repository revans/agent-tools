//! Telegram message entities -> Markdown, for reading messages back out.
//!
//! Telegram sends a message as plain `text` plus a list of entities whose
//! offsets are in **UTF-16 code units**, not bytes and not chars. Getting that
//! wrong silently corrupts any message containing an emoji or non-Latin text,
//! so all the arithmetic here happens in UTF-16 space.

use serde_json::Value;

/// One formatting span, already normalised out of the JSON.
#[derive(Debug, Clone)]
pub struct Entity {
    pub kind: String,
    pub offset: usize,
    pub length: usize,
    pub url: Option<String>,
    pub language: Option<String>,
    pub user_id: Option<i64>,
}

pub fn parse_entities(v: Option<&Value>) -> Vec<Entity> {
    let Some(arr) = v.and_then(Value::as_array) else {
        return Vec::new();
    };
    arr.iter()
        .filter_map(|e| {
            Some(Entity {
                kind: e.get("type")?.as_str()?.to_string(),
                offset: usize::try_from(e.get("offset")?.as_i64()?).ok()?,
                length: usize::try_from(e.get("length")?.as_i64()?).ok()?,
                url: e.get("url").and_then(Value::as_str).map(str::to_string),
                language: e
                    .get("language")
                    .and_then(Value::as_str)
                    .map(str::to_string),
                user_id: e
                    .get("user")
                    .and_then(|u| u.get("id"))
                    .and_then(Value::as_i64),
            })
        })
        .collect()
}

/// Render `text` with its entities as Markdown.
///
/// This is for reading, not round-tripping: the plain text is not escaped, so
/// what comes out is what a person (or a model) should see, not something to
/// feed straight back into `post`.
pub fn to_markdown(text: &str, entities: &[Entity]) -> String {
    let units: Vec<u16> = text.encode_utf16().collect();
    let n = units.len();

    // Opened outermost-first at each position, closed innermost-first.
    let mut opens: Vec<Vec<String>> = vec![Vec::new(); n + 1];
    let mut closes: Vec<Vec<String>> = vec![Vec::new(); n + 1];
    let mut quotes: Vec<(usize, usize)> = Vec::new();

    let mut sorted: Vec<&Entity> = entities.iter().collect();
    sorted.sort_by(|a, b| a.offset.cmp(&b.offset).then(b.length.cmp(&a.length)));

    for e in sorted {
        let start = e.offset.min(n);
        let end = (e.offset + e.length).min(n);
        if start >= end {
            continue;
        }
        let (open, close) = match e.kind.as_str() {
            "bold" => ("**".to_string(), "**".to_string()),
            "italic" => ("_".to_string(), "_".to_string()),
            "underline" => ("__".to_string(), "__".to_string()),
            "strikethrough" => ("~~".to_string(), "~~".to_string()),
            "spoiler" => ("||".to_string(), "||".to_string()),
            "code" => ("`".to_string(), "`".to_string()),
            "pre" => (
                format!("```{}\n", e.language.clone().unwrap_or_default()),
                "\n```".to_string(),
            ),
            "text_link" => (
                "[".to_string(),
                format!("]({})", e.url.clone().unwrap_or_default()),
            ),
            "text_mention" => (
                "[".to_string(),
                format!("](tg://user?id={})", e.user_id.unwrap_or_default()),
            ),
            "blockquote" | "expandable_blockquote" => {
                quotes.push((start, end));
                continue;
            }
            // mention, url, email, hashtag, bot_command, custom_emoji … the plain
            // text already reads correctly.
            _ => continue,
        };
        opens[start].push(open);
        closes[end].push(close);
    }

    let in_quote = |i: usize| quotes.iter().any(|(s, e)| i >= *s && i < *e);

    let mut out = String::with_capacity(text.len() + 16);
    if in_quote(0) {
        out.push_str("> ");
    }
    let mut i = 0;
    while i <= n {
        for c in closes[i].iter().rev() {
            out.push_str(c);
        }
        for o in &opens[i] {
            out.push_str(o);
        }
        if i == n {
            break;
        }
        let (ch, width) = decode_at(&units, i);
        out.push(ch);
        if ch == '\n' && in_quote(i + width) {
            out.push_str("> ");
        }
        i += width;
    }
    out
}

/// Decodes the character starting at `i`, returning it and its width in UTF-16 units.
fn decode_at(units: &[u16], i: usize) -> (char, usize) {
    let u = units[i];
    if (0xD800..0xDC00).contains(&u)
        && let Some(&low) = units.get(i + 1)
        && (0xDC00..0xE000).contains(&low)
    {
        let c = 0x10000 + (((u as u32) - 0xD800) << 10) + ((low as u32) - 0xDC00);
        return (char::from_u32(c).unwrap_or('\u{FFFD}'), 2);
    }
    (char::from_u32(u as u32).unwrap_or('\u{FFFD}'), 1)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn ent(kind: &str, offset: usize, length: usize) -> Entity {
        Entity {
            kind: kind.to_string(),
            offset,
            length,
            url: None,
            language: None,
            user_id: None,
        }
    }

    #[test]
    fn plain_text_is_unchanged() {
        assert_eq!(to_markdown("hello", &[]), "hello");
    }

    #[test]
    fn basic_spans() {
        assert_eq!(to_markdown("bold", &[ent("bold", 0, 4)]), "**bold**");
        assert_eq!(to_markdown("it", &[ent("italic", 0, 2)]), "_it_");
        assert_eq!(to_markdown("x", &[ent("strikethrough", 0, 1)]), "~~x~~");
        assert_eq!(to_markdown("s", &[ent("spoiler", 0, 1)]), "||s||");
        assert_eq!(to_markdown("c", &[ent("code", 0, 1)]), "`c`");
    }

    #[test]
    fn link_carries_its_url() {
        let mut e = ent("text_link", 0, 4);
        e.url = Some("https://x.dev".into());
        assert_eq!(to_markdown("docs", &[e]), "[docs](https://x.dev)");
    }

    #[test]
    fn code_block_keeps_language() {
        let mut e = ent("pre", 0, 9);
        e.language = Some("rust".into());
        assert_eq!(to_markdown("let x = 1", &[e]), "```rust\nlet x = 1\n```");
    }

    #[test]
    fn nested_spans_close_innermost_first() {
        // "bold italic" with bold over the whole thing and italic over "italic".
        let got = to_markdown("bold italic", &[ent("bold", 0, 11), ent("italic", 5, 6)]);
        assert_eq!(got, "**bold _italic_**");
    }

    #[test]
    fn offsets_are_utf16() {
        // "🚀 go" — the rocket is two UTF-16 units, so "go" starts at offset 3.
        let got = to_markdown("🚀 go", &[ent("bold", 3, 2)]);
        assert_eq!(got, "🚀 **go**");
    }

    #[test]
    fn non_latin_text_is_not_corrupted() {
        let got = to_markdown("привет мир", &[ent("bold", 7, 3)]);
        assert_eq!(got, "привет **мир**");
    }

    #[test]
    fn blockquote_prefixes_every_line() {
        let got = to_markdown("a\nb\nc", &[ent("blockquote", 0, 3)]);
        assert_eq!(got, "> a\n> b\nc");
    }

    #[test]
    fn unknown_entity_types_are_ignored() {
        assert_eq!(to_markdown("@bob", &[ent("mention", 0, 4)]), "@bob");
    }

    #[test]
    fn out_of_range_entities_do_not_panic() {
        assert_eq!(to_markdown("hi", &[ent("bold", 5, 9)]), "hi");
        assert_eq!(to_markdown("hi", &[ent("bold", 1, 9)]), "h**i**");
    }
}
