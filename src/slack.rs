//! Minimal blocking Slack Web API client (`chat.postMessage` and friends).

use anyhow::{Context, Result, anyhow, bail};
use serde::Serialize;
use serde_json::Value;
use std::cell::RefCell;
use std::collections::{HashMap, HashSet};
use std::time::Duration;

const API: &str = "https://slack.com/api";
/// Slack's per-request ceiling for history and replies.
const PAGE_MAX: usize = 1000;
/// Above this many unknown user ids, one `users.list` beats a run of `users.info`.
const BULK_LOOKUP_AT: usize = 8;
/// `users.list` pages are 200 members; stop walking a huge directory.
const MAX_USER_PAGES: usize = 10;

pub struct Slack {
    http: reqwest::blocking::Client,
    token: String,
}

/// Everything `chat.postMessage` needs for one post.
#[derive(Debug, Default, Serialize)]
pub struct Post {
    pub channel: String,
    pub text: String,
    pub mrkdwn: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub thread_ts: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub reply_broadcast: Option<bool>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub unfurl_links: Option<bool>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub unfurl_media: Option<bool>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub username: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub icon_emoji: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub icon_url: Option<String>,
}

#[derive(Debug)]
pub struct Posted {
    pub channel: String,
    pub ts: String,
    pub permalink: Option<String>,
}

#[derive(Debug)]
pub struct Identity {
    pub team: String,
    pub team_id: String,
    pub user: String,
    pub user_id: String,
    pub url: Option<String>,
    pub bot_id: Option<String>,
}

#[derive(Debug)]
pub struct Channel {
    pub id: String,
    pub name: String,
    pub is_private: bool,
    pub is_member: bool,
}

/// One message as `conversations.history` / `conversations.replies` returns it.
#[derive(Debug, Clone)]
pub struct Message {
    pub ts: String,
    /// Slack `mrkdwn`, exactly as stored.
    pub text: String,
    pub user_id: Option<String>,
    /// Apps post without a user id; this is their `username` / `bot_profile.name`.
    pub bot_name: Option<String>,
    /// `channel_join`, `bot_message`, … — absent for an ordinary message.
    pub subtype: Option<String>,
    /// The thread this message belongs to; equal to `ts` on a thread parent.
    pub thread_ts: Option<String>,
    pub reply_count: u64,
}

/// A message named on the command line: a `ts`, or a permalink that also names a channel.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct MessageRef {
    /// Only a permalink carries one.
    pub channel: Option<String>,
    pub ts: String,
    /// Set when the permalink already says which thread the message sits in.
    pub thread_ts: Option<String>,
}

impl Slack {
    pub fn new(token: String) -> Result<Slack> {
        let http = reqwest::blocking::Client::builder()
            .user_agent(concat!("notif/", env!("CARGO_PKG_VERSION")))
            .timeout(Duration::from_secs(30))
            .connect_timeout(Duration::from_secs(10))
            .https_only(true)
            .build()
            .context("building HTTP client")?;
        Ok(Slack { http, token })
    }

    fn call(&self, method: &str, body: &impl Serialize) -> Result<Value> {
        let url = format!("{API}/{method}");
        let resp = self
            .http
            .post(&url)
            .bearer_auth(&self.token)
            .json(body)
            .send()
            .with_context(|| format!("calling {method}"))?;
        let status = resp.status();
        let payload: Value = resp
            .json()
            .with_context(|| format!("{method} returned a non-JSON body (HTTP {status})"))?;
        check_ok(method, &payload)?;
        Ok(payload)
    }

    fn call_get(&self, method: &str, query: &[(&str, String)]) -> Result<Value> {
        let url = format!("{API}/{method}");
        let resp = self
            .http
            .get(&url)
            .bearer_auth(&self.token)
            .query(query)
            .send()
            .with_context(|| format!("calling {method}"))?;
        let status = resp.status();
        let payload: Value = resp
            .json()
            .with_context(|| format!("{method} returned a non-JSON body (HTTP {status})"))?;
        check_ok(method, &payload)?;
        Ok(payload)
    }

