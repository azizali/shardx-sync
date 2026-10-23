package core

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

const tTemp = "99999999-8888-7777-6666-555555555555"

// writeShardBrowser21 writes profiles the way ShardBrowser 2.1 (src-tauri/src/profile.rs) does:
// the envelope is _meta, the name sits beside the fingerprint, and last_launched_at is "@<unix secs>".
func writeShardBrowser21(t *testing.T, root string) {
	t.Helper()
	prof, _ := json.Marshal(map[string]any{
		"name": "Alice Shop",
		"_meta": map[string]any{
			"id": tID, "proxy_id": "px1", "folder": "Clients", "extensions": []string{tExt},
			"last_launched_at": "@1791471430", "created_at": "@1791000000", "pinned": true, "rev": 3,
		},
		"os": "macos",
	})
	w(t, root, "profiles/"+tID+".json", prof)
	temp, _ := json.Marshal(map[string]any{"name": "API", "_meta": map[string]any{"id": tTemp, "temporary": true}})
	w(t, root, "profiles/"+tTemp+".json", temp)
	w(t, root, "proxies.json", []byte(`{"proxies":[{"id":"px1","name":"DC","kind":"socks5","host":"1.2.3.4","port":1080}]}`))
}

func TestReadsShardBrowser21Profiles(t *testing.T) {
	root := t.TempDir()
	writeShardBrowser21(t, root)
	list, err := ListLocalProfiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != tID {
		t.Fatalf("temporary profiles must not sync, got %+v", list)
	}
	p := list[0]
	if p.Name != "Alice Shop" || p.Folder != "Clients" || p.ProxyID != "px1" || len(p.Extensions) != 1 {
		t.Fatalf("fields not read: %+v", p)
	}
	if p.LastLaunchedAt == nil || *p.LastLaunchedAt != 1791471430000 {
		t.Fatalf("last launch not read: %+v", p.LastLaunchedAt)
	}
}

func TestShardBrowser21ProfileRoundTrips(t *testing.T) {
	b := newFakeBackend()
	rootA, rootB := t.TempDir(), t.TempDir()
	writeShardBrowser21(t, rootA)
	w(t, rootA, "user-data/"+tID+"/Default/Local Storage/leveldb/000003.log", rnd(2000))
	A, _ := newEng(t, b, rootA, alice, "mac-a", "team-pass")
	B, _ := newEng(t, b, rootB, bob, "mac-b", "team-pass")
	row, err := A.Push(tID, PushOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if row.Folder != "Clients" {
		t.Fatalf("folder not sent: %+v", row)
	}
	if _, err := B.Pull(tID, PullOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, rootB, "profiles/"+tID+".json"); len(got) == 0 {
		t.Fatal("profile file missing after pull")
	}
	if exists(filepath.Join(rootB, "profiles", tTemp+".json")) {
		t.Fatal("temporary profile should never be pulled")
	}
}

func TestMemberDeleteIsSoft(t *testing.T) {
	b := newFakeBackend()
	root := makeShardx(t)
	A, fc := newEng(t, b, root, alice, "mac-a", "team-pass")
	if _, err := A.Push(tID, PushOptions{}); err != nil {
		t.Fatal(err)
	}
	fc.role = RoleMember
	if err := A.DeleteFromCloud(tID); err != nil {
		t.Fatal(err)
	}
	row := b.rows[tID]
	if row == nil || row.DeletedAt == "" {
		t.Fatalf("member delete should keep the row and mark it deleted: %+v", row)
	}
	if len(b.files) == 0 {
		t.Fatal("member delete must not remove the archive")
	}
}

func TestAdminDeleteRemovesForGood(t *testing.T) {
	b := newFakeBackend()
	root := makeShardx(t)
	A, fc := newEng(t, b, root, alice, "mac-a", "team-pass")
	if _, err := A.Push(tID, PushOptions{}); err != nil {
		t.Fatal(err)
	}
	fc.role = RoleAdmin
	if err := A.DeleteFromCloud(tID); err != nil {
		t.Fatal(err)
	}
	if b.rows[tID] != nil {
		t.Fatal("admin delete should remove the row")
	}
	if len(b.files) != 0 {
		t.Fatalf("admin delete should remove the archive, %d files left", len(b.files))
	}
}
