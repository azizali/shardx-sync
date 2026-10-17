//go:build live

// Live integration test against the real Neon project. Run with:
//   LIVE_A_EMAIL=.. LIVE_A_PASS=.. LIVE_B_EMAIL=.. LIVE_B_PASS=.. go test -tags live -run TestLive -v ./internal/core
// It needs two team accounts and removes the cloud rows and files it creates.
package core

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type memSessions struct{ s *Session }

func (m *memSessions) LoadSession() *Session  { return m.s }
func (m *memSessions) SaveSession(s *Session) { m.s = s }

// prefixedSettings keeps the live test's passphrase check away from the real team's row.
type prefixedSettings struct {
	*Cloud
	suffix string
}

func (p prefixedSettings) GetTeamSetting(k string) (string, bool, error) { return p.Cloud.GetTeamSetting(k + p.suffix) }
func (p prefixedSettings) InsertTeamSetting(k, v string) error         { return p.Cloud.InsertTeamSetting(k+p.suffix, v) }

func liveEngine(t *testing.T, email, pass, machine, root string, key []byte, suffix string) *Engine {
	t.Helper()
	c, err := NewCloud(DefaultNeonURL, DefaultStorageURL, DefaultAuthOrigin, &memSessions{})
	if err != nil {
		t.Fatal(err)
	}
	u, err := c.SignIn(email, pass)
	if err != nil {
		t.Fatalf("sign in %s: %v", email, err)
	}
	b := prefixedSettings{c, suffix}
	if err := VerifyTeamKey(b, key); err != nil {
		t.Fatal(err)
	}
	return NewEngine(Engine{Cloud: b, Key: key, Root: root, WorkDir: t.TempDir(), Machine: machine, User: *u, SkipCaches: true})
}

func TestLive(t *testing.T) {
	id := fmt.Sprintf("%08x-0000-4000-8000-%012x", time.Now().Unix(), time.Now().UnixNano()&0xffffffffffff)
	suffix := "-live-" + id
	key, _ := DeriveKey("live-"+id, DefaultNeonURL)
	rootA, rootB := t.TempDir(), t.TempDir()
	_ = os.MkdirAll(filepath.Join(rootB, "profiles"), 0o755)
	w(t, rootA, "profiles/"+id+".json", []byte(`{"_meta":{"folder":"Live"},"name":"Live Test Profile"}`))
	w(t, rootA, "user-data/"+id+"/Default/Cookies", []byte("cookie-db"))
	big := make([]byte, 45*1024*1024)
	_, _ = rand.Read(big)
	w(t, rootA, "user-data/"+id+"/Default/IndexedDB/big.blob", big)

	A := liveEngine(t, os.Getenv("LIVE_A_EMAIL"), os.Getenv("LIVE_A_PASS"), "live-mac", rootA, key, suffix)
	B := liveEngine(t, os.Getenv("LIVE_B_EMAIL"), os.Getenv("LIVE_B_PASS"), "live-win", rootB, key, suffix)
	defer func() {
		if err := A.DeleteFromCloud(id); err != nil {
			t.Errorf("cleanup: %v", err)
		}
		if r, _ := A.Cloud.GetProfile(id); r != nil {
			t.Error("row not removed")
		}
		if f, _ := A.Cloud.ListFolders(id); len(f) != 0 {
			t.Errorf("files not removed: %v", f)
		}
		t.Log("cleanup done")
	}()

	start := time.Now()
	row, err := A.Push(id, PushOptions{})
	if err != nil || row.Version != 1 || row.Parts != 2 {
		t.Fatalf("push: %v %+v", err, row)
	}
	t.Logf("upload 45 MB in 2 parts: %s", time.Since(start).Round(100*time.Millisecond))

	start = time.Now()
	if _, err := B.Pull(id, PullOptions{TakeLock: true}); err != nil {
		t.Fatal(err)
	}
	t.Logf("download: %s", time.Since(start).Round(100*time.Millisecond))
	if !bytes.Equal(read(t, rootB, "user-data/"+id+"/Default/IndexedDB/big.blob"), big) {
		t.Fatal("big file mismatch")
	}

	_, err = A.Push(id, PushOptions{Overwrite: true})
	mustCode(t, err, "LOCKED")
	_, err = A.Pull(id, PullOptions{TakeLock: true})
	mustCode(t, err, "LOCKED")

	w(t, rootB, "user-data/"+id+"/Default/Bookmarks", []byte(`{"bob":1}`))
	v2, err := B.Push(id, PushOptions{})
	if err != nil || v2.Version != 2 || v2.LockedBy != "" {
		t.Fatalf("v2: %v %+v", err, v2)
	}
	if f, _ := A.Cloud.ListFolders(id); len(f) != 1 {
		t.Fatalf("old version not cleaned: %v", f)
	}
	items, err := A.Overview()
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.ID == id && it.Status != "cloud-newer" {
			t.Fatalf("A status %s", it.Status)
		}
	}
	if _, err := A.Pull(id, PullOptions{}); err != nil {
		t.Fatal(err)
	}
	if string(read(t, rootA, "user-data/"+id+"/Default/Bookmarks")) != `{"bob":1}` {
		t.Fatal("A did not get B's change")
	}
	wrong, _ := DeriveKey("wrong", DefaultNeonURL)
	mustCode(t, VerifyTeamKey(A.Cloud, wrong), "PASSPHRASE")
}