    pub fn auth_test(&self) -> Result<Identity> {
        let v = self.call("auth.test", &serde_json::json!({}))?;
        Ok(Identity {
            team: str_at(&v, "team"),
            team_id: str_at(&v, "team_id"),
            user: str_at(&v, "user"),
            user_id: str_at(&v, "user_id"),
            url: v.get("url").and_then(Value::as_str).map(str::to_string),
            bot_id: v.get("bot_id").and_then(Value::as_str).map(str::to_string),
        })
    }

    pub fn post_message(&self, post: &Post) -> Result<Posted> {
        let v = self.call("chat.postMessage", post)?;
        let channel = str_at(&v, "channel");
        let ts = str_at(&v, "ts");
        let permalink = self.permalink(&channel, &ts).ok();
        Ok(Posted {
            channel,
            ts,
            permalink,
        })
    }

    pub fn permalink(&self, channel: &str, ts: &str) -> Result<String> {
        let v = self.call_get(
            "chat.getPermalink",
            &[
                ("channel", channel.to_string()),
                ("message_ts", ts.to_string()),
            ],
        )?;
        Ok(str_at(&v, "permalink"))
    }

    /// Every channel the token can see, for filling in the config's `[channels]` table.
    pub fn conversations(&self, include_private: bool) -> Result<Vec<Channel>> {
        let types = if include_private {
            "public_channel,private_channel"
        } else {
            "public_channel"
        };
        let mut out = Vec::new();
        let mut cursor = String::new();
        loop {
            let mut q = vec![
                ("types", types.to_string()),
                ("limit", "200".to_string()),
                ("exclude_archived", "true".to_string()),
            ];
            if !cursor.is_empty() {
                q.push(("cursor", cursor.clone()));
            }
            let v = self.call_get("conversations.list", &q)?;
            for c in v["channels"].as_array().unwrap_or(&vec![]) {
                out.push(Channel {
                    id: str_at(c, "id"),
                    name: str_at(c, "name"),
                    is_private: c["is_private"].as_bool().unwrap_or(false),
                    is_member: c["is_member"].as_bool().unwrap_or(false),
                });
            }
            cursor = v["response_metadata"]["next_cursor"]
                .as_str()
                .unwrap_or("")
                .to_string();
            if cursor.is_empty() {
                break;
            }
        }
        out.sort_by(|a, b| a.name.cmp(&b.name));
        Ok(out)
    }

    /// Recent messages, oldest first — the order a person reads a channel in.
    /// `oldest`/`latest` are Slack `ts` bounds and are inclusive.
    pub fn history(
        &self,
        channel: &str,
        limit: usize,
        oldest: Option<&str>,
        latest: Option<&str>,
    ) -> Result<Vec<Message>> {
        let mut out: Vec<Message> = Vec::new();
        let mut cursor = String::new();
        while out.len() < limit {
            let want = (limit - out.len()).min(PAGE_MAX);
            let mut q = vec![
                ("channel", channel.to_string()),
                ("limit", want.to_string()),
                ("inclusive", "true".to_string()),
            ];
            if let Some(o) = oldest {
                q.push(("oldest", o.to_string()));
            }
            if let Some(l) = latest {
                q.push(("latest", l.to_string()));
            }
            if !cursor.is_empty() {
                q.push(("cursor", cursor.clone()));
            }
            let v = self.call_get("conversations.history", &q)?;
            let page = messages_of(&v);
            let empty = page.is_empty();
            out.extend(page);
            cursor = next_cursor(&v);
            if empty || cursor.is_empty() {
                break;
            }
        }
        out.truncate(limit);
        // Slack hands back newest first.
        out.reverse();
        Ok(out)
    }

