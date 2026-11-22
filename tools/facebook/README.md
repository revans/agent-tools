# facebook

Post, share a photo and comment on a **Facebook Page**, and read its posts and
their numbers, through the Meta Graph API, from the command line or through an AI
assistant (see the MCP server). Built on [apikit](../../libs/apikit/README.md):
every API call is a TOML endpoint, so `facebook call NAME` reaches anything listed
by `facebook endpoints`.

```bash
facebook post --message "We shipped 2.0 today" --link https://example.com/blog/2-0
facebook photo --image-url https://example.com/launch.jpg --caption "Launch day"
facebook comment --post-id 1234567890_9876543210 --message "Thanks for the kind words"
facebook posts --limit 5
facebook metrics 1234567890_9876543210
facebook post --message "Test" --dry-run
```

Everything these commands publish is **public and cannot be undone through this
tool**. Use `--dry-run` first: it shows each request (the token is never shown) and
sends nothing.

## Pages only, and why

This tool publishes to a **Page** you manage, as the Page. It does not post to
Groups and it does not post to a personal profile.

- Publishing to Groups through the Graph API was deprecated by Meta (from Graph
  API v19). I did not re-read that announcement while writing this tool, so treat
  the date as from memory; the point stands that there is no supported route.
- Meta's API never offered posting to a personal profile timeline for ordinary
  apps. The Page feed reference describes only Pages.
- A **user** access token cannot post as the Page. Meta's reference requires a
  **Page access token** for these calls.

## Set up

1. In Meta for Developers create an app and add a Facebook Login product. You
   must be able to perform the `CREATE_CONTENT` task on the Page (to post),
   `MODERATE` (to comment and to read photos) and `ANALYZE` (for insights).
