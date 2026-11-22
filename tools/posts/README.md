# posts

The ledger of what the publishing tools have published. `x`, `linkedin` and
`instagram` write to it by themselves after every successful post, reply or
comment. This tool reads it: list and search what went out, store how it did, and
report. It also lets those tools refuse to publish the same text twice.

```bash
posts list --since 7d
posts find --text "Shipping the new release today"
posts refresh                  # read fresh metrics for recent posts
posts report --since 30d --by like_count
```

Output is JSON. Nothing in `posts` publishes anything or talks to a network itself;
`refresh` runs commands that *you* configured (below).

## The ledger

A plain text file with one JSON object per line, so it can be read with `grep` and
`jq` without this tool: `~/.local/state/agent-tools/posts.jsonl` (mode 0600), or the
path in `$AGENT_TOOLS_POSTS`. Setting that to `off` switches the log and the
repeat check off everywhere. It is append-only, so a mistake is corrected by adding,
not editing.

A publication:

```json
{"type":"post","id":"p_a79945ac95","at":"2026-11-22T22:39:12Z","platform":"x","account":"main",
 "kind":"post","remote_id":"1002","url":"https://x.com/i/status/1002","text":"...",
 "text_hash":"...","target":"","media":["/path/pic.png"],"source":"x post"}
```

A metrics reading is `{"type":"metrics","post_id":"p_...","at":"...","data":{...}}`.
`kind` is `post`, `reply` or `comment`; for the last two `target` is what it
answered. The URL is filled in where one can be built from the id (X, LinkedIn);
Instagram's permalink needs an API call, so it has none.

### The repeat check

Before it sends anything, each publishing command checks the ledger and refuses to
publish **the same text** (ignoring case and spacing) to **the same account** as the
**same kind** of thing, answering the **same target**, within **30 days**. The same
words from another account, or as a reply to a different post, are not a repeat.

- `--allow-duplicate` publishes anyway. (The MCP tools offer it as `allow_duplicate`.)
- `--no-log` skips recording; the repeat check still runs. It is not exposed to MCP,
  so an assistant cannot switch the log off.
- In a `--dry-run` a repeat is a warning on stderr, not a block, and nothing is
  recorded.
- A ledger that cannot be written never hides a post that went out: stderr says it
  **was published**, with its id.
- A failed post is not recorded, so retrying it is allowed.

## Metrics: one vocabulary

Every post carries its latest reading twice: `metrics` exactly as the platform gave
it, and `stats` in one vocabulary, so X's `impression_count`, YouTube's `viewCount`
(a string) and Instagram's `like_count` can be compared and added up:

| Name | Read from, among others |
| --- | --- |
| `views` | `impression_count` (X), `viewCount` (YouTube), `views` (Instagram) |
| `likes` | `like_count`, `likeCount`, `likes` |
| `comments` | `reply_count` (X), `comments_count` (Instagram), `commentCount`, `num_comments` (Reddit) |
| `shares` | `repost_count` (X), `shares` |
| `saves` | `bookmark_count` (X), `saved` (Instagram) |
| `quotes`, `reach` | `quote_count`; `reach` |

Numbers sent as strings are read as numbers, and a number one level down (X's raw
`public_metrics`) is found. A name that already is one of the above wins over an
alias. Anything else stays in `metrics`, and `report --by` can still rank by it
(`--by score` for Reddit). To map a platform's own name, add it to your config:

```toml
[posts.metrics.rename]
clicks = "views"
```

`list`, `show` and `find` show `stats`; `report` totals, averages and ranks them.
Only each post's **latest** reading counts, and older readings stay in `show` as a
history.

## Commands