    /// A whole thread: the parent first, then its replies in order.
    pub fn replies(&self, channel: &str, ts: &str, limit: usize) -> Result<Vec<Message>> {
        let mut out: Vec<Message> = Vec::new();
        let mut cursor = String::new();
        while out.len() < limit {
            let want = (limit - out.len()).min(PAGE_MAX);
            let mut q = vec![
                ("channel", channel.to_string()),
                ("ts", ts.to_string()),
                ("limit", want.to_string()),
            ];
            if !cursor.is_empty() {
                q.push(("cursor", cursor.clone()));
            }
            let v = self.call_get("conversations.replies", &q)?;
            let page = messages_of(&v);
            let empty = page.is_empty();
            out.extend(page);
            cursor = next_cursor(&v);
            if empty || cursor.is_empty() {
                break;
            }
        }
        out.truncate(limit);
        Ok(out)
    }

    /// The `ts` a reply should attach to. Replying to a reply belongs in the same
    /// thread, not in a thread of its own, so this follows `thread_ts` when there is one.
    pub fn thread_parent(&self, channel: &str, ts: &str) -> Result<String> {
        let v = self.call_get(
            "conversations.replies",
            &[
                ("channel", channel.to_string()),
                ("ts", ts.to_string()),
                ("limit", "1".to_string()),
            ],
        )?;
        let first = messages_of(&v)
            .into_iter()
            .next()
            .ok_or_else(|| anyhow!("no message with ts {ts} in {channel}"))?;
        Ok(first.thread_ts.unwrap_or(first.ts))
    }
}

/// Caches `users.info` / `conversations.info` lookups so rendering a long read does
/// not make one API call per mention.
pub struct Directory<'a> {
    slack: &'a Slack,
    users: RefCell<HashMap<String, Option<String>>>,
    channels: RefCell<HashMap<String, Option<String>>>,
}

impl<'a> Directory<'a> {
    pub fn new(slack: &'a Slack) -> Directory<'a> {
        Directory {
            slack,
            users: RefCell::new(HashMap::new()),
            channels: RefCell::new(HashMap::new()),
        }
    }

    /// Warms the cache for a batch of ids. Past a handful, one `users.list` walk is
    /// fewer round trips than a `users.info` per id.
    pub fn prefetch_users(&self, ids: &[String]) {
        let unknown: HashSet<&str> = {
            let seen = self.users.borrow();
            ids.iter()
                .map(String::as_str)
                .filter(|id| !seen.contains_key(*id))
                .collect()
        };
        if unknown.len() < BULK_LOOKUP_AT {
            return;
        }
        let mut cursor = String::new();
        for _ in 0..MAX_USER_PAGES {
            let mut q = vec![("limit", "200".to_string())];
            if !cursor.is_empty() {
                q.push(("cursor", cursor.clone()));
            }
            // Name resolution is cosmetic: a failure leaves ids raw, it does not fail the read.
            let Ok(v) = self.slack.call_get("users.list", &q) else {
                return;
            };
            let mut cache = self.users.borrow_mut();
            for m in v["members"].as_array().unwrap_or(&vec![]) {
                let id = str_at(m, "id");
                if !id.is_empty() {
                    cache.insert(id, display_name(m));
                }
            }
            drop(cache);
            cursor = next_cursor(&v);
            if cursor.is_empty() {
                return;
            }
        }
    }

    pub fn user_name(&self, id: &str) -> Option<String> {
        if let Some(hit) = self.users.borrow().get(id) {
            return hit.clone();
        }
        let found = self
            .slack
            .call_get("users.info", &[("user", id.to_string())])
            .ok()
            .and_then(|v| display_name(&v["user"]));
        // A miss is cached too: one failed lookup per id, not one per mention.
        self.users
            .borrow_mut()
            .insert(id.to_string(), found.clone());
        found
    }

    pub fn channel_name(&self, id: &str) -> Option<String> {
        if let Some(hit) = self.channels.borrow().get(id) {
            return hit.clone();
        }
        let found = self
            .slack
            .call_get("conversations.info", &[("channel", id.to_string())])
            .ok()
            .and_then(|v| v["channel"]["name"].as_str().map(str::to_string))
            .filter(|n| !n.is_empty());
        self.channels
            .borrow_mut()
            .insert(id.to_string(), found.clone());
        found
    }

    /// The name to show above a message.
    pub fn author(&self, m: &Message) -> String {
        if let Some(name) = &m.bot_name {
            return name.clone();
        }
        match &m.user_id {
            Some(id) => self.user_name(id).unwrap_or_else(|| id.clone()),
            None => "unknown".to_string(),
        }
    }
}

impl crate::mrkdwn::Names for Directory<'_> {
    fn user(&self, id: &str) -> Option<String> {
        self.user_name(id)
    }
    fn channel(&self, id: &str) -> Option<String> {
        self.channel_name(id)
    }
}

