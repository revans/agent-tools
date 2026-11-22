# reddit

Search Reddit and read posts with their top comments, from the command line or
through an AI assistant (see the MCP server). Built on
[apikit](../../libs/apikit/README.md): every API call is a TOML endpoint, so
adding more takes a few lines and no code.

```bash
reddit search --query "go modules" --subreddit golang
reddit fetch https://www.reddit.com/r/golang/comments/1abc2d/some_title/
reddit call subreddit_about --param subreddit=golang
```

## Set up

1. Create an app at <https://www.reddit.com/prefs/apps> (type *script* or *web*)
   and copy its client id and secret.
2. `reddit init` adds a `[reddit]` table to the shared file, `~/.config/agent-tools/config.toml` (mode 0600; see [docs/configuration.md](../../docs/configuration.md)). Fill it in:

   ```toml
   [reddit]
   default = "main"

   [reddit.accounts.main]
   client_id     = "..."          # or client_id_env / client_id_command
   client_secret = "..."
   # user_agent  = "agent-tools-reddit/0.1 (by /u/yourname)"
   ```
3. `reddit check` confirms Reddit issues a token.

It uses Reddit's app-only OAuth (`client_credentials`): public content only, no
posting, no private subreddits.

## Commands

| Command | Does |
| --- | --- |
| `search --query TEXT [--subreddit NAME]` | up to 10 posts: `id`, `title`, `subreddit`, `score`, `comment_count`, `permalink`, plus `results_truncated`. NSFW is always left out |
| `fetch URL\|ID\|t3_FULLNAME` | one post: `id`, `title`, `subreddit`, `score`, `author`, `content_type` (`self` or `link`), `content`, and its best 20 top-level comments, each cut at 2000 characters. NSFW is refused |
| `endpoints [NAME]` | list endpoints, or show one's parameters |
| `call ENDPOINT --param k=v [--pages N]` | call any endpoint and print its JSON |
| `check`, `accounts`, `init`, `where` | setup helpers |

`--dry-run` prints the request without sending it or fetching a token. Output is
JSON. See `reddit help` for the flags every command takes.

## Endpoints

| Endpoint | Path |
| --- | --- |
| `search` | `GET /search` |
| `subreddit_search` | `GET /r/{subreddit}/search` |
| `post_comments` | `GET /comments/{id}` |
| `subreddit_listing` | `GET /r/{subreddit}/{hot,new,top,rising}` |
| `subreddit_about` | `GET /r/{subreddit}/about` |

Add your own in `~/.config/agent-tools/reddit/endpoints.d/*.toml` (or a directory in
`$REDDIT_ENDPOINTS`). The format, with an example, is in the
[apikit README](../../libs/apikit/README.md#adding-an-endpoint-no-code-no-rebuild).
Through the MCP server an added endpoint is callable at once with `reddit_read`.

## Same as Mimir

This reproduces Mimir's `reddit` tool, split into `search` and `fetch` (Mimir has
one tool with a `mode`; two names are clearer to a model).

| Behavior | Here |
| --- | --- |
| Search: 10 results, relevance order, optional subreddit (`restrict_sr`) | same |
| NSFW excluded from search, refused by fetch | same |
| Fetch accepts a URL, a bare id, or a `t3_` fullname (and `redd.it` short links) | same |
| Fetch: top comments by score, top-level only, 20 at most, 2000 characters each | same |
| Subreddit name: 3 to 21 letters, digits or underscores | same (a leading `r/` is accepted and removed) |
| A 404 or 403 on a subreddit or post | the same "private/quarantined/inaccessible" message |
| Descriptive User-Agent, `raw_json=1` | same |

Mimir asks you to confirm each action in its own interface. Reddit access here is
read-only, so there is nothing to confirm.

## Not verified

Everything here is tested against a **fake Reddit server** that checks the
request details (token request, headers, parameters, paths). Nothing has been run
against the live Reddit API with real credentials. `search`, `subreddit_search`
and `post_comments` were written from Mimir's client, which says it was verified
live. `subreddit_listing` and `subreddit_about` were written from Reddit's public
API documentation and have no such source.
