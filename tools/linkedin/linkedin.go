package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/revans/agent-tools/apikit"
)

// These limits are the same ones Mimir applies.
const (
	// An engineer-chosen ceiling in Mimir: LinkedIn documents no number, and its
	// own 422 is the backstop.
	commentaryMaxChars = 3000
	imageMaxBytes      = 8 * 1024 * 1024
)

// littleReserved are the characters of LinkedIn's "little text" markup. Sent
// unescaped, a "#" or "(" in a post would be read as markup.
const littleReserved = `\|{}@[]()<>#*_~`

// escapeLittleText backslash-escapes every reserved character. The length limit
// is checked on the raw text, never on this longer form.
func escapeLittleText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(littleReserved, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func validateCommentary(s string) error {
	if strings.TrimSpace(s) == "" {
		return apikit.Usage("--text is required and cannot be blank")
	}
	if n := utf8.RuneCountInString(s); n > commentaryMaxChars {
		return fmt.Errorf("the post text is %d characters, over the %d-character ceiling", n, commentaryMaxChars)
	}
	return nil
}

// imageTypes are the formats LinkedIn's Images API takes. WebP is refused.
var imageTypes = map[string]bool{".jpg": true, ".jpeg": true, ".gif": true, ".png": true}

// validateImage checks the path, type and size, and returns the size. It reads
// no content.
func validateImage(path string) (int64, error) {
	if !imageTypes[strings.ToLower(filepath.Ext(path))] {
		return 0, fmt.Errorf("%s is not a JPEG, GIF, or PNG image: LinkedIn's Images API only accepts those formats", path)
	}
	st, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("image file not found: %s", path)
	}
	if !st.Mode().IsRegular() {
		return 0, fmt.Errorf("%s is not a regular file", path)
	}
	if st.Size() > imageMaxBytes {
		return 0, fmt.Errorf("%s is %d bytes, over the %d-byte image size ceiling", path, st.Size(), imageMaxBytes)
	}
	return st.Size(), nil
}

var personIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// normalizeAuthor accepts "", a full URN, or a bare member id.
func normalizeAuthor(s string) (string, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return "", nil
	case strings.HasPrefix(s, "urn:li:"):
		return s, nil
	case personIDRe.MatchString(s):
		return "urn:li:person:" + s, nil
	}
	return "", fmt.Errorf("author %q is not a urn:li: URN or a member id", s)
}

// wrapAPIError adds what a person should do about the failures that have a cure.
func wrapAPIError(err error) error {
	var he *apikit.HTTPError
	if !errors.As(err, &he) {
		return err
	}
	switch he.Status {
	case http.StatusUnauthorized:
		return fmt.Errorf("%w. The access token is expired or revoked (LinkedIn tokens last about 60 days and there is no refresh): paste a new one into the config", err)
	case http.StatusForbidden:
		return fmt.Errorf("%w. The token may lack a scope (posting needs w_member_social; finding the author needs openid and profile, or set `author` in the config)", err)
	}
	return err
}

func (t *tool) postCommand() *apikit.Command {
	var text, image string
	return &apikit.Command{
		Name:    "post",
		Summary: "publish a post, with an optional image, PUBLICLY to LinkedIn",
		Usage:   `post --text TEXT [--image PATH]`,
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&text, "text", "", "the post text, at most 3000 characters (required)")
			fs.StringVar(&image, "image", "", "a JPEG, GIF or PNG file to attach (at most 8 MB)")
		},
		Run: func(r *apikit.Run) (any, error) {
			if err := validateCommentary(text); err != nil {
				return nil, err
			}
			var size int64
			if image != "" {
				var err error
				if size, err = validateImage(image); err != nil {
					return nil, err
				}
			}
			out, err := t.publish(r, text, image, size)
			return out, wrapAPIError(err)
		},
	}
}

func (t *tool) publish(r *apikit.Run, text, image string, size int64) (any, error) {
	dry := r.DryRun()
	var steps []any
	note := func(res *apikit.Result) {
		if dry {
			steps = append(steps, res.Data)
		}
	}

	author := t.author
	if author == "" {
		if _, err := r.Client(); err != nil { // loads the config, sets t.author
			return nil, err
		}
		author = t.author
	}
	if author == "" {
		res, err := r.Call("userinfo", nil)
		if err != nil {
			return nil, fmt.Errorf("finding the author: %w", err)
		}
		note(res)
		if dry {
			author = "urn:li:person:<sub from userinfo>"
		} else {
			m, _ := res.Data.(map[string]any)
			sub, _ := m["sub"].(string)
			if sub == "" {
				return nil, errors.New("LinkedIn's userinfo answer had no sub: set `author` in the config")
			}
			author = "urn:li:person:" + sub
		}
	}

	if image != "" {
		initBody, _ := json.Marshal(map[string]string{"owner": author})
		res, err := r.Call("image_upload_init", map[string]string{"request": string(initBody)})
		if err != nil {
			return nil, err
		}
		note(res)
		uploadURL, imageURN := "<uploadUrl from initializeUpload>", "urn:li:image:<from initializeUpload>"
		if dry {
			steps = append(steps, map[string]any{"dry_run": true, "method": "PUT", "url": uploadURL,
				"headers": map[string]string{"Authorization": "[redacted]"}, "body": fmt.Sprintf("<%d bytes of %s>", size, image)})
		} else {
			m, _ := res.Data.(map[string]any)
			uploadURL, _ = m["uploadUrl"].(string)
			imageURN, _ = m["image"].(string)
			if uploadURL == "" || imageURN == "" {
				return nil, errors.New("LinkedIn's upload-init answer had no uploadUrl or image")
			}
			data, err := os.ReadFile(image)
			if err != nil {
				return nil, fmt.Errorf("reading %s: %w", image, err)
			}
			c, err := r.Client()
			if err != nil {
				return nil, err
			}
			// Mimir sends the bearer token to the upload URL too, so this does.
			if _, err := c.Send(r.Ctx, &apikit.Request{Method: "PUT", URL: uploadURL, Body: data},
				apikit.SendOptions{Name: "image_upload", Safety: apikit.SafetyWrite}); err != nil {
				return nil, fmt.Errorf("uploading the image (nothing was posted): %w", err)
			}
		}
		return t.create(r, author, text, imageURN, steps, dry)
	}
	return t.create(r, author, text, "", steps, dry)
}

func (t *tool) create(r *apikit.Run, author, text, imageURN string, steps []any, dry bool) (any, error) {
	params := map[string]string{"author": author, "commentary": escapeLittleText(text)}
	if imageURN != "" {
		b, _ := json.Marshal(map[string]any{"media": map[string]string{"id": imageURN}})
		params["content"] = string(b)
	}
	res, err := r.Call("post_create", params)
	if err != nil {
		return nil, err
	}
	if dry {
		return map[string]any{"dry_run": true, "steps": append(steps, res.Data)}, nil
	}
	id := res.Header.Get("X-Restli-Id")
	if id == "" {
		return nil, errors.New("LinkedIn's post-creation response did not include an x-restli-id header, so the post's id is unknown (the post may still have been created)")
	}
	return map[string]any{"id": id, "author": author, "image_attached": imageURN != ""}, nil
}
