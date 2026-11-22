package apikit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const maxResponse = 8 << 20

// HTTPError is a non-2xx answer. Its text is the API's own message when it has one.
type HTTPError struct {
	Status     int
	Body       string
	RetryAfter string
	Context    string
}

func (e *HTTPError) Error() string {
	what := "HTTP " + fmt.Sprint(e.Status)
	if e.Context != "" {
		what = e.Context + ": " + what
	}
	if e.Status == http.StatusTooManyRequests {
		what += " (rate limited"
		if e.RetryAfter != "" {
			what += "; retry after " + e.RetryAfter + "s"
		}
		what += ")"
	}
	if msg := apiMessage(e.Body); msg != "" {
		return what + ": " + msg
	}
	return what
}

// apiMessage pulls a human message out of the common error shapes.
func apiMessage(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	var v any
	if json.Unmarshal([]byte(body), &v) == nil {
		for _, path := range []string{"message", "error_description", "detail", "error.message", "errors[0].message", "error", "title"} {
			if s, _ := Select(v, path); s != nil {
				if str, ok := s.(string); ok && str != "" {
					return trunc(str, 400)
				}
			}
		}
	}
	return trunc(body, 400)
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Client sends endpoint calls.
type Client struct {
	HTTP       *http.Client
	BaseURL    string
	Auth       Authenticator
	UserAgent  string
	Debug      io.Writer // when set, one line per request: method, URL, status
	DryRun     bool      // build and show the request, send nothing
	MaxSafety  string    // refuse endpoints above this level (default write)
	MaxPages   int       // hard ceiling on pagination (default 10)
	Sleep      func(time.Duration)
	RetryLimit int
}

// Result is what a call returns.
type Result struct {
	Data   any    // the selected and projected part of the response
	Raw    any    // the whole decoded response
	Status int    // HTTP status
	Cursor string // next cursor, when the endpoint paginates and more exists
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{}
}

// Call runs an endpoint. pages > 1 follows the endpoint's cursor.
func (c *Client) Call(ctx context.Context, ep *Endpoint, params map[string]string, pages int) (*Result, error) {
	limit := c.MaxSafety
	if limit == "" {
		limit = SafetyWrite
	}
	if SafetyRank(ep.Safety) > SafetyRank(limit) {
		return nil, fmt.Errorf("endpoint %s is %s, above the allowed level %s (see --max-safety)", ep.Name, ep.Safety, limit)
	}
	if pages < 1 {
		pages = 1
	}
	if pages > 1 && ep.Paginate == nil {
		return nil, fmt.Errorf("endpoint %s does not paginate", ep.Name)
	}
	maxPages := c.MaxPages
	if maxPages == 0 {
		maxPages = 10
	}
	if ep.Paginate != nil && ep.Paginate.MaxPages > 0 {
		maxPages = ep.Paginate.MaxPages
	}
	if pages > maxPages {
		return nil, fmt.Errorf("endpoint %s allows at most %d pages", ep.Name, maxPages)
	}

	cur := map[string]string{}
	for k, v := range params {
		cur[k] = v
	}
	var (
		all  []any
		last *Result
	)
	for page := 1; page <= pages; page++ {
		res, err := c.once(ctx, ep, cur)
		if err != nil {
			return nil, err
		}
		if c.DryRun {
			return res, nil
		}
		last = res
		if pages == 1 {
			return res, nil
		}
		if l, ok := res.Data.([]any); ok {
			all = append(all, l...)
		} else {
			all = append(all, res.Data)
		}
		if res.Cursor == "" {
			break
		}
		cur[ep.Paginate.CursorParam] = res.Cursor
	}
	last.Data = all
	return last, nil
}

func (c *Client) once(ctx context.Context, ep *Endpoint, params map[string]string) (*Result, error) {
	built, err := ep.Build(c.BaseURL, params)
	if err != nil {
		return nil, err
	}
	if c.DryRun {
		return &Result{Data: c.describe(built)}, nil
	}
	retries := c.RetryLimit
	for attempt := 0; ; attempt++ {
		res, err := c.send(ctx, ep, built)
		var he *HTTPError
		if err != nil && attempt < retries && asHTTP(err, &he) && (he.Status == 429 || he.Status >= 500) && ep.Method == "GET" {
			c.sleep(backoff(attempt, he.RetryAfter))
			continue
		}
		return res, err
	}
}

func asHTTP(err error, target **HTTPError) bool {
	he, ok := err.(*HTTPError)
	if ok {
		*target = he
	}
	return ok
}

func backoff(attempt int, retryAfter string) time.Duration {
	var secs int
	if _, err := fmt.Sscan(retryAfter, &secs); err == nil && secs > 0 && secs <= 30 {
		return time.Duration(secs) * time.Second
	}
	return time.Duration(1<<attempt) * time.Second
}

func (c *Client) sleep(d time.Duration) {
	if c.Sleep != nil {
		c.Sleep(d)
		return
	}
	time.Sleep(d)
}

func (c *Client) newRequest(ctx context.Context, built *Request) (*http.Request, error) {
	var body io.Reader
	if built.Body != nil {
		body = bytes.NewReader(built.Body)
	}
	req, err := http.NewRequestWithContext(ctx, built.Method, built.URL, body)
	if err != nil {
		return nil, err
	}
	for k, v := range built.Headers {
		req.Header.Set(k, v)
	}
	if c.UserAgent != "" && req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	return req, nil
}

func (c *Client) send(ctx context.Context, ep *Endpoint, built *Request) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, ep.timeout)
	defer cancel()
	req, err := c.newRequest(ctx, built)
	if err != nil {
		return nil, err
	}
	if c.Auth != nil {
		if err := c.Auth.Apply(req, false); err != nil {
			return nil, err
		}
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", built.Method, redactURL(built.URL), err)
	}
	defer resp.Body.Close()
	if c.Debug != nil {
		fmt.Fprintf(c.Debug, "%s %s -> %d\n", built.Method, redactURL(built.URL), resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxResponse {
		return nil, fmt.Errorf("the response is larger than %d MB", maxResponse>>20)
	}
	if resp.StatusCode/100 != 2 {
		return nil, &HTTPError{Status: resp.StatusCode, Body: string(raw), RetryAfter: resp.Header.Get("Retry-After")}
	}
	var decoded any
	if len(bytes.TrimSpace(raw)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&decoded); err != nil {
			decoded = string(raw)
		}
	}
	res := &Result{Raw: decoded, Status: resp.StatusCode}
	data := decoded
	if ep.Select != "" {
		if data, err = Select(decoded, ep.Select); err != nil {
			return nil, fmt.Errorf("endpoint %s: select: %w", ep.Name, err)
		}
	}
	if data, err = Project(data, ep.Fields); err != nil {
		return nil, fmt.Errorf("endpoint %s: fields: %w", ep.Name, err)
	}
	res.Data = data
	if ep.Paginate != nil {
		if cur, _ := Select(decoded, ep.Paginate.CursorPath); cur != nil {
			if s, ok := cur.(string); ok {
				res.Cursor = s
			} else if n, ok := cur.(json.Number); ok {
				res.Cursor = n.String()
			}
		}
	}
	return res, nil
}

