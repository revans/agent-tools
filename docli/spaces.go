package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Spaces buckets are not in DigitalOcean's REST API (godo has no call for them): they are made, listed and
// deleted through the S3-compatible API at REGION.digitaloceanspaces.com, signed with a Spaces access key. That
// takes no extra dependency: AWS Signature Version 4 is a few HMAC steps over the standard library, and docli
// only needs four S3 calls (list buckets, create, delete, list one object to see whether a bucket is empty).
//
// The Spaces secret key is a credential like the API token. It is read from the environment or a 0600 file,
// never from a flag, never printed, never logged, never written to the manifest, and removed from any error.

const spacesHostSuffix = "digitaloceanspaces.com"

type spacesCreds struct{ access, secret string }

// loadSpacesCreds reads the access key and secret. Order: DOCLI_SPACES_ACCESS_KEY / DOCLI_SPACES_SECRET_KEY, then
// SPACES_ACCESS_KEY_ID / SPACES_SECRET_ACCESS_KEY (what many other tools use), then ~/.config/docli/spaces, which
// must not be readable by anyone else and holds two lines: access_key=... and secret_key=...
func (a *App) loadSpacesCreds() (spacesCreds, error) {
	for _, pair := range [][2]string{{"DOCLI_SPACES_ACCESS_KEY", "DOCLI_SPACES_SECRET_KEY"}, {"SPACES_ACCESS_KEY_ID", "SPACES_SECRET_ACCESS_KEY"}} {
		ak, sk := strings.TrimSpace(a.Getenv(pair[0])), strings.TrimSpace(a.Getenv(pair[1]))
		if ak != "" || sk != "" {
			if ak == "" || sk == "" {
				return spacesCreds{}, fmt.Errorf("%s and %s must both be set", pair[0], pair[1])
			}
			return spacesCreds{ak, sk}, nil
		}
	}
	none := errNoSpacesCreds
	home, err := a.HomeDir()
	if err != nil {
		return spacesCreds{}, none
	}
	path := filepath.Join(home, ".config", "docli", "spaces")
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return spacesCreds{}, none
		}
		return spacesCreds{}, fmt.Errorf("cannot read the Spaces key file %s: %v", path, pathErr(err))
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		return spacesCreds{}, fmt.Errorf("the Spaces key file %s can be read by other users (mode %04o); refusing to use it. Run: chmod 600 %s", path, fi.Mode().Perm(), path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return spacesCreds{}, fmt.Errorf("cannot read the Spaces key file %s: %v", path, pathErr(err))
	}
	var c spacesCreds
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		switch {
		case !ok:
		case strings.TrimSpace(k) == "access_key":
			c.access = strings.TrimSpace(v)
		case strings.TrimSpace(k) == "secret_key":
			c.secret = strings.TrimSpace(v)
		}
	}
	if c.access == "" || c.secret == "" {
		return spacesCreds{}, fmt.Errorf("the Spaces key file %s must have the lines access_key=... and secret_key=...", path)
	}
	return c, nil
}

var errNoSpacesCreds = errors.New("no Spaces access key found. Buckets are managed through DigitalOcean's S3-compatible API, which needs a Spaces access key with full access: set DOCLI_SPACES_ACCESS_KEY and DOCLI_SPACES_SECRET_KEY, or put access_key=... and secret_key=... in ~/.config/docli/spaces (mode 0600). They are never taken from a flag")

// Spaces is the minimal S3 client.
type Spaces struct {
	creds spacesCreds
	base  func(region string) string // the endpoint for a region, without a trailing slash
	hc    *http.Client
	now   func() time.Time
}

// newSpaces builds the client and registers the secret so it is removed from every message.
func (a *App) newSpaces(g Globals) (*Spaces, error) {
	c, err := a.loadSpacesCreds()
	if err != nil {
		return nil, err
	}
	a.rememberSecret(c.secret)
	var rt http.RoundTripper = &retryTransport{next: http.DefaultTransport, sleep: a.Sleep, now: a.Now, attempts: 4, base: 500 * time.Millisecond, max: 10 * time.Second}
	if g.Debug {
		rt.(*retryTransport).debug = &redactWriter{w: a.Stderr, token: c.secret}
	}
	base := func(region string) string { return "https://" + region + "." + spacesHostSuffix }
	if a.SpacesURL != "" {
		base = func(string) string { return a.SpacesURL }
	}
	return &Spaces{creds: c, base: base, hc: &http.Client{Transport: rt, Timeout: g.Timeout}, now: a.Now}, nil
}

// ---- AWS Signature Version 4 ----

const (
	sigAlgo      = "AWS4-HMAC-SHA256"
	emptySHA256  = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	spacesSvcS3  = "s3"
	amzDateLayou = "20060102T150405Z"
)

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// uriEncode is the encoding SigV4 wants: everything but A-Z a-z 0-9 - _ . ~ is %XX; '/' is kept in a path.
func uriEncode(s string, keepSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~', c == '/' && keepSlash:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func canonicalQuery(q url.Values) string {
	var keys []string
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, uriEncode(k, false)+"="+uriEncode(v, false))
		}
	}
	return strings.Join(parts, "&")
}

