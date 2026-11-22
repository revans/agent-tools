# x

Search X (Twitter), read timelines and threads, measure engagement, and post or
reply, from the command line or through an AI assistant (see the MCP server).
Built on [apikit](../../libs/apikit/README.md): every API call is a TOML endpoint,
so adding more takes a few lines and no code.

```bash
x search --query "go modules"
x timeline --username @golang
x thread 1234567890
x profile-metrics
x engagement-baseline --username golang
x post --text "hello" --dry-run
x reply 1234567890 --text "agreed" --image /home/me/pic.png --dry-run
x call user_by_username --param username=golang
```

## Set up

1. Sign in at <https://developer.x.com> and create a project and an app in it.
2. In the app's *User authentication settings*, turn on OAuth 1.0a and set the
   permission to **Read and write**. (Read only is enough for everything except
   `post` and `reply`.)
3. In *Keys and tokens*, copy the **API Key** and **API Key Secret**
   (the consumer keys), then generate the **Access Token** and **Access Token
   Secret**. Generate the access token *after* setting Read and write, or it will
   be read-only.
4. `x init` adds an `[x]` table to the shared file,
   `~/.config/agent-tools/config.toml` (mode 0600; see
   [docs/configuration.md](../../docs/configuration.md)). Fill it in:

   ```toml
   [x]
   default = "main"

   [x.accounts.main]
   api_key             = "..."          # or api_key_env / api_key_command
   api_secret          = "..."
   access_token        = "..."
   access_token_secret = "..."
   # baseline_refresh_interval = "weekly"   # or "monthly" (30 days)
   ```

   Each credential can be written three ways, and exactly one may be present:
   `api_key = "literal"`, `api_key_env = "NAME_OF_ENV_VAR"`, or
   `api_key_command = "pass show x/api_key"`. Several accounts are several
   `[x.accounts.NAME]` tables; pick one with `--account NAME`.
5. `x check` makes one real call (`GET /2/users/me`) and prints the account it is
   signed in as.

Optional account settings: `base_url` (default `https://api.x.com/2`, for tests or
a proxy), `state_dir` (an absolute directory for `baselines.json`; default
`$XDG_STATE_HOME/agent-tools/x`, i.e. `~/.local/state/agent-tools/x`).

X's API is paid for most uses. Which endpoints your plan can call is X's decision;
a refused call reports X's own message (for example a 403 naming the access tier).

## Commands

| Command | Does |
| --- | --- |
| `search --query TEXT` | up to 100 recent posts (about the last 7 days): `id`, `text`, `author_id`, `created_at`, `like_count`, `repost_count`, `reply_count`, `quote_count`, `bookmark_count`, `impression_count`, plus `results_truncated` |
| `timeline --username HANDLE` | the account's recent posts (up to 100, same fields) with `username`, `user_id`, `results_truncated`. `@` is optional |
| `thread TWEET_ID` | `tweet` (the post and its metrics), `replies` (up to 100, the post itself excluded) and `replies_truncated`. Replies only reach back about 7 days |
| `profile-metrics` | the signed-in account's own `followers_count`, `following_count`, `tweet_count`, `listed_count`, `like_count`, `media_count` |
| `engagement-baseline [--username HANDLE] [--force-refresh]` | per-metric averages over the account's recent posts, with `sample_size`, `computed_at`, `refreshed`. Cached; see below |
| `post --text TEXT [--image PATH]` | **publishes** a post. Prints `id`, `text`, `image_attached`, `account` |
| `reply TWEET_ID --text TEXT [--image PATH]` | **publishes** a reply (`reply.in_reply_to_tweet_id`). Same output |
| `endpoints [NAME]`, `call ENDPOINT --param k=v [--pages N]` | list or call any endpoint |
| `check`, `accounts`, `init`, `where` | setup helpers |

Every command takes `--account`, `--config`, `--dry-run`, `--debug`,
`--max-safety read|write|destructive` and `--pretty`. Output is JSON.

**Posting is public and cannot be undone.** Run `--dry-run` first: it prints the
requests (the image upload, then the post) with the signature and credentials
hidden and the image summarised by name, type and size, and sends nothing. `post`
and `reply` are never retried, so a failed call cannot publish twice; if X's reply
is lost after it accepted the post, check the account before trying again.
`--max-safety read` refuses both.

### Engagement baseline

The baseline is the mean of each count over the account's latest page of posts
(up to 100; replies and reposts by the account included, as X returns them). It is
kept in `baselines.json` in the state directory, one entry per account handle
(compared without regard to case). A cached baseline younger than the refresh
interval (7 days; 30 with `baseline_refresh_interval = "monthly"`) is returned as
is with `refreshed: false` and no request; at exactly the interval, or with
`--force-refresh`, it is recomputed. `--username` may be left out once exactly one
baseline exists. `--dry-run` shows the two requests and never writes the cache.

