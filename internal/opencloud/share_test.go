package opencloud

import (
	"context"
	"testing"
)

func TestPostOnlyForInvites(t *testing.T) {
	c := New("http://127.0.0.1:1")
	for _, p := range []string{"/graph/v1beta1/drives/a/items/b/createLink", "/graph/v1.0/users", "/graph/v1beta1/drives/a/root/invite",
		"/graph/v1beta1/drives/a/items/b/invite?x=1", "/graph/v1beta1/drives/a/items/b/permissions/p"} {
		if _, err := c.post(context.Background(), "t", p, nil); err == nil {
			t.Errorf("POST %s allowed", p)
		}
	}
}
