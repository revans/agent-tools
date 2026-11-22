package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/revans/agent-tools/apikit"
)

// These are the same limits and rules Mimir applies.
const (
	// maxImageBytes is X's own photo upload limit.
	maxImageBytes = 5 * 1024 * 1024
	// Mimir always asks X for the largest page (100) and reads one page.
	maxResults = "100"

	weekly  = 7 * 24 * time.Hour
	monthly = 30 * 24 * time.Hour
)

var (
	usernameRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,15}$`)
	tweetIDRe  = regexp.MustCompile(`^[0-9]{1,20}$`)

	// knownMetrics is the fixed vocabulary of a post's engagement counts, in the
	// order Mimir lists them.
	knownMetrics = []string{"like_count", "repost_count", "reply_count", "quote_count", "bookmark_count", "impression_count"}

	imageTypes = map[string]string{
		".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
		".gif": "image/gif", ".webp": "image/webp",
	}

	// now is the clock; tests replace it.
	now = time.Now
)

// ---- validation ----

func normalizeUsername(s string) string {
	return strings.TrimPrefix(strings.TrimSpace(s), "@")
}

func validateUsername(u string) error {
	if !usernameRe.MatchString(u) {
		return fmt.Errorf("%q is not a valid X username: X usernames are 1-15 characters, letters, digits, and underscores only", u)
	}
	return nil
}

func validateTweetID(id string) error {
	if !tweetIDRe.MatchString(id) {
		return fmt.Errorf("%q is not a valid X tweet id: X tweet ids are numeric strings only", id)
	}
	return nil
}

func refreshInterval(s string) (time.Duration, error) {
	switch s {
	case "weekly":
		return weekly, nil
	case "monthly":
		return monthly, nil
	}
	return 0, fmt.Errorf("baseline_refresh_interval must be \"weekly\" or \"monthly\", got %q", s)
}

// ---- reading a decoded response ----

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func num(m map[string]any, k string) int64 {
	switch n := m[k].(type) {
	case json.Number:
		if v, err := n.Int64(); err == nil {
			return v
		}
		f, _ := n.Float64()
		return int64(f)
	case float64:
		return int64(n)
	}
	return 0
}

func obj(v any) map[string]any { m, _ := v.(map[string]any); return m }

// extractPost reshapes one X post to the fields Mimir returns. A missing metric
// is 0, never an error.
func extractPost(raw map[string]any) map[string]any {
	pm := obj(raw["public_metrics"])
	post := map[string]any{
		"id":               nilIfEmpty(str(raw, "id")),
		"text":             nilIfEmpty(str(raw, "text")),
		"author_id":        nilIfEmpty(str(raw, "author_id")),
		"created_at":       nilIfEmpty(str(raw, "created_at")),
		"like_count":       num(pm, "like_count"),
		"repost_count":     num(pm, "retweet_count"),
		"reply_count":      num(pm, "reply_count"),
		"quote_count":      num(pm, "quote_count"),
		"bookmark_count":   num(pm, "bookmark_count"),
		"impression_count": num(pm, "impression_count"),
	}
	return post
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func extractPosts(raw any, skipID string) []map[string]any {
	out := []map[string]any{}
	list, _ := obj(raw)["data"].([]any)
	for _, p := range list {
		pm := obj(p)
		if pm == nil || (skipID != "" && str(pm, "id") == skipID) {
			continue
		}
		out = append(out, extractPost(pm))
	}
	return out
}

func hasNextToken(raw any) bool {
	return obj(obj(raw)["meta"])["next_token"] != nil
}

// ---- commands ----

func commands(st *settings) []*apikit.Command {
	return []*apikit.Command{
		searchCommand(), timelineCommand(), threadCommand(), profileCommand(),
		baselineCommand(st), postCommand(st), replyCommand(st),
	}
}

func searchCommand() *apikit.Command {
	var query string
	return &apikit.Command{
		Name:    "search",
		Summary: "search recent posts (about the last 7 days) by keyword",
		Usage:   "search --query TEXT",
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&query, "query", "", "keyword, topic or X search query (required)")
		},
		Run: func(r *apikit.Run) (any, error) {
			if strings.TrimSpace(query) == "" {
				return nil, apikit.Usage("--query is required and must not be empty")
			}
			res, err := r.Call("search_recent", map[string]string{"query": query, "max_results": maxResults})
			if err != nil {
				return nil, err
			}
			if r.DryRun() {
				return res.Data, nil
			}
			return map[string]any{"results": extractPosts(res.Raw, ""), "results_truncated": hasNextToken(res.Raw)}, nil
		},
	}
}

// isStatus reports whether err is an HTTP error with this status.
func isStatus(err error, status int) bool {
	var he *apikit.HTTPError
	return errors.As(err, &he) && he.Status == status
}

// timeline looks the account up, then reads its posts. In a dry run it shows both
// requests, with the user id left as a placeholder, and sends nothing.
func timeline(r *apikit.Run, username string) (map[string]any, error) {
	if r.DryRun() {
		a, err := r.Call("user_by_username", map[string]string{"username": username})
		if err != nil {
			return nil, err
		}
		b, err := r.Call("user_tweets", map[string]string{"id": "USER_ID", "max_results": maxResults})
		if err != nil {
			return nil, err
		}
		return map[string]any{"dry_run": true, "steps": []any{a.Data, b.Data}}, nil
	}
	missing := fmt.Errorf("no X account found for username: %s", username)
	user, err := r.Call("user_by_username", map[string]string{"username": username})
	if err != nil {
		if isStatus(err, 404) {
			return nil, missing
		}
		return nil, err
	}
	// X answers an unknown username with 200 and an errors array, no data.
	id := str(obj(obj(user.Raw)["data"]), "id")
	if id == "" {
		return nil, missing
	}
	posts, err := r.Call("user_tweets", map[string]string{"id": id, "max_results": maxResults})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"username":          username,
		"user_id":           id,
		"results":           extractPosts(posts.Raw, ""),
		"results_truncated": hasNextToken(posts.Raw),
	}, nil
}

func timelineCommand() *apikit.Command {
	var username string
	return &apikit.Command{
		Name:    "timeline",
		Summary: "an account's recent posts, by username",
		Usage:   "timeline --username HANDLE",
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&username, "username", "", "the account's handle, with or without @ (required)")
		},
		Run: func(r *apikit.Run) (any, error) {
			u := normalizeUsername(username)
			if u == "" {
				return nil, apikit.Usage("--username is required and must not be empty")
			}
			if err := validateUsername(u); err != nil {
				return nil, err
			}
			return timeline(r, u)
		},
	}
}

func threadCommand() *apikit.Command {
	return &apikit.Command{
		Name:    "thread",
		Summary: "one post's metrics and its replies (last ~7 days only)",
		Usage:   "thread TWEET_ID",
		Run: func(r *apikit.Run) (any, error) {
			if len(r.Args) != 1 {
				return nil, apikit.Usage("thread needs exactly one tweet id")
			}
			id := strings.TrimSpace(r.Args[0])
			if id == "" {
				return nil, apikit.Usage("thread needs a non-empty tweet id")
			}
			if err := validateTweetID(id); err != nil {
				return nil, err
			}
			if r.DryRun() {
				a, err := r.Call("get_tweet", map[string]string{"id": id})
				if err != nil {
					return nil, err
				}
				b, err := r.Call("search_recent", map[string]string{"query": "conversation_id:CONVERSATION_ID", "max_results": maxResults})
				if err != nil {
					return nil, err
				}
				return map[string]any{"dry_run": true, "steps": []any{a.Data, b.Data}}, nil
			}
			missing := fmt.Errorf("no X post found for id: %s", id)
			tw, err := r.Call("get_tweet", map[string]string{"id": id})
			if err != nil {
				if isStatus(err, 404) {
					return nil, missing
				}
				return nil, err
			}
			raw := obj(obj(tw.Raw)["data"])
			if raw == nil {
				return nil, missing
			}
			conv := str(raw, "conversation_id")
			if conv == "" {
				conv = id
			}
			rep, err := r.Call("search_recent", map[string]string{"query": "conversation_id:" + conv, "max_results": maxResults})
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"tweet":             extractPost(raw),
				"replies":           extractPosts(rep.Raw, id),
				"replies_truncated": hasNextToken(rep.Raw),
			}, nil
		},
	}
}

func profileCommand() *apikit.Command {
	return &apikit.Command{
		Name:    "profile-metrics",
		Summary: "the account's own follower, following, post and listed counts",
		Usage:   "profile-metrics",
		Run: func(r *apikit.Run) (any, error) {
			res, err := r.Call("users_me", nil)
			if err != nil {
				return nil, err
			}
			if r.DryRun() {
				return res.Data, nil
			}
			pm := obj(obj(obj(res.Raw)["data"])["public_metrics"])
			return map[string]any{
				"followers_count": num(pm, "followers_count"),
				"following_count": num(pm, "following_count"),
				"tweet_count":     num(pm, "tweet_count"),
				"listed_count":    num(pm, "listed_count"),
				"like_count":      num(pm, "like_count"),
				"media_count":     num(pm, "media_count"),
			}, nil
		},
	}
}

// ---- engagement baseline ----

type baseline struct {
	Account    string             `json:"account_identifier"`
	Metrics    map[string]float64 `json:"metrics"`
	SampleSize int                `json:"sample_size"`
	ComputedAt time.Time          `json:"computed_at"`
}

type baselineFile struct {
	Baselines map[string]baseline `json:"baselines"` // keyed by lower-case username
}

// baselinePath is the cache file: state_dir if the account sets one, otherwise
// $XDG_STATE_HOME/agent-tools/x (default ~/.local/state/agent-tools/x).
func baselinePath(st *settings) (string, error) {
	if st.stateDir != "" {
		return filepath.Join(st.stateDir, "baselines.json"), nil
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "agent-tools", "x", "baselines.json"), nil
}

// loadBaselines reads the cache. A missing or unreadable file is an empty cache:
// it only holds numbers that can be recomputed.
func loadBaselines(path string) baselineFile {
	f := baselineFile{Baselines: map[string]baseline{}}
	if data, err := os.ReadFile(path); err == nil {
		var in baselineFile
		if json.Unmarshal(data, &in) == nil && in.Baselines != nil {
			f = in
		}
	}
	return f
}

func saveBaselines(path string, f baselineFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".baselines-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// resolveIdentifier picks the account when --username is omitted: the one that
// already has a baseline, if exactly one does.
func resolveIdentifier(requested string, f baselineFile) (string, error) {
	if requested != "" {
		return requested, nil
	}
	var known []string
	for _, b := range f.Baselines {
		known = append(known, b.Account)
	}
	sort.Strings(known)
	switch len(known) {
	case 0:
		return "", errors.New("engagement-baseline requires --username: no baseline exists yet")
	case 1:
		return known[0], nil
	}
	return "", fmt.Errorf("engagement-baseline requires --username: multiple accounts already have a baseline: %s", strings.Join(known, ", "))
}

// isFresh is strictly "younger than the interval": exactly at the boundary the
// baseline is stale.
func isFresh(computed, at time.Time, interval time.Duration) bool {
	return at.Sub(computed) < interval
}

// computeAverages is the mean of each known metric over the posts. No posts
// gives no metrics at all, as Mimir does.
func computeAverages(posts []map[string]any) map[string]float64 {
	out := map[string]float64{}
	if len(posts) == 0 {
		return out
	}
	for _, k := range knownMetrics {
		var sum int64
		for _, p := range posts {
			sum += p[k].(int64)
		}
		out[k] = float64(sum) / float64(len(posts))
	}
	return out
}

func baselineOutput(b baseline, refreshed bool) map[string]any {
	return map[string]any{
		"platform":           "x",
		"account_identifier": b.Account,
		"metrics":            b.Metrics,
		"sample_size":        b.SampleSize,
		"computed_at":        b.ComputedAt.UTC().Format(time.RFC3339Nano),
		"refreshed":          refreshed,
	}
}

func baselineCommand(st *settings) *apikit.Command {
	var username string
	var force bool
	return &apikit.Command{
		Name:    "engagement-baseline",
		Summary: "average engagement per recent post, cached until the refresh interval passes",
		Usage:   "engagement-baseline [--username HANDLE] [--force-refresh]",
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&username, "username", "", "the account's handle (optional once one baseline exists)")
			fs.BoolVar(&force, "force-refresh", false, "recompute now even if the cached baseline is fresh")
		},
		Run: func(r *apikit.Run) (any, error) {
			requested := ""
			if strings.TrimSpace(username) != "" {
				requested = normalizeUsername(username)
				if err := validateUsername(requested); err != nil {
					return nil, err
				}
			} else if username != "" {
				return nil, apikit.Usage("--username must not be empty")
			}
			// The client builds the settings (refresh interval, state dir).
			if _, err := r.Client(); err != nil {
				return nil, err
			}
			path, err := baselinePath(st)
			if err != nil {
				return nil, err
			}
			cache := loadBaselines(path)
			id, err := resolveIdentifier(requested, cache)
			if err != nil {
				return nil, err
			}
			key := strings.ToLower(id)
			if r.DryRun() {
				return timeline(r, id)
			}
			if existing, ok := cache.Baselines[key]; ok && !force && isFresh(existing.ComputedAt, now(), st.refresh) {
				return baselineOutput(existing, false), nil
			}
			tl, err := timeline(r, id)
			if err != nil {
				return nil, err
			}
			posts, _ := tl["results"].([]map[string]any)
			b := baseline{Account: id, Metrics: computeAverages(posts), SampleSize: len(posts), ComputedAt: now().UTC()}
			cache.Baselines[key] = b
			if err := saveBaselines(path, cache); err != nil {
				return nil, fmt.Errorf("computed the baseline but could not save it to %s: %w", path, err)
			}
			return baselineOutput(b, true), nil
		},
	}
}

// ---- post and reply ----

type image struct {
	data     []byte
	filename string
	mime     string
}

// loadImage checks the file before anything is sent: an absolute path, a known
// extension, a regular file, at most 5 MiB.
func loadImage(cmd, path string) (*image, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%s requires an absolute --image path, got: %s", cmd, path)
	}
	mime, ok := imageTypes[strings.ToLower(filepath.Ext(path))]
	if !ok {
		return nil, fmt.Errorf("%s is not a recognized image file: %s supports png, jpg, jpeg, gif, and webp", path, cmd)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("image file not found: %s", path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > maxImageBytes {
		return nil, fmt.Errorf("%s is %d bytes, over the %d-byte image size ceiling", path, info.Size(), maxImageBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return &image{data: data, filename: filepath.Base(path), mime: mime}, nil
}

// publish uploads the image (if any) and creates the post. replyTo is empty for
// a plain post. With --dry-run it prints both requests and sends neither; the
// image bytes are summarised, never printed.
func publish(r *apikit.Run, st *settings, cmd, text, replyTo, imagePath string) (any, error) {
	if strings.TrimSpace(text) == "" {
		return nil, apikit.Usage("--text is required and must not be empty")
	}
	var img *image
	if imagePath != "" {
		var err error
		if img, err = loadImage(cmd, imagePath); err != nil {
			return nil, err
		}
	}
	c, err := r.Client()
	if err != nil {
		return nil, err
	}
	params := map[string]string{"text": text}
	if replyTo != "" {
		b, _ := json.Marshal(map[string]string{"in_reply_to_tweet_id": replyTo})
		params["reply"] = string(b)
	}
	uploadURL := strings.TrimRight(c.BaseURL, "/") + "/media/upload"

	if r.DryRun() {
		var steps []any
		if img != nil {
			steps = append(steps, map[string]any{
				"dry_run": true, "method": "POST", "url": uploadURL,
				"headers": map[string]string{"Authorization": "[redacted]", "Content-Type": "multipart/form-data"},
				"fields":  map[string]string{"media_category": "tweet_image"},
				"file":    map[string]any{"field": "media", "filename": img.filename, "content_type": img.mime, "bytes": len(img.data)},
			})
			params["media"] = `{"media_ids":["MEDIA_ID_FROM_UPLOAD"]}`
		}
		res, err := r.Call("create_post", params)
		if err != nil {
			return nil, err
		}
		return map[string]any{"dry_run": true, "steps": append(steps, res.Data)}, nil
	}

	if img != nil {
		body, ct, err := apikit.Multipart(map[string]string{"media_category": "tweet_image"},
			[]apikit.FilePart{{Field: "media", Filename: img.filename, ContentType: img.mime, Data: img.data}})
		if err != nil {
			return nil, err
		}
		up, err := c.Send(r.Ctx, &apikit.Request{Method: "POST", URL: uploadURL, Headers: map[string]string{"Content-Type": ct}, Body: body},
			apikit.SendOptions{Name: "media_upload", Safety: apikit.SafetyWrite, Timeout: 60 * time.Second})
		if err != nil {
			return nil, fmt.Errorf("image upload failed, nothing was posted: %w", err)
		}
		mediaID := str(obj(obj(up.Raw)["data"]), "id")
		if mediaID == "" {
			return nil, errors.New("X's media upload response did not include a media id; nothing was posted")
		}
		b, _ := json.Marshal(map[string]any{"media_ids": []string{mediaID}})
		params["media"] = string(b)
	}
	res, err := r.Call("create_post", params)
	if err != nil {
		return nil, err
	}
	data := obj(obj(res.Raw)["data"])
	if data == nil {
		return nil, errors.New("X's post-creation response did not include the created post")
	}
	posted := str(data, "text")
	if posted == "" {
		posted = text
	}
	return map[string]any{
		"id":             str(data, "id"),
		"text":           posted,
		"image_attached": img != nil,
		"account":        st.account,
	}, nil
}

func postCommand(st *settings) *apikit.Command {
	var text, img string
	return &apikit.Command{
		Name:    "post",
		Summary: "PUBLISH a new post, optionally with one image (cannot be undone)",
		Usage:   "post --text TEXT [--image /absolute/path.png] [--dry-run]",
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&text, "text", "", "the exact post text (required)")
			fs.StringVar(&img, "image", "", "absolute path to a png, jpg, jpeg, gif or webp (at most 5 MiB)")
		},
		Run: func(r *apikit.Run) (any, error) { return publish(r, st, "post", text, "", img) },
	}
}

func replyCommand(st *settings) *apikit.Command {
	var text, img string
	return &apikit.Command{
		Name:    "reply",
		Summary: "PUBLISH a reply to a post, optionally with one image (cannot be undone)",
		Usage:   "reply TWEET_ID --text TEXT [--image /absolute/path.png] [--dry-run]",
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&text, "text", "", "the exact reply text (required)")
			fs.StringVar(&img, "image", "", "absolute path to a png, jpg, jpeg, gif or webp (at most 5 MiB)")
		},
		Run: func(r *apikit.Run) (any, error) {
			if len(r.Args) != 1 {
				return nil, apikit.Usage("reply needs exactly one tweet id")
			}
			id := strings.TrimSpace(r.Args[0])
			if id == "" {
				return nil, apikit.Usage("reply needs a non-empty tweet id")
			}
			if err := validateTweetID(id); err != nil {
				return nil, err
			}
			return publish(r, st, "reply", text, id, img)
		},
	}
}