// describe shows a request without sending it, with secrets hidden.
func (c *Client) describe(built *Request) map[string]any {
	headers := map[string]string{}
	if req, err := c.newRequest(context.Background(), built); err == nil {
		if c.Auth != nil {
			_ = c.Auth.Apply(req, true)
		}
		for k := range req.Header {
			headers[k] = redactHeader(k, req.Header.Get(k))
		}
		built = &Request{Method: built.Method, URL: req.URL.String(), Body: built.Body}
	}
	out := map[string]any{"dry_run": true, "method": built.Method, "url": redactURL(built.URL), "headers": sortedMap(headers)}
	if len(built.Body) > 0 {
		var v any
		if json.Unmarshal(built.Body, &v) == nil {
			out["body"] = v
		} else {
			out["body"] = string(built.Body)
		}
	}
	return out
}

func sortedMap(m map[string]string) map[string]string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]string, len(m))
	for _, k := range keys {
		out[k] = m[k]
	}
	return out
}

var secretHeaders = map[string]bool{"authorization": true, "x-goog-api-key": true, "x-api-key": true, "cookie": true}

func redactHeader(k, v string) string {
	if secretHeaders[strings.ToLower(k)] && !strings.HasPrefix(v, "Bearer <") {
		return "[redacted]"
	}
	return v
}

var secretQuery = []string{"key=", "access_token=", "api_key=", "token="}

// redactURL hides secret-looking query values, so logs and dry runs are safe to paste.
func redactURL(u string) string {
	i := strings.IndexByte(u, '?')
	if i < 0 {
		return u
	}
	parts := strings.Split(u[i+1:], "&")
	for n, p := range parts {
		for _, s := range secretQuery {
			if strings.HasPrefix(strings.ToLower(p), s) {
				parts[n] = p[:len(s)] + "[redacted]"
			}
		}
	}
	return u[:i+1] + strings.Join(parts, "&")
}
