package core

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const keepBackups = 3

type Progress struct {
	ID     string `json:"id"`
	Step   string `json:"step"`
	Pct    int    `json:"pct"`
	Detail string `json:"detail"`
}

type syncState struct {
	Version     int    `json:"version"`
	Fingerprint string `json:"fingerprint"`
	At          string `json:"at"`
}

// stateStore remembers, per profile, which cloud version this computer last synced and what the files looked like.
type stateStore struct {
	file string
	mu   sync.Mutex
	data map[string]syncState
}

func newStateStore(file string) *stateStore {
	s := &stateStore{file: file, data: map[string]syncState{}}
	if b, err := os.ReadFile(file); err == nil {
		_ = json.Unmarshal(b, &s.data)
	}
	return s
}

func (s *stateStore) get(id string) (syncState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[id]
	return v, ok
}

func (s *stateStore) set(id string, v *syncState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v == nil {
		delete(s.data, id)
	} else {
		s.data[id] = *v
	}
	b, _ := json.MarshalIndent(s.data, "", "  ")
	_ = writeFileAtomic(s.file, b)
}

type Engine struct {
	Cloud      Backend
	Key        []byte
	Root       string // ShardX data folder
	WorkDir    string // this app's data folder
	Machine    string
	User       User
	SkipCaches bool
	OnProgress func(Progress)

	state     *stateStore
	tmpDir    string
	backupDir string
}

func NewEngine(e Engine) *Engine {
	e.state = newStateStore(filepath.Join(e.WorkDir, "sync-state.json"))
	e.tmpDir = filepath.Join(e.WorkDir, "tmp")
	e.backupDir = filepath.Join(e.WorkDir, "backups")
	return &e
}

func (e *Engine) BackupDir() string { return e.backupDir }

func (e *Engine) progress(id, step string, pct int, detail string) {
	if e.OnProgress != nil {
		e.OnProgress(Progress{ID: id, Step: step, Pct: pct, Detail: detail})
	}
}

func (e *Engine) isMine(r *Row) bool {
	return r != nil && r.LockedBy == e.User.ID && r.LockedFrom == e.Machine
}

func (e *Engine) localFingerprint(p *Profile) ([]FileEntry, string, error) {
	files, err := CollectFiles(e.Root, p, e.SkipCaches)
	if err != nil {
		return nil, "", err
	}
	return files, Fingerprint(files), nil
}

func (e *Engine) CleanTmp() { _ = os.RemoveAll(e.tmpDir) }

// ---------- overview ----------

type LockInfo struct {
	Email   string `json:"email"`
	Machine string `json:"machine"`
	At      string `json:"at"`
	Mine    bool   `json:"mine"`
}

type RemoteInfo struct {
	Version     int       `json:"version"`
	SizeBytes   int64     `json:"sizeBytes"`
	UpdatedAt   string    `json:"updatedAt"`
	UpdatedBy   string    `json:"updatedBy"`
	UpdatedFrom string    `json:"updatedFrom"`
	UpdatedOS   string    `json:"updatedOs"`
	Lock        *LockInfo `json:"lock"`
	Deleted     bool      `json:"deleted"` // soft-deleted; only admins see these rows
}

type Item struct {
	ID             string      `json:"id"`
	Name           string      `json:"name"`
	Folder         string      `json:"folder"`
	Status         string      `json:"status"`
	Running        bool        `json:"running"`
	LocalBytes     *int64      `json:"localBytes"`
	LastLaunchedAt *int64      `json:"lastLaunchedAt"`
	LocalVersion   *int        `json:"localVersion"`
	LastSyncedHere *string     `json:"lastSyncedHere"`
	Remote         *RemoteInfo `json:"remote"`
}