/// `profile.display_name`, else the real name, else the handle.
fn display_name(user: &Value) -> Option<String> {
    for v in [
        &user["profile"]["display_name"],
        &user["profile"]["real_name"],
        &user["real_name"],
        &user["name"],
    ] {
        if let Some(s) = v.as_str()
            && !s.is_empty()
        {
            return Some(s.to_string());
        }
    }
    None
}

fn messages_of(v: &Value) -> Vec<Message> {
    v["messages"]
        .as_array()
        .map(|a| a.iter().map(message_from).collect())
        .unwrap_or_default()
}

fn message_from(v: &Value) -> Message {
    Message {
        ts: str_at(v, "ts"),
        text: str_at(v, "text"),
        user_id: opt_str(v, "user"),
        bot_name: opt_str(v, "username")
            .or_else(|| v["bot_profile"]["name"].as_str().map(str::to_string)),
        subtype: opt_str(v, "subtype"),
        thread_ts: opt_str(v, "thread_ts"),
        reply_count: v["reply_count"].as_u64().unwrap_or(0),
    }
}

fn next_cursor(v: &Value) -> String {
    v["response_metadata"]["next_cursor"]
        .as_str()
        .unwrap_or("")
        .to_string()
}

/// Parses `--reply-to` / `notif thread`: a permalink, a `ts`, or a permalink's `p…` form.
///
/// `https://gierd.slack.com/archives/C0100000000/p1718031234567890` names both the
/// channel and the message; `p1718031234567890` is `1718031234.567890` with the dot
/// taken out.
pub fn parse_message_ref(input: &str) -> Result<MessageRef> {
    let s = input.trim();
    let bad = || {
        anyhow!(
            "could not read \"{input}\" as a message; pass a Slack permalink \
             (https://…/archives/C…/p…) or a ts like 1718031234.567890"
        )
    };
    if s.is_empty() {
        return Err(bad());
    }

    if let Some(rest) = s
        .strip_prefix("https://")
        .or_else(|| s.strip_prefix("http://"))
    {
        let (path, query) = rest.split_once('?').unwrap_or((rest, ""));
        let segs: Vec<&str> = path.split('/').filter(|p| !p.is_empty()).collect();
        let at = segs.iter().position(|p| *p == "archives").ok_or_else(bad)?;
        let ts = ts_from_compact(segs.get(at + 2).copied().ok_or_else(bad)?).ok_or_else(bad)?;
        let channel = segs
            .get(at + 1)
            .map(|c| (*c).to_string())
            .or_else(|| query_param(query, "cid"))
            .filter(|c| !c.is_empty());
        return Ok(MessageRef {
            channel,
            ts,
            thread_ts: query_param(query, "thread_ts").and_then(|t| ts_from_compact(&t)),
        });
    }

    let ts = ts_from_compact(s).ok_or_else(bad)?;
    Ok(MessageRef {
        channel: None,
        ts,
        thread_ts: None,
    })
}

