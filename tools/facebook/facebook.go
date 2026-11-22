package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/revans/agent-tools/apikit"
	"github.com/revans/agent-tools/postlog"
)

const (
	maxIDLen = 64
	maxPosts = 100 // Meta: "You can only read a maximum of 100 feed posts with the limit field"
	defLimit = 10

	// Meta's documented Graph API error codes, matched by number, never by text.
	codePermission    = 10
	codePermissionErr = 200
	codeInvalidToken  = 190
	codePageRateLimit = 80001
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

// ---- validation ----

// Meta documents no length limit for a Page post's message or a photo's caption,
// so none is enforced here: only that a message is not empty.
func validateMessage(flagName, message string) error {
	if strings.TrimSpace(message) == "" {
		return apikit.Usage("%s is required and cannot be empty", flagName)
	}
	return nil
}

var idRe = regexp.MustCompile(`^[0-9]+(_[0-9]+)?$`)

// validateObjectID accepts a Graph id: digits, or PAGEID_POSTID.
func validateObjectID(flagName, v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", apikit.Usage("%s is required", flagName)
	}
	if len(v) > maxIDLen || !idRe.MatchString(v) {
		return "", fmt.Errorf("%s must be a Facebook post id (digits, or PAGEID_POSTID) of at most %d characters, got %q", flagName, maxIDLen, v)
	}
	return v, nil
}

// validateURL checks an absolute http(s) URL. Facebook itself fetches it.
func validateURL(flagName, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("%s must be a full http(s) URL, got %q", flagName, raw)
	}
	return raw, nil
}

// ---- errors ----

func mapError(err error, permissionHint bool) error {
	var he *apikit.HTTPError
	if !errors.As(err, &he) {
		return err
	}
	switch code := errorCode(he.Body); {
	case he.Status == 429 || code == codePageRateLimit:
		return fmt.Errorf("facebook: rate limited (%v); wait and try again later", err)
	case code == codeInvalidToken:
		return fmt.Errorf("facebook: the access token is invalid or expired (%v). Generate a new Page access token and update access_token in the config", err)
	case (code == codePermissionErr || code == codePermission) && permissionHint:
		return fmt.Errorf("facebook: the token is missing a permission this call needs, or its user cannot perform the task on the Page (%v). Posting needs pages_manage_posts, commenting pages_manage_engagement, reading pages_read_engagement and insights read_insights", err)
	}
	return fmt.Errorf("facebook: %w", err)
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

// pageID returns the account's page_id, which commands that touch the Page's own
// edges need.
func pageID(r *apikit.Run) (string, error) {
	c, err := r.Client()
	if err != nil {
		return "", err
	}
	if a, ok := c.Auth.(*tokenAuth); ok && a.PageID != "" {
		if !idRe.MatchString(a.PageID) || strings.Contains(a.PageID, "_") {
			return "", fmt.Errorf("facebook: page_id must be numeric, got %q", a.PageID)
		}
		return a.PageID, nil
	}
	return "", fmt.Errorf("facebook: this command needs page_id in the account's config (the numeric id of the Page)")
}

// ---- post and photo ----

func postCommand() *apikit.Command {
	var message, link string
	var lo postlog.Options
	return &apikit.Command{
		Name:    "post",
		Summary: "publish a text post to the Page, optionally with a link (public; cannot be undone)",
		Usage:   "post --message TEXT [--link URL] [--dry-run]",
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&message, "message", "", "the post text (required)")
			fs.StringVar(&link, "link", "", "a URL to attach (optional)")
			lo.Flags(fs)
		},
		Run: func(r *apikit.Run) (any, error) {
			if err := validateMessage("--message", message); err != nil {
				return nil, err
			}
			params := map[string]string{"message": message}
			var media []string
			if strings.TrimSpace(link) != "" {
				l, err := validateURL("--link", link)
				if err != nil {
					return nil, err
				}
				params["link"] = l
				media = []string{l}
			}
			pid, err := pageID(r)
			if err != nil {
				return nil, err
			}
			params["page_id"] = pid
			entry := postlog.Entry{Platform: "facebook", Account: r.AccountName(), Kind: "post", Text: message, Media: media, Source: "facebook post"}
			return publish(r, lo, entry, "post_create", params)
		},
	}
}

func photoCommand() *apikit.Command {
	var imageURL, caption string
	var lo postlog.Options
	return &apikit.Command{
		Name:    "photo",
		Summary: "publish a photo from a public URL to the Page (public; cannot be undone)",
		Usage:   "photo --image-url URL [--caption TEXT] [--dry-run]",
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&imageURL, "image-url", "", "public URL of the image (required)")
			fs.StringVar(&caption, "caption", "", "caption (optional)")
			lo.Flags(fs)
		},
		Run: func(r *apikit.Run) (any, error) {
			if strings.TrimSpace(imageURL) == "" {
				return nil, apikit.Usage("--image-url is required: a public URL of an image")
			}
			img, err := validateURL("--image-url", imageURL)
			if err != nil {
				return nil, err
			}
			pid, err := pageID(r)
			if err != nil {
				return nil, err
			}
			params := map[string]string{"page_id": pid, "url": img}
			text := caption
			if strings.TrimSpace(caption) != "" {
				params["caption"] = caption
			} else {
				text = img // no caption: the image URL is what identifies a repeat
			}
			entry := postlog.Entry{Platform: "facebook", Account: r.AccountName(), Kind: "post", Text: text, Media: []string{img}, Source: "facebook photo"}
			return publish(r, lo, entry, "photo_create", params)
		},
	}
}

