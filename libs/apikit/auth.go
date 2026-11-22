package apikit

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Authenticator adds credentials to a request. With dry set it must not touch
// the network or reveal a secret: it marks the request as it would be signed.
type Authenticator interface {
	Apply(req *http.Request, dry bool) error
}

// NoAuth sends the request as it is.
type NoAuth struct{}

func (NoAuth) Apply(*http.Request, bool) error { return nil }

// Bearer sends Authorization: Bearer TOKEN.
type Bearer struct{ Token string }

func (b Bearer) Apply(r *http.Request, dry bool) error {
	r.Header.Set("Authorization", "Bearer "+b.Token)
	return nil
}

// HeaderKey sends a secret in a named header, such as X-Goog-Api-Key.
type HeaderKey struct{ Name, Value string }

func (h HeaderKey) Apply(r *http.Request, dry bool) error {
	r.Header.Set(h.Name, h.Value)
	return nil
}

// QueryKey sends a secret as a query parameter, for APIs that offer nothing else.
type QueryKey struct{ Name, Value string }

func (q QueryKey) Apply(r *http.Request, dry bool) error {
	v := r.URL.Query()
	v.Set(q.Name, q.Value)
	r.URL.RawQuery = v.Encode()
	return nil
}

// Basic sends HTTP basic auth.
type Basic struct{ User, Pass string }

func (b Basic) Apply(r *http.Request, dry bool) error {
	r.SetBasicAuth(b.User, b.Pass)
	return nil
}

// ClientCredentials fetches an OAuth2 access token with the client_credentials
// grant (client id and secret as basic auth) and sends it as a bearer token.
// The token is fetched on first use and kept for the life of the process.
type ClientCredentials struct {
	TokenURL  string
	ID        string
	Secret    string
	UserAgent string
	Scope     string
	HTTP      *http.Client

	token string
}

func (c *ClientCredentials) Apply(r *http.Request, dry bool) error {
	if dry {
		r.Header.Set("Authorization", "Bearer <fetched at send time>")
		return nil
	}
	if c.token == "" {
		t, err := c.fetch()
		if err != nil {
			return err
		}
		c.token = t
	}
	r.Header.Set("Authorization", "Bearer "+c.token)
	return nil
}

// Token returns the access token, fetching it if needed.
func (c *ClientCredentials) Token() (string, error) {
	if c.token == "" {
		t, err := c.fetch()
		if err != nil {
			return "", err
		}
		c.token = t
	}
	return c.token, nil
}

func (c *ClientCredentials) fetch() (string, error) {
	form := url.Values{"grant_type": {"client_credentials"}}
	if c.Scope != "" {
		form.Set("scope", c.Scope)
	}
	req, err := http.NewRequest("POST", c.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.ID, c.Secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching an access token: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return "", &HTTPError{Status: resp.StatusCode, Body: string(body), Context: "token request"}
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.AccessToken == "" {
		return "", fmt.Errorf("the token response had no access_token")
	}
	return out.AccessToken, nil
}