/// `p1718031234567890`, `1718031234567890` and `1718031234.567890` all mean the same ts.
fn ts_from_compact(s: &str) -> Option<String> {
    let s = s.trim().trim_start_matches('p');
    if s.is_empty() {
        return None;
    }
    if let Some((secs, frac)) = s.split_once('.') {
        let ok = !secs.is_empty()
            && !frac.is_empty()
            && secs.bytes().chain(frac.bytes()).all(|b| b.is_ascii_digit());
        return ok.then(|| format!("{secs}.{frac:0<6}"));
    }
    if !s.bytes().all(|b| b.is_ascii_digit()) {
        return None;
    }
    match s.len() {
        // Epoch seconds, as a ts with no microseconds.
        n if n <= 11 => Some(format!("{s}.000000")),
        n if n > 6 => {
            let (secs, frac) = s.split_at(n - 6);
            Some(format!("{secs}.{frac}"))
        }
        _ => None,
    }
}

fn query_param(query: &str, key: &str) -> Option<String> {
    query.split('&').find_map(|pair| {
        let (k, v) = pair.split_once('=')?;
        (k == key).then(|| v.replace("%2E", ".").replace("%2e", "."))
    })
}

/// Slack's own permalink shape, built locally: reading a channel should not cost one
/// `chat.getPermalink` per message. `workspace` is the account's `gierd.slack.com`.
pub fn build_permalink(
    workspace: &str,
    channel: &str,
    ts: &str,
    thread_ts: Option<&str>,
) -> Option<String> {
    let host = workspace
        .trim()
        .trim_start_matches("https://")
        .trim_start_matches("http://")
        .trim_end_matches('/');
    if host.is_empty() || channel.is_empty() || ts.is_empty() {
        return None;
    }
    let host = if host.contains('.') {
        host.to_string()
    } else {
        format!("{host}.slack.com")
    };
    let compact = ts.replace('.', "");
    let mut url = format!("https://{host}/archives/{channel}/p{compact}");
    if let Some(parent) = thread_ts
        && parent != ts
    {
        url.push_str(&format!("?thread_ts={parent}&cid={channel}"));
    }
    Some(url)
}

fn opt_str(v: &Value, key: &str) -> Option<String> {
    v.get(key)
        .and_then(Value::as_str)
        .filter(|s| !s.is_empty())
        .map(str::to_string)
}

fn str_at(v: &Value, key: &str) -> String {
    v.get(key)
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_string()
}

fn check_ok(method: &str, payload: &Value) -> Result<()> {
    if payload["ok"].as_bool().unwrap_or(false) {
        return Ok(());
    }
    let code = payload["error"].as_str().unwrap_or("unknown_error");
    let needed = payload["needed"].as_str().unwrap_or("");
    let mut msg = format!("Slack rejected {method}: {code}");
    if let Some(hint) = hint(code) {
        msg.push_str("\n  hint: ");
        msg.push_str(hint);
    }
    if !needed.is_empty() {
        msg.push_str(&format!("\n  missing scope: {needed}"));
    }
    bail!(msg)
}