2. Request the permissions below, get a **long-lived user access token**, then ask
   for the Page's token with `GET /{page-id}?fields=access_token` or
   `GET /me/accounts`. Meta states that a long-lived Page access token made from a
   long-lived user token has **no expiration date** and only stops working when
   something invalidates it (the user changes their password, removes the app, loses
   their role on the Page, ...). A short-lived token lasts hours. Details:
   [Get a long-lived Page access token](https://developers.facebook.com/docs/facebook-login/guides/access-tokens/get-long-lived/).
3. `facebook init` adds a `[facebook]` table to the shared file,
   `~/.config/agent-tools/config.toml` (mode 0600; see
   [docs/configuration.md](../../docs/configuration.md)). Paste the Page id and the
   token in:

   ```toml
   [facebook]
   default = "main"

   [facebook.accounts.main]
   page_id = "1234567890"
   access_token = "..."        # or access_token_env / access_token_command
   # api_version = "v27.0"     # optional
   ```

   A second Page is another `[facebook.accounts.NAME]` table; pick it with
   `--account NAME`. Config can also be given with `--config` or `$FACEBOOK_CONFIG`.
4. `facebook check` reads `GET /{page_id}?fields=id,name` and prints the Page's id
   and name.

### Permissions

As Meta's v27.0 reference lists them:

| Call | Needs |
| --- | --- |
| `post`, `photo` (`POST /{page-id}/feed`, `/photos`) | a Page token of someone who can perform `CREATE_CONTENT`; `pages_manage_posts`, `pages_read_engagement`, `pages_show_list` |
| `comment` (`POST /{post-id}/comments`) | a Page token of someone who can perform `MODERATE`; `pages_manage_engagement` |
| `posts` (`GET /{page-id}/published_posts`), `metrics` counts | `pages_read_engagement` (the feed reference); Page Public Content Access may apply for content you did not create |
| `metrics` insights (`GET /{post-id}/insights`) | `read_insights` and `pages_read_engagement`, a Page token of someone who can perform `ANALYZE` (the Page insights reference) |

An app in development mode can use these on Pages its own admins manage. For
anyone else, Meta's App Review applies.

## Images: a public URL only

`photo` sends the image as the `url` parameter, so Facebook fetches it. It must be
a public http(s) URL. Meta's photo reference lists jpeg, bmp, png, gif and tiff and a
10 MB limit (and advises keeping PNGs under 1 MB). The tool checks only that the URL
is http(s); Facebook rejects an unreachable or unsuitable image and its message is
shown. Upload a local file somewhere public first, for example with `wharf`.

## Commands

| Command | Does |
| --- | --- |
| `post --message TEXT [--link URL]` | publish a text post; prints `id` (`PAGEID_POSTID`) and `account` |
| `photo --image-url URL [--caption TEXT]` | publish a photo; prints `id` (the feed post), `post_id`, `photo_id` |
| `comment --post-id ID --message TEXT` | comment on a post as the Page; prints the comment `id` |
| `posts [--limit N]` | the Page's published posts (1 to 100, default 10): `id`, `message`, `created_time`, `permalink_url` |
| `metrics POST_ID` | a post's numbers, see below |
| `endpoints [NAME]` | list endpoints, or show one's parameters |
| `call ENDPOINT --param k=v` | call any endpoint and print its JSON |
| `check`, `accounts`, `init`, `where` | setup helpers |

Messages must not be empty. **No length limit is enforced**, because Meta's
reference documents none for a Page post, a photo caption or a comment (the facebook
product has its own limits that the reference does not state; Facebook's error is
shown if one is hit). `--link` and `--image-url` must be full http(s) URLs. A post id
is digits, or `PAGEID_POSTID`, at most 64 characters.

## Endpoints

All on `https://graph.facebook.com/v27.0` (`base_url` and `api_version` settable per
account; `base_url` is what the tests point at a fake server).

| Endpoint | Request | Safety |
| --- | --- | --- |
| `page_info` | `GET /{page_id}?fields=id,name` | read |
| `post_create` | `POST /{page_id}/feed` (form: `message`, `link`) | write |
| `photo_create` | `POST /{page_id}/photos` (form: `url`, `caption`) | write |
| `comment_create` | `POST /{post_id}/comments` (form: `message`) | write |
| `published_posts` | `GET /{page_id}/published_posts?fields=id,message,created_time,permalink_url&limit=N` (pages with `--pages`) | read |
| `post_fields` | `GET /{post_id}?fields=id,message,created_time,permalink_url,reactions.summary(true).limit(0),comments.summary(true).limit(0),shares` | read |
| `post_insights` | `GET /{post_id}/insights?metric=post_media_view,post_total_media_view_unique&period=lifetime` | read |

**Where the token goes.** As the `access_token` parameter, which is how Meta's
reference shows it: in the form-encoded body of a POST (`-d access_token=...` in its
photo examples) and in the query string of a GET. Never in a POST's URL, never in a
header, never a flag or a parameter. Add endpoints in
`~/.config/agent-tools/facebook/endpoints.d/*.toml` (format in the
[apikit README](../../libs/apikit/README.md#adding-an-endpoint-no-code-no-rebuild)).
A POST is never retried automatically, so nothing is published twice by a retry.

## Reading a post's numbers

`facebook metrics POST_ID` returns flat keys:

```json
{"reactions": 14, "comments": 4, "shares": 2,
 "post_media_view": 120, "post_total_media_view_unique": 90,
 "permalink_url": "...", "created_time": "..."}
```

The counts come from the post node (`reactions.summary(true).limit(0)`,
`comments.summary(true).limit(0)`, `shares`). `shares` is `0` when Meta leaves the
field out, which is what it does for an unshared post; that is observed behavior,
not something the reference states.

The insights are a second call. If Meta refuses them (no `read_insights`, a Page
token without the ANALYZE task, an app without access), the counts are still
returned with `insights_error` beside them.

**The insight names differ from what you might expect.** `post_impressions` and
`post_impressions_unique` no longer work: Meta's list of deprecated Page Insights
metrics shows `post_impressions*` removed on 15 November 2025 and
`post_impressions_unique*` earlier, for all API versions, and says the API answers
"invalid metric" for them. Their documented replacements are `post_media_view`
("the number of times your content was played or displayed") and
`post_total_media_view_unique`, both with period `lifetime`. Those are what is asked
for, and they are returned under their own names. The `posts` tool's metrics
vocabulary does not know them yet, so they stay in a reading's raw `metrics` until
it maps them to `views` and `reach` (a `[posts.metrics.rename]` entry does that).

## MCP

`mcp/manifests/facebook.toml` exposes `facebook_post`, `facebook_photo` and
`facebook_comment` (safety `destructive`, each with `allow_duplicate`),
`facebook_posts`, `facebook_metrics`, `facebook_read` (read-only endpoints only) and
`facebook_endpoints`. There is no generic write tool.

## Errors

The API's own message is shown with the HTTP status. These are rewritten, by Meta's
numeric error code, never by text: HTTP 429 or code 80001 (rate limited), code 190
(the token is invalid or expired: make a new Page token), and, for publishing,
`posts` and insights, codes 200 and 10 (a permission is missing or the token's user
cannot perform the task on the Page: the message names the permissions).

## The post log

Every successful post, photo and comment is recorded in the post log (see
[posts](../posts/README.md)), as platform `facebook`, kind `post` or `comment`, with
the remote id, the text as written, the media (the link or image URL) and, for a
comment, the post it answered. `facebook` refuses to publish the same text to the same
account as the same kind of thing, answering the same post, within 30 days.
`--allow-duplicate` overrides that and `--no-log` skips the record. A `--dry-run`
never records, and only warns about a repeat. If the log cannot be written, the post
still goes out and stderr says so. A photo with no caption is keyed by its image URL
so that two different captionless photos are not mistaken for a repeat. The "same
page" is the same account name, so two account names pointing at one Page are not
compared.

## Not verified

**Nothing here has run against the live Facebook API.** There were no credentials.
The tests use a local fake server that checks the requests the Graph API reference
describes: paths, parameters, the token's place, the `fields` and `metric` lists.

Confirmed in Meta's v27.0 documentation (read while writing this): the feed, photos
and comments endpoints and their parameters (`message`, `link`; `url`, `caption`;
`message`), that posting needs a Page access token and the permissions in the table,
the `{"id":...}` and `{id, post_id}` answers, the `access_token` parameter in form
POSTs and query GETs, the 100-post `limit` ceiling, that a long-lived Page token made
from a long-lived user token has no expiration date, the insights metrics
`post_media_view` and `post_total_media_view_unique` (period `lifetime`), and the
deprecation of `post_impressions` and `post_impressions_unique`.

Not confirmed: that these exact calls succeed with a real token; that Meta accepts
the token in a form body on every call; the length limits of a post, caption and
comment (none is documented); how `shares` appears on a post nobody shared; the
exact shape of the insights answer (the code reads `data[].values[0].value`, as in
Meta's examples for other insights); whether `post_media_view` is available on every
kind of post (video, reel, photo) or for every Page; whether the post node's
`reactions.summary` needs `pages_read_engagement` for every Page; the `GET
/{page_id}?fields=id,name` check call with a Page token; whether Graph API
versions after v27.0 change any of this; and App Review requirements for the
permissions on apps that are not in development mode. Not covered at all: Groups,
personal profiles, videos and reels, stories, multi-photo posts, scheduled posts,
link-preview overrides, editing or deleting posts and comments, replying to comments,
hiding comments, reading comments, and token refresh.
