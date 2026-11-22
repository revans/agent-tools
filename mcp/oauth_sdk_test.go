package main

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// TestOAuthWithSDKClient connects the MCP SDK's own OAuth client, written by
// someone else, to this server: discovery from the 401 challenge, dynamic
// registration, PKCE, the browser step (played by the test), the token
// exchange, and an authorized tool call. It checks our reading of the spec
// against an independent implementation.
func TestOAuthWithSDKClient(t *testing.T) {
	fakeCLI(t)
	e := newOAuthEnv(t)

	browser := func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
		u, _ := url.Parse(args.URL)
		resp := e.consent(u.RequestURI(), ownerSecret, "approve")
		io.Copy(io.Discard, resp.Body)
		if resp.StatusCode != 302 {
			t.Errorf("browser step: %d", resp.StatusCode)
			return nil, io.ErrUnexpectedEOF
		}
		loc, _ := url.Parse(resp.Header.Get("Location"))
		return &auth.AuthorizationResult{Code: loc.Query().Get("code"), State: loc.Query().Get("state"), Iss: loc.Query().Get("iss")}, nil
	}
	handler, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{
			RedirectURIs:            []string{cb},
			ClientName:              "SDK test client",
			TokenEndpointAuthMethod: "none",
			GrantTypes:              []string{"authorization_code", "refresh_token"},
			ResponseTypes:           []string{"code"},
		}},
		RedirectURL:              cb,
		AuthorizationCodeFetcher: browser,
		Client:                   &http.Client{},
	})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := mcp.NewClient(&mcp.Implementation{Name: "sdk"}, nil).Connect(context.Background(),
		&mcp.StreamableClientTransport{Endpoint: e.hs.URL + "/mcp", OAuthHandler: handler}, nil)
	if err != nil {
		t.Fatalf("the SDK's OAuth client could not connect: %v", err)
	}
	defer sess.Close()
	out, isErr := call(t, sess, "demo", map[string]any{"file": "viaoauth"})
	if isErr || !strings.Contains(out, "ARG=viaoauth") {
		t.Errorf("tool call after OAuth: %v %q", isErr, out)
	}
}
