package apikit

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// OAuth1 signs requests with OAuth 1.0a (RFC 5849, HMAC-SHA1), the way X's user
// context does. The signature covers the method, the URL, the query, and a
// form-encoded body; a JSON or multipart body is not signed, as the RFC says.
type OAuth1 struct {
	ConsumerKey    string
	ConsumerSecret string
	Token          string
	TokenSecret    string

	// Now and Nonce exist so tests can pin the output. They default to the clock
	// and a random string.
	Now   func() time.Time
	Nonce func() string
}

// oauthEscape is RFC 3986 percent-encoding: only A-Z a-z 0-9 - . _ ~ are kept.
func oauthEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func (o *OAuth1) nonce() string {
	if o.Nonce != nil {
		return o.Nonce()
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func (o *OAuth1) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

type kv struct{ k, v string }

// Apply signs the request and sets its Authorization header. With dry set it
// signs nothing and shows only that a signature would be added.
func (o *OAuth1) Apply(r *http.Request, dry bool) error {
	if dry {
		r.Header.Set("Authorization", "OAuth <signed at send time>")
		return nil
	}
	oauth := []kv{
		{"oauth_consumer_key", o.ConsumerKey},
		{"oauth_nonce", o.nonce()},
		{"oauth_signature_method", "HMAC-SHA1"},
		{"oauth_timestamp", strconv.FormatInt(o.now().Unix(), 10)},
		{"oauth_token", o.Token},
		{"oauth_version", "1.0"},
	}
	params := append([]kv{}, oauth...)
	for k, vs := range r.URL.Query() {
		for _, v := range vs {
			params = append(params, kv{k, v})
		}
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") && r.GetBody != nil {
		rc, err := r.GetBody()
		if err != nil {
			return err
		}
		raw, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return err
		}
		form, err := parseForm(string(raw))
		if err != nil {
			return err
		}
		params = append(params, form...)
	}
	sig := o.sign(r.Method, baseURL(r), params)
	oauth = append(oauth, kv{"oauth_signature", sig})
	sort.Slice(oauth, func(i, j int) bool { return oauth[i].k < oauth[j].k })
	parts := make([]string, len(oauth))
	for i, p := range oauth {
		parts[i] = oauthEscape(p.k) + `="` + oauthEscape(p.v) + `"`
	}
	r.Header.Set("Authorization", "OAuth "+strings.Join(parts, ", "))
	return nil
}

func baseURL(r *http.Request) string {
	scheme := strings.ToLower(r.URL.Scheme)
	host := strings.ToLower(r.URL.Host)
	if (scheme == "https" && strings.HasSuffix(host, ":443")) || (scheme == "http" && strings.HasSuffix(host, ":80")) {
		host = host[:strings.LastIndex(host, ":")]
	}
	return scheme + "://" + host + r.URL.EscapedPath()
}

// sign builds the RFC 5849 signature base string and signs it.
func (o *OAuth1) sign(method, base string, params []kv) string {
	enc := make([]kv, len(params))
	for i, p := range params {
		enc[i] = kv{oauthEscape(p.k), oauthEscape(p.v)}
	}
	sort.Slice(enc, func(i, j int) bool {
		if enc[i].k != enc[j].k {
			return enc[i].k < enc[j].k
		}
		return enc[i].v < enc[j].v
	})
	pairs := make([]string, len(enc))
	for i, p := range enc {
		pairs[i] = p.k + "=" + p.v
	}
	baseString := strings.ToUpper(method) + "&" + oauthEscape(base) + "&" + oauthEscape(strings.Join(pairs, "&"))
	key := oauthEscape(o.ConsumerSecret) + "&" + oauthEscape(o.TokenSecret)
	mac := hmac.New(sha1.New, []byte(key))
	mac.Write([]byte(baseString))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func parseForm(s string) ([]kv, error) {
	var out []kv
	if s == "" {
		return nil, nil
	}
	for _, pair := range strings.Split(s, "&") {
		k, v, _ := strings.Cut(pair, "=")
		uk, err := queryUnescape(k)
		if err != nil {
			return nil, err
		}
		uv, err := queryUnescape(v)
		if err != nil {
			return nil, err
		}
		out = append(out, kv{uk, uv})
	}
	return out, nil
}

func queryUnescape(s string) (string, error) { return url.QueryUnescape(s) }
