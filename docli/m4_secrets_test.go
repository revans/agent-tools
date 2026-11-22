package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A Spaces secret key, like the API token, must never reach stdout, stderr, --json, --debug, the manifest or an
// error, whether the server returns it in an answer, echoes it in a failure, or it was read from the
// environment. The only place a created key's secret may go is the --secret-file. The token must stay out of the
// output of every new command too.

var m4SecretMarkers = []string{fakeSpacesSecret, fakeNewKeySecret, fakeToken, "FAKETOKEN", "fAkE+SpAcEs", "nEwKeY+SeCrEt", "SeCrEt0123456789"}

func assertNoM4Secrets(t *testing.T, where string, args []string, e *testEnv) {
	t.Helper()
	all := e.out.String() + "\n" + e.errb.String()
	for _, m := range m4SecretMarkers {
		if strings.Contains(all, m) {
			t.Errorf("[%s] docli %v leaked %q:\n%s", where, args, m, all)
		}
	}
	if m, err := os.ReadFile(e.manifest()); err == nil {
		for _, s := range m4SecretMarkers {
			if strings.Contains(string(m), s) {
				t.Errorf("[%s] docli %v wrote %q to the manifest", where, args, s)
			}
		}
	}
}

func TestSpacesSecretsAndTokenNeverPrinted(t *testing.T) {
	sfDir := t.TempDir()
	n := 0
	sf := func() string { n++; return filepath.Join(sfDir, "secret"+itoa(n)) }
	cmds := func() [][]string {
		return [][]string{
			{"spaces", "bucket", "list"}, {"spaces", "bucket", "list", "--json", "--debug"},
			{"spaces", "bucket", "create", "--name", "b1", "--yes"}, {"spaces", "bucket", "create", "--name", "b2", "--yes", "--json", "--debug"},
			{"spaces", "bucket", "delete", "legacy-assets", "--force-unmanaged", "--yes", "--confirm-name", "legacy-assets", "--debug"},
			{"spaces", "key", "list"}, {"spaces", "key", "list", "--json", "--debug"},
			{"spaces", "key", "create", "--name", "k1", "--grant", "legacy-assets:read", "--secret-file", sf(), "--yes"},
			{"spaces", "key", "create", "--name", "k2", "--grant", "legacy-assets:read", "--secret-file", sf(), "--yes", "--json", "--debug"},
			{"spaces", "key", "create", "--name", "k3", "--full-access", "--secret-file", sf(), "--yes", "--debug"},
			{"spaces", "key", "delete", "legacy-key", "--force-unmanaged", "--yes", "--confirm-name", "legacy-key", "--json"},
			{"manifest", "verify", "--debug"}, {"manifest", "refresh", "--yes", "--debug"}, {"manifest", "show"},
			{"project", "list", "--json", "--debug"}, {"volume", "list", "--debug"}, {"lb", "list", "--json", "--debug"}, {"reserved-ip", "list", "--debug"},
			{"volume", "create", "--name", "v1", "--size", "5", "--yes", "--debug", "--json"},
			{"lb", "create", "--name", "w", "--forward", "http:80=http:80", "--tag", "t", "--yes", "--debug", "--json"},
			{"reserved-ip", "create", "--yes", "--debug", "--json"},
			{"project", "create", "--name", "p", "--yes", "--debug", "--json"},
		}
	}
	echo := " secret_key=" + fakeNewKeySecret + " " + fakeSpacesSecret + " token " + fakeToken
	modes := map[string]func(e *testEnv){
		"good":                       func(e *testEnv) {},
		"list answers carry secrets": func(e *testEnv) { e.fake.m4.ListReturnsSecret = true },
		"server fails and echoes": func(e *testEnv) {
			for _, k := range []string{"GET /v2/spaces/keys", "POST /v2/spaces/keys", "DELETE /v2/spaces/keys/DO00LEGACYKEY000000", "POST /v2/volumes", "POST /v2/load_balancers", "POST /v2/reserved_ips", "POST /v2/projects", "GET /v2/projects", "GET /v2/volumes", "GET /v2/load_balancers", "GET /v2/reserved_ips"} {
				e.fake.Fails[k] = []fakeFail{{Status: 500, Message: "boom" + echo}, {Status: 500, Message: "boom" + echo}, {Status: 500, Message: "boom" + echo}, {Status: 500, Message: "boom" + echo}}
			}
		},
		"S3 fails and echoes": func(e *testEnv) {
			for _, k := range []string{"GET /", "PUT /docli-b1", "PUT /docli-b2", "DELETE /legacy-assets"} {
				e.fake.m4.S3Fails[k] = 500
			}
		},
		"S3 rejects the signature": func(e *testEnv) { e.env["DOCLI_SPACES_SECRET_KEY"] = "wrong-" + fakeSpacesSecret },
		"server rejects the token": func(e *testEnv) { e.fake.token = "other" },
		"keys endpoint missing":    func(e *testEnv) { e.fake.m4.KeysNotFound = true },
	}
	for mode, setup := range modes {
		for _, args := range cmds() {
			e := newEnvM4(t)
			setup(e)
			e.tty(false)
			e.run(args...)
			assertNoM4Secrets(t, mode, args, e)
			notContain(t, e.out.String()+e.errb.String(), "wrong-")
		}
	}
}

