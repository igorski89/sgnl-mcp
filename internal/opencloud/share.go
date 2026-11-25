package opencloud

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
)

// Sharing: only with users of this instance (no links, no other kinds of
// recipient), only through Share, after the person confirmed.

// Roles a share may give (OpenCloud's unified role ids; each must also be
// among the item's allowed roles at the time).
const (
	RoleViewer     = "b1e2218d-eef8-4d4c-b82d-0f1a1b48f3b5"
	RoleEditor     = "fb6c3e19-e378-47e5-b277-9732f9de6e21" // folders
	RoleFileEditor = "2d00ce52-1fc2-4dbc-8b95-a73b73395f5a" // files
)

// invitePath: the one POST the client ever makes.
var invitePath = regexp.MustCompile(`^/graph/v1beta1/drives/[^/?#]+/items/[^/?#]+/invite$`)

// Item: what a share needs to know about a file or folder.
type Item struct {
	ID     string // storage$space!opaque
	Drive  string // storage$space
	Folder bool
}

// Item: the id of the file or folder at path (PROPFIND, depth 0).
func (c *Client) Item(ctx context.Context, token, path string) (Item, error) {
	if !strings.HasPrefix(path, davPrefix) || strings.Contains(path, "..") {
		return Item{}, fmt.Errorf("path must come from folder_list or file_search")
	}
	body := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:prop><oc:fileid/><d:resourcetype/></d:prop></d:propfind>`
	resp, err := c.do(ctx, token, "PROPFIND", path, strings.NewReader(body), map[string]string{"Depth": "0", "Content-Type": "application/xml"})
	if err != nil {
		return Item{}, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 207 {
		return Item{}, fmt.Errorf("files: %d %.200s", resp.StatusCode, b)
	}
	var ms struct {
		Responses []struct {
			Propstat []struct {
				Prop struct {
					FileID       string `xml:"fileid"`
					ResourceType struct {
						Collection *struct{} `xml:"collection"`
					} `xml:"resourcetype"`
				} `xml:"prop"`
			} `xml:"propstat"`
		} `xml:"response"`
	}
	if err := xml.Unmarshal(b, &ms); err != nil {
		return Item{}, err
	}
	for _, r := range ms.Responses {
		for _, ps := range r.Propstat {
			if id := ps.Prop.FileID; id != "" {
				drive, _, ok := strings.Cut(id, "!")
				if !ok {
					return Item{}, fmt.Errorf("files: unexpected file id")
				}
				return Item{ID: id, Drive: drive, Folder: ps.Prop.ResourceType.Collection != nil}, nil
			}
		}
	}
	return Item{}, fmt.Errorf("files: no id for %s", path)
}

// User: someone the person can share with.
type User struct {
	ID       string `json:"id"`
	Name     string `json:"displayName"`
	Mail     string `json:"mail"`
	Username string `json:"onPremisesSamAccountName"`
}

// FindUsers: users of this instance matching q (OpenCloud wants at least
// 3 characters).
func (c *Client) FindUsers(ctx context.Context, token, q string) ([]User, error) {
	v := url.Values{"$search": {`"` + strings.ReplaceAll(q, `"`, "") + `"`}, "$select": {"id,displayName,mail,onPremisesSamAccountName"}}
	var r struct {
		Value []User `json:"value"`
	}
	if err := c.getJSON(ctx, token, "/graph/v1.0/users?"+v.Encode(), &r); err != nil {
		return nil, err
	}
	return r.Value, nil
}

// AllowedRoles: the role ids the person may give on this item.
func (c *Client) AllowedRoles(ctx context.Context, token string, it Item) ([]string, error) {
	v := url.Values{"$select": {"@libre.graph.permissions.roles.allowedValues"}}
	var r struct {
		Roles []struct {
			ID string `json:"id"`
		} `json:"@libre.graph.permissions.roles.allowedValues"`
	}
	if err := c.getJSON(ctx, token, "/graph/v1beta1/drives/"+url.PathEscape(it.Drive)+"/items/"+url.PathEscape(it.ID)+"/permissions?"+v.Encode(), &r); err != nil {
		return nil, err
	}
	ids := make([]string, len(r.Roles))
	for i, x := range r.Roles {
		ids[i] = x.ID
	}
	return ids, nil
}

// Share gives one user one role on the item (no expiry, no link).
func (c *Client) Share(ctx context.Context, token string, it Item, userID, role string) error {
	if role != RoleViewer && role != RoleEditor && role != RoleFileEditor {
		return fmt.Errorf("opencloud: role %s is not allowed", role)
	}
	body, _ := json.Marshal(map[string]any{
		"recipients": []map[string]string{{"objectId": userID, "@libre.graph.recipient.type": "user"}},
		"roles":      []string{role},
	})
	p := "/graph/v1beta1/drives/" + url.PathEscape(it.Drive) + "/items/" + url.PathEscape(it.ID) + "/invite"
	resp, err := c.post(ctx, token, p, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return fmt.Errorf("files: share %d %.300s", resp.StatusCode, b)
	}
	return nil
}

func (c *Client) getJSON(ctx context.Context, token, path string, out any) error {
	resp, err := c.do(ctx, token, "GET", path, nil, map[string]string{"Accept": "application/json"})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != 200 {
		return fmt.Errorf("files: %d %.200s", resp.StatusCode, b)
	}
	return json.Unmarshal(b, out)
}
