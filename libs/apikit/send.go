package apikit

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/textproto"
	"sort"
	"strings"
	"time"
)

// SendOptions describes a hand-built request.
type SendOptions struct {
	Name    string        // shown in errors and the debug log
	Safety  string        // read, write or destructive (default write); --max-safety applies
	Timeout time.Duration // default 30s
}

// Send runs a request that an endpoint spec cannot describe: a binary upload, a
// multipart body, a step of a multi-call flow. It gets everything a spec'd call
// gets: credentials, --dry-run (with secrets hidden), --max-safety, the debug
// log, error mapping, and a retry only for a GET.
func (c *Client) Send(ctx context.Context, req *Request, o SendOptions) (*Result, error) {
	if o.Safety == "" {
		o.Safety = SafetyWrite
	}
	if SafetyRank(o.Safety) < 0 {
		return nil, fmt.Errorf("send %s: unknown safety level %q", o.Name, o.Safety)
	}
	limit := c.MaxSafety
	if limit == "" {
		limit = SafetyWrite
	}
	if SafetyRank(o.Safety) > SafetyRank(limit) {
		return nil, fmt.Errorf("%s is %s, above the allowed level %s (see --max-safety)", o.Name, o.Safety, limit)
	}
	if o.Timeout == 0 {
		o.Timeout = 30 * time.Second
	}
	if c.DryRun {
		return &Result{Data: c.describe(req)}, nil
	}
	ep := &Endpoint{Name: o.Name, Method: req.Method, Safety: o.Safety, timeout: o.Timeout}
	return c.sendWithRetry(ctx, ep, req)
}

// FilePart is one file in a multipart body.
type FilePart struct {
	Field       string
	Filename    string
	ContentType string
	Data        []byte
}

// Multipart builds a multipart/form-data body. Fields are written in sorted
// order, then the files in the order given, so the output is stable.
func Multipart(fields map[string]string, files []FilePart) (body []byte, contentType string, err error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := w.WriteField(k, fields[k]); err != nil {
			return nil, "", err
		}
	}
	for _, f := range files {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, escapeQuotes(f.Field), escapeQuotes(f.Filename)))
		ct := f.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		h.Set("Content-Type", ct)
		part, err := w.CreatePart(h)
		if err != nil {
			return nil, "", err
		}
		if _, err := part.Write(f.Data); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

func escapeQuotes(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\r", "", "\n", "").Replace(s)
}
