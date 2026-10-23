package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/azizali/shardx-sync/internal/core"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

var version = "dev" // set at build time with -ldflags "-X main.version=1.2.3"

// Config is the non-secret settings file (config.json in the app data folder).
type Config struct {
	NeonURL    string `json:"neonUrl,omitempty"`
	StorageURL string `json:"storageUrl,omitempty"`
	AuthOrigin string `json:"authOrigin,omitempty"`
	ShardxRoot string `json:"shardxRoot,omitempty"`
	Machine    string `json:"machine,omitempty"`
	SkipCaches *bool  `json:"skipCaches,omitempty"`
}

func (c Config) neonURL() string    { return or(c.NeonURL, core.DefaultNeonURL) }
func (c Config) storageURL() string { return or(c.StorageURL, core.DefaultStorageURL) }
func (c Config) authOrigin() string { return or(c.AuthOrigin, core.DefaultAuthOrigin) }
func (c Config) machine() string {
	if c.Machine != "" {
		return c.Machine
	}
	h, _ := os.Hostname()
	return h
}
func (c Config) skipCaches() bool { return c.SkipCaches == nil || *c.SkipCaches }

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Result is what every UI call returns: errors come back as data so the UI can react to their code.
type Result struct {
	OK    bool            `json:"ok"`
	Data  any             `json:"data,omitempty"`
	Error *core.SyncError `json:"error,omitempty"`
}

func result(data any, err error) Result {
	if err == nil {
		return Result{OK: true, Data: data}
	}
	if se, ok := err.(*core.SyncError); ok {
		return Result{Error: se}
	}
	return Result{Error: &core.SyncError{Code: "ERROR", Message: err.Error()}}
}

type App struct {
	ctx     context.Context
	workDir string
	secrets *secretStore

	mu     sync.Mutex
	cloud  *core.Cloud
	engine *core.Engine
}

func NewApp() *App {
	dir, _ := os.UserConfigDir()
	work := filepath.Join(dir, "ShardX Sync")
	_ = os.MkdirAll(work, 0o755)
	return &App{workDir: work, secrets: newSecretStore(work)}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.buildCloud()
}

// beforeClose keeps the window open while a sync is running unless the user insists.
func (a *App) beforeClose(ctx context.Context) bool {
	a.mu.Lock()
	e := a.engine
	a.mu.Unlock()
	if e == nil || e.Busy() == 0 {
		return false
	}
	choice, _ := wruntime.MessageDialog(ctx, wruntime.MessageDialogOptions{
		Type: wruntime.QuestionDialog, Title: "Sync in progress",
		Message:       "A sync is in progress. Quitting now cancels it (nothing on disk is damaged).",
		Buttons:       []string{"Keep running", "Quit anyway"},
		DefaultButton: "Keep running", CancelButton: "Keep running",
	})
	return choice != "Quit anyway"
}

func (a *App) configFile() string { return filepath.Join(a.workDir, "config.json") }

func (a *App) loadConfig() Config {
	var c Config
	if b, err := os.ReadFile(a.configFile()); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	return c
}

func (a *App) saveConfig(c Config) error {
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(a.configFile(), b, 0o644)
}

func (a *App) buildCloud() {
	c := a.loadConfig()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.engine = nil
	cl, err := core.NewCloud(c.neonURL(), c.storageURL(), c.authOrigin(), a.secrets)
	if err != nil {
		a.cloud = nil
		return
	}
	a.cloud = cl
}

func (a *App) getCloud() (*core.Cloud, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cloud == nil {
		return nil, core.Errf("SETUP", "Check the Neon URLs in Settings.")
	}
	return a.cloud, nil
}

func (a *App) getEngine() (*core.Engine, error) {
	a.mu.Lock()
	if a.engine != nil {
		defer a.mu.Unlock()
		return a.engine, nil
	}
	a.mu.Unlock()
	cl, err := a.getCloud()
	if err != nil {
		return nil, err
	}
	user := cl.CurrentUser()
	if user == nil {
		return nil, core.Errf("AUTH", "Please sign in.")
	}
	pass := a.secrets.Passphrase()
	if pass == "" {
		return nil, core.Errf("SETUP", "Open Settings and enter the team passphrase.")
	}
	c := a.loadConfig()
	root := core.ResolveShardxRoot(c.ShardxRoot)
	if !core.IsValidShardxRoot(root) {
		return nil, core.Errf("SETUP", "ShardX data folder not found at %s. Set it in Settings.", root)
	}
	key, err := core.DeriveKey(pass, c.neonURL())
	if err != nil {
		return nil, err
	}
	if err := core.VerifyTeamKey(cl, key); err != nil {
		return nil, err
	}
	e := core.NewEngine(core.Engine{
		Cloud: cl, Key: key, Root: root, WorkDir: a.workDir, Machine: c.machine(), User: *user, SkipCaches: c.skipCaches(),
		OnProgress: func(p core.Progress) { wruntime.EventsEmit(a.ctx, "progress", p) },
	})
	e.CleanTmp()
	a.mu.Lock()
	a.engine = e
	a.mu.Unlock()
	return e, nil
}

