// Package opencloud: a minimal OpenCloud client: search (REPORT
// oc:search-files), folder listing (PROPFIND), spaces (Graph, GET),
// download (GET) and saving NEW files (PUT with If-None-Match: *, so an
// existing file is never overwritten). No other method can be sent.
package opencloud

import (
	"context"
	"encoding/json"
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

var allowedMethods = []string{"GET", "PROPFIND", "REPORT", "PUT"}

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
	if !slices.Contains(allowedMethods, method) {
		return nil, fmt.Errorf("opencloud: %s is not allowed (read-only)", method)
	}
	if method == "PUT" && hdr["If-None-Match"] != "*" {
		return nil, fmt.Errorf("opencloud: PUT only for new files (never overwrite)")
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
	return c.SearchKQL(ctx, token, `name:"*`+q+`*"`, limit)
}

// SearchKQL: OpenCloud's search with a KQL pattern (e.g. mtime:today).
func (c *Client) SearchKQL(ctx context.Context, token, kql string, limit int) ([]File, error) {
	body := `<?xml version="1.0"?>
<oc:search-files xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
  <d:prop><oc:name/><d:getcontenttype/><d:getcontentlength/><oc:size/><d:getlastmodified/><d:resourcetype/></d:prop>
  <oc:search><oc:pattern>` + xmlEscape(kql) + `</oc:pattern><oc:limit>` + strconv.Itoa(limit) + `</oc:limit></oc:search>
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
	return parse(b)
}

func parse(b []byte) ([]File, error) {
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

// Space: a drive the person can see (personal, project, shares).
type Space struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Path string `json:"path" jsonschema:"pass to folder_list / file_save"`
}

// Spaces: the person's drives (Graph /me/drives).
func (c *Client) Spaces(ctx context.Context, token string) ([]Space, error) {
	resp, err := c.do(ctx, token, "GET", "/graph/v1.0/me/drives", nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("files: spaces %d %.200s", resp.StatusCode, b)
	}
	var r struct {
		Value []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			DriveType string `json:"driveType"`
			Root      struct {
				WebDavURL string `json:"webDavUrl"`
			} `json:"root"`
		} `json:"value"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	out := []Space{}
	for _, d := range r.Value {
		if d.DriveType == "virtual" || d.DriveType == "mountpoint" {
			continue
		}
		p := davPrefix + url.PathEscape(d.ID) + "/"
		if u, err := url.Parse(d.Root.WebDavURL); err == nil && strings.HasPrefix(u.Path, davPrefix) {
			p = u.Path
			if !strings.HasSuffix(p, "/") {
				p += "/"
			}
		}
		out = append(out, Space{d.Name, d.DriveType, p})
	}
	return out, nil
}

// List: a folder's entries (PROPFIND, depth 1).
func (c *Client) List(ctx context.Context, token, path string) ([]File, error) {
	if !strings.HasPrefix(path, davPrefix) || strings.Contains(path, "..") {
		return nil, fmt.Errorf("path must come from folder_list or file_search")
	}
	body := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:prop>` +
		`<oc:name/><d:getcontenttype/><d:getcontentlength/><oc:size/><d:getlastmodified/><d:resourcetype/></d:prop></d:propfind>`
	resp, err := c.do(ctx, token, "PROPFIND", path, strings.NewReader(body), map[string]string{"Depth": "1", "Content-Type": "application/xml"})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != 207 {
		return nil, fmt.Errorf("files: list %d %.200s", resp.StatusCode, b)
	}
	files, err := parse(b)
	if err != nil {
		return nil, err
	}
	out := []File{}
	self := strings.TrimSuffix(path, "/")
	for _, f := range files {
		if u, err := url.PathUnescape(strings.TrimSuffix(f.Path, "/")); err == nil && (u == self || f.Path == path) {
			continue // the folder itself
		}
		out = append(out, f)
	}
	return out, nil
}

// SaveNew: create a file; fails if one exists at that path.
func (c *Client) SaveNew(ctx context.Context, token, folder, name, ctype string, data []byte) (string, error) {
	if !strings.HasPrefix(folder, davPrefix) || strings.Contains(folder, "..") {
		return "", fmt.Errorf("folder must come from folder_list")
	}
	if name == "" || strings.ContainsAny(name, "/\\") || strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("give a plain file name (no slashes, not hidden)")
	}
	if !strings.HasSuffix(folder, "/") {
		folder += "/"
	}
	p := folder + url.PathEscape(name)
	// OpenCloud ignores If-None-Match on PUT (found 2026-11-23): check first.
	if r, err := c.do(ctx, token, "PROPFIND", p, nil, map[string]string{"Depth": "0"}); err == nil {
		r.Body.Close()
		if r.StatusCode != 404 {
			if r.StatusCode == 207 {
				return "", fmt.Errorf("a file named %q already exists there; pick another name", name)
			}
			return "", fmt.Errorf("files: can't check whether %q exists (%d); not saving", name, r.StatusCode)
		}
	} else {
		return "", err
	}
	resp, err := c.do(ctx, token, "PUT", p, strings.NewReader(string(data)), map[string]string{"If-None-Match": "*", "Content-Type": ctype})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 201, 204:
		return p, nil
	case 412:
		return "", fmt.Errorf("a file named %q already exists there; pick another name", name)
	default:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return "", fmt.Errorf("files: save %d %.200s", resp.StatusCode, b)
	}
}