| Command | Does |
| --- | --- |
| `list [--platform P] [--account A] [--kind K] [--since 7d] [--limit N]` | what went out, newest first, with each post's latest metrics |
| `show ID` | one post and every reading taken for it |
| `find --text TEXT [--contains] [--within 30d]` | search by text; exact by default |
| `report [--since 30d] [--platform P] [--by like_count] [--top N]` | counts per platform and kind; totals and averages of each metric; the top posts |
| `add --platform P --remote-id ID --text TEXT [--kind K] [--at DATE]` | record a post made without these tools |
| `metrics ID --data '{"like_count": 12}'` | store a metrics reading (`--data -` reads stdin) |
| `refresh [ID] [--platform P] [--since 30d] [--limit N] [--dry-run]` | read fresh metrics with the configured command and store them |
| `where` | the ledger's path and size, and what `refresh` will run |

An `ID` is the ledger id (`p_...`), the platform's own id, or a unique start of
either. `--since` takes `7d`, `12h`, `2w`, a date, or an RFC 3339 time.

In `report`, a post's metrics are the numbers at the top level of its **latest**
reading; older readings do not count twice. An average is over the posts that have
that metric.

## Refreshing metrics

`refresh` runs one command per platform and stores what it prints. These are the
defaults, all read-only calls to tools in this repository:

| Platform | Default command | Gives |
| --- | --- | --- |
| `x` | `x thread {remote_id}` | views, likes, comments, shares, saves, quotes |
| `instagram` | `instagram metrics {remote_id}` | likes and comments; views, reach, saves, shares if the token may read insights |
| `facebook` | `facebook metrics {remote_id}` | reactions (likes), comments, shares; views and reach (`post_media_view`, `post_total_media_view_unique`: Meta retired `post_impressions`) if the token may read insights |
| `youtube` | `youtube call videos --param id={remote_id}` | views, likes, comments (for a video recorded with `posts add`) |
| `reddit` | `reddit call post_comments --param id={remote_id}` | score and comments (for a post recorded with `posts add`) |

LinkedIn has none: reading a post's numbers there needs the `r_member_postAnalytics`
permission, part of a restricted API product that needs LinkedIn's approval and that
the `linkedin` tool does not use. If you get that access, add a command for it below.

To change a default or add a platform, put a table in your shared config
(`~/.config/agent-tools/config.toml`):

```toml
[posts.refresh.x]
command = ["x", "thread", "{remote_id}", "--account", "{account}"]
select  = "tweet"          # which part of the JSON output to keep; optional
```

- `command` is a **list of arguments, never a shell line**; a string is refused.
  `{remote_id}`, `{account}`, `{id}`, `{platform}`, `{target}` and `{url}` are filled
  in inside a single argument, so an id can never run anything. A post with no
  account loses its `--account` argument and the flag's value.
- The command must print JSON. The `select` path uses the same syntax as the API
  tools (`data.metrics`, `items[0]`).
- `command = []` switches a platform off.
- The command comes **only from the config file**, never from a flag or an MCP
  argument, so an assistant cannot make `refresh` run anything of its own.
- A platform with no command is skipped; store numbers for it with `posts metrics`.

### Keeping them up to date

`posts refresh --stale 6h` skips a post that already has a reading newer than six
hours, so it is safe to run on a schedule: each run reads only what has gone stale.
A user timer does it (`~/.config/systemd/user/posts-refresh.service` and `.timer`):

```ini
# posts-refresh.service
[Service]
Type=oneshot
ExecStart=%h/.cargo/bin/posts refresh --stale 6h --since 14d

# posts-refresh.timer
[Timer]
OnCalendar=*-*-* 00/6:15:00
Persistent=true
[Install]
WantedBy=timers.target
```

Enable it with `systemctl --user enable --now posts-refresh.timer`. Each run calls the
platforms' APIs once per stale post, so keep `--since` short: a post's numbers settle
within days.

## MCP

`posts_list`, `posts_show`, `posts_find`, `posts_report` and `posts_where` are
read-only. `posts_add`, `posts_metrics` and `posts_refresh` write to the ledger and
never publish.

## Not verified

The ledger and every command are tested end to end, including through a real MCP
session against a fake X server. `refresh` is tested with scripts standing in for
`x thread`; it has never read real metrics. The default X command assumes the output
shape of this repository's `x thread`, whose own shape follows Mimir's but has never
run against the live X API.
