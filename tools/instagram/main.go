// instagram posts to Instagram, comments on your media and replies to comments
// through the Instagram Graph API, from the command line. It is built on apikit:
// every API call is a TOML endpoint (see `instagram endpoints`), and the three
// publishing commands combine them into the flows the API needs.
package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/revans/agent-tools/apikit"
)

//go:embed endpoints.toml
var builtin string

var version = "0.1.0"

const (
	apiBase    = "https://graph.instagram.com"
	apiVersion = "v25.0"
)

const starter = `# instagram config. This first version has no browser login: create an
# Instagram app (Instagram API with Instagram Login), generate a long-lived access
# token for your professional account, and paste it here. Long-lived tokens expire
# after about 60 days and nothing refreshes them yet; generate a new one and update
# access_token before then.
# The token can be written three ways: access_token, access_token_env (the name of
# an environment variable) or access_token_command (a command that prints it).

default = "main"

[accounts.main]
access_token = "..."
# The Instagram user id is looked up with the token (GET /me) when it is not set:
# user_id = "17841400000000000"
`

var sleep = time.Sleep

func service() *apikit.Service {
	return &apikit.Service{
		Name:      "instagram",
		Summary:   "post to Instagram, comment on media and reply to comments",
		Version:   version,
		Starter:   starter,
		Builtin:   builtin,
		UserAgent: "agent-tools-instagram/" + version,
		NewClient: newClient,
		Check:     check,
		Commands:  []*apikit.Command{postCommand(), commentCommand(), replyCommand()},
	}
}

func newClient(name string, acct map[string]any) (*apikit.Client, error) {
	tok, err := apikit.ResolveSecret(name, acct, "access_token")
	if err != nil {
		return nil, err
	}
	uid := ""
	switch v := acct["user_id"].(type) {
	case nil:
	case string:
		uid = strings.TrimSpace(v)
	case int64:
		uid = fmt.Sprint(v)
	default:
		return nil, fmt.Errorf("account %q: user_id must be a string such as \"17841400000000000\"", name)
	}
	base := strings.TrimRight(apikit.OptString(acct, "base_url", apiBase), "/")
	ver := strings.Trim(apikit.OptString(acct, "api_version", apiVersion), "/")
	return &apikit.Client{
		BaseURL:    base + "/" + ver,
		Auth:       &tokenAuth{Token: tok, UserID: uid},
		RetryLimit: 2, // only a GET is ever retried; a POST could publish twice
		Sleep:      func(d time.Duration) { sleep(d) },
	}, nil
}

// tokenAuth sends the access token where Mimir's live-verified client does: in
// the JSON body of a POST and in the query string of a GET. A dry run adds
// nothing, so nothing can leak.
type tokenAuth struct {
	Token  string
	UserID string // optional override; otherwise looked up with GET /me
}

func (a *tokenAuth) Apply(req *http.Request, dry bool) error {
	if dry {
		return nil
	}
	if req.Method == http.MethodPost && req.GetBody != nil && strings.HasPrefix(req.Header.Get("Content-Type"), "application/json") {
		rc, err := req.GetBody()
		if err != nil {
			return err
		}
		raw, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return err
		}
		obj := map[string]any{}
		if len(bytes.TrimSpace(raw)) > 0 {
			if err := json.Unmarshal(raw, &obj); err != nil {
				return err
			}
		}
		obj["access_token"] = a.Token
		b, err := json.Marshal(obj)
		if err != nil {
			return err
		}
		req.Body = io.NopCloser(bytes.NewReader(b))
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
		req.ContentLength = int64(len(b))
		return nil
	}
	q := req.URL.Query()
	q.Set("access_token", a.Token)
	req.URL.RawQuery = q.Encode()
	return nil
}

func check(ctx context.Context, r *apikit.Run) (any, error) {
	res, err := r.Call("me", nil)
	if err != nil {
		return nil, mapError(err, false)
	}
	if r.DryRun() {
		return res.Data, nil
	}
	me := firstEntry(res.Raw)
	if me == nil || str(me, "user_id") == "" {
		return nil, fmt.Errorf("instagram: the token was accepted but the answer has no user id: %v", res.Raw)
	}
	return map[string]any{"user_id": str(me, "user_id"), "username": str(me, "username")}, nil
}

func main() { apikit.Main(service()) }
