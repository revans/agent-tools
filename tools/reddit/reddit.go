package main

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/revans/agent-tools/apikit"
)

// These limits are the same ones Mimir applies to what it hands a model.
const (
	searchMaxResults  = 10
	commentFetchLimit = 100
	maxComments       = 20
	maxTextChars      = 2000
)

var (
	subredditRe = regexp.MustCompile(`^[A-Za-z0-9_]{3,21}$`)
	postIDRe    = regexp.MustCompile(`^[a-z0-9]{1,12}$`)
)

func validateSubreddit(s string) (string, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "/"), "r/")
	if !subredditRe.MatchString(s) {
		return "", fmt.Errorf("%q is not a valid subreddit name: subreddit names are 3-21 characters, letters, digits and underscores only", s)
	}
	return s, nil
}

// parsePostID accepts a full post URL, a bare id, or a t3_ fullname.
func parsePostID(input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", errors.New("fetch needs a post URL, id or t3_ fullname")
	}
	var id string
	switch {
	case strings.HasPrefix(input, "t3_"):
		id = strings.TrimPrefix(input, "t3_")
	case strings.Contains(input, "://"):
		u, err := url.Parse(input)
		if err != nil {
			return "", fmt.Errorf("%q is not a recognizable Reddit post URL, bare id, or t3_ fullname", input)
		}
		var segs []string
		for _, s := range strings.Split(u.Path, "/") {
			if s != "" {
				segs = append(segs, s)
			}
		}
		for i, s := range segs {
			if s == "comments" && i+1 < len(segs) {
				id = segs[i+1]
				break
			}
		}
		if id == "" && len(segs) == 1 {
			id = segs[0] // a short link such as https://redd.it/abc123
		}
		if id == "" {
			return "", fmt.Errorf("%q is not a recognizable Reddit post URL: no post id in its path", input)
		}
	default:
		id = input
	}
	if !postIDRe.MatchString(id) {
		return "", fmt.Errorf("%q is not a valid Reddit post id: ids are lowercase letters and digits only", id)
	}
	return id, nil
}

func truncateText(s string) string {
	r := []rune(s)
	if len(r) <= maxTextChars {
		return s
	}
	return string(r[:maxTextChars]) + "...[truncated]"
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func num(m map[string]any, k string) int64 {
	switch n := m[k].(type) {
	case interface{ Int64() (int64, error) }:
		v, _ := n.Int64()
		return v
	case float64:
		return int64(n)
	}
	return 0
}

func boolean(m map[string]any, k string) bool { b, _ := m[k].(bool); return b }

func children(listing any) []any {
	m, _ := listing.(map[string]any)
	d, _ := m["data"].(map[string]any)
	c, _ := d["children"].([]any)
	return c
}

// ---- search ----

func searchCommand() *apikit.Command {
	var query, subreddit string
	return &apikit.Command{
		Name:    "search",
		Summary: "search Reddit by keyword, optionally inside one subreddit",
		Usage:   `search --query TEXT [--subreddit NAME]`,
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&query, "query", "", "keyword or topic to search for (required)")
			fs.StringVar(&subreddit, "subreddit", "", "narrow to one subreddit (name without r/)")
		},
		Run: func(r *apikit.Run) (any, error) {
			query = strings.TrimSpace(query)
			if query == "" {
				return nil, apikit.Usage("--query is required")
			}
			endpoint := "search"
			params := map[string]string{"q": query, "sort": "relevance", "limit": fmt.Sprint(searchMaxResults)}
			if subreddit != "" {
				sub, err := validateSubreddit(subreddit)
				if err != nil {
					return nil, err
				}
				endpoint = "subreddit_search"
				params["subreddit"] = sub
			}
			res, err := r.Call(endpoint, params)
			if err != nil {
				if subreddit != "" && isNotFound(err) {
					return nil, fmt.Errorf("no Reddit subreddit found, or it's private/quarantined/inaccessible to this app: r/%s", params["subreddit"])
				}
				return nil, err
			}
			if r.DryRun() {
				return res.Data, nil
			}
			return shapeSearch(res.Raw), nil
		},
	}
}

