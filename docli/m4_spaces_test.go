package main

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The one published AWS Signature Version 4 example that does not need S3: the IAM ListUsers request from AWS's
// signing documentation. docli's signing function must give AWS's signature for it.
func TestSigV4MatchesAWSPublishedExample(t *testing.T) {
	headers := map[string]string{
		"content-type": "application/x-www-form-urlencoded; charset=utf-8",
		"host":         "iam.amazonaws.com",
		"x-amz-date":   "20150830T123600Z",
	}
	q := url.Values{"Action": {"ListUsers"}, "Version": {"2010-05-08"}}
	sig, signed := sigV4("wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "GET", "/", q, headers, emptySHA256, "20150830T123600Z", "us-east-1", "iam")
	if signed != "content-type;host;x-amz-date" {
		t.Fatal(signed)
	}
	if want := "5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7"; sig != want {
		t.Fatalf("signature %s, AWS says %s", sig, want)
	}
}

func TestSpacesBucketLifecycle(t *testing.T) {
	e := newEnvM4(t)
	e.mustRun("spaces", "bucket", "create", "--name", "assets", "--dry-run")
	contain(t, e.out.String(), `create the private bucket "docli-assets" in nyc3`)
	contain(t, e.out.String(), "S3-compatible API")
	contain(t, e.out.String(), "monthly cost: not shown")
	if len(e.fake.S3Mutations()) != 0 {
		t.Fatal("dry run sent a change")
	}
	e.expect(exitRefused, "spaces", "bucket", "create", "--name", "assets")
	e.expect(exitUsage, "spaces", "bucket", "create", "--name", "Bad_Name", "--yes")
	e.env["DOCLI_REGION"] = ""
	e.expect(exitUsage, "spaces", "bucket", "list")
	e.env["DOCLI_REGION"] = "nyc3"
	e.mustRun("spaces", "bucket", "create", "--name", "assets", "--yes", "--json")
	var b bucketJSON
	jsonOut(t, e, &b)
	if b.Name != "docli-assets" || b.Region != "nyc3" || !b.Managed {
		t.Fatalf("%+v", b)
	}
	// every S3 call carried a valid signature (the fake rejects the rest)
	for _, l := range e.fake.S3Requests() {
		if !strings.HasSuffix(l, "region=nyc3") {
			t.Fatalf("a bucket request was not signed for nyc3: %s", l)
		}
	}
	contain(t, e.readManifest(), "spaces_buckets:")
	contain(t, e.readManifest(), "id: nyc3/docli-assets")
	e.expect(exitError, "spaces", "bucket", "create", "--name", "assets", "--yes")
	contain(t, e.errb.String(), "already exists")
	e.mustRun("spaces", "bucket", "list")
	contain(t, e.out.String(), "docli-assets")
	contain(t, e.out.String(), "legacy-assets")
	e.mustRun("spaces", "bucket", "list", "--json")
	var list []bucketJSON
	jsonOut(t, e, &list)
	managed := 0
	for _, x := range list {
		if x.Managed {
			managed++
		}
	}
	if len(list) != 2 || managed != 1 {
		t.Fatalf("%+v", list)
	}
	e.expect(exitRefused, "spaces", "bucket", "delete", "assets", "--yes") // typed name needed
	e.mustRun("spaces", "bucket", "delete", "assets", "--yes", "--confirm-name", "docli-assets")
	if len(e.fake.S3Mutations()) != 2 { // the create and the delete
		t.Fatal(e.fake.S3Requests())
	}
	notContain(t, e.readManifest(), "docli-assets")
}

func TestSpacesBucketDeleteRefusals(t *testing.T) {
	e := newEnvM4(t)
	e.mustRun("spaces", "bucket", "create", "--name", "full", "--yes")
	e.fake.m4.Buckets["docli-full"].Objects = 2
	e.expect(exitRefused, "spaces", "bucket", "delete", "full", "--yes", "--confirm-name", "docli-full")
	contain(t, e.errb.String(), "not empty")
	// a bucket docli did not create is refused, empty or not
	e.expect(exitRefused, "spaces", "bucket", "delete", "legacy-assets", "--yes", "--confirm-name", "legacy-assets")
	contain(t, e.errb.String(), "did not create it")
	e.expect(exitRefused, "spaces", "bucket", "delete", "legacy-assets", "--yes", "--force-unmanaged", "--confirm-name", "legacy-assets")
	contain(t, e.errb.String(), "not empty") // forcing past ownership never forces past "not empty"
	e.expect(exitError, "spaces", "bucket", "delete", "nonesuch", "--yes", "--confirm-name", "nonesuch")
	for _, l := range e.fake.S3Requests() {
		if strings.HasPrefix(l, "DELETE") {
			t.Fatal("a DELETE was sent for a refused bucket:", l)
		}
	}
	if len(e.fake.m4.Buckets) != 2 {
		t.Fatal("a bucket went missing")
	}
	e.fake.m4.Buckets["docli-full"].Objects = 0
	e.mustRun("spaces", "bucket", "delete", "docli-full", "--yes", "--confirm-name", "docli-full")
}

func TestSpacesBucketNeedsCredentialsAndGoodSignature(t *testing.T) {
	e := newEnvM4(t)
	delete(e.env, "DOCLI_SPACES_ACCESS_KEY")
	delete(e.env, "DOCLI_SPACES_SECRET_KEY")
	e.expect(exitError, "spaces", "bucket", "list")
	contain(t, e.errb.String(), "no Spaces access key found")
	contain(t, e.errb.String(), "never taken from a flag")
	e.env["DOCLI_SPACES_ACCESS_KEY"] = fakeSpacesAccess // only half
	e.expect(exitError, "spaces", "bucket", "list")
	contain(t, e.errb.String(), "must both be set")
	// the wrong secret: the signature does not match, the server says so, and the secret stays out of the message
	e.env["DOCLI_SPACES_SECRET_KEY"] = "a-different-secret-0123456789abcdefghij"
	e.expect(exitError, "spaces", "bucket", "list")
	contain(t, e.errb.String(), "403 SignatureDoesNotMatch")
	contain(t, e.errb.String(), "full access")
	notContain(t, e.errb.String(), "a-different-secret")
	if regexp.MustCompile(`Signature=[0-9a-f]{16}`).MatchString(e.errb.String()) {
		t.Fatalf("a request signature reached the error text:\n%s", e.errb.String())
	}
	// the file form: refused when others can read it
	delete(e.env, "DOCLI_SPACES_ACCESS_KEY")
	delete(e.env, "DOCLI_SPACES_SECRET_KEY")
	dir := filepath.Join(e.home, ".config", "docli")
	os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, "spaces")
	os.WriteFile(path, []byte("access_key="+fakeSpacesAccess+"\nsecret_key="+fakeSpacesSecret+"\n"), 0o644)
	e.expect(exitError, "spaces", "bucket", "list")
	contain(t, e.errb.String(), "can be read by other users")
	notContain(t, e.errb.String(), fakeSpacesSecret)
	os.Chmod(path, 0o600)
	e.mustRun("spaces", "bucket", "list")
	contain(t, e.out.String(), "legacy-assets")
	// the shared SPACES_* names work too
	os.Remove(path)
	e.env["SPACES_ACCESS_KEY_ID"], e.env["SPACES_SECRET_ACCESS_KEY"] = fakeSpacesAccess, fakeSpacesSecret
	e.mustRun("spaces", "bucket", "list")
}

