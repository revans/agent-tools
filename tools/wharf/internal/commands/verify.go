package commands

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"wharf/internal/config"
	"wharf/internal/logging"
)

// verifyURL builds the public URL for a repo-relative file path.
func verifyURL(baseURL, rel string) string {
	segs := strings.Split(rel, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.TrimRight(baseURL, "/") + "/" + strings.Join(segs, "/")
}

// verifyTargets returns the URLs to check for each local file: the file's
// own URL (with its expected size) and, for index.html files, the
// directory URL a visitor would actually type (size unknown, -1).
func verifyTargets(baseURL string, candidates []localCandidate) []verifyTarget {
	var out []verifyTarget
	for _, c := range candidates {
		out = append(out, verifyTarget{url: verifyURL(baseURL, c.relPath), rel: c.relPath, size: c.size})
		if c.relPath == "index.html" || strings.HasSuffix(c.relPath, "/index.html") {
			dir := strings.TrimSuffix(c.relPath, "index.html")
			u := verifyURL(baseURL, dir)
			if dir == "" {
				u = strings.TrimRight(baseURL, "/") + "/"
			}
			out = append(out, verifyTarget{url: u, rel: dir, size: -1})
		}
	}
	return out
}

type verifyTarget struct {
	url  string
	rel  string
	size int64 // expected body size, or -1 if not comparable
}

type verifyResult struct {
	target verifyTarget
	status int
	got    int64
	err    error
}

// Verify requests the public URL of every local site file and reports any
// that do not return a 2xx response, or whose body size differs from the
// local file. A successful upload only means the file landed; this checks
// that the site actually serves it. Size mismatches are warnings (a host
// or CDN may legitimately rewrite content); non-2xx and network errors
// fail the run.
func Verify(cfg *config.Config, repoRoot, baseURLFlag string, logger *logging.Logger, out io.Writer) error {
	baseURL := baseURLFlag
	if baseURL == "" {
		baseURL = cfg.BaseURL
	}
	if baseURL == "" {
		return logged(logger, fmt.Errorf("no base URL: add base_url = \"https://your-site\" to .wharf, or pass --base-url"))
	}
	if u, err := url.Parse(baseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return logged(logger, fmt.Errorf("base URL %q must look like https://example.com", baseURL))
	}

	candidates, err := localCandidates(repoRoot, logger)
	if err != nil {
		return err
	}
	targets := verifyTargets(baseURL, candidates)

	client := &http.Client{
		Timeout:   20 * time.Second,
		Transport: &http.Transport{DisableCompression: true},
	}
	results := make([]verifyResult, len(targets))
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, t verifyTarget) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = checkURL(client, t)
		}(i, t)
	}
	wg.Wait()

	var failed, warned int
	for _, r := range results {
		switch {
		case r.err != nil:
			failed++
			fmt.Fprintf(out, "FAIL  %s  (%v)\n", r.target.url, r.err)
			logger.Error("verify %s: %v", r.target.url, r.err)
		case r.status < 200 || r.status > 299:
			failed++
			fmt.Fprintf(out, "FAIL  %s  (HTTP %d)\n", r.target.url, r.status)
			logger.Error("verify %s: HTTP %d", r.target.url, r.status)
		case r.target.size >= 0 && r.got != r.target.size:
			warned++
			fmt.Fprintf(out, "WARN  %s  (served %d bytes, local file is %d)\n", r.target.url, r.got, r.target.size)
			logger.Step("verify %s: size mismatch served=%d local=%d", r.target.url, r.got, r.target.size)
		default:
			logger.Step("verify %s: ok (HTTP %d)", r.target.url, r.status)
		}
	}
	fmt.Fprintf(out, "\nChecked %d URL(s): %d ok, %d warning(s), %d failed.\n", len(results), len(results)-failed-warned, warned, failed)
	if failed > 0 {
		return fmt.Errorf("%d of %d URL(s) failed verification", failed, len(results))
	}
	return nil
}

func checkURL(client *http.Client, t verifyTarget) verifyResult {
	req, err := http.NewRequest(http.MethodGet, t.url, nil)
	if err != nil {
		return verifyResult{target: t, err: err}
	}
	// Ask for the bytes as stored, bypassing intermediate caches and
	// compression so the size is comparable to the local file.
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := client.Do(req)
	if err != nil {
		return verifyResult{target: t, err: err}
	}
	defer resp.Body.Close()
	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		return verifyResult{target: t, status: resp.StatusCode, err: err}
	}
	return verifyResult{target: t, status: resp.StatusCode, got: n}
}