// withEngine runs one engine call. Wails runs each bound call on its own goroutine, so different profiles
// sync in parallel; the engine itself keeps two operations on one profile apart.
func (a *App) withEngine(fn func(e *core.Engine) (any, error)) Result {
	e, err := a.getEngine()
	if err != nil {
		return result(nil, err)
	}
	return result(fn(e))
}

// ---------- methods bound to the UI ----------

func (a *App) State() Result {
	c := a.loadConfig()
	var user *core.User
	var role string
	cl, cloudErr := a.getCloud()
	if cloudErr == nil {
		user = cl.CurrentUser()
	}
	if user != nil {
		// Register also adds users who signed in before team roles existed, so admins can grant them access.
		role, _ = cl.Register()
	}
	var offboard map[string]any
	if role == core.RoleRemoved {
		offboard = a.offboard(cl)
		if done, _ := offboard["done"].(bool); done {
			user = nil
		}
	}
	root := core.ResolveShardxRoot(c.ShardxRoot)
	hasPass := a.secrets.Passphrase() != ""
	return result(map[string]any{
		"configured": hasPass && cloudErr == nil,
		"user":       user,
		"role":       role,
		"offboard":   offboard,
		"config": map[string]any{
			"neonUrl": c.neonURL(), "storageUrl": c.storageURL(), "shardxRoot": c.ShardxRoot, "machine": c.machine(),
			"skipCaches": c.skipCaches(), "shardxRootResolved": root, "shardxRootValid": core.IsValidShardxRoot(root), "hasPassphrase": hasPass,
		},
		"platform": runtime.GOOS,
		"version":  version,
	}, nil)
}

type SettingsInput struct {
	NeonURL    string `json:"neonUrl"`
	StorageURL string `json:"storageUrl"`
	Passphrase string `json:"passphrase"`
	Machine    string `json:"machine"`
	ShardxRoot string `json:"shardxRoot"`
	SkipCaches bool   `json:"skipCaches"`
}

var httpsRe = regexp.MustCompile(`^https://[^/]+`)

func (a *App) SaveSettings(s SettingsInput) Result {
	old := a.loadConfig()
	next := old
	trim := func(v string) string { return strings.TrimRight(strings.TrimSpace(v), "/") }
	next.NeonURL, next.StorageURL = trim(s.NeonURL), trim(s.StorageURL)
	for _, u := range []string{next.NeonURL, next.StorageURL} {
		if u != "" && !httpsRe.MatchString(u) {
			return result(nil, core.Errf("SETUP", "Neon URLs must start with https://"))
		}
	}
	if next.NeonURL == core.DefaultNeonURL {
		next.NeonURL = "" // keep following the built-in default
	}
	if next.StorageURL == core.DefaultStorageURL {
		next.StorageURL = ""
	}
	next.ShardxRoot = strings.TrimSpace(s.ShardxRoot)
	next.Machine = strings.TrimSpace(s.Machine)
	skip := s.SkipCaches
	next.SkipCaches = &skip
	if err := a.saveConfig(next); err != nil {
		return result(nil, err)
	}
	changedProject := next.neonURL() != old.neonURL()
	a.secrets.update(func(d *secretData) {
		if s.Passphrase != "" {
			d.Passphrase = s.Passphrase
		}
		if changedProject {
			d.Session = nil
		}
	})
	a.buildCloud()
	return result(true, nil)
}

func (a *App) PickFolder() Result {
	dir, err := wruntime.OpenDirectoryDialog(a.ctx, wruntime.OpenDialogOptions{Title: `Select the ShardX data folder (contains "profiles")`})
	if dir == "" {
		return result(nil, err)
	}
	return result(dir, err)
}

func (a *App) SignIn(email, password string) Result {
	cl, err := a.getCloud()
	if err != nil {
		return result(nil, err)
	}
	u, err := cl.SignIn(strings.TrimSpace(email), password)
	a.mu.Lock()
	a.engine = nil
	a.mu.Unlock()
	return result(u, err)
}

func (a *App) SendCode(email string) Result {
	cl, err := a.getCloud()
	if err != nil {
		return result(nil, err)
	}
	return result(true, cl.SendPasswordCode(strings.TrimSpace(email)))
}

func (a *App) ResetPassword(email, otp, password string) Result {
	cl, err := a.getCloud()
	if err != nil {
		return result(nil, err)
	}
	return result(true, cl.ResetPassword(strings.TrimSpace(email), strings.TrimSpace(otp), password))
}

