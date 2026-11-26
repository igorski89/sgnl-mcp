package opencloud

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
)

// Shares, versions, the trash bin and new folders: reads, plus MKCOL
// through Mkcol only.

const metaPrefix = "/remote.php/dav/meta/"

type propResponse struct {
	Href     string `xml:"href"`
	Propstat []struct {
		Status string `xml:"status"`
		Prop   struct {
			ShareTypes struct {
				Types []string `xml:"share-type"`
			} `xml:"share-types"`
			Length       string `xml:"getcontentlength"`
			LastModified string `xml:"getlastmodified"`
			Etag         string `xml:"getetag"`
			OrigName     string `xml:"trashbin-original-filename"`
			OrigLocation string `xml:"trashbin-original-location"`
			DeletedAt    string `xml:"trashbin-delete-datetime"`
			ResourceType struct {
				Collection *struct{} `xml:"collection"`
			} `xml:"resourcetype"`
		} `xml:"prop"`
	} `xml:"propstat"`
}

func (c *Client) propfind(ctx context.Context, token, p, depth, props string) ([]propResponse, error) {
	body := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:prop>` + props + `</d:prop></d:propfind>`
	resp, err := c.do(ctx, token, "PROPFIND", p, strings.NewReader(body), map[string]string{"Depth": depth, "Content-Type": "application/xml"})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == 404 {
		return nil, errNotFound
	}
	if resp.StatusCode != 207 {
		return nil, fmt.Errorf("files: %d %.200s", resp.StatusCode, b)
	}
	var ms struct {
		Responses []propResponse `xml:"response"`
	}
	if err := xml.Unmarshal(b, &ms); err != nil {
		return nil, err
	}
	return ms.Responses, nil
}

var errNotFound = fmt.Errorf("files: not found")

// SharedWithOthers: whether the item at p, or a folder it lies in (below
// the space's top root), is shared with someone. Writes there are seen by
// them.
func (c *Client) SharedWithOthers(ctx context.Context, token, root, p string) (bool, error) {
	if !strings.HasPrefix(p, root) || strings.Contains(p, "..") {
		return false, fmt.Errorf("path must come from folder_list or file_search")
	}
	cur := strings.TrimSuffix(p, "/")
	for len(cur) > len(strings.TrimSuffix(root, "/")) {
		rs, err := c.propfind(ctx, token, cur, "0", `<oc:share-types/>`)
		if err != nil && err != errNotFound {
			return false, err
		}
		for _, r := range rs {
			for _, ps := range r.Propstat {
				if strings.Contains(ps.Status, " 200 ") && len(ps.Prop.ShareTypes.Types) > 0 {
					return true, nil
				}
			}
		}
		cur = path.Dir(cur)
	}
	return false, nil
}

// Version: one earlier version of a file.
type Version struct {
	ID       string `json:"id" jsonschema:"pass to file_version_restore"`
	Modified string `json:"modified"`
	Size     string `json:"size,omitempty"`
}

// Versions: a file's earlier versions (the current one isn't listed).
func (c *Client) Versions(ctx context.Context, token, p string) ([]Version, error) {
	it, err := c.Item(ctx, token, p)
	if err != nil {
		return nil, err
	}
	if it.Folder {
		return nil, fmt.Errorf("folders have no versions")
	}
	base := metaPrefix + url.PathEscape(it.ID) + "/v"
	rs, err := c.propfind(ctx, token, base, "1", `<d:getlastmodified/><d:getcontentlength/>`)
	if err != nil {
		return nil, err
	}
	out := []Version{}
	for _, r := range rs {
		u, _ := url.PathUnescape(strings.TrimSuffix(r.Href, "/"))
		i := strings.LastIndex(u, "/v/")
		if i < 0 {
			continue // the collection itself
		}
		id := u[i+3:]
		if id == "" || strings.Contains(id, "/") {
			continue
		}
		v := Version{ID: id}
		for _, ps := range r.Propstat {
			if strings.Contains(ps.Status, " 200 ") {
				v.Modified, v.Size = ps.Prop.LastModified, ps.Prop.Length
			}
		}
		out = append(out, v)
	}
	return out, nil
}

// TrashItem: something deleted from a space, restorable.
type TrashItem struct {
	ID        string `json:"id" jsonschema:"pass to file_restore"`
	Name      string `json:"name"`
	Location  string `json:"location" jsonschema:"where it was, from the space's top"`
	DeletedAt string `json:"deleted_at"`
	Folder    bool   `json:"folder,omitempty"`
	Size      string `json:"size,omitempty"`
}

// Trash: the trash bin of the space with this id.
func (c *Client) Trash(ctx context.Context, token, spaceID string) ([]TrashItem, error) {
	if spaceID == "" || strings.ContainsAny(spaceID, "/?#") {
		return nil, fmt.Errorf("files: bad space")
	}
	base := trashPrefix + url.PathEscape(spaceID)
	rs, err := c.propfind(ctx, token, base, "1", `<oc:trashbin-original-filename/><oc:trashbin-original-location/><oc:trashbin-delete-datetime/><d:getcontentlength/><d:resourcetype/>`)
	if err != nil {
		return nil, err
	}
	out := []TrashItem{}
	for _, r := range rs {
		u, _ := url.PathUnescape(r.Href)
		id := strings.Trim(strings.TrimPrefix(u, base), "/")
		if id == "" || strings.Contains(id, "/") {
			continue // the bin itself, or inside a deleted folder
		}
		t := TrashItem{ID: id}
		for _, ps := range r.Propstat {
			if strings.Contains(ps.Status, " 200 ") {
				t.Name, t.Location, t.DeletedAt = ps.Prop.OrigName, ps.Prop.OrigLocation, ps.Prop.DeletedAt
				t.Folder = ps.Prop.ResourceType.Collection != nil
				t.Size = ps.Prop.Length
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// Mkcol: a new, empty folder name in folder (never over an existing one).
func (c *Client) Mkcol(ctx context.Context, token, folder, name string) (string, error) {
	if !strings.HasPrefix(folder, davPrefix) || strings.HasPrefix(folder, trashPrefix) || strings.Contains(folder, "..") || strings.ContainsAny(folder, "?#") {
		return "", fmt.Errorf("folder must come from folder_list")
	}
	if name == "" || strings.ContainsAny(name, "/\\") || strings.HasPrefix(name, ".") || len(name) > 200 {
		return "", fmt.Errorf("give a plain folder name (no slashes, not hidden)")
	}
	if !strings.HasSuffix(folder, "/") {
		folder += "/"
	}
	p := folder + url.PathEscape(name)
	if _, err := c.propfind(ctx, token, p, "0", `<d:resourcetype/>`); err == nil {
		return "", fmt.Errorf("%q already exists there", name)
	} else if err != errNotFound {
		return "", err
	}
	resp, err := c.send(ctx, token, "MKCOL", p, nil, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 201:
		return p + "/", nil
	case 405:
		return "", fmt.Errorf("%q already exists there", name)
	case 409:
		return "", fmt.Errorf("the folder to create it in doesn't exist")
	default:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return "", fmt.Errorf("files: create folder %d %.200s", resp.StatusCode, b)
	}
}

// davItem: a file or folder path the write calls accept (not the trash
// bin, no "..", no "?" or "#").
func davItem(p string) bool {
	return strings.HasPrefix(p, davPrefix) && !strings.HasPrefix(p, trashPrefix) && !strings.Contains(p, "..") && !strings.ContainsAny(p, "?#")
}

// Exists: whether something is at p.
func (c *Client) Exists(ctx context.Context, token, p string) (bool, error) {
	_, err := c.propfind(ctx, token, p, "0", `<d:resourcetype/>`)
	if err == errNotFound {
		return false, nil
	}
	return err == nil, err
}

// Move: src to dst (a new name and/or another folder of the same space),
// never over something that exists (checked, and Overwrite: F).
func (c *Client) Move(ctx context.Context, token, src, dst string) error {
	if !davItem(src) || !davItem(dst) {
		return fmt.Errorf("paths must come from folder_list or file_search")
	}
	if ex, err := c.Exists(ctx, token, strings.TrimSuffix(dst, "/")); err != nil {
		return err
	} else if ex {
		return fmt.Errorf("something with that name already exists there")
	}
	resp, err := c.send(ctx, token, "MOVE", src, nil, map[string]string{"Destination": c.Base + dst, "Overwrite": "F"})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 201:
		return nil
	case 412, 204:
		return fmt.Errorf("something with that name already exists there")
	case 423:
		return fmt.Errorf("it is locked (open for editing somewhere?): nothing was moved")
	case 409:
		return fmt.Errorf("the folder to move it into doesn't exist")
	default:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("files: move %d %.200s", resp.StatusCode, b)
	}
}

// Copy: src to dst (any space the person can write to), never over
// something that exists (checked, and Overwrite: F); a folder with all
// it holds.
func (c *Client) Copy(ctx context.Context, token, src, dst string) error {
	if !davItem(src) || !davItem(dst) {
		return fmt.Errorf("paths must come from folder_list or file_search")
	}
	if ex, err := c.Exists(ctx, token, strings.TrimSuffix(dst, "/")); err != nil {
		return err
	} else if ex {
		return fmt.Errorf("something with that name already exists there")
	}
	resp, err := c.send(ctx, token, "COPY", src, nil, map[string]string{"Destination": c.Base + dst, "Overwrite": "F", "Depth": "infinity"})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 201:
		return nil
	case 412, 204:
		return fmt.Errorf("something with that name already exists there")
	case 403:
		return fmt.Errorf("you can't add files there")
	case 409:
		return fmt.Errorf("the folder to copy it into doesn't exist")
	case 507:
		return fmt.Errorf("there isn't enough space there (the size limit is reached)")
	default:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("files: copy %d %.200s", resp.StatusCode, b)
	}
}

// ToTrash: p into its space's trash bin (DELETE on a space path: OpenCloud
// keeps it in the trash bin, restorable). Never the trash bin itself.
func (c *Client) ToTrash(ctx context.Context, token, p string) error {
	if !davItem(p) {
		return fmt.Errorf("path must come from folder_list or file_search")
	}
	resp, err := c.send(ctx, token, "DELETE", p, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 204, 200:
		return nil
	case 423:
		return fmt.Errorf("it is locked (open for editing somewhere?): nothing was moved to the trash")
	case 404:
		return fmt.Errorf("it isn't there (anymore)")
	default:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("files: trash %d %.200s", resp.StatusCode, b)
	}
}

// Restore: a trash item back to where it was, never over something there.
func (c *Client) Restore(ctx context.Context, token, spaceID, spacePath, id, location string) error {
	if spaceID == "" || strings.ContainsAny(spaceID, "/?#") || id == "" || strings.ContainsAny(id, "/?#") || strings.Contains(location, "..") || location == "" {
		return fmt.Errorf("files: bad trash item")
	}
	if !strings.HasSuffix(spacePath, "/") || !davItem(spacePath) {
		return fmt.Errorf("files: bad space")
	}
	parts := strings.Split(strings.Trim(location, "/"), "/")
	for i, x := range parts {
		parts[i] = url.PathEscape(x)
	}
	dst := spacePath + strings.Join(parts, "/")
	if ex, err := c.Exists(ctx, token, dst); err != nil {
		return err
	} else if ex {
		return fmt.Errorf("something is at its old place again (%s): rename that first", location)
	}
	resp, err := c.send(ctx, token, "MOVE", trashPrefix+url.PathEscape(spaceID)+"/"+url.PathEscape(id), nil,
		map[string]string{"Destination": c.Base + dst, "Overwrite": "F"})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 201, 204:
		return nil
	case 412:
		return fmt.Errorf("something is at its old place again (%s): rename that first", location)
	case 409:
		return fmt.Errorf("the folder it was in is gone: restore that first")
	default:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("files: restore %d %.200s", resp.StatusCode, b)
	}
}

// RestoreVersion: the file at p gets the content of its earlier version
// versionID (OpenCloud keeps the current content as a version). versionID
// must be one Versions lists for p.
func (c *Client) RestoreVersion(ctx context.Context, token, p, versionID string) error {
	if !davItem(p) || versionID == "" || strings.ContainsAny(versionID, "/?#") {
		return fmt.Errorf("files: bad version")
	}
	it, err := c.Item(ctx, token, p)
	if err != nil {
		return err
	}
	vs, err := c.Versions(ctx, token, p)
	if err != nil {
		return err
	}
	found := false
	for _, v := range vs {
		found = found || v.ID == versionID
	}
	if !found {
		return fmt.Errorf("no version %q of this file (see file_versions)", versionID)
	}
	src := metaPrefix + url.PathEscape(it.ID) + "/v/" + url.PathEscape(versionID)
	resp, err := c.send(ctx, token, "COPY", src, nil, map[string]string{"Destination": c.Base + p})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 204, 201, 200:
		return nil
	case 423:
		return fmt.Errorf("the file is locked (open for editing somewhere?): nothing was changed")
	default:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("files: restore version %d %.200s", resp.StatusCode, b)
	}
}
