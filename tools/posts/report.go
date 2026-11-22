package main

import (
	"flag"
	"sort"
	"time"

	"github.com/revans/agent-tools/postlog"
)

// numericMetrics returns the numbers at the top level of a metrics reading.
func numericMetrics(data any) map[string]float64 {
	m, ok := data.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]float64{}
	for k, v := range m {
		if f, ok := v.(float64); ok {
			out[k] = f
		}
	}
	return out
}

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func reportCmd() *command {
	var f filter
	var by string
	var top int
	return &command{
		name: "report", summary: "what was published, and how it did, per platform", usage: "report [--since 30d] [--platform P] [--account A] [--by like_count] [--top N]",
		setup: func(fs *flag.FlagSet) {
			f.flags(fs, 0, "")
			fs.StringVar(&by, "by", "", "rank the top posts by this metric (default: like_count if there is one)")
			fs.IntVar(&top, "top", 5, "how many top posts to list")
		},
		run: func(c *ctx) (any, error) {
			if top < 0 {
				return nil, usage("--top cannot be negative")
			}
			_, d, err := readLedger()
			if err != nil {
				return nil, err
			}
			es, err := f.apply(d.Entries)
			if err != nil {
				return nil, err
			}
			last := latest(d)

			counts := map[string]map[string]int{}
			type agg struct {
				measured int
				sum, n   map[string]float64
			}
			per := map[string]*agg{}
			type row struct {
				e   postlog.Entry
				val float64
			}
			all := map[string]map[string]float64{} // ledger id -> metrics
			for _, e := range es {
				if counts[e.Platform] == nil {
					counts[e.Platform] = map[string]int{}
				}
				counts[e.Platform][e.Kind+"s"]++
				s, ok := last[e.ID]
				if !ok {
					continue
				}
				nm := numericMetrics(s.Data)
				if len(nm) == 0 {
					continue
				}
				all[e.ID] = nm
				a := per[e.Platform]
				if a == nil {
					a = &agg{sum: map[string]float64{}, n: map[string]float64{}}
					per[e.Platform] = a
				}
				a.measured++
				for k, v := range nm {
					a.sum[k] += v
					a.n[k]++
				}
			}

			metrics := map[string]any{}
			seen := map[string]bool{}
			for p, a := range per {
				avg := map[string]float64{}
				for k, s := range a.sum {
					avg[k] = s / a.n[k]
					seen[k] = true
				}
				metrics[p] = map[string]any{"posts_measured": a.measured, "totals": a.sum, "averages": avg}
			}
			if by == "" {
				switch {
				case seen["like_count"]:
					by = "like_count"
				default:
					var names []string
					for k := range seen {
						names = append(names, k)
					}
					sort.Strings(names)
					if len(names) > 0 {
						by = names[0]
					}
				}
			}
			var rows []row
			for _, e := range es {
				if v, ok := all[e.ID][by]; ok && by != "" {
					rows = append(rows, row{e, v})
				}
			}
			sort.SliceStable(rows, func(i, j int) bool { return rows[i].val > rows[j].val })
			if len(rows) > top {
				rows = rows[:top]
			}
			topList := []map[string]any{}
			for _, r := range rows {
				topList = append(topList, map[string]any{"id": r.e.ID, "platform": r.e.Platform, "account": r.e.Account,
					"remote_id": r.e.RemoteID, "url": r.e.URL, "text": cut(r.e.Text, 100), "at": r.e.At.Format(time.RFC3339), by: r.val})
			}
			out := map[string]any{"published": len(es), "by_platform": counts, "metrics": metrics, "ranked_by": by, "top": topList}
			if len(all) == 0 && len(es) > 0 {
				out["note"] = "no metrics have been recorded yet; run `posts refresh`, or `posts metrics ID --data '{...}'`"
			}
			return out, nil
		},
	}
}