func (a *App) SignOut() Result {
	if cl, err := a.getCloud(); err == nil {
		cl.SignOut()
	}
	a.mu.Lock()
	a.engine = nil
	a.mu.Unlock()
	return result(true, nil)
}

func (a *App) List() Result {
	e, err := a.getEngine()
	if err != nil {
		return result(nil, err)
	}
	enforced, enforceErr := a.enforce(e)
	items, err := e.Overview()
	if err != nil {
		return result(nil, err)
	}
	out := map[string]any{"items": items, "launcherRunning": core.IsLauncherRunning(), "machine": e.Machine, "root": e.Root,
		"removed": enforced.Removed, "pending": enforced.Pending}
	if enforceErr != nil {
		out["enforceError"] = enforceErr.Error()
	}
	return result(out, nil)
}

// enforce deletes local copies of profiles the user may no longer have. A profile that is syncing right
// now is skipped by the engine and removed on a later refresh.
func (a *App) enforce(e *core.Engine) (*core.EnforceResult, error) {
	return e.Enforce()
}

// offboard runs for a member an admin removed: delete every workspace profile here, tell the admins, sign out.
// While a profile is still open in ShardX the member stays signed in, so the next start finishes the job.
func (a *App) offboard(cl *core.Cloud) map[string]any {
	e, err := a.getEngine()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	res, err := a.enforce(e)
	out := map[string]any{"removed": res.Removed, "pending": res.Pending}
	if err != nil {
		out["error"] = err.Error()
		return out
	}
	if len(res.Pending) > 0 {
		return out
	}
	_ = cl.ReportWipe()
	cl.SignOut()
	a.mu.Lock()
	a.engine = nil
	a.mu.Unlock()
	out["done"] = true
	return out
}

func (a *App) Push(id string, opt core.PushOptions) Result {
	return a.withEngine(func(e *core.Engine) (any, error) {
		return e.Push(id, opt)
	})
}

func (a *App) Pull(id string, opt core.PullOptions) Result {
	return a.withEngine(func(e *core.Engine) (any, error) {
		return e.Pull(id, opt)
	})
}

func (a *App) ReleaseLock(id string, force bool) Result {
	return a.withEngine(func(e *core.Engine) (any, error) {
		return e.ReleaseLock(id, force)
	})
}

func (a *App) TakeLock(id string, force bool) Result {
	return a.withEngine(func(e *core.Engine) (any, error) {
		return e.TakeLock(id, force)
	})
}

// RestoreRemote brings back a profile that was deleted in the cloud. Admins only.
func (a *App) RestoreRemote(id string) Result {
	cl, err := a.getCloud()
	if err != nil {
		return result(nil, err)
	}
	return result(true, cl.RestoreProfile(id))
}

// Team returns the member list and who can see what. Admins only.
func (a *App) Team() Result {
	cl, err := a.getCloud()
	if err != nil {
		return result(nil, err)
	}
	members, err := cl.ListMembers()
	if err != nil {
		return result(nil, err)
	}
	grants, err := cl.ListGrants()
	if err != nil {
		return result(nil, err)
	}
	return result(map[string]any{"members": members, "grants": grants}, nil)
}

func (a *App) SetProfileAccess(id string, userIDs []string) Result {
	cl, err := a.getCloud()
	if err != nil {
		return result(nil, err)
	}
	return result(true, cl.SetProfileAccess(id, userIDs))
}

func (a *App) SetFolderAccess(folder string, userIDs []string) Result {
	cl, err := a.getCloud()
	if err != nil {
		return result(nil, err)
	}
	return result(true, cl.SetFolderAccess(folder, userIDs))
}

func (a *App) SetMemberStatus(userID, status string) Result {
	cl, err := a.getCloud()
	if err != nil {
		return result(nil, err)
	}
	return result(true, cl.SetMemberStatus(userID, status))
}

func (a *App) SetMemberRole(userID, role string) Result {
	cl, err := a.getCloud()
	if err != nil {
		return result(nil, err)
	}
	return result(true, cl.SetMemberRole(userID, role))
}

func (a *App) DeleteRemote(id string) Result {
	return a.withEngine(func(e *core.Engine) (any, error) {
		return true, e.DeleteFromCloud(id)
	})
}

func (a *App) OpenBackups() Result {
	dir := filepath.Join(a.workDir, "backups")
	_ = os.MkdirAll(dir, 0o755)
	return result(true, openPath(dir))
}

func (a *App) OpenShardx() Result {
	return result(true, openPath(core.ResolveShardxRoot(a.loadConfig().ShardxRoot)))
}

func openPath(p string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", p).Start()
	case "windows":
		return exec.Command("explorer", p).Start()
	default:
		return exec.Command("xdg-open", p).Start()
	}
}