## Endpoints

| Endpoint | Call | Source |
| --- | --- | --- |
| `search_recent` | `GET /2/tweets/search/recent` | Mimir |
| `user_by_username` | `GET /2/users/by/username/{username}` | Mimir |
| `user_tweets` | `GET /2/users/{id}/tweets` | Mimir |
| `get_tweet` | `GET /2/tweets/{id}` | Mimir |
| `users_me` | `GET /2/users/me` | Mimir |
| `create_post` | `POST /2/tweets` (safety `write`) | Mimir |
| `user_by_id` | `GET /2/users/{id}` | **not from Mimir**, unverified |
| `users_lookup` | `GET /2/users/by?usernames=` | **not from Mimir**, unverified |
| `tweets_lookup` | `GET /2/tweets?ids=` | **not from Mimir**, unverified |

The image upload (`POST /2/media/upload`, multipart) is not an endpoint, because a
spec cannot describe a multipart body; the `post` and `reply` commands do it.

`search_recent` and `user_tweets` also follow a cursor with `--pages N`
(`next_token` / `pagination_token`). Mimir reads one page only; those cursor
parameter names are from X's documentation and are unverified.

Add your own in `~/.config/agent-tools/x/endpoints.d/*.toml` (or a directory in
`$X_ENDPOINTS`). The format is in the
[apikit README](../../libs/apikit/README.md#adding-an-endpoint-no-code-no-rebuild).
Through the MCP server, `x_read` calls any read-only endpoint by name. There is
no generic write tool: the MCP server can publish only through `x_post` and
`x_reply`, both marked destructive.

## Parity with Mimir

Mimir is the spec. Requests were written from its `x_*.rs`, whose comments say they
were verified live against X.

| Behavior | Status | Why |
| --- | --- | --- |
| search, timeline, thread, profile metrics, post, reply: same API calls, query parameters, page size (100), body shapes | same | Mimir's verified requests |
| Output field names (`results`, `results_truncated`, `repost_count`, `replies_truncated`, ...); a missing metric is 0 | same | |
| Username: `@` stripped, 1-15 letters/digits/underscores, checked before any request | same | blocks a value that would redirect a signed request |
| Tweet id: 1-20 digits, checked before any request | same | same reason |
| Unknown user and missing post messages (`no X account found for username: ...`, `no X post found for id: ...`) | same, and X's 200-with-errors answer is treated as not found too | Mimir does the same when `data` is absent |
| Image: png/jpg/jpeg/gif/webp, absolute path, regular file, at most 5 MiB, checked before sending; uploaded as `media_category=tweet_image` | same | |
| Post and reply text: must not be blank; **no length check** | same | Mimir has none; X's limit depends on the account, so X decides |
| Baseline: averages of the six metrics, strict-younger-than-interval freshness, weekly default / monthly 30 days, username optional once one exists, first call needs one | same | |
| Baseline cache | different | a JSON file in the state directory instead of Mimir's database; handles compared case-insensitively |
| Baseline refresh interval | different | a per-account config key instead of an app setting |
| HTTP errors | different wording | apikit's `HTTP 403: <X's detail>`; a 429 says "rate limited" and is never retried (reads or writes) |
| `post` and `reply` | different | `--dry-run` instead of a confirmation window. A human approves at the keyboard; an assistant may call them only through the destructive MCP tools |
| Account choice | different | `--account` and the config file instead of `as_account` and the OS keychain |
| `post`/`reply` result `account_id` | different | `account` (the config name); Mimir's value is an internal id |
| `platform` argument of the baseline tool | not included | only `x` exists; the output still says `"platform": "x"` |
| `original_tweet_text` / `original_tweet_author` | not included | they only fed Mimir's confirmation window |
| Local record of posts made (`social_posts` table) | not included | Mimir's own bookkeeping |
| Subagent exclusion, tool confirmation plumbing, keychain | not included | Mimir internals |
| `user_by_id`, `users_lookup`, `tweets_lookup`, `--pages` | added | not in Mimir; labeled unverified |

## Not verified

**Nothing here has been run against the live X API.** The tests use a fake X
server that checks the request details: the `Authorization: OAuth ...` header is
present and well-formed (consumer key, token, HMAC-SHA1, nonce, timestamp,
signature, and no secret in it), the paths, `max_results`, `tweet.fields`,
`conversation_id:` queries, the JSON body of a post, the multipart parts of an
upload, and `reply.in_reply_to_tweet_id`. The OAuth 1.0a signer itself is tested
in apikit against X's documented example, but a signature has never been checked
by X.

The requests for the Mimir-sourced commands come from Mimir's code, which says it
was verified live; that claim is Mimir's, not tested here. The three extra
endpoints and the pagination cursors are from X's public documentation alone. X's
current access tiers, rate limits and response details (for example whether
`impression_count` is returned for other people's posts) may differ from what the
fake returns.
