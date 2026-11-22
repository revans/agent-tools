//! Minimal blocking Telegram Bot API client.

use crate::entities;
use anyhow::{Context, Result};
use serde::Serialize;
use serde_json::{Value, json};
use std::time::Duration;

const API: &str = "https://api.telegram.org";

pub struct Telegram {
    http: reqwest::blocking::Client,
    token: String,
}

/// Everything `sendMessage` needs for one post.
#[derive(Debug, Default)]
pub struct Send {
    pub chat_id: String,
    pub html: String,
    /// Forum topic, for supergroups with topics enabled.
    pub topic_id: Option<i64>,
    /// Message to reply to, which is what a Telegram "thread" actually is.
    pub reply_to: Option<i64>,
    pub silent: bool,
    pub no_preview: bool,
}

#[derive(Debug)]
pub struct Sent {
    pub message_id: i64,
    pub chat_id: i64,
    pub link: Option<String>,
}

#[derive(Debug)]
pub struct Identity {
    pub id: i64,
    pub username: String,
    pub name: String,
    /// False when privacy mode is on, which is why a bot may see no group messages.
    pub reads_all_group_messages: bool,
}

/// One incoming message, flattened out of an update.
#[derive(Debug, Clone)]
pub struct Msg {
    pub update_id: i64,
    /// `message`, `channel_post`, `edited_message`, …
    pub kind: String,
    pub message_id: i64,
    pub date: i64,
    pub chat_id: i64,
    pub chat_type: String,
    pub chat_title: String,
    pub from_id: Option<i64>,
    pub from: String,
    pub from_username: Option<String>,
    /// Raw text exactly as Telegram sent it.
    pub text: String,
    /// The same text with its entities rendered as Markdown.
    pub markdown: String,
    /// Set when this message replies to another — reply to the same id to continue the thread.
    pub reply_to: Option<i64>,
    pub topic_id: Option<i64>,
    /// Description of an attachment when the message is not (only) text.
    pub attachment: Option<String>,
}

impl Telegram {
    pub fn new(token: String) -> Result<Telegram> {
        let http = reqwest::blocking::Client::builder()
            .user_agent(concat!("notif-tg/", env!("CARGO_PKG_VERSION")))
            .timeout(Duration::from_secs(30))
            .connect_timeout(Duration::from_secs(10))
            .https_only(true)
            .build()
            .context("building HTTP client")?;
        Ok(Telegram { http, token })
    }

    /// The bot token sits in the request path, so it must never reach an error message.
    fn scrub(&self, s: String) -> String {
        s.replace(&self.token, "<token>")
    }

    fn call_with_timeout(
        &self,
        method: &str,
        body: &impl Serialize,
        timeout: Duration,
    ) -> Result<Value> {
        let url = format!("{API}/bot{}/{method}", self.token);
        let resp = self
            .http
            .post(&url)
            .timeout(timeout)
            .json(body)
            .send()
            .map_err(|e| anyhow::anyhow!("{}", self.scrub(e.without_url().to_string())))
            .with_context(|| format!("calling {method}"))?;
        let status = resp.status();
        let payload: Value = resp
            .json()
            .map_err(|e| anyhow::anyhow!("{}", self.scrub(e.without_url().to_string())))
            .with_context(|| format!("{method} returned a non-JSON body (HTTP {status})"))?;
        check_ok(method, &payload)?;
        payload
            .get("result")
            .cloned()
            .with_context(|| format!("{method} returned no result"))
    }

    fn call(&self, method: &str, body: &impl Serialize) -> Result<Value> {
        self.call_with_timeout(method, body, Duration::from_secs(30))
    }

    pub fn get_me(&self) -> Result<Identity> {
        let v = self.call("getMe", &json!({}))?;
        Ok(Identity {
            id: v["id"].as_i64().unwrap_or_default(),
            username: v["username"].as_str().unwrap_or_default().to_string(),
            name: v["first_name"].as_str().unwrap_or_default().to_string(),
            reads_all_group_messages: v["can_read_all_group_messages"].as_bool().unwrap_or(false),
        })
    }