func TestSpacesBucketCreateIsNeverRetried(t *testing.T) {
	e := newEnvM4(t)
	e.fake.m4.S3Fails["PUT /docli-x"] = 500
	e.expect(exitError, "spaces", "bucket", "create", "--name", "x", "--yes")
	contain(t, e.errb.String(), "500 InternalError")
	notContain(t, e.errb.String(), fakeSpacesSecret)
	n := 0
	for _, l := range e.fake.S3Requests() {
		if strings.HasPrefix(l, "PUT") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("a create was sent %d times", n)
	}
	// reads are retried
	e.fake.m4.S3Fails["GET /"] = 500
	e.mustRun("spaces", "bucket", "list")
}

// ---------- keys ----------

func TestSpacesKeyCreateWritesSecretToFileOnly(t *testing.T) {
	e := newEnvM4(t)
	dir := t.TempDir()
	sf := filepath.Join(dir, "key.secret")
	base := []string{"spaces", "key", "create", "--name", "ci", "--grant", "legacy-assets:read", "--secret-file", sf}
	e.mustRun(append(append([]string{}, base...), "--dry-run")...)
	contain(t, e.out.String(), `create the Spaces key "docli-ci"`)
	contain(t, e.out.String(), "legacy-assets: read")
	contain(t, e.out.String(), "never printed")
	if len(e.fake.Mutations()) != 0 {
		t.Fatal("dry run sent a change")
	}
	if _, err := os.Stat(sf); err == nil {
		t.Fatal("a dry run wrote the file")
	}
	e.expect(exitRefused, base...)
	e.mustRun(append(append([]string{}, base...), "--yes", "--json")...)
	var k spacesKeyJSON
	jsonOut(t, e, &k)
	if k.Name != "docli-ci" || !k.Managed || k.SecretFile != sf || len(k.Grants) != 1 || k.Grants[0].Bucket != "legacy-assets" || !strings.HasPrefix(k.AccessKeyID, "DO00NEWKEY") {
		t.Fatalf("%+v", k)
	}
	got, err := os.ReadFile(sf)
	if err != nil || string(got) != fakeNewKeySecret {
		t.Fatalf("the secret file holds %q (%v)", got, err)
	}
	if fi, _ := os.Stat(sf); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", fi.Mode().Perm())
	}
	notContain(t, e.out.String()+e.errb.String(), fakeNewKeySecret)
	m := e.readManifest()
	contain(t, m, "spaces_keys:")
	contain(t, m, "legacy-assets:read")
	notContain(t, m, fakeNewKeySecret)
	// a second create would overwrite: refused before anything is created
	n := len(e.fake.m4.SpacesKeys)
	e.expect(exitUsage, append(append([]string{}, base...), "--name", "ci2", "--yes")...)
	contain(t, e.errb.String(), "already exists")
	if len(e.fake.m4.SpacesKeys) != n {
		t.Fatal("a key was created although its secret file already exists")
	}
	// --force replaces it
	e.mustRun(append(append([]string{}, base...), "--name", "ci2", "--yes", "--force")...)
	got, _ = os.ReadFile(sf)
	if string(got) != fakeNewKeySecret {
		t.Fatal("not rewritten")
	}
}

