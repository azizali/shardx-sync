package core

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const tID = "11111111-2222-3333-4444-555555555555"
const tExt = "abcdef0123456789"

func w(t *testing.T, root, rel string, data []byte) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func rnd(n int) []byte { b := make([]byte, n); _, _ = rand.Read(b); return b }

func makeShardx(t *testing.T) string {
	root := t.TempDir()
	prof, _ := json.Marshal(map[string]any{"_meta": map[string]any{"id": tID, "folder": "Clients", "proxy_id": "px1", "extensions": []string{tExt}}, "name": "Alice Shop"})
	w(t, root, "profiles/"+tID+".json", prof)
	w(t, root, "proxies.json", []byte(`{"proxies":[{"id":"px1","host":"1.2.3.4","port":1080,"password":"secret"}]}`))
	w(t, root, "user-data/"+tID+"/Default/Cookies", []byte("cookie-db"))
	w(t, root, "user-data/"+tID+"/Default/Bookmarks", []byte(`{"roots":{}}`))
	w(t, root, "user-data/"+tID+"/Default/Local Storage/leveldb/000003.log", rnd(5000))
	w(t, root, "user-data/"+tID+"/Default/Cache/Cache_Data/data_0", []byte("cache-junk"))
	w(t, root, "user-data/"+tID+"/component_crx_cache/x.crx", []byte("junk"))
	w(t, root, "user-data/"+tID+"/fingerprint.json", []byte(`{"fp":1}`))
	w(t, root, "user-data/"+tID+"/Local State", []byte(`{}`))
	w(t, root, "extensions/"+tExt+"/manifest.json", []byte(`{"name":"ext"}`))
	return root
}

func emptyShardx(t *testing.T) string {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "profiles"), 0o755)
	return root
}

var alice = User{ID: "u-alice", Email: "alice@team"}
var bob = User{ID: "u-bob", Email: "bob@team"}

func newEng(t *testing.T, b *fakeBackend, root string, u User, machine, pass string) (*Engine, *fakeCloud) {
	t.Helper()
	key, err := DeriveKey(pass, "https://x.neon.tech/neondb")
	if err != nil {
		t.Fatal(err)
	}
	fc := &fakeCloud{b: b, user: u}
	return NewEngine(Engine{Cloud: fc, Key: key, Root: root, WorkDir: t.TempDir(), Machine: machine, User: u, SkipCaches: true}), fc
}

func status(t *testing.T, e *Engine) string {
	t.Helper()
	items, err := e.Overview()
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.ID == tID {
			return it.Status
		}
	}
	return "absent"
}

func mustCode(t *testing.T, err error, code string) {
	t.Helper()
	if CodeOf(err) != code {
		t.Fatalf("want error code %s, got %v", code, err)
	}
}

func read(t *testing.T, root, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRoundTrip(t *testing.T) {
	b := newFakeBackend()
	rootA, rootB := makeShardx(t), emptyShardx(t)
	A, ca := newEng(t, b, rootA, alice, "mac-a", "team-pass")
	B, cb := newEng(t, b, rootB, bob, "win-b", "team-pass")
	if err := VerifyTeamKey(ca, A.Key); err != nil {
		t.Fatal(err)
	}
	if err := VerifyTeamKey(cb, B.Key); err != nil {
		t.Fatal(err)
	}
	row, err := A.Push(tID, PushOptions{})
	if err != nil || row.Version != 1 {
		t.Fatalf("push: %v %+v", err, row)
	}
	if s := status(t, A); s != "synced" {
		t.Fatalf("A status %s", s)
	}
	for _, d := range b.files {
		if bytes.Contains(d, []byte("cookie-db")) || bytes.Contains(d, []byte("secret")) {
			t.Fatal("cloud data is not encrypted")
		}
	}
	if s := status(t, B); s != "cloud-only" {
		t.Fatalf("B status %s", s)
	}
	if _, err := B.Pull(tID, PullOptions{TakeLock: true}); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"profiles/" + tID + ".json", "user-data/" + tID + "/Default/Cookies", "user-data/" + tID + "/Default/Bookmarks",
		"user-data/" + tID + "/Default/Local Storage/leveldb/000003.log", "user-data/" + tID + "/fingerprint.json", "extensions/" + tExt + "/manifest.json"} {
		if !bytes.Equal(read(t, rootA, rel), read(t, rootB, rel)) {
			t.Fatalf("mismatch %s", rel)
		}
	}
	if exists(filepath.Join(rootB, "user-data", tID, "Default", "Cache")) || exists(filepath.Join(rootB, "user-data", tID, "component_crx_cache")) {
		t.Fatal("caches should be skipped")
	}
	if !bytes.Contains(read(t, rootB, "proxies.json"), []byte(`"secret"`)) {
		t.Fatal("proxy not merged")
	}
	if r, _ := cb.GetProfile(tID); r.LockedByEmail != "bob@team" {
		t.Fatal("download should check out to bob")
	}
	if s := status(t, B); s != "synced" {
		t.Fatalf("B status after pull %s", s)
	}
}

