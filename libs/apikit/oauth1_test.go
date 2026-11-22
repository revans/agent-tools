package apikit

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The example from X's own documentation, "Creating a signature". Its signature
// was also computed with an independent HMAC-SHA1 implementation (Python).
func docOAuth1() *OAuth1 {
	return &OAuth1{
		ConsumerKey:    "xvz1evFS4wEEPTGEFPHBog",
		ConsumerSecret: "kAcSOqF21Fu85e7zjz7ZN2U4ZRhfV3WpwPAoE3Z7kBw",
		Token:          "370773112-GmHxMAgYyLbNEtIKZeRNFsMKPR9EyMZeS9weJAEb",
		TokenSecret:    "LswwdoUaIvS8ltyTt5jkRh4J50vUPVVHtR2YPi5kE",
		Now:            func() time.Time { return time.Unix(1318622958, 0) },
		Nonce:          func() string { return "kYjzVBB8Y0ZFabxSWbWovY3uYSQ2pTgmZeNu2VS4cg" },
	}
}

func TestOAuth1MatchesTheDocumentedSignature(t *testing.T) {
	form := url.Values{"status": {"Hello Ladies + Gentlemen, a signed OAuth request!"}}
	req, _ := http.NewRequest("POST", "https://api.twitter.com/1.1/statuses/update.json?include_entities=true", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := docOAuth1().Apply(req, false); err != nil {
		t.Fatal(err)
	}
	auth := req.Header.Get("Authorization")
	if !strings.Contains(auth, `oauth_signature="hCtSmYh%2BiHYCEqBWrE7C7hYmtUk%3D"`) {
		t.Errorf("the signature differs from the documented one:\n%s", auth)
	}
	for _, want := range []string{`oauth_consumer_key="xvz1evFS4wEEPTGEFPHBog"`, `oauth_nonce="kYjzVBB8Y0ZFabxSWbWovY3uYSQ2pTgmZeNu2VS4cg"`,
		`oauth_signature_method="HMAC-SHA1"`, `oauth_timestamp="1318622958"`, `oauth_version="1.0"`, "OAuth "} {
		if !strings.Contains(auth, want) {
			t.Errorf("%s missing from %s", want, auth)
		}
	}
}

func TestOAuth1WhatIsSignedAndWhatIsNot(t *testing.T) {
	sig := func(method, rawURL, ctype, body string) string {
		var r io.Reader
		if body != "" {
			r = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, rawURL, r)
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		docOAuth1().Apply(req, false)
		return req.Header.Get("Authorization")
	}
	base := sig("POST", "https://api.x.com/2/tweets", "application/json", `{"text":"hi"}`)
	if sig("POST", "https://api.x.com/2/tweets", "application/json", `{"text":"completely different"}`) != base {
		t.Error("a JSON body is not part of an OAuth 1.0a signature")
	}
	if sig("POST", "https://api.x.com/2/tweets?x=1", "application/json", `{"text":"hi"}`) == base {
		t.Error("a query parameter must change the signature")
	}
	if sig("GET", "https://api.x.com/2/tweets", "", "") == base {
		t.Error("the method must change the signature")
	}
	// default ports and the host's case do not matter; the path does
	if sig("GET", "https://API.x.com:443/2/tweets", "", "") != sig("GET", "https://api.x.com/2/tweets", "", "") {
		t.Error("an explicit default port and a capitalised host must sign the same")
	}
	if sig("GET", "https://api.x.com/2/tweet", "", "") == sig("GET", "https://api.x.com/2/tweets", "", "") {
		t.Error("the path must change the signature")
	}
	// a body is not consumed by signing
	req, _ := http.NewRequest("POST", "https://x.example/p", strings.NewReader("a=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	docOAuth1().Apply(req, false)
	if b, _ := io.ReadAll(req.Body); string(b) != "a=1" {
		t.Errorf("signing must leave the body intact, got %q", b)
	}
	// dry run signs nothing
	req, _ = http.NewRequest("GET", "https://x.example/", nil)
	docOAuth1().Apply(req, true)
	if a := req.Header.Get("Authorization"); strings.Contains(a, "oauth_signature") || strings.Contains(a, "kAcSOq") {
		t.Errorf("dry run: %s", a)
	}
}

func TestOAuth1SpecialCharacters(t *testing.T) {
	for in, want := range map[string]string{
		"AZaz09-._~": "AZaz09-._~", " ": "%20", "+": "%2B", "!": "%21", "*": "%2A", "'": "%27", "(": "%28", ")": "%29",
		"é": "%C3%A9", "/": "%2F", "=": "%3D", "&": "%26",
	} {
		if got := oauthEscape(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestSendRunsAHandBuiltRequestWithTheSameGuards(t *testing.T) {
	var hits, authed int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.Header.Get("Authorization") != "" {
			atomic.AddInt32(&authed, 1)
		}
		if r.URL.Path == "/flaky" && atomic.LoadInt32(&hits) < 3 {
			http.Error(w, "busy", 503)
			return
		}
		w.Header().Set("X-Restli-Id", "urn:li:share:123")
		w.WriteHeader(201)
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Auth: Bearer{Token: "SECRET"}, RetryLimit: 3, Sleep: func(time.Duration) {}}
	ctx := context.Background()

	res, err := c.Send(ctx, &Request{Method: "POST", URL: srv.URL + "/posts", Body: []byte(`{}`), Headers: map[string]string{"Content-Type": "application/json"}}, SendOptions{Name: "create_post"})
	if err != nil || res.Status != 201 || res.Header.Get("X-Restli-Id") != "urn:li:share:123" {
		t.Fatalf("response headers must be readable: %v %+v", err, res)
	}
	if authed != 1 {
		t.Error("credentials must be sent")
	}
	if _, err := c.Send(ctx, &Request{Method: "PUT", URL: srv.URL + "/upload", SkipAuth: true, Body: []byte("bytes")}, SendOptions{Name: "upload"}); err != nil || authed != 1 {
		t.Errorf("SkipAuth must send no credentials (authed=%d): %v", authed, err)
	}
	atomic.StoreInt32(&hits, 0)
	if _, err := c.Send(ctx, &Request{Method: "GET", URL: srv.URL + "/flaky"}, SendOptions{Name: "flaky", Safety: SafetyRead}); err != nil || hits != 3 {
		t.Errorf("a GET is retried: %v hits=%d", err, hits)
	}
	atomic.StoreInt32(&hits, 0)
	if _, err := c.Send(ctx, &Request{Method: "POST", URL: srv.URL + "/flaky"}, SendOptions{Name: "post"}); err == nil || hits != 1 {
		t.Errorf("a POST is never retried: %v hits=%d", err, hits)
	}
	c.MaxSafety = SafetyRead
	if _, err := c.Send(ctx, &Request{Method: "POST", URL: srv.URL}, SendOptions{Name: "post"}); err == nil || !strings.Contains(err.Error(), "above the allowed level") {
		t.Errorf("--max-safety applies to Send: %v", err)
	}
	c.MaxSafety = ""
	atomic.StoreInt32(&hits, 0)
	c.DryRun = true
	res, err = c.Send(ctx, &Request{Method: "POST", URL: srv.URL + "/posts?access_token=TOPSECRET", Body: []byte(`{"a":1}`)}, SendOptions{Name: "post"})
	b, _ := json.Marshal(res.Data)
	if err != nil || hits != 0 || strings.Contains(string(b), "SECRET") || !strings.Contains(string(b), `"dry_run":true`) {
		t.Errorf("dry run: %v hits=%d %s", err, hits, b)
	}
}

func TestMultipartIsWellFormedAndStable(t *testing.T) {
	body, ct, err := Multipart(map[string]string{"b": "2", "a": "1"}, []FilePart{{Field: "media", Filename: `we"ird.png`, ContentType: "image/png", Data: []byte("PNGDATA")}})
	if err != nil {
		t.Fatal(err)
	}
	mt, params, err := mime.ParseMediaType(ct)
	if err != nil || mt != "multipart/form-data" {
		t.Fatalf("%v %s", err, ct)
	}
	r := multipart.NewReader(strings.NewReader(string(body)), params["boundary"])
	var names []string
	for {
		p, err := r.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(p)
		names = append(names, p.FormName()+"="+string(data))
		if p.FormName() == "media" && (p.FileName() != `we"ird.png` || p.Header.Get("Content-Type") != "image/png") {
			t.Errorf("file part: %q %q", p.FileName(), p.Header.Get("Content-Type"))
		}
	}
	if strings.Join(names, ",") != "a=1,b=2,media=PNGDATA" {
		t.Errorf("fields sorted, then files: %v", names)
	}
}