// Whatever the server does, the secret of a key that was created lands in the file and nowhere else, and an
// access key id (an identifier, not a secret) is what is shown.
func TestSpacesKeySecretFileIsTheOnlyPlace(t *testing.T) {
	e := newEnvM4(t)
	sf := filepath.Join(t.TempDir(), "secret")
	e.mustRun("spaces", "key", "create", "--name", "k", "--grant", "legacy-assets:readwrite", "--secret-file", sf, "--yes", "--debug", "--json")
	assertNoM4Secrets(t, "create", nil, e)
	contain(t, e.out.String(), "access_key_id")
	b, _ := os.ReadFile(sf)
	if string(b) != fakeNewKeySecret {
		t.Fatal("the secret was not saved")
	}
	// the key is created, then the manifest update fails and the server echoes the secret it just handed out: the warning
	// must not carry it, because docli registered it the moment the create answered
	echo := fakeFail{Status: 500, Message: "snapshot failed, secret_key is " + fakeNewKeySecret + " ok"}
	e.fake.Fails["GET /v2/droplets"] = []fakeFail{echo, echo, echo, echo}
	e.mustRun("spaces", "key", "create", "--name", "k5", "--grant", "legacy-assets:read", "--secret-file", filepath.Join(t.TempDir(), "s5"), "--yes", "--debug")
	contain(t, e.errb.String(), "the manifest was not updated")
	assertNoM4Secrets(t, "manifest update fails and echoes", nil, e)
	// a file that cannot be written: the key exists, the error says so and how to recover, and no secret is in it
	e.fake.m4.SpacesKeys = e.fake.m4.SpacesKeys[:1]
	ro := t.TempDir()
	os.Chmod(ro, 0o500)
	defer os.Chmod(ro, 0o700)
	if os.Getuid() != 0 { // root can write anywhere
		e.expect(exitError, "spaces", "key", "create", "--name", "k9", "--grant", "legacy-assets:read", "--secret-file", filepath.Join(ro, "s"), "--yes")
		contain(t, e.errb.String(), "its secret was not saved")
		contain(t, e.errb.String(), "docli spaces key delete docli-k9")
		assertNoM4Secrets(t, "unwritable file", nil, e)
	}
}

// The JSON the new commands print has no field that could hold a secret.
func TestM4JSONShapesHaveNoSecretFields(t *testing.T) {
	var walk func(v any, path string)
	walk = func(v any, path string) {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				switch k {
				case "password", "uri", "secret", "secret_key", "access_key", "token":
					t.Errorf("%s has a field named %q", path, k)
				}
				walk(val, path+"."+k)
			}
		case []any:
			for _, i := range x {
				walk(i, path+"[]")
			}
		}
	}
	for _, v := range []any{spacesKeyJSON{Grants: []grantJSON{{}}}, bucketJSON{}, projectJSON{}, projectDetailJSON{Resources: []projectResourceJSON{{}}}, volumeJSON{}, reservedIPJSON{}, lbJSON{ForwardingRules: []lbRuleJSON{{}}, HealthCheck: &lbHealthJSON{}}} {
		b, _ := json.Marshal(v)
		var generic any
		json.Unmarshal(b, &generic)
		walk(generic, string(b)[:20])
	}
}

// The scrubbers: a secret registered when it was read is removed wherever it appears, and the shapes a server
// might echo from a signed request are removed even for a secret docli never saw.
func TestScrubSpacesShapes(t *testing.T) {
	a := &App{}
	a.rememberSecret("SeenSecretValue123456")
	for _, c := range []struct{ in, gone string }{
		{"bad SeenSecretValue123456 here", "SeenSecretValue123456"},
		{"Authorization: AWS4-HMAC-SHA256 Credential=A/20260101/nyc3/s3/aws4_request, SignedHeaders=host, Signature=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "0123456789abcdef0123456789abcdef"},
		{`{"secret_key":"NeverSeen0123456789abcdefABCDEF"}`, "NeverSeen0123456789abcdefABCDEF"},
		{"aws_secret_access_key = NeverSeen0123456789abcdefABCDEF", "NeverSeen0123456789abcdefABCDEF"},
		{"SECRET_KEY: NeverSeen0123456789abcdefABCDEF", "NeverSeen0123456789abcdefABCDEF"},
	} {
		if got := a.scrubSecrets(c.in); strings.Contains(got, c.gone) {
			t.Errorf("%q still has %q: %q", c.in, c.gone, got)
		}
	}
	if got := a.scrubSecrets("access key DO00ABCDEFGHIJKLMNOP is an identifier"); !strings.Contains(got, "DO00ABCDEFGHIJKLMNOP") {
		t.Errorf("an access key id is not a secret and is kept: %q", got)
	}
}