// Overview combines local profiles and cloud rows into the list the UI shows.
func (e *Engine) Overview() ([]Item, error) {
	locals, _ := ListLocalProfiles(e.Root)
	rows, err := e.Cloud.ListProfiles()
	if err != nil {
		return nil, err
	}
	type pair struct {
		local  *Profile
		remote *Row
	}
	byID := map[string]*pair{}
	for _, l := range locals {
		byID[l.ID] = &pair{local: l}
	}
	for i := range rows {
		r := &rows[i]
		if p, ok := byID[r.ID]; ok {
			p.remote = r
		} else {
			byID[r.ID] = &pair{remote: r}
		}
	}
	items := make([]Item, 0, len(byID))
	for id, p := range byID {
		st, hasState := e.state.get(id)
		it := Item{ID: id}
		if p.local != nil {
			files, fp, _ := e.localFingerprint(p.local)
			n := TotalBytes(files)
			it.LocalBytes = &n
			it.Running = IsProfileRunning(e.Root, id)
			it.Name, it.Folder, it.LastLaunchedAt = p.local.Name, p.local.Folder, p.local.LastLaunchedAt
			changed := !hasState || fp != st.Fingerprint
			switch {
			case p.remote == nil:
				it.Status = "local-only"
			case !hasState:
				it.Status = "unlinked"
			default:
				newer := p.remote.Version > st.Version
				switch {
				case changed && newer:
					it.Status = "conflict"
				case changed:
					it.Status = "local-changes"
				case newer:
					it.Status = "cloud-newer"
				default:
					it.Status = "synced"
				}
			}
		} else {
			it.Status = "cloud-only"
			it.Name, it.Folder = p.remote.Name, p.remote.Folder
		}
		if hasState {
			v, at := st.Version, st.At
			it.LocalVersion, it.LastSyncedHere = &v, &at
		}
		if r := p.remote; r != nil {
			ri := &RemoteInfo{Version: r.Version, SizeBytes: r.SizeBytes, UpdatedAt: r.UpdatedAt, UpdatedBy: r.UpdatedByEmail, UpdatedFrom: r.UpdatedFrom, UpdatedOS: r.UpdatedOS, Deleted: r.DeletedAt != ""}
			if r.LockedBy != "" {
				ri.Lock = &LockInfo{Email: r.LockedByEmail, Machine: r.LockedFrom, At: r.LockedAt, Mine: e.isMine(r)}
			}
			it.Remote = ri
		}
		if it.Name == "" {
			it.Name = id
		}
		items = append(items, it)
	}
	sort.Slice(items, func(i, j int) bool { return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name) })
	return items, nil
}

// ---------- upload ----------

type PushOptions struct {
	KeepLock  bool `json:"keepLock"`
	Overwrite bool `json:"overwrite"`
}

