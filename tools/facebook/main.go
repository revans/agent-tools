// facebook publishes to and reads from a Facebook Page through the Meta Graph
// API, from the command line. It is built on apikit: every API call is a TOML
// endpoint (see `facebook endpoints`), and the high-level commands combine them.
// It works on Pages only: Meta no longer offers publishing to Groups, and never
// offered it for personal profiles.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/revans/agent-tools/apikit"
)

//go:embed endpoints.toml
var builtin string

var version = "0.1.0"

const (
	apiBase    = "https://graph.facebook.com"
	apiVersion = "v27.0"
)

const starter = `# facebook config. This tool publishes to a Facebook PAGE you manage, with a PAGE
# access token (a user token cannot post as the Page). Create an app in Meta for
# Developers, give it pages_manage_posts, pages_read_engagement,
# pages_manage_engagement, pages_show_list and (for metrics) read_insights, and
# generate a Page access token from a long-lived user token: a Page token made
# that way has no expiry date (see the README for what is and is not confirmed).
# The token can be written three ways: access_token, access_token_env (the name of
# an environment variable) or access_token_command (a command that prints it).

default = "main"

[accounts.main]
page_id = "1234567890"
access_token = "..."
# api_version = "v27.0"
`

func service() *apikit.Service {
	return &apikit.Service{
		Name:      "facebook",
		Summary:   "post, photo and comment on a Facebook Page, and read its posts and their numbers",
		Version:   version,
		Starter:   starter,
		Builtin:   builtin,
		UserAgent: "agent-tools-facebook/" + version,
		NewClient: newClient,
		Check:     check,
		Commands:  []*apikit.Command{postCommand(), photoCommand(), commentCommand(), postsCommand(), metricsCommand()},
	}
}

func newClient(name string, acct map[string]any) (*apikit.Client, error) {
	tok, err := apikit.ResolveSecret(name, acct, "access_token")
	if err != nil {
		return nil, err
	}
	pid := ""
	switch v := acct["page_id"].(type) {
	case nil:
	case string:
		pid = strings.TrimSpace(v)
	case int64:
		pid = fmt.Sprint(v)
	default:
		return nil, fmt.Errorf("account %q: page_id must be a string such as \"1234567890\"", name)
	}
	base := strings.TrimRight(apikit.OptString(acct, "base_url", apiBase), "/")
	ver := strings.Trim(apikit.OptString(acct, "api_version", apiVersion), "/")
	return &apikit.Client{
		BaseURL:    base + "/" + ver,
		Auth:       &tokenAuth{Token: tok, PageID: pid},
		RetryLimit: 2, // only a GET is ever retried; a POST could publish twice
		Sleep:      func(d time.Duration) { time.Sleep(d) },
	}, nil
}

// tokenAuth sends the Page access token as the access_token parameter, which is
// how Meta's reference shows it: in the form body of a POST and in the query
// string of a GET. A dry run adds nothing, so nothing can leak.
type tokenAuth struct {
	Token  string
	PageID string
}

func (a *tokenAuth) Apply(req *http.Request, dry bool) error {
	if dry {
		return nil
	}
	if req.Method == http.MethodPost && req.GetBody != nil && strings.HasPrefix(req.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		rc, err := req.GetBody()
		if err != nil {
			return err
		}
		raw, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return err
		}
		form, err := url.ParseQuery(string(raw))
		if err != nil {
			return err
		}
		form.Set("access_token", a.Token)
		b := []byte(form.Encode())
		req.Body = io.NopCloser(strings.NewReader(string(b)))
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(string(b))), nil }
		req.ContentLength = int64(len(b))
		return nil
	}
	q := req.URL.Query()
	q.Set("access_token", a.Token)
	req.URL.RawQuery = q.Encode()
	return nil
}

func check(ctx context.Context, r *apikit.Run) (any, error) {
	pid, err := pageID(r)
	if err != nil {
		return nil, err
	}
	res, err := r.Call("page_info", map[string]string{"page_id": pid})
	if err != nil {
		return nil, mapError(err, false)
	}
	if r.DryRun() {
		return res.Data, nil
	}
	m, _ := res.Raw.(map[string]any)
	if str(m, "id") == "" {
		return nil, fmt.Errorf("facebook: the token was accepted but the answer has no page id: %v", res.Raw)
	}
	return map[string]any{"page_id": str(m, "id"), "name": str(m, "name")}, nil
}

func main() { apikit.Main(service()) }
