package core

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// fakeBackend is an in-memory cloud that mirrors the rules in neon/setup.sql.
type fakeBackend struct {
	mu       sync.Mutex
	rows     map[string]*Row
	files    map[string][]byte
	settings map[string]string
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{rows: map[string]*Row{}, files: map[string][]byte{}, settings: map[string]string{}}
}

// fakeCloud is one signed-in user talking to the shared fakeBackend.
type fakeCloud struct {
	b    *fakeBackend
	user User
	// beforeCommit lets a test inject a competing upload.
	beforeCommit func()
}

func (c *fakeCloud) holder(r *Row, machine string) bool {
	return r.LockedBy != "" && !(r.LockedBy == c.user.ID && r.LockedFrom == machine)
}

func (c *fakeCloud) ListProfiles() ([]Row, error) {
	c.b.mu.Lock()
	defer c.b.mu.Unlock()
	var out []Row
	for _, r := range c.b.rows {
		out = append(out, *r)
	}
	return out, nil
}

func (c *fakeCloud) GetProfile(id string) (*Row, error) {
	c.b.mu.Lock()
	defer c.b.mu.Unlock()
	if r, ok := c.b.rows[id]; ok {
		cp := *r
		return &cp, nil
	}
	return nil, nil
}

func (c *fakeCloud) ClaimLock(id, machine string, force bool) (*Row, error) {
	c.b.mu.Lock()
	defer c.b.mu.Unlock()
	r := c.b.rows[id]
	if r == nil {
		return nil, Errf("CLOUD", "missing")
	}
	if c.holder(r, machine) && !force {
		return nil, Errf("LOCKED", "In use by %s on %s.", r.LockedByEmail, r.LockedFrom)
	}
	r.LockedBy, r.LockedByEmail, r.LockedFrom, r.LockedAt = c.user.ID, c.user.Email, machine, time.Now().Format(time.RFC3339)
	cp := *r
	return &cp, nil
}

func (c *fakeCloud) ReleaseLock(id, machine string, force bool) (*Row, error) {
	c.b.mu.Lock()
	defer c.b.mu.Unlock()
	r := c.b.rows[id]
	if c.holder(r, machine) && !force {
		return nil, Errf("LOCKED", "locked")
	}
	r.LockedBy, r.LockedByEmail, r.LockedFrom, r.LockedAt = "", "", "", ""
	cp := *r
	return &cp, nil
}

func (c *fakeCloud) CommitVersion(a CommitArgs) (*Row, error) {
	if c.beforeCommit != nil {
		f := c.beforeCommit
		c.beforeCommit = nil
		f()
	}
	c.b.mu.Lock()
	defer c.b.mu.Unlock()
	r := c.b.rows[a.ID]
	if r == nil {
		if a.Expected != 0 {
			return nil, Errf("CONFLICT", "conflict")
		}
		r = &Row{ID: a.ID}
		c.b.rows[a.ID] = r
	} else {
		if r.Version != a.Expected {
			return nil, Errf("CONFLICT", "conflict")
		}
		if c.holder(r, a.Machine) {
			return nil, Errf("LOCKED", "locked")
		}
	}
	r.Version++
	r.Name, r.Folder, r.Parts, r.SizeBytes, r.Sha256, r.BlobPrefix = a.Name, a.Folder, a.Parts, a.Size, a.Sha256, a.Prefix
	r.UpdatedAt, r.UpdatedBy, r.UpdatedByEmail, r.UpdatedFrom, r.UpdatedOS = time.Now().Format(time.RFC3339), c.user.ID, c.user.Email, a.Machine, a.OS
	if a.KeepLock {
		r.LockedBy, r.LockedByEmail, r.LockedFrom = c.user.ID, c.user.Email, a.Machine
	} else {
		r.LockedBy, r.LockedByEmail, r.LockedFrom = "", "", ""
	}
	cp := *r
	return &cp, nil
}

func (c *fakeCloud) DeleteProfileRow(id string) error {
	c.b.mu.Lock()
	defer c.b.mu.Unlock()
	delete(c.b.rows, id)
	return nil
}

func (c *fakeCloud) Upload(key string, data []byte) error {
	c.b.mu.Lock()
	defer c.b.mu.Unlock()
	c.b.files[key] = append([]byte(nil), data...)
	return nil
}

func (c *fakeCloud) Download(key string) ([]byte, error) {
	c.b.mu.Lock()
	defer c.b.mu.Unlock()
	d, ok := c.b.files[key]
	if !ok {
		return nil, fmt.Errorf("not found: %s", key)
	}
	return d, nil
}

func (c *fakeCloud) RemoveFolder(prefix string) error {
	c.b.mu.Lock()
	defer c.b.mu.Unlock()
	for k := range c.b.files {
		if strings.HasPrefix(k, prefix+"/") {
			delete(c.b.files, k)
		}
	}
	return nil
}

func (c *fakeCloud) ListFolders(id string) ([]string, error) {
	c.b.mu.Lock()
	defer c.b.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for k := range c.b.files {
		if strings.HasPrefix(k, id+"/") {
			parts := strings.SplitN(k, "/", 3)
			f := parts[0] + "/" + parts[1]
			if !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func (c *fakeCloud) GetTeamSetting(key string) (string, bool, error) {
	c.b.mu.Lock()
	defer c.b.mu.Unlock()
	v, ok := c.b.settings[key]
	return v, ok, nil
}

func (c *fakeCloud) InsertTeamSetting(key, value string) error {
	c.b.mu.Lock()
	defer c.b.mu.Unlock()
	c.b.settings[key] = value
	return nil
}
