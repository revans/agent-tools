package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/revans/agent-tools/apikit"
)

// These limits are the same ones Mimir applies.
const (
	searchMaxResults = 10
	maxSnippetChars  = 1000
)

// Google's documented quota error reasons.
var quotaReasons = map[string]bool{
	"quotaExceeded": true, "dailyLimitExceeded": true,
	"rateLimitExceeded": true, "userRateLimitExceeded": true,
}

func truncateSnippet(s string) string {
	r := []rune(s)
	if len(r) <= maxSnippetChars {
		return s
	}
	return string(r[:maxSnippetChars]) + "...[truncated]"
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func sub(m map[string]any, k string) map[string]any {
	s, _ := m[k].(map[string]any)
	return s
}

// explain says "quota" plainly when Google's error reason is a quota one (a 403
// can also mean the API is not enabled, which is a different problem).
func explain(err error) error {
	var he *apikit.HTTPError
	if !errors.As(err, &he) {
		return err
	}
	var body struct {
		Error struct {
			Errors []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	quota := he.Status == 429
	if json.Unmarshal([]byte(he.Body), &body) == nil {
		for _, e := range body.Error.Errors {
			if quotaReasons[e.Reason] {
				quota = true
			}
		}
	}
	if quota {
		return fmt.Errorf("YouTube quota exceeded or rate limited (the daily quota resets at midnight Pacific time): %w", err)
	}
	return err
}

func searchCommand() *apikit.Command {
	var query string
	return &apikit.Command{
		Name:    "search",
		Summary: "search YouTube for videos (up to 10; title, channel, URL, snippet, published date)",
		Usage:   "search --query TEXT",
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&query, "query", "", "plain-text query (required)")
		},
		Run: func(r *apikit.Run) (any, error) {
			if strings.TrimSpace(query) == "" {
				return nil, apikit.Usage("--query is required and must not be empty")
			}
			res, err := r.Call("search", map[string]string{"q": query, "max_results": fmt.Sprint(searchMaxResults)})
			if err != nil {
				return nil, explain(err)
			}
			if r.DryRun() {
				return res.Data, nil
			}
			return shapeSearch(res.Raw), nil
		},
	}
}

func shapeSearch(raw any) map[string]any {
	root, _ := raw.(map[string]any)
	items, _ := root["items"].([]any)
	results := []map[string]any{}
	for _, it := range items {
		m, _ := it.(map[string]any)
		id := sub(m, "id")
		videoID := str(id, "videoId")
		if str(id, "kind") != "youtube#video" || videoID == "" {
			continue // not a video: never surfaced with a malformed URL
		}
		sn := sub(m, "snippet")
		results = append(results, map[string]any{
			"url":          "https://www.youtube.com/watch?v=" + videoID,
			"title":        str(sn, "title"),
			"channel":      str(sn, "channelTitle"),
			"snippet":      truncateSnippet(str(sn, "description")),
			"published_at": str(sn, "publishedAt"),
		})
	}
	truncated := false
	if total, ok := sub(root, "pageInfo")["totalResults"].(interface{ Int64() (int64, error) }); ok {
		n, _ := total.Int64()
		truncated = int(n) > len(results)
	} else if f, ok := sub(root, "pageInfo")["totalResults"].(float64); ok {
		truncated = int(f) > len(results)
	}
	return map[string]any{"results": results, "results_truncated": truncated}
}
