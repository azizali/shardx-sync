package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

const tLocal = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" // a profile Bob made and never uploaded

// shared uploads Alice's profile and has Bob download it, with a Trash copy, a backup, and a profile of his own.
func shared(t *testing.T) (b *fakeBackend, B *Engine, fb *fakeCloud, rootB string) {
	t.Helper()
	b = newFakeBackend()
	rootA := makeShardx(t)
	rootB = emptyShardx(t)
	A, fa := newEng(t, b, rootA, alice, "mac-a", "team-pass")
	fa.role = RoleAdmin
	if _, err := A.Push(tID, PushOptions{}); err != nil {
		t.Fatal(err)
	}
	B, fb = newEng(t, b, rootB, bob, "mac-b", "team-pass")
	if _, err := B.Pull(tID, PullOptions{}); err != nil {
		t.Fatal(err)
	}
	w(t, rootB, "trash/"+tID+".zip", []byte("old copy"))
	w(t, B.BackupDir(), tID+"/2026-10-01/profiles/"+tID+".json", []byte("{}"))
	w(t, rootB, "profiles/"+tLocal+".json", []byte(`{"name":"Bob sandbox","_meta":{"id":"`+tLocal+`"}}`))
	w(t, rootB, "user-data/"+tLocal+"/Default/Bookmarks", []byte("{}"))
	return b, B, fb, rootB
}

func assertGone(t *testing.T, B *Engine, root, id string) {
	t.Helper()
	for _, p := range []string{
		filepath.Join(root, "profiles", id+".json"), filepath.Join(root, "user-data", id),
		filepath.Join(root, "trash", id+".zip"), filepath.Join(B.BackupDir(), id),
	} {
		if exists(p) {
			t.Fatalf("%s should be deleted", p)
		}
	}
	if _, ok := B.state.get(id); ok {
		t.Fatal("sync state should be cleared")
	}
}

func assertKept(t *testing.T, root, id string) {
	t.Helper()
	if !exists(filepath.Join(root, "profiles", id+".json")) || !exists(filepath.Join(root, "user-data", id)) {
		t.Fatalf("profile %s should still be here", id)
	}
}

func TestEnforceRemovesRevokedProfile(t *testing.T) {
	b, B, _, rootB := shared(t)
	res, err := B.Enforce()
	if err != nil || len(res.Removed) != 0 {
		t.Fatalf("nothing to remove while Bob has access: %v %+v", err, res)
	}
	b.denied[bob.ID+"|"+tID] = true
	res, err = B.Enforce()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 1 || res.Removed[0] != "Alice Shop" {
		t.Fatalf("want Alice Shop removed, got %+v", res)
	}
	assertGone(t, B, rootB, tID)
	assertKept(t, rootB, tLocal)
}

func TestEnforceRemovesDeletedProfiles(t *testing.T) {
	for _, admin := range []bool{false, true} {
		t.Run(fmt.Sprintf("admin=%v", admin), func(t *testing.T) {
			b, B, _, rootB := shared(t)
			deleter := &fakeCloud{b: b, user: alice, role: RoleMember}
			if admin {
				deleter.role = RoleAdmin // deleted for good: leaves a tombstone
			}
			if err := deleter.DeleteProfile(tID); err != nil {
				t.Fatal(err)
			}
			if _, err := B.Enforce(); err != nil {
				t.Fatal(err)
			}
			assertGone(t, B, rootB, tID)
			assertKept(t, rootB, tLocal)
		})
	}
}

func TestEnforceRemovesTrashOnlyCopy(t *testing.T) {
	b, B, _, rootB := shared(t)
	// Bob moved it to ShardBrowser's Trash: only the zip and its manifest remain.
	_ = os.Remove(filepath.Join(rootB, "profiles", tID+".json"))
	_ = os.RemoveAll(filepath.Join(rootB, "user-data", tID))
	w(t, rootB, "trash/"+tID+".json", []byte("{}"))
	b.denied[bob.ID+"|"+tID] = true
	if _, err := B.Enforce(); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(rootB, "trash", tID+".zip")) || exists(filepath.Join(rootB, "trash", tID+".json")) {
		t.Fatal("Trash copy should be deleted")
	}
}

func TestEnforceRemovesNothingWhenServerUnreachable(t *testing.T) {
	b, B, _, rootB := shared(t)
	b.denied[bob.ID+"|"+tID] = true
	b.verdictErr = errors.New("network down")
	if _, err := B.Enforce(); err == nil {
		t.Fatal("expected the error to be reported")
	}
	assertKept(t, rootB, tID)
}

func TestEnforceWaitsForOpenBrowser(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("running-profile detection on Windows uses a locked file")
	}
	b, B, _, rootB := shared(t)
	host, _ := os.Hostname()
	lock := filepath.Join(rootB, "user-data", tID, "SingletonLock")
	if err := os.Symlink(fmt.Sprintf("%s-%d", host, os.Getpid()), lock); err != nil {
		t.Fatal(err)
	}
	b.denied[bob.ID+"|"+tID] = true
	res, err := B.Enforce()
	if err != nil || len(res.Pending) != 1 || len(res.Removed) != 0 {
		t.Fatalf("open profile should wait: %v %+v", err, res)
	}
	assertKept(t, rootB, tID)
	_ = os.Remove(lock)
	if res, err = B.Enforce(); err != nil || len(res.Removed) != 1 {
		t.Fatalf("closed profile should go: %v %+v", err, res)
	}
	assertGone(t, B, rootB, tID)
}

func TestEnforceNeverWipesAdmin(t *testing.T) {
	b, B, fb, rootB := shared(t)
	fb.role = RoleAdmin
	b.denied[bob.ID+"|"+tID] = true // an admin has access regardless of grants
	if res, err := B.Enforce(); err != nil || len(res.Removed) != 0 {
		t.Fatalf("admin keeps everything: %v %+v", err, res)
	}
	assertKept(t, rootB, tID)
}

func TestRemovedMemberKeepsOnlyOwnProfiles(t *testing.T) {
	b, B, _, rootB := shared(t)
	b.removed[bob.ID] = true
	if _, err := B.Enforce(); err != nil {
		t.Fatal(err)
	}
	assertGone(t, B, rootB, tID)
	assertKept(t, rootB, tLocal)
}
