package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/revans/agent-tools/apikit"
)

// The same limits Mimir applies.
const (
	maxCaptionChars = 2200
	maxMessageChars = 2200
	maxIDLen        = 64

	// Mimir polls a container's status at most this many times, this far apart,
	// and only after publishing it straight away failed.
	maxStatusPolls = 5
	pollInterval   = 3 * time.Second

	// Meta's documented Graph API error codes, matched by number, never by text.
	codePermissionDenied = 10
	codeInvalidToken     = 190
)

func str(m map[string]any, k string) string {
	switch v := m[k].(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	}
	return ""
}

// firstEntry returns data[0] of a {"data":[{...}]} answer.
func firstEntry(raw any) map[string]any {
	m, _ := raw.(map[string]any)
	l, _ := m["data"].([]any)
	if len(l) == 0 {
		return nil
	}
	e, _ := l[0].(map[string]any)
	return e
}

// ---- validation ----

func validateCaption(caption string) error {
	if strings.TrimSpace(caption) == "" {
		return apikit.Usage("--caption is required and cannot be empty")
	}
	if n := len([]rune(caption)); n > maxCaptionChars {
		return fmt.Errorf("the caption is %d characters, over Instagram's own %d-character maximum", n, maxCaptionChars)
	}
	return nil
}

func validateMessage(what, message string) error {
	if strings.TrimSpace(message) == "" {
		return apikit.Usage("--message is required and cannot be empty")
	}
	if n := len([]rune(message)); n > maxMessageChars {
		return fmt.Errorf("the %s is %d characters, over Instagram's own %d-character maximum", what, n, maxMessageChars)
	}
	return nil
}

func validateObjectID(flagName, v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", apikit.Usage("%s is required", flagName)
	}
	ok := len(v) <= maxIDLen
	for _, c := range v {
		if c < '0' || c > '9' {
			ok = false
		}
	}
	if !ok {
		return "", fmt.Errorf("%s must be a numeric Instagram id of at most %d characters, got %q", flagName, maxIDLen, v)
	}
	return v, nil
}

var notJPEG = map[string]bool{".png": true, ".gif": true, ".webp": true, ".heic": true, ".heif": true, ".bmp": true, ".tif": true, ".tiff": true, ".svg": true}

// validateImageURL checks what can be checked without fetching: an absolute
// http(s) URL that does not name another image type. Instagram itself fetches the
// URL and enforces JPEG and public access.
func validateImageURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", apikit.Usage("--image-url is required: a public URL of a JPEG image")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("--image-url must be a full http(s) URL of a public JPEG image, got %q. Instagram cannot read local files; upload the image somewhere public first (see the README)", raw)
	}
	if ext := strings.ToLower(path.Ext(u.Path)); notJPEG[ext] {
		return "", fmt.Errorf("%s is not a JPEG image: Instagram posting here only accepts JPEG images", raw)
	}
	return raw, nil
}

// ---- errors ----

// mapError turns the API's error into a plain message. permissionHint is set for
// comment and reply calls, as Mimir does.
func mapError(err error, permissionHint bool) error {
	var he *apikit.HTTPError
	if !errors.As(err, &he) {
		return err
	}
	switch code := errorCode(he.Body); {
	case he.Status == 429:
		msg := "instagram: rate limited (HTTP 429); wait and try again later"
		if he.RetryAfter != "" {
			msg += " (retry after " + he.RetryAfter + "s)"
		}
		return errors.New(msg)
	case code == codeInvalidToken:
		return fmt.Errorf("instagram: the access token is expired or invalid (%v). Generate a new long-lived token and update access_token in the config; there is no refresh flow yet", err)
	case code == codePermissionDenied && permissionHint:
		return fmt.Errorf("instagram: the token is missing a permission this call needs (%v). Generate a token that includes instagram_business_manage_comments (comments and replies) and instagram_business_content_publish (posting)", err)
	}
	return fmt.Errorf("instagram: %w", err)
}

func errorCode(body string) int {
	var v struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(body), &v) != nil {
		return 0
	}
	return v.Error.Code
}

// ---- user id ----

func userID(r *apikit.Run) (string, error) {
	c, err := r.Client()
	if err != nil {
		return "", err
	}
	if a, ok := c.Auth.(*tokenAuth); ok && a.UserID != "" {
		return a.UserID, nil
	}
	if r.DryRun() {
		return "USER_ID", nil // not looked up: a dry run sends nothing
	}
	res, err := r.Call("me", nil)
	if err != nil {
		return "", mapError(err, false)
	}
	me := firstEntry(res.Raw)
	if me == nil || str(me, "user_id") == "" {
		return "", fmt.Errorf("instagram: could not find the account's user id in GET /me (%v); set user_id in the account's config", res.Raw)
	}
	return str(me, "user_id"), nil
}

// ---- post ----