type archiveMeta struct {
	Format       int             `json:"format"`
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Extensions   []string        `json:"extensions"`
	Proxy        json.RawMessage `json:"proxy"`
	FileCount    int             `json:"fileCount"`
	RawBytes     int64           `json:"rawBytes"`
	ArchiveBytes int64           `json:"archiveBytes"`
	Sha256       string          `json:"sha256"`
	OS           string          `json:"os"`
	Machine      string          `json:"machine"`
	CreatedAt    string          `json:"createdAt"`
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (e *Engine) Push(id string, opt PushOptions) (*Row, error) {
	e.progress(id, "Checking", 0, "")
	p, err := ReadProfile(e.Root, id)
	if err != nil {
		return nil, Errf("MISSING", "This profile is not on this computer.")
	}
	if IsProfileRunning(e.Root, id) {
		return nil, Errf("RUNNING", "Close the %q browser window first, then upload.", p.Name)
	}
	remote, err := e.Cloud.GetProfile(id)
	if err != nil {
		return nil, err
	}
	st, hasState := e.state.get(id)
	expected := 0
	if remote != nil {
		if remote.LockedBy != "" && !e.isMine(remote) {
			return nil, Errf("LOCKED", "In use by %s on %s. They must upload or release it first.", remote.LockedByEmail, remote.LockedFrom)
		}
		if (!hasState || remote.Version > st.Version) && !opt.Overwrite {
			return nil, Errf("CONFIRM_OVERWRITE_CLOUD", "The cloud has version %d from %s (%s) that you have not downloaded. Uploading will replace it.",
				remote.Version, remote.UpdatedByEmail, remote.UpdatedFrom)
		}
		expected = remote.Version
	}

	e.progress(id, "Scanning files", 2, "")
	files, fp, err := e.localFingerprint(p)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(e.tmpDir, 0o755); err != nil {
		return nil, err
	}
	archive := filepath.Join(e.tmpDir, id+"-"+randHex(4)+".tar.gz")
	defer os.Remove(archive)
	prefix := id + "/" + strconv.FormatInt(time.Now().UnixMilli(), 36) + "-" + randHex(4)
	committed := false
	defer func() {
		if !committed {
			_ = e.Cloud.RemoveFolder(prefix)
		}
	}()

	raw := TotalBytes(files)
	e.progress(id, fmt.Sprintf("Compressing %d files", len(files)), 5, FormatBytes(raw))
	extra, cookieNote := e.cookieBundle(id)
	size, sha, err := PackToFile(e.Root, files, extra, archive)
	if err != nil {
		return nil, err
	}

	meta := archiveMeta{Format: 1, ID: id, Name: p.Name, FileCount: len(files), RawBytes: raw, ArchiveBytes: size, Sha256: sha,
		OS: runtime.GOOS, Machine: e.Machine, CreatedAt: time.Now().UTC().Format(time.RFC3339), Proxy: json.RawMessage("null")}
	meta.Proxy = e.findProxy(p.ProxyID)
	for _, ext := range p.Extensions {
		for _, f := range files {
			if strings.HasPrefix(f.Rel, "extensions/"+ext+"/") {
				meta.Extensions = append(meta.Extensions, ext)
				break
			}
		}
	}
	mb, _ := json.Marshal(meta)
	enc, err := Encrypt(e.Key, mb, MetaAAD(id, prefix))
	if err != nil {
		return nil, err
	}
	if err := e.Cloud.Upload(prefix+"/meta", enc); err != nil {
		return nil, err
	}
	parts, err := ForEachEncryptedPart(e.Key, archive, id, prefix, func(i, count int, data []byte) error {
		e.progress(id, fmt.Sprintf("Uploading part %d of %d", i+1, count), 10+85*i/count, FormatBytes(size))
		return e.Cloud.Upload(fmt.Sprintf("%s/part-%04d", prefix, i), data)
	})
	if err != nil {
		return nil, err
	}

	e.progress(id, "Publishing", 96, "")
	row, err := e.Cloud.CommitVersion(CommitArgs{ID: id, Expected: expected, Name: p.Name, Folder: p.Folder, Parts: parts, Size: size,
		Sha256: sha, Prefix: prefix, Machine: e.Machine, OS: runtime.GOOS, KeepLock: opt.KeepLock})
	if err != nil {
		return nil, err
	}
	committed = true
	e.state.set(id, &syncState{Version: row.Version, Fingerprint: fp, At: time.Now().UTC().Format(time.RFC3339)})

	e.progress(id, "Cleaning up old versions", 98, "")
	if folders, err := e.Cloud.ListFolders(id); err == nil {
		for _, f := range folders {
			if f != prefix {
				_ = e.Cloud.RemoveFolder(f)
			}
		}
	}
	e.progress(id, fmt.Sprintf("Uploaded version %d", row.Version), 100, cookieNote)
	return row, nil
}

// cookieBundle puts the profile's decrypted cookies into the archive as cookies.json. A failure is not
// fatal: the Cookies database still uploads, and the returned note tells the user what may not carry over.
func (e *Engine) cookieBundle(id string) (map[string][]byte, string) {
	cookies, err := ExportCookies(filepath.Join(e.Root, "user-data", id), e.tmpDir)
	if err != nil {
		return nil, "Cookies were not exported, so site logins may need to be redone: " + err.Error()
	}
	if cookies == nil {
		cookies = []Cookie{}
	}
	b, _ := json.Marshal(cookies)
	return map[string][]byte{CookiesFile: b}, ""
}

func (e *Engine) findProxy(proxyID string) json.RawMessage {
	if proxyID == "" {
		return json.RawMessage("null")
	}
	var store struct {
		Proxies []json.RawMessage `json:"proxies"`
	}
	b, err := os.ReadFile(filepath.Join(e.Root, "proxies.json"))
	if err != nil || json.Unmarshal(b, &store) != nil {
		return json.RawMessage("null")
	}
	for _, raw := range store.Proxies {
		var p struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &p) == nil && p.ID == proxyID {
			return raw
		}
	}
	return json.RawMessage("null")
}

