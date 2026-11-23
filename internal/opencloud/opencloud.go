// Package opencloud: a minimal, read-only OpenCloud (WebDAV) client:
// search (REPORT oc:search-files) and download (GET). No other method can
// be sent.
package opencloud

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

var readOnly = []string{"GET", "PROPFIND", "REPORT"}

type Client struct {
	Base string // e.g. https://drive.example.com
	HTTP *http.Client
}

func New(base string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// davPrefix: the only paths file_read accepts (what search returns).
const davPrefix = "/remote.php/dav/spaces/"

func (c *Client) do(ctx context.Context, token, method, path string, body io.Reader, hdr map[string]string) (*http.Response, error) {
	if !slices.Contains(readOnly, method) {
		return nil, fmt.Errorf("opencloud: %s is not allowed (read-only)", method)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return c.HTTP.Do(req)
}

type File struct {
	Path     string `json:"path" jsonschema:"pass to file_read"`
	Name     string `json:"name"`
	Type     string `json:"type,omitempty"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
	Folder   bool   `json:"folder,omitempty"`
}

type multistatus struct {
	Responses []struct {
		Href     string `xml:"href"`
		Propstat []struct {
			Status string `xml:"status"`
			Prop   struct {
				Name         string `xml:"name"`
				ContentType  string `xml:"getcontenttype"`
				Length       string `xml:"getcontentlength"`
				Size         string `xml:"size"`
				LastModified string `xml:"getlastmodified"`
				ResourceType struct {
					Collection *struct{} `xml:"collection"`
				} `xml:"resourcetype"`
			} `xml:"prop"`
		} `xml:"propstat"`
	} `xml:"response"`
}

// Search: files whose name (or indexed content) matches query, across
// every space the person can see.
func (c *Client) Search(ctx context.Context, token, query string, limit int) ([]File, error) {
	q := strings.ReplaceAll(query, `"`, ``)
	body := `<?xml version="1.0"?>
<oc:search-files xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
  <d:prop><oc:name/><d:getcontenttype/><d:getcontentlength/><oc:size/><d:getlastmodified/><d:resourcetype/></d:prop>
  <oc:search><oc:pattern>` + xmlEscape(`name:"*`+q+`*"`) + `</oc:pattern><oc:limit>` + strconv.Itoa(limit) + `</oc:limit></oc:search>
</oc:search-files>`
	resp, err := c.do(ctx, token, "REPORT", davPrefix, strings.NewReader(body), map[string]string{"Content-Type": "application/xml"})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != 207 {
		return nil, fmt.Errorf("files: search %d %.200s", resp.StatusCode, b)
	}
	var ms multistatus
	if err := xml.Unmarshal(b, &ms); err != nil {
		return nil, err
	}
	files := []File{}
	for _, r := range ms.Responses {
		f := File{Path: r.Href}
		for _, ps := range r.Propstat {
			if !strings.Contains(ps.Status, " 200 ") {
				continue
			}
			p := ps.Prop
			f.Name, f.Type, f.Modified = p.Name, p.ContentType, p.LastModified
			f.Folder = p.ResourceType.Collection != nil
			if n, err := strconv.ParseInt(firstNonEmpty(p.Length, p.Size), 10, 64); err == nil {
				f.Size = n
			}
		}
		if f.Name == "" {
			if u, err := url.PathUnescape(r.Href); err == nil {
				f.Name = u[strings.LastIndex(strings.TrimRight(u, "/"), "/")+1:]
			}
		}
		files = append(files, f)
	}
	return files, nil
}

// Download: at most max bytes of the file at path (a path from Search).
func (c *Client) Download(ctx context.Context, token, path string, max int64) (data []byte, contentType string, truncated bool, err error) {
	if !strings.HasPrefix(path, davPrefix) || strings.Contains(path, "..") {
		return nil, "", false, fmt.Errorf("path must come from files_search")
	}
	resp, err := c.do(ctx, token, "GET", path, nil, nil)
	if err != nil {
		return nil, "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, "", false, fmt.Errorf("files: download %d %.200s", resp.StatusCode, b)
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, max+1))
	if int64(len(data)) > max {
		data, truncated = data[:max], true
	}
	return data, resp.Header.Get("Content-Type"), truncated, err
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
