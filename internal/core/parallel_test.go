package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const tID2 = "22222222-3333-4444-5555-666666666666"

// twoProfiles adds a second profile, with its own proxy, to a ShardX folder made by makeShardx.
func twoProfiles(t *testing.T, root string) {
	t.Helper()
	prof, _ := json.Marshal(map[string]any{"name": "Bob Shop", "_meta": map[string]any{"id": tID2, "proxy_id": "px2"}})
	w(t, root, "profiles/"+tID2+".json", prof)
	w(t, root, "user-data/"+tID2+"/Default/Bookmarks", []byte(`{"roots":{}}`))
	w(t, root, "proxies.json", []byte(`{"proxies":[{"id":"px1","host":"1.2.3.4","port":1080},{"id":"px2","host":"5.6.7.8","port":1080}]}`))
}

func parallel(fns ...func() error) []error {
	errs := make([]error, len(fns))
	var wg sync.WaitGroup
	for i, fn := range fns {
		wg.Add(1)
		go func() { defer wg.Done(); errs[i] = fn() }()
	}
	wg.Wait()
	return errs
}

func TestDifferentProfilesSyncInParallel(t *testing.T) {
	b := newFakeBackend()
	rootA, rootB := makeShardx(t), emptyShardx(t)
	twoProfiles(t, rootA)
	A, _ := newEng(t, b, rootA, alice, "mac-a", "team-pass")
	B, _ := newEng(t, b, rootB, bob, "mac-b", "team-pass")
	for _, err := range parallel(
		func() error { _, err := A.Push(tID, PushOptions{}); return err },
		func() error { _, err := A.Push(tID2, PushOptions{}); return err },
	) {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, err := range parallel(
		func() error { _, err := B.Pull(tID, PullOptions{}); return err },
		func() error { _, err := B.Pull(tID2, PullOptions{}); return err },
	) {
		if err != nil {
			t.Fatal(err)
		}
	}
	// Both downloads merged their proxy into the one shared proxies.json.
	var store struct {
		Proxies []struct {
			ID string `json:"id"`
		} `json:"proxies"`
	}
	if err := json.Unmarshal(read(t, rootB, "proxies.json"), &store); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, p := range store.Proxies {
		got[p.ID] = true
	}
	if !got["px1"] || !got["px2"] {
		t.Fatalf("both proxies should be merged, got %+v", store.Proxies)
	}
}

func TestSameProfileIsBusy(t *testing.T) {
	b := newFakeBackend()
	A, _ := newEng(t, b, makeShardx(t), alice, "mac-a", "team-pass")
	release, err := A.begin(tID, "Downloading")
	if err != nil {
		t.Fatal(err)
	}
	_, err = A.Push(tID, PushOptions{})
	mustCode(t, err, "BUSY")
	_, err = A.TakeLock(tID, false)
	mustCode(t, err, "BUSY")
	release()
	if _, err := A.Push(tID, PushOptions{}); err != nil {
		t.Fatalf("free again after release: %v", err)
	}
}

func TestTransfersBeyondTheLimitQueue(t *testing.T) {
	b := newFakeBackend()
	A, _ := newEng(t, b, makeShardx(t), alice, "mac-a", "team-pass")
	var mu sync.Mutex
	var steps []string
	A.OnProgress = func(p Progress) { mu.Lock(); steps = append(steps, p.Step); mu.Unlock() }
	for i := 0; i < MaxParallel; i++ {
		A.slots <- struct{}{} // every slot taken by other transfers
	}
	done := make(chan error, 1)
	go func() { _, err := A.Push(tID, PushOptions{}); done <- err }()
	deadline := time.After(5 * time.Second)
	for queued := false; !queued; {
		select {
		case <-deadline:
			t.Fatal("upload never reported Queued")
		case <-time.After(10 * time.Millisecond):
			mu.Lock()
			queued = len(steps) > 0 && steps[0] == "Queued"
			mu.Unlock()
		}
	}
	select {
	case err := <-done:
		t.Fatalf("upload must wait for a slot, finished early: %v", err)
	default:
	}
	<-A.slots // one transfer finishes
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("upload did not start once a slot was free")
	}
}

func TestEnforceSkipsProfileThatIsSyncing(t *testing.T) {
	b, B, _, rootB := shared(t)
	b.denied[bob.ID+"|"+tID] = true
	release, err := B.begin(tID, "Downloading")
	if err != nil {
		t.Fatal(err)
	}
	if res, err := B.Enforce(); err != nil || len(res.Removed) != 0 {
		t.Fatalf("a syncing profile must not be removed: %v %+v", err, res)
	}
	if _, err := os.Stat(filepath.Join(rootB, "profiles", tID+".json")); err != nil {
		t.Fatal("profile should still be here")
	}
	release()
	if res, err := B.Enforce(); err != nil || len(res.Removed) != 1 {
		t.Fatalf("removed once the sync is done: %v %+v", err, res)
	}
}
