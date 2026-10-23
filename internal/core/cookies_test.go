package core

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func sortedCookies(c []Cookie) []Cookie {
	out := append([]Cookie(nil), c...)
	sort.Slice(out, func(i, j int) bool { return out[i].Domain+out[i].Name < out[j].Domain+out[j].Name })
	return out
}

func sampleCookies() []Cookie {
	exp := 1893456000.0
	return []Cookie{
		{Domain: ".example.com", Name: "session", Value: "abc123", Path: "/", Expires: &exp, Secure: true, HTTPOnly: true, SameSite: "Lax"},
		{Domain: "shop.test", Name: "cart", Value: "k=v; with spaces", Path: "/shop", SameSite: "None"},
	}
}

func TestCookiesRoundTripThroughDatabase(t *testing.T) {
	udd := t.TempDir()
	if err := ImportCookies(udd, sampleCookies()); err != nil {
		t.Fatal(err)
	}
	// The value must not sit in clear text on disk.
	raw := read(t, udd, "Default/Cookies")
	if bytes.Contains(raw, []byte("abc123")) {
		t.Fatal("cookie value stored in clear text")
	}
	got, err := ExportCookies(udd, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want := sortedCookies(sampleCookies())
	got = sortedCookies(got)
	if len(got) != len(want) {
		t.Fatalf("want %d cookies, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i].Domain != want[i].Domain || got[i].Name != want[i].Name || got[i].Value != want[i].Value ||
			got[i].Path != want[i].Path || got[i].Secure != want[i].Secure || got[i].HTTPOnly != want[i].HTTPOnly ||
			got[i].SameSite != want[i].SameSite {
			t.Fatalf("cookie %d: want %+v, got %+v", i, want[i], got[i])
		}
	}
	if got[0].Expires == nil || *got[0].Expires != 1893456000 || got[1].Expires != nil {
		t.Fatalf("expiry lost: %+v", got[0])
	}
}

func TestImportReplacesTheStore(t *testing.T) {
	udd := t.TempDir()
	if err := ImportCookies(udd, sampleCookies()); err != nil {
		t.Fatal(err)
	}
	if err := ImportCookies(udd, sampleCookies()[:1]); err != nil {
		t.Fatal(err)
	}
	got, err := ExportCookies(udd, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "session" {
		t.Fatalf("import should replace the store, got %+v", got)
	}
}

func TestExportWithoutCookieStore(t *testing.T) {
	got, err := ExportCookies(t.TempDir(), t.TempDir())
	if err != nil || len(got) != 0 {
		t.Fatalf("empty profile: %v %+v", err, got)
	}
}

func TestCookiesTravelBetweenComputers(t *testing.T) {
	b := newFakeBackend()
	rootA, rootB := makeShardx(t), emptyShardx(t)
	udA := filepath.Join(rootA, "user-data", tID)
	if err := os.Remove(filepath.Join(udA, "Default", "Cookies")); err != nil { // makeShardx leaves a placeholder here
		t.Fatal(err)
	}
	if err := ImportCookies(udA, sampleCookies()); err != nil {
		t.Fatal(err)
	}
	A, _ := newEng(t, b, rootA, alice, "mac-a", "team-pass")
	B, _ := newEng(t, b, rootB, bob, "win-b", "team-pass")
	if _, err := A.Push(tID, PushOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := B.Pull(tID, PullOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err := ExportCookies(filepath.Join(rootB, "user-data", tID), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(sampleCookies()) {
		t.Fatalf("B should have %d cookies, got %+v", len(sampleCookies()), got)
	}
	// cookies.json is only an archive entry; it must not land in the ShardX folder.
	if exists(filepath.Join(rootB, CookiesFile)) {
		t.Fatal("cookies.json was installed into the ShardX folder")
	}
}

func TestMissingCookieExportDoesNotBlockPush(t *testing.T) {
	b := newFakeBackend()
	root := makeShardx(t) // its Cookies file is junk, so the export fails
	A, _ := newEng(t, b, root, alice, "mac-a", "team-pass")
	var last Progress
	A.OnProgress = func(p Progress) { last = p }
	if _, err := A.Push(tID, PushOptions{}); err != nil {
		t.Fatalf("push should still work: %v", err)
	}
	if last.Detail == "" {
		t.Fatal("expected a note that cookies were not exported")
	}
}