// ---------- download ----------

type PullOptions struct {
	TakeLock       bool `json:"takeLock"`
	ForceLock      bool `json:"forceLock"`
	OverwriteLocal bool `json:"overwriteLocal"`
}

type PullResult struct {
	Version int    `json:"version"`
	CrossOS bool   `json:"crossOs"`
	FromOS  string `json:"fromOs"`
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func (e *Engine) Pull(id string, opt PullOptions) (res *PullResult, err error) {
	e.progress(id, "Checking", 0, "")
	remote, err := e.Cloud.GetProfile(id)
	if err != nil {
		return nil, err
	}
	if remote == nil || remote.BlobPrefix == "" {
		return nil, Errf("MISSING", "This profile is not in the cloud.")
	}
	if remote.LockedBy != "" && !e.isMine(remote) && !opt.ForceLock {
		return nil, &SyncError{Code: "LOCKED", Message: fmt.Sprintf("In use by %s on %s. Downloading now would give you an outdated copy.", remote.LockedByEmail, remote.LockedFrom),
			Data: map[string]any{"email": remote.LockedByEmail, "machine": remote.LockedFrom}}
	}
	if exists(filepath.Join(e.Root, "profiles", id+".json")) {
		if IsProfileRunning(e.Root, id) {
			return nil, Errf("RUNNING", "Close this profile's browser window first, then download.")
		}
		if !opt.OverwriteLocal {
			st, hasState := e.state.get(id)
			p, perr := ReadProfile(e.Root, id)
			if perr == nil {
				_, fp, _ := e.localFingerprint(p)
				if !hasState {
					return nil, Errf("CONFIRM_OVERWRITE_LOCAL", "This profile already exists here and was never synced. Downloading replaces it (a backup is kept).")
				}
				if st.Fingerprint != fp {
					return nil, Errf("CONFIRM_OVERWRITE_LOCAL", "This profile was used here since the last sync. Downloading replaces those changes (a backup is kept).")
				}
			}
		}
	}
	claimedNow := false
	if opt.TakeLock && !e.isMine(remote) {
		if _, err := e.Cloud.ClaimLock(id, e.Machine, opt.ForceLock); err != nil {
			return nil, err
		}
		claimedNow = true
	}
	defer func() {
		if err != nil && claimedNow {
			_, _ = e.Cloud.ReleaseLock(id, e.Machine, false)
		}
	}()

	prefix := remote.BlobPrefix
	if err := os.MkdirAll(e.tmpDir, 0o755); err != nil {
		return nil, err
	}
	archive := filepath.Join(e.tmpDir, id+"-"+randHex(4)+".tar.gz")
	staging := filepath.Join(e.tmpDir, "stage-"+id+"-"+randHex(4))
	defer os.Remove(archive)
	defer os.RemoveAll(staging)

	e.progress(id, "Downloading", 3, "")
	encMeta, err := e.Cloud.Download(prefix + "/meta")
	if err != nil {
		return nil, err
	}
	mb, err := Decrypt(e.Key, encMeta, MetaAAD(id, prefix))
	if err != nil {
		return nil, err
	}
	var meta archiveMeta
	if err := json.Unmarshal(mb, &meta); err != nil {
		return nil, Errf("CORRUPT", "Archive metadata is damaged.")
	}
	out, err := os.Create(archive)
	if err != nil {
		return nil, err
	}
	for i := 0; i < remote.Parts; i++ {
		e.progress(id, fmt.Sprintf("Downloading part %d of %d", i+1, remote.Parts), 5+70*i/remote.Parts, FormatBytes(remote.SizeBytes))
		buf, err := e.Cloud.Download(fmt.Sprintf("%s/part-%04d", prefix, i))
		if err != nil {
			out.Close()
			return nil, err
		}
		plain, err := Decrypt(e.Key, buf, PartAAD(id, prefix, i))
		if err != nil {
			out.Close()
			return nil, err
		}
		if _, err := out.Write(plain); err != nil {
			out.Close()
			return nil, err
		}
	}
	if err := out.Close(); err != nil {
		return nil, err
	}
	if sum, _ := SHA256File(archive); sum != remote.Sha256 {
		return nil, Errf("CORRUPT", "Downloaded data failed its integrity check. Nothing was changed. Try again.")
	}

	e.progress(id, "Unpacking", 78, "")
	if _, err := ExtractProfile(archive, staging, id, meta.Extensions); err != nil {
		return nil, err
	}
	if !exists(filepath.Join(staging, "profiles", id+".json")) {
		return nil, Errf("CORRUPT", "Archive has no profile file. Nothing was changed.")
	}
	cookies, hasCookies, err := readCookiesFile(filepath.Join(staging, CookiesFile))
	if err != nil {
		return nil, err
	}
	e.progress(id, "Backing up current copy", 85, "")
	if err := e.installFromStaging(id, staging, meta); err != nil {
		return nil, err
	}
	cookieNote := ""
	if hasCookies {
		if err := ImportCookies(filepath.Join(e.Root, "user-data", id), cookies); err != nil {
			cookieNote = "Cookies could not be restored here, so site logins may need to be redone: " + err.Error()
		}
	}
	if p, err := ReadProfile(e.Root, id); err == nil {
		_, fp, _ := e.localFingerprint(p)
		e.state.set(id, &syncState{Version: remote.Version, Fingerprint: fp, At: time.Now().UTC().Format(time.RFC3339)})
	}
	e.pruneBackups(id)
	cross := meta.OS != "" && meta.OS != runtime.GOOS
	detail := cookieNote
	if cross {
		detail = strings.TrimSpace("Uploaded from a different OS. Some site logins may need to be redone. " + cookieNote)
	}
	e.progress(id, fmt.Sprintf("Downloaded version %d", remote.Version), 100, detail)
	return &PullResult{Version: remote.Version, CrossOS: cross, FromOS: meta.OS}, nil
}

// installFromStaging swaps the staged profile into the ShardX folder. Rolls back if anything fails.
func (e *Engine) installFromStaging(id, staging string, meta archiveMeta) (err error) {
	stamp := strings.NewReplacer(":", "-", ".", "-").Replace(time.Now().UTC().Format("2006-01-02T15:04:05.000Z"))
	bdir := filepath.Join(e.backupDir, id, stamp)
	type mv struct{ live, backup string }
	var moved []mv
	var installed []string
	defer func() {
		if err == nil {
			return
		}
		for i := len(installed) - 1; i >= 0; i-- {
			_ = os.RemoveAll(installed[i])
		}
		for i := len(moved) - 1; i >= 0; i-- {
			_ = movePath(moved[i].backup, moved[i].live)
		}
		err = Errf("INSTALL", "Could not install the profile, your previous copy was restored: %v", err)
	}()
	backup := func(rel string) error {
		live := filepath.Join(e.Root, filepath.FromSlash(rel))
		if !exists(live) {
			return nil
		}
		b := filepath.Join(bdir, filepath.FromSlash(rel))
		if err := movePath(live, b); err != nil {
			return err
		}
		moved = append(moved, mv{live, b})
		return nil
	}
	rels := []string{"profiles/" + id + ".json", "user-data/" + id}
	if entries, err := os.ReadDir(filepath.Join(staging, "extensions")); err == nil {
		for _, en := range entries {
			rels = append(rels, "extensions/"+en.Name())
		}
	}
	for _, rel := range rels {
		if err := backup(rel); err != nil {
			return err
		}
	}
	proxiesFile := filepath.Join(e.Root, "proxies.json")
	hasProxy := len(meta.Proxy) > 0 && string(meta.Proxy) != "null"
	if hasProxy && exists(proxiesFile) {
		if err := copyFile(proxiesFile, filepath.Join(bdir, "proxies.json")); err != nil {
			return err
		}
	}
	e.progress(id, "Installing", 92, "")
	for _, rel := range rels {
		src := filepath.Join(staging, filepath.FromSlash(rel))
		if !exists(src) {
			continue
		}
		dst := filepath.Join(e.Root, filepath.FromSlash(rel))
		if err := movePath(src, dst); err != nil {
			return err
		}
		installed = append(installed, dst)
	}
	if hasProxy {
		return mergeProxy(proxiesFile, meta.Proxy)
	}
	return nil
}

func mergeProxy(file string, proxy json.RawMessage) error {
	store := map[string]json.RawMessage{}
	if b, err := os.ReadFile(file); err == nil {
		_ = json.Unmarshal(b, &store)
	}
	var list []json.RawMessage
	_ = json.Unmarshal(store["proxies"], &list)
	var newID struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(proxy, &newID)
	kept := list[:0]
	for _, raw := range list {
		var p struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &p) == nil && p.ID == newID.ID {
			continue
		}
		kept = append(kept, raw)
	}
	kept = append(kept, proxy)
	store["proxies"], _ = json.Marshal(kept)
	b, _ := json.MarshalIndent(store, "", "  ")
	return writeFileAtomic(file, b)
}