func TestSpacesKeyCreateValidation(t *testing.T) {
	e := newEnvM4(t)
	sf := filepath.Join(t.TempDir(), "s")
	e.expect(exitUsage, "spaces", "key", "create", "--name", "ci", "--grant", "b:read", "--yes") // no --secret-file
	contain(t, e.errb.String(), "--secret-file PATH is required")
	e.expect(exitUsage, "spaces", "key", "create", "--name", "ci", "--secret-file", sf, "--yes") // no scope
	contain(t, e.errb.String(), "say what the key may reach")
	e.expect(exitUsage, "spaces", "key", "create", "--name", "ci", "--secret-file", sf, "--grant", "b:read", "--full-access", "--yes")
	e.expect(exitUsage, "spaces", "key", "create", "--name", "ci", "--secret-file", sf, "--grant", "b:everything", "--yes")
	e.expect(exitUsage, "spaces", "key", "create", "--name", "ci", "--secret-file", sf, "--grant", "bread", "--yes")
	e.expect(exitUsage, "spaces", "key", "create", "--name", "c i", "--secret-file", sf, "--grant", "b:read", "--yes")
	e.expect(exitUsage, "spaces", "key", "create", "--name", "ci", "--secret-file", "/no/such/dir/s", "--grant", "b:read", "--yes")
	if len(e.fake.Mutations()) != 0 {
		t.Fatal(e.fake.Mutations())
	}
	e.mustRun("spaces", "key", "create", "--name", "all", "--secret-file", sf, "--full-access", "--dry-run")
	contain(t, e.out.String(), "every bucket in the account")
	contain(t, e.out.String(), "Warning: a full-access key")
	e.mustRun("spaces", "key", "create", "--name", "all", "--secret-file", sf, "--full-access", "--yes")
	contain(t, e.fake.Bodies["POST /v2/spaces/keys"][0], `"permission":"fullaccess"`)
}

func TestSpacesKeyListAndDelete(t *testing.T) {
	e := newEnvM4(t)
	e.mustRun("spaces", "key", "list")
	contain(t, e.out.String(), "legacy-key")
	contain(t, e.out.String(), "DO00LEGACYKEY000000")
	contain(t, e.out.String(), "legacy-assets:read")
	e.expect(exitRefused, "spaces", "key", "delete", "legacy-key", "--yes", "--confirm-name", "legacy-key")
	contain(t, e.errb.String(), "did not create it")
	sf := filepath.Join(t.TempDir(), "s")
	e.mustRun("spaces", "key", "create", "--name", "ci", "--grant", "legacy-assets:read", "--secret-file", sf, "--yes")
	e.expect(exitRefused, "spaces", "key", "delete", "ci", "--yes") // typed name needed
	e.mustRun("spaces", "key", "delete", "ci", "--yes", "--confirm-name", "docli-ci")
	if len(e.fake.Requests("DELETE /v2/spaces/keys/")) != 1 {
		t.Fatal(e.fake.Log)
	}
	notContain(t, e.readManifest(), "docli-ci")
}

func TestSpacesKeysAPIConflictIsExplained(t *testing.T) {
	e := newEnvM4(t)
	e.fake.m4.KeysNotFound = true
	e.expect(exitError, "spaces", "key", "list")
	contain(t, e.errb.String(), "documentation disagrees")
	contain(t, e.errb.String(), "control panel")
}