    pub fn send_message(&self, send: &Send) -> Result<Sent> {
        let mut body = json!({
            "chat_id": chat_id_value(&send.chat_id),
            "text": send.html,
            "parse_mode": "HTML",
        });
        let map = body.as_object_mut().expect("object literal");
        if let Some(t) = send.topic_id {
            map.insert("message_thread_id".into(), json!(t));
        }
        if let Some(r) = send.reply_to {
            map.insert(
                "reply_parameters".into(),
                json!({"message_id": r, "allow_sending_without_reply": true}),
            );
        }
        if send.silent {
            map.insert("disable_notification".into(), json!(true));
        }
        if send.no_preview {
            map.insert("link_preview_options".into(), json!({"is_disabled": true}));
        }

        let v = self.call("sendMessage", &body)?;
        let message_id = v["message_id"].as_i64().unwrap_or_default();
        let chat_id = v["chat"]["id"].as_i64().unwrap_or_default();
        Ok(Sent {
            link: message_link(&v["chat"], message_id),
            message_id,
            chat_id,
        })
    }

    /// `getUpdates`. `timeout_secs > 0` long-polls, which is how `watch` waits
    /// without hammering the API.
    pub fn get_updates(
        &self,
        offset: Option<i64>,
        limit: u32,
        timeout_secs: u32,
    ) -> Result<Vec<Msg>> {
        let mut body = json!({
            "limit": limit.clamp(1, 100),
            "timeout": timeout_secs,
            "allowed_updates": ["message", "edited_message", "channel_post", "edited_channel_post"],
        });
        if let Some(o) = offset {
            body.as_object_mut()
                .expect("object literal")
                .insert("offset".into(), json!(o));
        }
        // Outlast the long poll itself, or the HTTP client cancels it first.
        let http_timeout = Duration::from_secs(u64::from(timeout_secs) + 20);
        let v = self.call_with_timeout("getUpdates", &body, http_timeout)?;
        let arr = v.as_array().cloned().unwrap_or_default();
        Ok(arr.iter().filter_map(parse_update).collect())
    }

    pub fn get_chat(&self, chat_id: &str) -> Result<Value> {
        self.call("getChat", &json!({"chat_id": chat_id_value(chat_id)}))
    }
}

/// Numeric ids must go over the wire as numbers; `@username` stays a string.
fn chat_id_value(id: &str) -> Value {
    match id.parse::<i64>() {
        Ok(n) => json!(n),
        Err(_) => json!(id),
    }
}

/// Public supergroups and channels have `t.me` links; private chats do not.
fn message_link(chat: &Value, message_id: i64) -> Option<String> {
    if let Some(username) = chat.get("username").and_then(Value::as_str) {
        return Some(format!("https://t.me/{username}/{message_id}"));
    }
    let id = chat.get("id")?.as_i64()?;
    // -100 prefixes the internal id of supergroups and channels in t.me/c links.
    let internal = id.to_string().strip_prefix("-100")?.to_string();
    Some(format!("https://t.me/c/{internal}/{message_id}"))
}

fn parse_update(u: &Value) -> Option<Msg> {
    let update_id = u.get("update_id")?.as_i64()?;
    let kind = [
        "message",
        "channel_post",
        "edited_message",
        "edited_channel_post",
    ]
    .into_iter()
    .find(|k| u.get(*k).is_some())?;
    let m = u.get(kind)?;

    let chat = m.get("chat")?;
    let text = m
        .get("text")
        .or_else(|| m.get("caption"))
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_string();
    let ents = entities::parse_entities(m.get("entities").or_else(|| m.get("caption_entities")));

    Some(Msg {
        update_id,
        kind: kind.to_string(),
        message_id: m.get("message_id")?.as_i64()?,
        date: m.get("date").and_then(Value::as_i64).unwrap_or_default(),
        chat_id: chat.get("id")?.as_i64()?,
        chat_type: chat
            .get("type")
            .and_then(Value::as_str)
            .unwrap_or("")
            .to_string(),
        chat_title: display_chat(chat),
        from_id: m
            .get("from")
            .and_then(|f| f.get("id"))
            .and_then(Value::as_i64),
        from: m.get("from").map(display_user).unwrap_or_else(|| {
            // Channel posts have no sender; the channel itself is the author.
            display_chat(chat)
        }),
        from_username: m
            .get("from")
            .and_then(|f| f.get("username"))
            .and_then(Value::as_str)
            .map(str::to_string),
        markdown: entities::to_markdown(&text, &ents),
        text,
        reply_to: m
            .get("reply_to_message")
            .and_then(|r| r.get("message_id"))
            .and_then(Value::as_i64),
        topic_id: m.get("message_thread_id").and_then(Value::as_i64),
        attachment: describe_attachment(m),
    })
}

