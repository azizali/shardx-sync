package core

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// Top-level folders in a Chromium user-data dir that are pure caches or component downloads.
// Chromium re-creates them; skipping them makes syncs 2-4x smaller.
var skipRootDirs = toSet(
	"component_crx_cache", "extensions_crx_cache", "GraphiteDawnCache", "GPUPersistentCache", "GrShaderCache",
	"ShaderCache", "WidevineCdm", "WasmTtsEngine", "OnDeviceHeadSuggestModel", "OptimizationHints",
	"optimization_guide_model_store", "Safe Browsing", "Subresource Filter", "ZxcvbnData", "ActorSafetyLists",
	"CertificateRevocation", "PKIMetadata", "Crowd Deny", "FileTypePolicies", "MEIPreload", "SSLErrorAssistant",
	"TrustTokenKeyCommitments", "FirstPartySetsPreloaded", "OriginTrials", "PrivacySandboxAttestationsPreloaded",
	"SafetyTips", "AmountExtractionHeuristicRegexes", "CaptchaProviders", "download_cache", "Crashpad",
	"BrowserMetrics", "Webstore Downloads", "screen_ai", "hyphen-data",
)

// Cache folders inside a browser profile folder (Default, Profile 1, ...).
var skipProfileDirs = toSet("Cache", "Code Cache", "GPUCache", "DawnGraphiteCache", "DawnWebGPUCache", "GrShaderCache", "ShaderCache")

// Never sync these: lock files of a running browser and OS junk.
var skipFiles = toSet("SingletonLock", "SingletonCookie", "SingletonSocket", "lockfile", ".DS_Store", "Thumbs.db", "desktop.ini")

var profileDirRe = regexp.MustCompile(`^Profile \d+$`)
var safeIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func toSet(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, s := range items {
		m[s] = true
	}
	return m
}

func isBrowserProfileDir(name string) bool {
	return name == "Default" || name == "Guest Profile" || name == "System Profile" || profileDirRe.MatchString(name)
}

// IncludeUserDataPath decides whether a "/"-separated path under user-data/<id>/ is synced.
func IncludeUserDataPath(rel string, isDir, skipCaches bool) bool {
	parts := strings.Split(rel, "/")
	name := parts[len(parts)-1]
	if skipFiles[name] {
		return false
	}
	if !skipCaches {
		return true
	}
	if len(parts) == 3 { // user-data/<id>/<name>
		if isDir && skipRootDirs[name] {
			return false
		}
		if strings.HasPrefix(name, "BrowserMetrics") {
			return false
		}
	}
	if len(parts) == 4 && isDir && isBrowserProfileDir(parts[2]) && skipProfileDirs[name] {
		return false
	}
	return true
}

// Profile is the summary of a ShardX profile read from profiles/<id>.json.
type Profile struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Folder         string   `json:"folder"`
	ProxyID        string   `json:"proxyId"`
	Extensions     []string `json:"extensions"`
	LastLaunchedAt *int64   `json:"lastLaunchedAt"` // unix ms
	Temporary      bool     `json:"-"`              // ShardBrowser's API-made profiles; never synced
}