func shapeSearch(raw any) map[string]any {
	results := []map[string]any{}
	for _, c := range children(raw) {
		cm, _ := c.(map[string]any)
		data, _ := cm["data"].(map[string]any)
		if data == nil || boolean(data, "over_18") || str(data, "id") == "" {
			continue // NSFW is always excluded
		}
		results = append(results, map[string]any{
			"id":            str(data, "id"),
			"title":         str(data, "title"),
			"subreddit":     str(data, "subreddit"),
			"score":         num(data, "score"),
			"comment_count": num(data, "num_comments"),
			"permalink":     str(data, "permalink"),
		})
		if len(results) == searchMaxResults {
			break
		}
	}
	truncated := false
	if m, ok := raw.(map[string]any); ok {
		if d, ok := m["data"].(map[string]any); ok {
			truncated = d["after"] != nil
		}
	}
	return map[string]any{"results": results, "results_truncated": truncated}
}

func isNotFound(err error) bool {
	var he *apikit.HTTPError
	return errors.As(err, &he) && (he.Status == 404 || he.Status == 403)
}

// ---- fetch ----

func fetchCommand() *apikit.Command {
	return &apikit.Command{
		Name:    "fetch",
		Summary: "fetch one post's full text and its top comments",
		Usage:   "fetch URL|ID|t3_FULLNAME",
		Run: func(r *apikit.Run) (any, error) {
			if len(r.Args) != 1 {
				return nil, apikit.Usage("fetch needs exactly one post URL, id or t3_ fullname")
			}
			id, err := parsePostID(r.Args[0])
			if err != nil {
				return nil, err
			}
			res, err := r.Call("post_comments", map[string]string{"id": id, "sort": "top", "limit": fmt.Sprint(commentFetchLimit)})
			if err != nil {
				if isNotFound(err) {
					return nil, fmt.Errorf("no Reddit post found, or it's private/quarantined/inaccessible to this app, for id: %s", id)
				}
				return nil, err
			}
			if r.DryRun() {
				return res.Data, nil
			}
			return shapePost(res.Raw, id)
		},
	}
}

func shapePost(raw any, id string) (any, error) {
	notFound := fmt.Errorf("no Reddit post found, or it's private/quarantined/inaccessible to this app, for id: %s", id)
	listings, ok := raw.([]any)
	if !ok || len(listings) == 0 {
		return nil, notFound
	}
	first := children(listings[0])
	if len(first) == 0 {
		return nil, notFound
	}
	fm, _ := first[0].(map[string]any)
	post, _ := fm["data"].(map[string]any)
	if post == nil {
		return nil, notFound
	}
	if boolean(post, "over_18") {
		return nil, fmt.Errorf("fetch refuses to return NSFW-marked content for id: %s", id)
	}
	postID := str(post, "id")
	if postID == "" {
		postID = id
	}
	contentType, content := "link", str(post, "url")
	if boolean(post, "is_self") {
		contentType, content = "self", str(post, "selftext")
	}
	type comment struct {
		Author string `json:"author"`
		Body   string `json:"body"`
		Score  int64  `json:"score"`
	}
	comments := []comment{}
	if len(listings) > 1 {
		for _, c := range children(listings[1]) {
			cm, _ := c.(map[string]any)
			if str(cm, "kind") != "t1" {
				continue // skip "load more" stubs
			}
			d, _ := cm["data"].(map[string]any)
			if d == nil {
				continue
			}
			comments = append(comments, comment{str(d, "author"), truncateText(str(d, "body")), num(d, "score")})
		}
	}
	sort.SliceStable(comments, func(i, j int) bool { return comments[i].Score > comments[j].Score })
	if len(comments) > maxComments {
		comments = comments[:maxComments]
	}
	return map[string]any{
		"id":           postID,
		"title":        str(post, "title"),
		"subreddit":    str(post, "subreddit"),
		"score":        num(post, "score"),
		"author":       str(post, "author"),
		"content_type": contentType,
		"content":      truncateText(content),
		"comments":     comments,
	}, nil
}
