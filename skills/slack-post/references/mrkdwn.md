# What `notif` does to your Markdown

Slack does not render Markdown. `notif` rewrites the common subset into Slack's
`mrkdwn` before posting, so write ordinary Markdown and expect this:

| You write | Slack shows |
| --- | --- |
| `# Heading` … `###### Heading` | bold line (`*Heading*`), any nesting flattened |
| `**bold**` | `*bold*` |
| `*italic*` or `_italic_` | `_italic_` |
| `***both***` | `*_both_*` |
| `~~struck~~` | `~struck~` |
| `[text](https://x)` | `<https://x|text>` |
| `![alt](https://x.png)` | `<https://x.png|alt>` (Slack unfurls the image) |
| `<https://x>` | left alone |
| `- item` | `• item` |
| nested `  - item` | `◦ item`, then `▪` deeper |
| `- [x] done` / `- [ ] todo` | `☑ done` / `☐ todo` |
| `1. item` | left alone |
| `> quote` | left alone |
| `---` on its own line | a horizontal line of box-drawing characters |
| `` `code` `` and ```` ```blocks``` ```` | left alone, contents never rewritten |
| a `\| … \|` table | aligned columns inside a code block |
| `@here`, `@channel`, `@everyone` | real broadcast mentions |
| `<@U123>`, `<#C456\|dev>` | left alone — hand-written Slack entities survive |
| `&`, `<`, `>` in prose | escaped to `&amp;`, `&lt;`, `&gt;` as Slack requires |

Notes:

- **Headings are flattened to bold.** Slack has no heading sizes, so `#` and
  `####` look identical. Use at most one or two per message.
- **Tables become monospaced.** They read fine for a few short columns and badly
  for many wide ones. Prefer bullets when the table is more than ~4 columns.
- **A hard line break is just a newline.** Trailing double-space does nothing.
- **To mention a person you need their user id**, not their display name:
  `<@U01ABCDEF>`. `@alice` posts as literal text. The id comes from Slack, so
  ask the user for it rather than guessing.
- **`--raw` skips all of this** and sends the text byte-for-byte. Use it only
  when you have already written Slack `mrkdwn` or a pre-escaped payload.
- **Slack caps a message at 40,000 characters** and `notif` refuses longer ones.
  Split the message or link out instead.

## Reading: `mrkdwn` back to Markdown

`notif read` and `notif thread` run the mapping the other way before printing a
message, so what you see is Markdown again:

| Slack stores | `notif` shows |
| --- | --- |
| `*bold*` | `**bold**` |
| `_italic_` | `_italic_` |
| `~struck~` | `~~struck~~` |
| `<https://x\|text>` | `[text](https://x)` |
| `<https://x>` | `https://x` |
| `<mailto:a@b\|a@b>` | `a@b` |
| `• item`, `◦ item` | `- item`, nested `  - item` |
| `☑` / `☐` | `- [x]` / `- [ ]` |
| the drawn horizontal line | `---` |
| `&amp;`, `&lt;`, `&gt;` | `&`, `<`, `>` |
| `<@U01ABCDEF>` | `@alice` (via `users.info`, cached per run) |
| `<#C0123\|dev>` | `#dev` |
| `<!here>`, `<!channel>` | `@here`, `@channel` |
| `` `code` `` and ```` ```blocks``` ```` | left alone, contents never rewritten |

Notes:

- **Headings do not come back.** The forward direction flattens them to bold, so
  a heading reads as `**Heading**`.
- **An unresolvable id stays raw.** If `users:read` is missing or the lookup
  fails, you get `<@U01ABCDEF>` rather than an invented name.
- **`--raw` on `read`/`thread` skips all of this** and prints exactly what the
  API returned, which is what you want when debugging a message's markup.

Check any of this without posting:

```bash
notif render -t deploy -v service=api -v version=v1
echo '## Hi **there**' | notif post -a gierd -c general --dry-run
notif read -c general -n 3 --raw      # the other direction, before conversion
```