// sigV4 computes the signature and the SignedHeaders list for a request. headers are the headers to sign, with
// lower case names; host must be among them.
func sigV4(secret, method, uri string, query url.Values, headers map[string]string, payloadHash, amzDate, region, service string) (signature, signedHeaders string) {
	var names []string
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, k := range names {
		canonHeaders.WriteString(k + ":" + strings.Join(strings.Fields(headers[k]), " ") + "\n")
	}
	signedHeaders = strings.Join(names, ";")
	canonical := strings.Join([]string{method, uriEncode(uri, true), canonicalQuery(query), canonHeaders.String(), signedHeaders, payloadHash}, "\n")
	date := amzDate[:8]
	scope := date + "/" + region + "/" + service + "/aws4_request"
	toSign := sigAlgo + "\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonical))
	key := hmacSHA256([]byte("AWS4"+secret), date)
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, service)
	key = hmacSHA256(key, "aws4_request")
	return hex.EncodeToString(hmacSHA256(key, toSign)), signedHeaders
}

// do sends one signed request. The body is always empty for the calls docli makes.
func (s *Spaces) do(ctx context.Context, method, region, path string, query url.Values, extra map[string]string) (int, []byte, error) {
	u := s.base(region) + path
	if len(query) > 0 {
		u += "?" + canonicalQuery(query)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return 0, nil, err
	}
	amzDate := s.now().UTC().Format(amzDateLayou)
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", emptySHA256)
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	signed := map[string]string{"host": req.URL.Host}
	for k := range req.Header {
		signed[strings.ToLower(k)] = req.Header.Get(k)
	}
	sig, sh := sigV4(s.creds.secret, method, req.URL.Path, query, signed, emptySHA256, amzDate, region, spacesSvcS3)
	req.Header.Set("Authorization", fmt.Sprintf("%s Credential=%s/%s/%s/%s/aws4_request, SignedHeaders=%s, Signature=%s", sigAlgo, s.creds.access, amzDate[:8], region, spacesSvcS3, sh, sig))
	resp, err := s.hc.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("the Spaces request failed: %v", urlErr(err))
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, b, nil
}

func urlErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

type s3Error struct {
	XMLName xml.Name `xml:"Error"`
	Code    string   `xml:"Code"`
	Message string   `xml:"Message"`
}

// spacesError describes a failed answer. The status code is kept so callers can tell "not found" from "denied".
type spacesError struct {
	Status        int
	Code, Message string
}

func (e *spacesError) Error() string {
	msg := fmt.Sprintf("Spaces answered %d", e.Status)
	if e.Code != "" {
		msg += " " + e.Code
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	switch e.Status {
	case 401, 403:
		msg += ". Check the Spaces access key: bucket calls need a key with full access (a key limited to some buckets cannot list, create or delete buckets)"
	}
	return msg
}

func parseS3Error(status int, body []byte) error {
	e := &spacesError{Status: status}
	var x s3Error
	if xml.Unmarshal(body, &x) == nil {
		e.Code, e.Message = x.Code, x.Message
	}
	return e
}

type bucketInfo struct {
	Name    string `xml:"Name"`
	Created string `xml:"CreationDate"`
}

func (s *Spaces) ListBuckets(ctx context.Context, region string) ([]bucketInfo, error) {
	st, body, err := s.do(ctx, http.MethodGet, region, "/", nil, nil)
	if err != nil {
		return nil, err
	}
	if st != http.StatusOK {
		return nil, parseS3Error(st, body)
	}
	var out struct {
		Buckets []bucketInfo `xml:"Buckets>Bucket"`
	}
	if err := xml.NewDecoder(bytes.NewReader(body)).Decode(&out); err != nil {
		return nil, fmt.Errorf("Spaces sent a bucket list docli could not read: %v", err)
	}
	return out.Buckets, nil
}

// CreateBucket makes a private bucket. A create is never retried (see retry.go: only GET is).
func (s *Spaces) CreateBucket(ctx context.Context, region, name string) error {
	st, body, err := s.do(ctx, http.MethodPut, region, "/"+name, nil, map[string]string{"x-amz-acl": "private"})
	if err != nil {
		return err
	}
	if st != http.StatusOK {
		return parseS3Error(st, body)
	}
	return nil
}

func (s *Spaces) DeleteBucket(ctx context.Context, region, name string) error {
	st, body, err := s.do(ctx, http.MethodDelete, region, "/"+name, nil, nil)
	if err != nil {
		return err
	}
	if st != http.StatusNoContent && st != http.StatusOK {
		return parseS3Error(st, body)
	}
	return nil
}

// BucketEmpty asks for one object. A bucket that has none is empty as far as listing can tell (versioned or
// half-uploaded objects can still stop a delete; the delete then fails and says so).
func (s *Spaces) BucketEmpty(ctx context.Context, region, name string) (bool, error) {
	st, body, err := s.do(ctx, http.MethodGet, region, "/"+name, url.Values{"list-type": {"2"}, "max-keys": {"1"}}, nil)
	if err != nil {
		return false, err
	}
	if st != http.StatusOK {
		return false, parseS3Error(st, body)
	}
	var out struct {
		KeyCount int      `xml:"KeyCount"`
		Keys     []string `xml:"Contents>Key"`
	}
	if err := xml.Unmarshal(body, &out); err != nil {
		return false, fmt.Errorf("Spaces sent an object list docli could not read: %v", err)
	}
	return out.KeyCount == 0 && len(out.Keys) == 0, nil
}
