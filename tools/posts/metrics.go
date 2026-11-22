package main

import (
	"math"
	"sort"
	"strconv"
	"strings"
)

// Platforms name the same measurement differently: X says impression_count,
// YouTube viewCount (as a string), Instagram like_count. Every reading is kept as
// the platform gave it; for listing and reporting, the well-known names are mapped to
// one vocabulary: views, likes, comments, shares, saves, quotes and reach.
//
// A key in a user's [posts.metrics.rename] table wins over these.
var canonicalNames = map[string]string{
	// views
	"views": "views", "view_count": "views", "viewcount": "views", "views_count": "views",
	"impression_count": "views", "impressions": "views", "impression": "views", "total_views": "views",
	// likes
	"likes": "likes", "like_count": "likes", "likecount": "likes", "reaction": "likes", "reactions": "likes", "reaction_count": "likes",
	// comments
	"comments": "comments", "comment_count": "comments", "commentcount": "comments", "comments_count": "comments",
	"reply_count": "comments", "replies": "comments", "num_comments": "comments", "comment": "comments",
	// shares
	"shares": "shares", "share_count": "shares", "shares_count": "shares", "repost_count": "shares",
	"retweet_count": "shares", "reshare": "shares", "reposts": "shares",
	// saves
	"saves": "saves", "saved": "saves", "saved_count": "saves", "save_count": "saves", "bookmark_count": "saves", "bookmarks": "saves",
	// quotes and reach
	"quotes": "quotes", "quote_count": "quotes",
	"reach": "reach", "members_reached": "reach",
}

// CanonicalOrder is the order the vocabulary is shown in.
var canonicalOrder = []string{"views", "likes", "comments", "shares", "saves", "quotes", "reach"}

func canonical(raw string, rename map[string]string) (string, bool) {
	low := strings.ToLower(raw) // rename keys are stored lower-cased, like the built-in names
	if c, ok := rename[low]; ok && c != "" {
		return c, true
	}
	c, ok := canonicalNames[low]
	return c, ok
}

// number reads a JSON number, or a numeric string such as YouTube's "1234".
func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, !math.IsNaN(n) && !math.IsInf(n, 0)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	return 0, false
}

// flatten returns the top-level numbers of a reading, and those one level down in
// a nested object (X's raw `public_metrics`). When a name appears at both levels the
// top-level one wins, whatever order the keys come in: a top-level number is always
// written, and a nested one only fills a name that is still free.
func flatten(data any) map[string]float64 {
	m, ok := data.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]float64{}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if sub, ok := m[k].(map[string]any); ok {
			for sk, sv := range sub {
				if f, ok := number(sv); ok {
					if _, taken := out[sk]; !taken {
						out[sk] = f
					}
				}
			}
			continue
		}
		if f, ok := number(m[k]); ok {
			out[k] = f
		}
	}
	return out
}

// stats maps a reading to the canonical vocabulary. When two raw names map to the
// same measurement, the name that already is the canonical one wins, then the first
// in alphabetical order, so the result never depends on map order.
func stats(data any, rename map[string]string) map[string]float64 {
	raw := flatten(data)
	if len(raw) == 0 {
		return nil
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := map[string]float64{}
	exact := map[string]bool{}
	for _, k := range keys {
		c, ok := canonical(k, rename)
		if !ok {
			continue
		}
		if strings.EqualFold(k, c) {
			out[c], exact[c] = raw[k], true
			continue
		}
		if _, have := out[c]; !have && !exact[c] {
			out[c] = raw[k]
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// whole turns 12.0 into 12, so JSON shows 12 and not 12.0.
func whole(m map[string]float64) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if v == math.Trunc(v) && math.Abs(v) < 1e15 {
			out[k] = int64(v)
		} else {
			out[k] = v
		}
	}
	return out
}