/// Turns Slack's terse error codes into something actionable.
fn hint(code: &str) -> Option<&'static str> {
    Some(match code {
        "invalid_auth" | "not_authed" | "token_revoked" | "account_inactive" => {
            "the token is wrong, revoked or disabled — check the account's token in the config"
        }
        "missing_scope" => "add the listed scope to the Slack app and reinstall it",
        "channel_not_found" => {
            "the channel id is wrong, or this token's workspace cannot see it — try `notif discover <account>`"
        }
        "not_in_channel" => {
            "invite the bot to the channel first: /invite @your-app in that channel"
        }
        "is_archived" => "the channel is archived",
        "thread_not_found" => {
            "no thread with that ts — check the ts and that it is in this channel"
        }
        "message_not_found" => {
            "no message with that ts in this channel — `notif read` prints the ts of each message"
        }
        "invalid_ts_oldest" | "invalid_ts_latest" | "invalid_ts_ending" => {
            "--since/--until want a Slack ts (1718031234.567890) or an ISO-8601 date"
        }
        "invalid_cursor" => "the pagination cursor was rejected; run the command again",
        "users_not_found" | "user_not_found" => "no such user in this workspace",
        "not_allowed_token_type" => "this API does not accept that kind of token",
        "msg_too_long" => "Slack caps a message at 40,000 characters",
        "restricted_action" | "restricted_action_read_only_channel" => {
            "workspace settings forbid this app from posting there"
        }
        "rate_limited" | "ratelimited" => "slow down and retry in a few seconds",
        "no_permission" => "the token lacks chat:write for this conversation",
        "invalid_blocks" | "invalid_arguments" => "the message payload was rejected",
        _ => return None,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn permalink_names_channel_and_message() {
        let r = parse_message_ref("https://gierd.slack.com/archives/C0100000000/p1718031234567890")
            .unwrap();
        assert_eq!(r.channel.as_deref(), Some("C0100000000"));
        assert_eq!(r.ts, "1718031234.567890");
        assert_eq!(r.thread_ts, None);
    }

    #[test]
    fn permalink_query_carries_the_thread() {
        let r = parse_message_ref(
            "https://gierd.slack.com/archives/C01/p1718031299000100?thread_ts=1718031234.567890&cid=C01",
        )
        .unwrap();
        assert_eq!(r.channel.as_deref(), Some("C01"));
        assert_eq!(r.ts, "1718031299.000100");
        assert_eq!(r.thread_ts.as_deref(), Some("1718031234.567890"));
        // Slack sometimes percent-encodes the dot.
        let r = parse_message_ref(
            "https://x.slack.com/archives/C01/p1718031299000100?thread_ts=1718031234%2E567890",
        )
        .unwrap();
        assert_eq!(r.thread_ts.as_deref(), Some("1718031234.567890"));
    }

    #[test]
    fn bare_timestamps() {
        for input in ["1718031234.567890", "p1718031234567890", "1718031234567890"] {
            let r = parse_message_ref(input).unwrap();
            assert_eq!(r.ts, "1718031234.567890", "{input}");
            assert_eq!(r.channel, None);
        }
        assert_eq!(
            parse_message_ref("1718031234").unwrap().ts,
            "1718031234.000000"
        );
        assert_eq!(
            parse_message_ref(" 1718031234.5678 ").unwrap().ts,
            "1718031234.567800"
        );
    }

    #[test]
    fn rejects_other_urls_and_junk() {
        for input in [
            "",
            "yesterday",
            "https://gierd.slack.com/messages/general",
            "https://gierd.slack.com/archives/C01",
            "https://gierd.slack.com/archives/C01/not-a-ts",
        ] {
            assert!(
                parse_message_ref(input).is_err(),
                "{input} should not parse"
            );
        }
    }

    #[test]
    fn permalinks_build_the_way_they_parse() {
        let url = build_permalink("gierd.slack.com", "C01", "1718031234.567890", None).unwrap();
        assert_eq!(
            url,
            "https://gierd.slack.com/archives/C01/p1718031234567890"
        );
        let parsed = parse_message_ref(&url).unwrap();
        assert_eq!(parsed.channel.as_deref(), Some("C01"));
        assert_eq!(parsed.ts, "1718031234.567890");

        let reply = build_permalink(
            "gierd",
            "C01",
            "1718031299.000100",
            Some("1718031234.567890"),
        )
        .unwrap();
        assert_eq!(
            reply,
            "https://gierd.slack.com/archives/C01/p1718031299000100?thread_ts=1718031234.567890&cid=C01"
        );
        assert_eq!(
            parse_message_ref(&reply).unwrap().thread_ts.as_deref(),
            Some("1718031234.567890")
        );
        assert_eq!(build_permalink("", "C01", "1718031234.567890", None), None);
    }
}