fn display_chat(chat: &Value) -> String {
    for key in ["title", "username", "first_name"] {
        if let Some(s) = chat.get(key).and_then(Value::as_str)
            && !s.is_empty()
        {
            return s.to_string();
        }
    }
    chat.get("id")
        .and_then(Value::as_i64)
        .map(|i| i.to_string())
        .unwrap_or_default()
}

fn display_user(from: &Value) -> String {
    let first = from.get("first_name").and_then(Value::as_str).unwrap_or("");
    let last = from.get("last_name").and_then(Value::as_str).unwrap_or("");
    let name = format!("{first} {last}").trim().to_string();
    if !name.is_empty() {
        return name;
    }
    from.get("username")
        .and_then(Value::as_str)
        .map(|u| format!("@{u}"))
        .unwrap_or_else(|| "unknown".to_string())
}

/// A short note so a photo or document does not read as an empty message.
fn describe_attachment(m: &Value) -> Option<String> {
    for kind in [
        "photo",
        "video",
        "animation",
        "voice",
        "video_note",
        "audio",
        "sticker",
        "location",
        "contact",
        "poll",
        "venue",
    ] {
        if m.get(kind).is_some() {
            return Some(kind.to_string());
        }
    }
    if let Some(doc) = m.get("document") {
        let name = doc
            .get("file_name")
            .and_then(Value::as_str)
            .unwrap_or("file");
        return Some(format!("document: {name}"));
    }
    None
}

/// A refusal from Telegram itself, as opposed to a transport failure.
#[derive(Debug)]
pub struct ApiError {
    pub method: String,
    pub code: i64,
    pub description: String,
    pub retry_after: Option<i64>,
}

impl ApiError {
    /// True when retrying cannot help: a bad token, or another reader holding
    /// the update stream. Polling loops stop instead of spinning.
    pub fn is_fatal(&self) -> bool {
        matches!(self.code, 401 | 403 | 404 | 409)
    }
}

impl std::fmt::Display for ApiError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "Telegram rejected {}: {}", self.method, self.description)?;
        if let Some(h) = hint(self.code, &self.description) {
            write!(f, "\n  hint: {h}")?;
        }
        if let Some(retry) = self.retry_after {
            write!(f, "\n  retry after: {retry}s")?;
        }
        Ok(())
    }
}

impl std::error::Error for ApiError {}

/// Picks the `ApiError` out of an error chain, if the failure came from Telegram.
pub fn api_error(e: &anyhow::Error) -> Option<&ApiError> {
    e.chain().find_map(|c| c.downcast_ref::<ApiError>())
}

fn check_ok(method: &str, payload: &Value) -> Result<()> {
    if payload["ok"].as_bool().unwrap_or(false) {
        return Ok(());
    }
    Err(anyhow::Error::new(ApiError {
        method: method.to_string(),
        code: payload["error_code"].as_i64().unwrap_or(0),
        description: payload["description"]
            .as_str()
            .unwrap_or("unknown error")
            .to_string(),
        retry_after: payload["parameters"]["retry_after"].as_i64(),
    }))
}

/// Turns Telegram's prose errors into something actionable.
fn hint(code: i64, desc: &str) -> Option<&'static str> {
    let d = desc.to_ascii_lowercase();
    Some(match () {
        _ if code == 401 => {
            "the bot token is wrong or revoked — check the account's token in the config"
        }
        _ if code == 409 => {
            "another getUpdates reader or a webhook is active for this bot; only one may consume updates (deleteWebhook, or stop the other reader)"
        }
        _ if d.contains("chat not found") => {
            "the chat id is wrong, or the bot has never been added to that chat — try `notif-tg discover`"
        }
        _ if d.contains("bot was blocked") => "the user blocked this bot",
        _ if d.contains("bot is not a member") || d.contains("not enough rights") => {
            "add the bot to the chat and, for channels, make it an administrator"
        }
        _ if d.contains("message thread not found") => {
            "the topic_id does not exist in that forum supergroup"
        }
        _ if d.contains("message to reply not found") => {
            "the message being replied to was deleted, or is in a different chat"
        }
        _ if d.contains("can't parse entities") => {
            "the rendered HTML was rejected — rerun with --dry-run to inspect it, or use --raw"
        }
        _ if d.contains("message is too long") => {
            "a single message caps at 4096 characters; notif-tg normally splits, so this means --raw was used"
        }
        _ if code == 429 => "rate limited; wait the retry period before sending again",
        _ => return None,
    })
}