func TestLocksConflictsBackups(t *testing.T) {
	b := newFakeBackend()
	rootA, rootB := makeShardx(t), emptyShardx(t)
	A, ca := newEng(t, b, rootA, alice, "mac-a", "p")
	B, _ := newEng(t, b, rootB, bob, "win-b", "p")
	if _, err := A.Push(tID, PushOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := B.Pull(tID, PullOptions{TakeLock: true}); err != nil {
		t.Fatal(err)
	}
	_, err := A.Push(tID, PushOptions{Overwrite: true})
	mustCode(t, err, "LOCKED")
	_, err = A.Pull(tID, PullOptions{TakeLock: true})
	mustCode(t, err, "LOCKED")

	w(t, rootB, "user-data/"+tID+"/Default/Bookmarks", []byte(`{"roots":{"bob":1}}`))
	if s := status(t, B); s != "local-changes" {
		t.Fatalf("B status %s", s)
	}
	v2, err := B.Push(tID, PushOptions{})
	if err != nil || v2.Version != 2 || v2.LockedBy != "" {
		t.Fatalf("v2: %v %+v", err, v2)
	}
	if s := status(t, A); s != "cloud-newer" {
		t.Fatalf("A status %s", s)
	}
	w(t, rootA, "user-data/"+tID+"/Default/History", []byte("alice-history"))
	if s := status(t, A); s != "conflict" {
		t.Fatalf("A status %s", s)
	}
	_, err = A.Push(tID, PushOptions{})
	mustCode(t, err, "CONFIRM_OVERWRITE_CLOUD")
	_, err = A.Pull(tID, PullOptions{TakeLock: true})
	mustCode(t, err, "CONFIRM_OVERWRITE_LOCAL")

	if _, err := A.Pull(tID, PullOptions{TakeLock: true, OverwriteLocal: true}); err != nil {
		t.Fatal(err)
	}
	if string(read(t, rootA, "user-data/"+tID+"/Default/Bookmarks")) != `{"roots":{"bob":1}}` {
		t.Fatal("did not get bob's version")
	}
	if exists(filepath.Join(rootA, "user-data", tID, "Default", "History")) {
		t.Fatal("alice's file should have moved to backup")
	}
	snaps, _ := os.ReadDir(filepath.Join(A.BackupDir(), tID))
	if len(snaps) != 1 || string(read(t, filepath.Join(A.BackupDir(), tID, snaps[0].Name()), "user-data/"+tID+"/Default/History")) != "alice-history" {
		t.Fatal("backup missing")
	}
	if f, _ := ca.ListFolders(tID); len(f) != 1 {
		t.Fatalf("old versions not cleaned: %v", f)
	}
}

func TestConcurrentUploadLosesCleanly(t *testing.T) {
	b := newFakeBackend()
	rootA := makeShardx(t)
	A, ca := newEng(t, b, rootA, alice, "mac-a", "p")
	if _, err := A.Push(tID, PushOptions{}); err != nil {
		t.Fatal(err)
	}
	B, cb := newEng(t, b, makeShardx(t), bob, "win-b", "p")
	cb.beforeCommit = func() {
		w(t, rootA, "user-data/"+tID+"/Default/Bookmarks", []byte("changed"))
		if _, err := A.Push(tID, PushOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	B.state.set(tID, &syncState{Version: 1, Fingerprint: "x"})
	_, err := B.Push(tID, PushOptions{})
	mustCode(t, err, "CONFLICT")
	if f, _ := ca.ListFolders(tID); len(f) != 1 {
		t.Fatalf("bob's partial upload should be removed: %v", f)
	}
}

func TestWrongPassphrase(t *testing.T) {
	b := newFakeBackend()
	A, ca := newEng(t, b, makeShardx(t), alice, "a", "right")
	if err := VerifyTeamKey(ca, A.Key); err != nil {
		t.Fatal(err)
	}
	B, cb := newEng(t, b, emptyShardx(t), bob, "b", "wrong")
	mustCode(t, VerifyTeamKey(cb, B.Key), "PASSPHRASE")
}

func TestMultiPart(t *testing.T) {
	b := newFakeBackend()
	root := makeShardx(t)
	big := rnd(PartSize + 3*1024*1024)
	w(t, root, "user-data/"+tID+"/Default/IndexedDB/big.blob", big)
	A, _ := newEng(t, b, root, alice, "a", "p")
	row, err := A.Push(tID, PushOptions{})
	if err != nil || row.Parts != 2 {
		t.Fatalf("want 2 parts: %v %+v", err, row)
	}
	root2 := emptyShardx(t)
	B, _ := newEng(t, b, root2, bob, "b", "p")
	if _, err := B.Pull(tID, PullOptions{TakeLock: true}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(read(t, root2, "user-data/"+tID+"/Default/IndexedDB/big.blob"), big) {
		t.Fatal("big file mismatch")
	}
}

func TestExtractRejectsForeignPaths(t *testing.T) {
	src := t.TempDir()
	w(t, src, "profiles/"+tID+".json", []byte("{}"))
	w(t, src, "proxies.json", []byte("evil"))
	w(t, src, "user-data/other-id/x", []byte("evil"))
	file := filepath.Join(t.TempDir(), "a.tar.gz")
	if _, _, err := PackToFile(src, []FileEntry{{Rel: "profiles/" + tID + ".json"}, {Rel: "proxies.json"}, {Rel: "user-data/other-id/x"}}, file); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	rejected, err := ExtractProfile(file, dest, tID, nil)
	if err != nil || rejected != 2 {
		t.Fatalf("rejected=%d err=%v", rejected, err)
	}
	if !exists(filepath.Join(dest, "profiles", tID+".json")) || exists(filepath.Join(dest, "proxies.json")) {
		t.Fatal("wrong files extracted")
	}
}

func TestDeriveURLs(t *testing.T) {
	a, r, err := DeriveURLs(DefaultNeonURL)
	if err != nil {
		t.Fatal(err)
	}
	if a != "https://ep-soft-sound-b4if536k.neonauth.c-6.us-east-2.aws.neon.tech/neondb/auth" ||
		r != "https://ep-soft-sound-b4if536k.apirest.c-6.us-east-2.aws.neon.tech/neondb/rest/v1" {
		t.Fatalf("got %s %s", a, r)
	}
}