// publish runs the repeat check, sends one POST endpoint and records the result.
func publish(r *apikit.Run, lo postlog.Options, entry postlog.Entry, endpoint string, params map[string]string) (any, error) {
	if err := lo.Before(entry, r.DryRun(), r.Stderr); err != nil {
		return nil, err
	}
	res, err := r.Call(endpoint, params)
	if err != nil {
		return nil, mapError(err, true)
	}
	if r.DryRun() {
		return res.Data, nil
	}
	m, _ := res.Raw.(map[string]any)
	id := str(m, "id")
	out := map[string]any{"account": r.AccountName()}
	if pid := str(m, "post_id"); pid != "" { // a photo answers with the photo id and the feed post's id
		out["post_id"] = pid
		out["photo_id"] = id
		id = pid
	}
	if id == "" {
		return nil, fmt.Errorf("facebook: the answer has no id: %v", res.Raw)
	}
	out["id"] = id
	entry.RemoteID = id
	lo.After(entry, r.Stderr)
	return out, nil
}

// ---- comment ----

func commentCommand() *apikit.Command {
	var postID, message string
	var lo postlog.Options
	return &apikit.Command{
		Name:    "comment",
		Summary: "publish a comment on one of the Page's posts (public; cannot be undone)",
		Usage:   "comment --post-id ID --message TEXT [--dry-run]",
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&postID, "post-id", "", "id of the post to comment on (required)")
			fs.StringVar(&message, "message", "", "comment text (required)")
			lo.Flags(fs)
		},
		Run: func(r *apikit.Run) (any, error) {
			id, err := validateObjectID("--post-id", postID)
			if err != nil {
				return nil, err
			}
			if err := validateMessage("--message", message); err != nil {
				return nil, err
			}
			if _, err := r.Client(); err != nil { // loads the config: the account name
				return nil, err
			}
			entry := postlog.Entry{Platform: "facebook", Account: r.AccountName(), Kind: "comment", Target: id, Text: message, Source: "facebook comment"}
			return publish(r, lo, entry, "comment_create", map[string]string{"post_id": id, "message": message})
		},
	}
}

// ---- posts ----

func postsCommand() *apikit.Command {
	var limit int
	return &apikit.Command{
		Name:    "posts",
		Summary: "list the Page's published posts: id, message, created_time, permalink_url",
		Usage:   "posts [--limit N]",
		Setup: func(fs *flag.FlagSet) {
			fs.IntVar(&limit, "limit", defLimit, "how many posts (1 to 100)")
		},
		Run: func(r *apikit.Run) (any, error) {
			if limit < 1 || limit > maxPosts {
				return nil, apikit.Usage("--limit must be between 1 and %d", maxPosts)
			}
			pid, err := pageID(r)
			if err != nil {
				return nil, err
			}
			res, err := r.Call("published_posts", map[string]string{"page_id": pid, "limit": fmt.Sprint(limit)})
			if err != nil {
				return nil, mapError(err, true)
			}
			return res.Data, nil
		},
	}
}

// ---- metrics ----

// metricsCommand reads one post's numbers. The reaction, comment and share counts
// come from the post node. The insights are read separately: if Meta refuses
// them the counts are still returned, with the reason beside them.
//
// post_impressions and post_impressions_unique are deprecated for all API
// versions (Meta's list of deprecated Page Insights metrics) and answer "invalid
// metric", so the insights asked for are their documented replacements,
// post_media_view and post_total_media_view_unique.
func metricsCommand() *apikit.Command {
	return &apikit.Command{
		Name:    "metrics",
		Summary: "read a post's numbers: reactions, comments, shares, and (with permission) post_media_view and post_total_media_view_unique",
		Usage:   "metrics POST_ID",
		Run: func(r *apikit.Run) (any, error) {
			if len(r.Args) != 1 {
				return nil, apikit.Usage("metrics needs exactly one post id")
			}
			id, err := validateObjectID("POST_ID", r.Args[0])
			if err != nil {
				return nil, err
			}
			counts, err := r.Call("post_fields", map[string]string{"post_id": id})
			if err != nil {
				return nil, mapError(err, false)
			}
			if r.DryRun() {
				ins, err := r.Call("post_insights", map[string]string{"post_id": id})
				if err != nil {
					return nil, err
				}
				return map[string]any{"dry_run": true, "steps": []any{counts.Data, ins.Data}}, nil
			}
			out := map[string]any{}
			m, _ := counts.Raw.(map[string]any)
			out["reactions"] = summaryCount(m["reactions"])
			out["comments"] = summaryCount(m["comments"])
			if sh, ok := m["shares"].(map[string]any); ok && sh["count"] != nil {
				out["shares"] = sh["count"]
			} else {
				out["shares"] = 0 // Meta leaves the field out when nobody has shared the post
			}
			for _, k := range []string{"permalink_url", "created_time"} {
				if v, ok := m[k]; ok {
					out[k] = v
				}
			}
			ins, err := r.Call("post_insights", map[string]string{"post_id": id})
			if err != nil {
				out["insights_error"] = mapError(err, true).Error()
				return out, nil
			}
			im, _ := ins.Raw.(map[string]any)
			list, _ := im["data"].([]any)
			for _, it := range list {
				row, _ := it.(map[string]any)
				name, _ := row["name"].(string)
				if name == "" {
					continue
				}
				if vals, ok := row["values"].([]any); ok && len(vals) > 0 {
					if v, ok := vals[0].(map[string]any); ok && v["value"] != nil {
						out[name] = v["value"]
					}
				}
			}
			return out, nil
		},
	}
}

// summaryCount reads {"summary":{"total_count":N}} from a reactions or comments edge.
func summaryCount(edge any) any {
	e, _ := edge.(map[string]any)
	s, _ := e["summary"].(map[string]any)
	if n := s["total_count"]; n != nil {
		return n
	}
	return 0
}
