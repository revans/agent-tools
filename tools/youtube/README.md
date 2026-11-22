# youtube

Search YouTube videos from the command line, or through an AI assistant (see the
MCP server), using the YouTube Data API v3. Built on
[apikit](../../libs/apikit/README.md): every API call is a TOML endpoint, so
adding more takes a few lines and no code. Read-only: nothing here can post,
rate, comment or delete.

```bash
youtube search --query "rust lifetimes tutorial"
youtube call videos --param id=VIDEO_ID
```

## Set up

1. In the [Google Cloud console](https://console.cloud.google.com/) create (or pick) a project.
2. Enable **YouTube Data API v3** for it (APIs & Services, Library).
3. Create an API key (APIs & Services, Credentials, Create credentials, API key). Restricting it to the YouTube Data API is a good idea.
4. `youtube init` adds a `[youtube]` table to the shared file, `~/.config/agent-tools/config.toml` (mode 0600; see [docs/configuration.md](../../docs/configuration.md)). Fill it in:

   ```toml
   [youtube]
   default = "main"

   [youtube.accounts.main]
   api_key = "..."                 # or api_key_env = "YOUTUBE_API_KEY"
                                   # or api_key_command = "pass show youtube/key"
   # base_url = "..."              # only for tests
   ```
5. `youtube check` confirms the key is accepted.

The key is sent in the `X-Goog-Api-Key` header, never in the URL, so it does not
end up in logs or proxies. `--dry-run` shows the request with the key redacted.

## Commands

| Command | Does |
| --- | --- |
| `search --query TEXT` | up to 10 videos: `url`, `title`, `channel`, `snippet` (cut at 1000 characters), `published_at`, plus `results_truncated` |
| `endpoints [NAME]` | list endpoints, or show one's parameters |
| `call ENDPOINT --param k=v [--pages N]` | call any endpoint and print its JSON |
| `check`, `accounts`, `init`, `where` | setup helpers |

`check` calls `videos.list` with a made-up id and `part=id`: documented to cost 1
quota unit (a search costs 100), and a valid key gets an empty list back.

## Endpoints

| Endpoint | Path | Source |
| --- | --- | --- |
| `search` | `GET /search` (`type=video`) | Mimir |
| `videos` | `GET /videos` (snippet, statistics, contentDetails by id) | **not from Mimir, from YouTube's public docs, unverified** |
| `channels` | `GET /channels` (by id or handle) | **not from Mimir, from YouTube's public docs, unverified** |

`youtube call search` returns raw items reshaped to `video_id`, `title`,
`channel`, `description`, `published_at` and follows `nextPageToken` with
`--pages`. The `search` command is the tidy form. Add your own endpoints in
`~/.config/agent-tools/youtube/endpoints.d/*.toml`.

## Same as Mimir

| Behavior | Here |
| --- | --- |
| `GET /search` with `part=snippet`, `type=video`, `q`, `maxResults=10` | same |
| Key in `X-Goog-Api-Key`, never `?key=` | same |
| Query percent-encoded, not interpolated | same |
| Output `results[]` of `url`, `title`, `channel`, `snippet`, `published_at`, plus `results_truncated` | same field names |
| URL built as `https://www.youtube.com/watch?v=ID` | same |
| Items whose kind is not `youtube#video` or with no video id are skipped | same |
| Snippet cut at 1000 characters (not bytes) plus `...[truncated]` | same |
| `results_truncated` true only when `pageInfo.totalResults` exceeds the results returned | same |
| Empty or missing `items` gives an empty list, not an error | same |
| Quota errors (reasons `quotaExceeded`, `dailyLimitExceeded`, `rateLimitExceeded`, `userRateLimitExceeded`) or HTTP 429 | message says quota/rate limited; the API's own message is kept. Mimir returns a typed RateLimited error |
| A 403 `accessNotConfigured` is not a quota error | same |
| Other errors: `HTTP <status>: <API message>` | different wording, same content (apikit's format) |
| Empty query rejected before any request | same rule; exit code 2 (usage error) instead of a Validation error |
| 30 second timeout | same (apikit default) |
| Missing credential fails before any request | same, with apikit's own message |
| Key stored in the OS keychain | different: a config file entry (key, `_env` or `_command`), per this repo's convention |
| Retries | different: apikit retries GET twice on 429/5xx; Mimir sets no retry |
| `videos`, `channels` endpoints | not in Mimir (extras) |
| Video transcripts, channel or playlist search, view counts in `search` | not included: Mimir excludes them too (view counts need `videos`) |

## Not verified

Everything is tested against a **fake YouTube server** that checks the request
details (header key, no `key=` in the URL, `type=video`, `part`, `maxResults`,
encoding) and the shaping. **Nothing has been run against the live YouTube API**;
no credentials were available. `search` follows Mimir's client, whose author says
its request shape was live-verified. The quota error body shape comes from Google's
documentation, which Mimir's own notes say was not live-verified. The `videos` and
`channels` endpoints, the choice of `videos.list` for `check`, and the quota costs
quoted here (100 for search, 1 for the others) are from YouTube's public docs only.

The API has a daily quota per project (10,000 units by default per Google's docs, so
about 100 searches a day). When it runs out, calls fail with a quota error until it
resets. `--pages` and repeated searches spend it quickly.
