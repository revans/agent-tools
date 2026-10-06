//! Minimal blocking Slack Web API client (`chat.postMessage` and friends).

use anyhow::{Context, Result, bail};
use serde::Serialize;
use serde_json::Value;
use std::time::Duration;

const API: &str = "https://slack.com/api";

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
