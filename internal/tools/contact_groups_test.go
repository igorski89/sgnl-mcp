package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestContactGroups(t *testing.T) {
	f := newFakeMail()
	f.cards = map[string]any{
		"c1": map[string]any{"id": "c1", "uid": "urn:uuid:1", "kind": "individual", "name": map[string]any{"full": "Anna Example"},
			"emails": map[string]any{"e1": map[string]any{"address": "anna@other.example"}}},
		"c2": map[string]any{"id": "c2", "uid": "B0B", "name": map[string]any{"full": "Bob Example"},
			"emails": map[string]any{"e1": map[string]any{"address": "bob@other.example"}}},
		"g1": map[string]any{"id": "g1", "uid": "urn:uuid:g", "kind": "group", "name": map[string]any{"full": "Board"},
			"members": map[string]any{"urn:uuid:1": true, "urn:uuid:gone": true}},
		"g2": map[string]any{"id": "g2", "uid": "g2", "kind": "group", "name": map[string]any{"full": "App made"},
			"members": map[string]any{"urn:uuid:b0b": true}},
	}
	cs := sendSession(t, f, nil, false)
	txt, isErr := callTool(t, cs, "contact_groups", nil)
	if isErr || !strings.Contains(txt, `{"id":"g1","members":[{"email":"anna@other.example","id":"c1","name":"Anna Example"}],"members_not_in_address_book":1,"name":"Board"}`) ||
		!strings.Contains(txt, `{"id":"g2","members":[{"email":"bob@other.example","id":"c2","name":"Bob Example"}],"name":"App made"}`) {
		t.Errorf("groups: %s", txt)
	}
	// Add Bob by address, remove Anna by id: the member not in the address book stays.
	txt, isErr = callTool(t, cs, "contact_group_update", map[string]any{"id": "g1", "add": []string{"BOB@other.example"}, "remove": []string{"c1"}})
	if isErr || !strings.Contains(txt, `"members":[{"email":"bob@other.example","id":"c2","name":"Bob Example"}],"members_not_in_address_book":1`) {
		t.Errorf("update: %s", txt)
	}
	b, _ := json.Marshal(f.cardSets[len(f.cardSets)-1]["update"])
	if string(b) != `{"g1":{"members":{"urn:uuid:B0B":true,"urn:uuid:gone":true}}}` {
		t.Errorf("update sent: %s", b)
	}
	txt, isErr = callTool(t, cs, "contact_group_create", map[string]any{"name": "Team", "members": []string{"c1", "bob@other.example"}})
	if isErr || !strings.Contains(txt, `"members":[{"email":"anna@other.example","id":"c1","name":"Anna Example"},{"email":"bob@other.example","id":"c2","name":"Bob Example"}],"name":"Team"`) {
		t.Errorf("create: %s", txt)
	}
	b, _ = json.Marshal(f.cardSets[len(f.cardSets)-1]["create"])
	if !strings.Contains(string(b), `"kind":"group","members":{"urn:uuid:1":true,"urn:uuid:B0B":true}`) {
		t.Errorf("create sent: %s", b)
	}
	// The app-made group: removing Bob finds his member key whatever its case or prefix.
	if txt, isErr := callTool(t, cs, "contact_group_update", map[string]any{"id": "g2", "remove": []string{"c2"}}); isErr {
		t.Errorf("remove from app group: %s", txt)
	}
	if b, _ := json.Marshal(f.cardSets[len(f.cardSets)-1]["update"]); string(b) != `{"g2":{"members":{}}}` {
		t.Errorf("app group update: %s", b)
	}
	n := len(f.cardSets)
	for _, c := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"contact_group_create", map[string]any{"name": "board"}, "already have a group"},
		{"contact_group_update", map[string]any{"id": "g1", "add": []string{"c2"}, "remove": []string{"c2"}}, "both added and removed"},
		{"contact_group_create", map[string]any{"name": "X", "members": []string{"nobody@x.example"}}, "isn't in your address book"},
		{"contact_group_create", map[string]any{"name": "X", "members": []string{"g1"}}, "isn't in your address book"},
		{"contact_group_update", map[string]any{"id": "c1", "add": []string{"c2"}}, "no group"},
		{"contact_group_update", map[string]any{"id": "g1", "remove": []string{"c1"}}, "isn't in the group"},
		{"contact_group_update", map[string]any{"id": "g1"}, "nothing to change"},
	} {
		if txt, isErr := callTool(t, cs, c.tool, c.args); !isErr || !strings.Contains(txt, c.want) {
			t.Errorf("%s %v: %s, want %q", c.tool, c.args, txt, c.want)
		}
	}
	if len(f.cardSets) != n {
		t.Error("a refused change was sent")
	}
}