func (e *Engine) pruneBackups(id string) {
	dir := filepath.Join(e.backupDir, id)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, en := range entries {
		names = append(names, en.Name())
	}
	sort.Strings(names)
	for i := 0; i < len(names)-keepBackups; i++ {
		_ = os.RemoveAll(filepath.Join(dir, names[i]))
	}
}

// ---------- locks & delete ----------

func (e *Engine) ReleaseLock(id string, force bool) (*Row, error) { return e.Cloud.ReleaseLock(id, e.Machine, force) }
func (e *Engine) TakeLock(id string, force bool) (*Row, error)    { return e.Cloud.ClaimLock(id, e.Machine, force) }

// DeleteFromCloud is a soft delete for everyone but admins: the profile disappears from the team's list
// and an admin can restore it. An admin deletes for good, which also removes the uploaded archive.
func (e *Engine) DeleteFromCloud(id string) error {
	remote, err := e.Cloud.GetProfile(id)
	if err != nil || remote == nil {
		return err
	}
	if remote.LockedBy != "" && !e.isMine(remote) {
		return Errf("LOCKED", "In use by %s. Ask them to release it first.", remote.LockedByEmail)
	}
	role, err := e.Cloud.MyRole()
	if err != nil {
		return err
	}
	if role != RoleAdmin {
		return e.Cloud.DeleteProfile(id)
	}
	folders, err := e.Cloud.ListFolders(id)
	if err != nil {
		return err
	}
	for _, f := range folders {
		if err := e.Cloud.RemoveFolder(f); err != nil {
			return err
		}
	}
	if err := e.Cloud.DeleteProfile(id); err != nil {
		return err
	}
	e.state.set(id, nil)
	return nil
}

