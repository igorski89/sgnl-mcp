package opencloud

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Editing a file the person owns: the only way the client overwrites
// anything. Overwrite needs the etag the content was read at, checks it
// again just before writing, and sends it as If-Match (OpenCloud refuses
// the PUT with 412 when the file changed in between).

// davFile: a file path the edit calls accept: a DAV path, no "..", and no
// "?" or "#" (which would make the request reach a different file than
// the one named).
func davFile(path string) bool {
	return strings.HasPrefix(path, davPrefix) && !strings.HasPrefix(path, trashPrefix) && !strings.Contains(path, "..") && !strings.ContainsAny(path, "?#") && !strings.HasSuffix(path, "/")
}

// ErrChanged: the file is no longer the version that was read.
var ErrChanged = errors.New("the file changed since it was read")

// ReadForEdit: the whole file and its etag. A file larger than max is
// refused rather than shortened, so an edit never drops the rest.
func (c *Client) ReadForEdit(ctx context.Context, token, path string, max int64) (data []byte, etag string, err error) {
	if !davFile(path) {
		return nil, "", fmt.Errorf("path must be a file from folder_list or file_search")
	}
	resp, err := c.do(ctx, token, "GET", path, nil, nil)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, "", fmt.Errorf("files: read %d %.200s", resp.StatusCode, b)
	}
	etag = resp.Header.Get("ETag")
	if etag == "" {
		return nil, "", fmt.Errorf("files: no version tag for this file; not editing it")
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) > max {
		return nil, "", fmt.Errorf("the file is larger than %d MB: too large to edit here", max>>20)
	}
	return data, etag, nil
}

// Etag: the file's current etag (PROPFIND, depth 0).
func (c *Client) Etag(ctx context.Context, token, path string) (string, error) {
	if !davFile(path) {
		return "", fmt.Errorf("path must be a file from folder_list or file_search")
	}
	body := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop><d:getetag/></d:prop></d:propfind>`
	resp, err := c.do(ctx, token, "PROPFIND", path, strings.NewReader(body), map[string]string{"Depth": "0", "Content-Type": "application/xml"})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 207 {
		return "", fmt.Errorf("files: %d %.200s", resp.StatusCode, b)
	}
	var ms struct {
		Responses []struct {
			Propstat []struct {
				Prop struct {
					Etag string `xml:"getetag"`
				} `xml:"prop"`
			} `xml:"propstat"`
		} `xml:"response"`
	}
	if err := xml.Unmarshal(b, &ms); err != nil {
		return "", err
	}
	for _, r := range ms.Responses {
		for _, ps := range r.Propstat {
			if ps.Prop.Etag != "" {
				return ps.Prop.Etag, nil
			}
		}
	}
	return "", fmt.Errorf("files: no version tag for %s", path)
}

// Overwrite: replace the content of the file at path, only if it is still
// at etag. Returns the new etag.
func (c *Client) Overwrite(ctx context.Context, token, path, etag, ctype string, data []byte) (string, error) {
	if !davFile(path) {
		return "", fmt.Errorf("path must be a file from folder_list or file_search")
	}
	if etag == "" {
		return "", fmt.Errorf("opencloud: overwrite without the version it was read at")
	}
	now, err := c.Etag(ctx, token, path)
	if err != nil {
		return "", err
	}
	if !sameEtag(now, etag) {
		return "", ErrChanged
	}
	resp, err := c.send(ctx, token, "PUT", path, bytes.NewReader(data), map[string]string{"If-Match": etag, "Content-Type": ctype})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 200, 201, 204:
	case 412:
		return "", ErrChanged
	case 423:
		return "", fmt.Errorf("the file is locked (open for editing somewhere?): nothing was changed")
	default:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return "", fmt.Errorf("files: save %d %.200s", resp.StatusCode, b)
	}
	// Don't claim a change the server didn't make.
	after, err := c.Etag(ctx, token, path)
	if err != nil {
		return "", fmt.Errorf("files: saved, but can't confirm the new version: %w", err)
	}
	if sameEtag(after, etag) {
		return "", fmt.Errorf("files: the server answered %d but the file's version didn't change", resp.StatusCode)
	}
	return after, nil
}

// sameEtag: GET's ETag header and PROPFIND's getetag name the same version
// (reva quotes the latter itself; compare without quotes or W/).
func sameEtag(a, b string) bool {
	n := func(s string) string { return strings.Trim(strings.TrimPrefix(strings.TrimSpace(s), "W/"), `"`) }
	return n(a) == n(b)
}
