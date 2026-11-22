# What `notif-tg` does to your Markdown

Telegram renders a small fixed set of HTML tags and nothing else — no headings,
no tables, no nesting beyond those tags. `notif-tg` converts ordinary Markdown
into that subset before sending.

| You write | Telegram gets | Looks like |
| --- | --- | --- |
| `# Heading` … `###### H` | `<b>Heading</b>` | bold line, all levels identical |
| `**bold**` | `<b>` | **bold** |
| `*italic*` / `_italic_` | `<i>` | _italic_ |
| `***both***` | `<b><i>` | bold italic |
| `~~struck~~` | `<s>` | struck through |
| `\|\|spoiler\|\|` | `<span class="tg-spoiler">` | tap to reveal |
| `[text](https://x)` | `<a href="https://x">text</a>` | a link |
| `<https://x>` | plain text | Telegram auto-links it |
| `` `code` `` | `<code>` | monospace |
| ```` ```rust … ``` ```` | `<pre><code class="language-rust">` | syntax-highlighted block |
| `- item` | `• item` | bullet |
| nested `  - item` | `◦`, then `▪` deeper | indented bullet |
| `- [x] done` / `- [ ] todo` | `☑` / `☐` | checkbox |
| `1. item` | unchanged | numbered |
| `> quote` | `<blockquote>` | quoted block, consecutive lines merged |
| `---` alone on a line | box-drawing rule | a divider |
| a `\| … \|` table | aligned columns in `<pre>` | monospaced table |
| `&`, `<`, `>` | escaped | literal characters |

Notes:

- **Headings are flattened to bold**, and inner emphasis inside a heading is
  dropped rather than nested — Telegram will not render a bold inside a bold.
- **`snake_case` is safe.** Underscores only italicise when they sit on word
  boundaries, so `some_function_name` survives intact.
- **Messages over 4096 characters are split automatically**, at paragraph
  boundaries first, then lines, then characters. A long fenced code block is
  split into several fenced blocks. Each piece is sent as its own message about
  0.4s apart, and only the first carries `--reply-to`. The limit counts UTF-16
  units, so emoji cost two.
- **Mentioning someone** needs their username (`@alice`, which Telegram links
  itself) or a `[name](tg://user?id=123456789)` link. There is no way to
  mention by display name.
- **`--raw` skips all of this** and sends plain text with no formatting applied.
  Use it when the content must appear exactly as written.

## Reading back

`notif-tg read` turns Telegram's text plus its entity list back into Markdown:
`<b>` becomes `**bold**`, links become `[text](url)`, `pre` becomes a fenced
block, blockquotes get `> ` prefixes, spoilers become `||…||`. Entity offsets
are UTF-16, which the tool handles, so emoji and non-Latin text come back
intact.

That output is for reading, not for round-tripping: the plain text is not
re-escaped, so do not pipe `read` straight into `post` and expect identical
formatting. `--raw` on `read` gives Telegram's untouched text instead.

Check any of this without sending:

```bash
notif-tg render -t deploy -v service=api -v version=v1
echo '## Hi **there**' | notif-tg post -c me --dry-run
```