// ---------- removal of profiles the user may no longer have ----------

// EnforceResult names the profiles Enforce deleted, and those it will delete once their browser window closes.
type EnforceResult struct {
	Removed []string `json:"removed"`
	Pending []string `json:"pending"`
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Enforce deletes this computer's copies of workspace profiles the user may no longer have: their access was
// removed, the profile was deleted, or the user was offboarded. Profiles that were never uploaded are never
// touched. It acts only on an explicit "remove" from the server, so a network error removes nothing.
func (e *Engine) Enforce() (*EnforceResult, error) {
	res := &EnforceResult{Removed: []string{}, Pending: []string{}}
	names := e.localCopies()
	ids := make([]string, 0, len(names))
	for id := range names {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return res, nil
	}
	verdicts, err := e.Cloud.ProfileVerdicts(ids)
	if err != nil {
		return res, err
	}
	for _, id := range ids {
		if verdicts[id] != "remove" {
			continue
		}
		if IsProfileRunning(e.Root, id) {
			res.Pending = append(res.Pending, names[id])
			continue
		}
		if err := e.removeLocal(id); err != nil {
			return res, err
		}
		res.Removed = append(res.Removed, names[id])
	}
	return res, nil
}

// localCopies maps every profile id with a copy on this computer to its name: live profiles, profiles in
// ShardBrowser's Trash, and this app's backups.
func (e *Engine) localCopies() map[string]string {
	names := map[string]string{}
	locals, _ := ListLocalProfiles(e.Root)
	for _, p := range locals {
		names[p.ID] = p.Name
	}
	addDir := func(dir, suffix string) {
		entries, _ := os.ReadDir(dir)
		for _, en := range entries {
			id := strings.TrimSuffix(en.Name(), suffix)
			if _, seen := names[id]; !seen && uuidRe.MatchString(id) {
				names[id] = id
			}
		}
	}
	addDir(filepath.Join(e.Root, "trash"), ".json")
	addDir(e.backupDir, "")
	for id := range names {
		if !uuidRe.MatchString(id) {
			delete(names, id)
		}
	}
	return names
}

// removeLocal deletes every copy of a profile on this computer. The profile file goes last, so a failure
// part-way leaves it listed and the next refresh tries again. Extensions are shared, so they stay.
func (e *Engine) removeLocal(id string) error {
	for _, p := range []string{
		filepath.Join(e.Root, "user-data", id),
		filepath.Join(e.Root, "trash", id+".zip"),
		filepath.Join(e.Root, "trash", id+".json"),
		filepath.Join(e.backupDir, id),
		filepath.Join(e.Root, "profiles", id+".json"),
	} {
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	e.state.set(id, nil)
	return nil
}

// ---------- team key check ----------

const keyCheckPlain = "shardx-sync-key-check-v1"

// VerifyTeamKey stores an encrypted marker the first time, and later checks the passphrase decrypts it.
func VerifyTeamKey(c Backend, key []byte) error {
	val, found, err := c.GetTeamSetting("key_check")
	if err != nil {
		return err
	}
	if !found {
		enc, err := Encrypt(key, []byte(keyCheckPlain), "keycheck")
		if err != nil {
			return err
		}
		return c.InsertTeamSetting("key_check", base64.StdEncoding.EncodeToString(enc))
	}
	blob, err := base64.StdEncoding.DecodeString(val)
	if err == nil {
		if plain, err := Decrypt(key, blob, "keycheck"); err == nil && string(plain) == keyCheckPlain {
			return nil
		}
	}
	return Errf("PASSPHRASE", "Wrong team passphrase. It must match the one your team set up first.")
}

// ---------- file helpers ----------

func FormatBytes(n int64) string {
	units := []string{"B", "KB", "MB", "GB"}
	f := float64(n)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

func writeFileAtomic(file string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	tmp := file + "." + randHex(4) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// movePath renames, falling back to copy+delete across volumes.
func movePath(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}
	var le *os.LinkError
	if !errors.As(err, &le) || !errors.Is(le.Err, syscall.EXDEV) {
		return err
	}
	if err := copyTree(src, dst); err != nil {
		return err
	}
	return os.RemoveAll(src)
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if err := copyFile(p, target); err != nil {
			return err
		}
		return os.Chtimes(target, info.ModTime(), info.ModTime())
	})
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