func ReadProfile(root, id string) (*Profile, error) {
	b, err := os.ReadFile(filepath.Join(root, "profiles", id+".json"))
	if err != nil {
		return nil, err
	}
	var raw struct {
		Name string `json:"name"`
		Meta struct {
			Folder         string   `json:"folder"`
			ProxyID        *string  `json:"proxy_id"`
			Extensions     []string `json:"extensions"`
			LastLaunchedAt any      `json:"last_launched_at"`
			Temporary      bool     `json:"temporary"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	p := &Profile{ID: id, Name: raw.Name, Folder: raw.Meta.Folder, Extensions: raw.Meta.Extensions, Temporary: raw.Meta.Temporary}
	if p.Name == "" {
		p.Name = id
	}
	if raw.Meta.ProxyID != nil {
		p.ProxyID = *raw.Meta.ProxyID
	}
	if p.Extensions == nil {
		p.Extensions = []string{}
	}
	if s, ok := raw.Meta.LastLaunchedAt.(string); ok { // "@1791471430"
		if sec, err := strconv.ParseInt(strings.TrimPrefix(s, "@"), 10, 64); err == nil {
			ms := sec * 1000
			p.LastLaunchedAt = &ms
		}
	}
	return p, nil
}

func ListLocalProfiles(root string) ([]*Profile, error) {
	entries, err := os.ReadDir(filepath.Join(root, "profiles"))
	if err != nil {
		return nil, nil
	}
	var out []*Profile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if p, err := ReadProfile(root, strings.TrimSuffix(e.Name(), ".json")); err == nil && !p.Temporary {
			out = append(out, p)
		}
	}
	return out, nil
}

// FileEntry is one file in the archive, path relative to the ShardX root with "/" separators.
type FileEntry struct {
	Rel   string
	Size  int64
	Mtime int64 // unix ms
}

// CollectFiles lists every file that goes into the archive for a profile.
func CollectFiles(root string, p *Profile, skipCaches bool) ([]FileEntry, error) {
	var files []FileEntry
	add := func(rel string) {
		st, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
		if err == nil && st.Mode().IsRegular() {
			files = append(files, FileEntry{Rel: rel, Size: st.Size(), Mtime: st.ModTime().UnixMilli()})
		}
	}
	var walk func(rel string, filter func(string, bool) bool)
	walk = func(rel string, filter func(string, bool) bool) {
		entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return
		}
		for _, e := range entries {
			child := rel + "/" + e.Name()
			if e.Type()&os.ModeSymlink != 0 {
				continue
			}
			if !filter(child, e.IsDir()) {
				continue
			}
			if e.IsDir() {
				walk(child, filter)
			} else if e.Type().IsRegular() {
				add(child)
			}
		}
	}
	add("profiles/" + p.ID + ".json")
	walk("user-data/"+p.ID, func(rel string, isDir bool) bool { return IncludeUserDataPath(rel, isDir, skipCaches) })
	for _, ext := range p.Extensions {
		if safeIDRe.MatchString(ext) {
			walk("extensions/"+ext, func(rel string, _ bool) bool { return !skipFiles[path.Base(rel)] })
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Rel < files[j].Rel })
	return files, nil
}

// Fingerprint is a cheap change detector: hash of every synced file's path, size and modified time.
func Fingerprint(files []FileEntry) string {
	h := sha1.New()
	for _, f := range files {
		fmt.Fprintf(h, "%s|%d|%d\n", f.Rel, f.Size, f.Mtime)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TotalBytes(files []FileEntry) int64 {
	var n int64
	for _, f := range files {
		n += f.Size
	}
	return n
}

// IsProfileRunning reports whether this profile's browser window is open right now.
func IsProfileRunning(root, id string) bool {
	dir := filepath.Join(root, "user-data", id)
	if runtime.GOOS == "windows" {
		lock := filepath.Join(dir, "lockfile")
		if _, err := os.Stat(lock); err != nil {
			return false
		}
		f, err := os.OpenFile(lock, os.O_RDWR, 0)
		if err != nil {
			return true // held open exclusively by Chromium
		}
		f.Close()
		return false
	}
	target, err := os.Readlink(filepath.Join(dir, "SingletonLock")) // "hostname-pid"
	if err != nil {
		return false
	}
	i := strings.LastIndex(target, "-")
	if i < 0 {
		return true
	}
	host, _ := os.Hostname()
	pid, err := strconv.Atoi(target[i+1:])
	if target[:i] != host || err != nil || pid <= 0 {
		return true // can't verify, be safe
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil || err == syscall.EPERM
}

var launcherRe = regexp.MustCompile(`(?i)shardx[- ]launcher`)

// IsLauncherRunning reports whether the ShardX Launcher app itself is open.
func IsLauncherRunning() bool {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("tasklist", "/FO", "CSV", "/NH")
	} else {
		cmd = exec.Command("ps", "-A", "-o", "comm=")
	}
	hideWindow(cmd)
	out, err := cmd.Output()
	return err == nil && launcherRe.Match(out)
}