func postCommand() *apikit.Command {
	var caption, imageURL string
	return &apikit.Command{
		Name:    "post",
		Summary: "publish a photo with a caption (public; cannot be undone)",
		Usage:   "post --caption TEXT --image-url URL [--dry-run]",
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&caption, "caption", "", "caption, up to 2200 characters (required)")
			fs.StringVar(&imageURL, "image-url", "", "public URL of a JPEG image (required)")
		},
		Run: func(r *apikit.Run) (any, error) {
			if err := validateCaption(caption); err != nil {
				return nil, err
			}
			img, err := validateImageURL(imageURL)
			if err != nil {
				return nil, err
			}
			uid, err := userID(r)
			if err != nil {
				return nil, err
			}
			createParams := map[string]string{"user_id": uid, "image_url": img, "caption": caption}
			if r.DryRun() {
				create, err := r.Call("container_create", createParams)
				if err != nil {
					return nil, err
				}
				pub, err := r.Call("media_publish", map[string]string{"user_id": uid, "creation_id": "<container id from step 1>"})
				if err != nil {
					return nil, err
				}
				return map[string]any{"dry_run": true, "steps": []any{create.Data, pub.Data}}, nil
			}
			created, err := r.Call("container_create", createParams)
			if err != nil {
				return nil, mapError(err, false)
			}
			cm, _ := created.Raw.(map[string]any)
			containerID := str(cm, "id")
			if containerID == "" {
				return nil, fmt.Errorf("instagram: the container answer has no id: %v", created.Raw)
			}
			mediaID, err := publish(r, uid, containerID)
			if err != nil {
				return nil, err
			}
			return map[string]any{"id": mediaID, "container_id": containerID, "account": r.AccountName()}, nil
		},
	}
}

// publish publishes the container straight away, as Mimir does. Only when that
// fails does it poll the container's status and publish again once it is FINISHED.
func publish(r *apikit.Run, uid, containerID string) (string, error) {
	id, pubErr := tryPublish(r, uid, containerID)
	if pubErr == nil {
		return id, nil
	}
	var he *apikit.HTTPError
	if errors.As(pubErr, &he) && (he.Status == 429 || errorCode(he.Body) == codeInvalidToken) {
		return "", mapError(pubErr, false) // waiting will not help
	}
	for i := 0; i < maxStatusPolls; i++ {
		sleep(pollInterval)
		res, err := r.Call("container_status", map[string]string{"container_id": containerID})
		if err != nil {
			continue // a failed poll is not fatal; try again
		}
		m, _ := res.Raw.(map[string]any)
		switch s := str(m, "status_code"); s {
		case "FINISHED":
			id, err := tryPublish(r, uid, containerID)
			if err != nil {
				return "", mapError(err, false)
			}
			return id, nil
		case "IN_PROGRESS":
			continue
		case "PUBLISHED":
			return "", fmt.Errorf("instagram: the container was already published, but no media id was ever returned to this tool (%v)", pubErr)
		default:
			return "", fmt.Errorf("instagram: the media container did not become publishable (status: %s)", s)
		}
	}
	return "", mapError(pubErr, false)
}

func tryPublish(r *apikit.Run, uid, containerID string) (string, error) {
	res, err := r.Call("media_publish", map[string]string{"user_id": uid, "creation_id": containerID})
	if err != nil {
		return "", err
	}
	m, _ := res.Raw.(map[string]any)
	id := str(m, "id")
	if id == "" {
		return "", fmt.Errorf("the publish answer has no media id: %v", res.Raw)
	}
	return id, nil
}

// ---- comment and reply ----

func commentCommand() *apikit.Command {
	var mediaID, message string
	return &apikit.Command{
		Name:    "comment",
		Summary: "post a public comment on one of your media (cannot be undone)",
		Usage:   "comment --media-id ID --message TEXT [--dry-run]",
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&mediaID, "media-id", "", "numeric id of the media (required)")
			fs.StringVar(&message, "message", "", "comment text, up to 2200 characters (required)")
		},
		Run: func(r *apikit.Run) (any, error) {
			id, err := validateObjectID("--media-id", mediaID)
			if err != nil {
				return nil, err
			}
			if err := validateMessage("comment", message); err != nil {
				return nil, err
			}
			return sendText(r, "comment_create", "media_id", id, message)
		},
	}
}

func replyCommand() *apikit.Command {
	var commentID, message string
	return &apikit.Command{
		Name:    "reply",
		Summary: "post a public reply to a comment (cannot be undone)",
		Usage:   "reply --comment-id ID --message TEXT [--dry-run]",
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&commentID, "comment-id", "", "numeric id of the comment (required)")
			fs.StringVar(&message, "message", "", "reply text, up to 2200 characters (required)")
		},
		Run: func(r *apikit.Run) (any, error) {
			id, err := validateObjectID("--comment-id", commentID)
			if err != nil {
				return nil, err
			}
			if err := validateMessage("reply", message); err != nil {
				return nil, err
			}
			return sendText(r, "reply_create", "comment_id", id, message)
		},
	}
}

func sendText(r *apikit.Run, endpoint, idParam, id, message string) (any, error) {
	res, err := r.Call(endpoint, map[string]string{idParam: id, "message": message})
	if err != nil {
		return nil, mapError(err, true)
	}
	if r.DryRun() {
		return res.Data, nil
	}
	m, _ := res.Raw.(map[string]any)
	out := str(m, "id")
	if out == "" {
		return nil, fmt.Errorf("instagram: the answer has no id: %v", res.Raw)
	}
	return map[string]any{"id": out, "account": r.AccountName()}, nil
}
