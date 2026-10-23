package core

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Session is the persisted Neon Auth login (opaque session cookie, valid ~7 days and renewed on use).
type Session struct {
	Cookie string `json:"cookie"`
	User   User   `json:"user"`
}

// SessionStore persists the session between app restarts (OS keychain in the app).
type SessionStore interface {
	LoadSession() *Session
	SaveSession(*Session)
}

// DeriveURLs maps https://ep-x.c-6.us-east-2.aws.neon.tech/neondb to the Auth and Data API base URLs.
func DeriveURLs(neonURL string) (authURL, restURL string, err error) {
	u, err := url.Parse(neonURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", "", Errf("SETUP", "Neon database URL must look like https://ep-xxx.<region>.aws.neon.tech/neondb")
	}
	labels := strings.SplitN(u.Hostname(), ".", 2)
	if len(labels) != 2 {
		return "", "", Errf("SETUP", "Unexpected Neon host %q", u.Hostname())
	}
	db := strings.TrimRight(u.Path, "/")
	if db == "" {
		db = "/neondb"
	}
	return fmt.Sprintf("https://%s.neonauth.%s%s/auth", labels[0], labels[1], db),
		fmt.Sprintf("https://%s.apirest.%s%s/rest/v1", labels[0], labels[1], db), nil
}

type Cloud struct {
	authURL, restURL, fnURL, origin string
	store                           SessionStore
	http                            *http.Client

	mu     sync.Mutex
	jwt    string
	jwtExp time.Time
}

func NewCloud(neonURL, storageURL, origin string, store SessionStore) (*Cloud, error) {
	a, r, err := DeriveURLs(neonURL)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(storageURL, "https://") {
		return nil, Errf("SETUP", "Storage function URL must be an https URL")
	}
	return &Cloud{
		authURL: a, restURL: r, fnURL: strings.TrimRight(storageURL, "/"), origin: origin, store: store,
		http: &http.Client{Timeout: 10 * time.Minute},
	}, nil
}

// ---------- auth (Better Auth REST) ----------

var authMessages = map[string]string{
	"INVALID_EMAIL_OR_PASSWORD": "Wrong email or password.",
	"INVALID_OTP":               "That code is wrong. Check the email or request a new code.",
	"OTP_EXPIRED":               "That code expired. Request a new one.",
	"TOO_MANY_ATTEMPTS":         "Too many attempts. Request a new code.",
	"INVALID_ORIGIN":            "Neon Auth rejected this app. Add the app origin as a trusted domain (see README).",
	"PASSWORD_TOO_SHORT":        "Password is too short (minimum 8 characters).",
}

// mergeCookies applies Set-Cookie headers to a "a=1; b=2" cookie string.
func mergeCookies(prev string, res *http.Response) string {
	order := []string{}
	jar := map[string]string{}
	for _, c := range strings.Split(prev, "; ") {
		if i := strings.Index(c, "="); i > 0 {
			if _, ok := jar[c[:i]]; !ok {
				order = append(order, c[:i])
			}
			jar[c[:i]] = c
		}
	}
	for _, c := range res.Cookies() {
		if _, ok := jar[c.Name]; !ok {
			order = append(order, c.Name)
		}
		if c.MaxAge < 0 || (!c.Expires.IsZero() && c.Expires.Before(time.Now())) {
			jar[c.Name] = ""
		} else {
			jar[c.Name] = c.Name + "=" + c.Value
		}
	}
	var out []string
	for _, n := range order {
		if jar[n] != "" {
			out = append(out, jar[n])
		}
	}
	return strings.Join(out, "; ")
}

func (c *Cloud) authPost(path string, body any, withCookie bool) (*http.Response, map[string]any, error) {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", c.authURL+path, bytes.NewReader(b))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("origin", c.origin)
	if s := c.store.LoadSession(); withCookie && s != nil {
		req.Header.Set("cookie", s.Cookie)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, nil, Errf("NETWORK", "Cannot reach Neon Auth: %v", err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	if res.StatusCode >= 300 {
		code, _ := out["code"].(string)
		msg, _ := out["message"].(string)
		if m, ok := authMessages[code]; ok {
			msg = m
		}
		if msg == "" {
			msg = fmt.Sprintf("Sign-in failed (HTTP %d)", res.StatusCode)
		}
		return nil, nil, Errf("AUTH", "%s", msg)
	}
	return res, out, nil
}

func (c *Cloud) CurrentUser() *User {
	if s := c.store.LoadSession(); s != nil {
		return &s.User
	}
	return nil
}

func (c *Cloud) SignIn(email, password string) (*User, error) {
	res, out, err := c.authPost("/sign-in/email", map[string]string{"email": email, "password": password}, false)
	if err != nil {
		return nil, err
	}
	cookie := mergeCookies("", res)
	u, _ := out["user"].(map[string]any)
	if cookie == "" || u == nil {
		return nil, Errf("AUTH", "Sign-in returned no session.")
	}
	user := User{ID: fmt.Sprint(u["id"]), Email: fmt.Sprint(u["email"])}
	c.store.SaveSession(&Session{Cookie: cookie, User: user})
	c.mu.Lock()
	c.jwt = ""
	c.mu.Unlock()
	if _, err := c.Token(); err != nil { // proves the session works end to end
		return nil, err
	}
	return &user, nil
}

func (c *Cloud) SignOut() {
	_, _, _ = c.authPost("/sign-out", map[string]any{}, true)
	c.store.SaveSession(nil)
	c.mu.Lock()
	c.jwt = ""
	c.mu.Unlock()
}

// SendPasswordCode emails a one-time code. Works for new team accounts that have no password yet.
func (c *Cloud) SendPasswordCode(email string) error {
	_, _, err := c.authPost("/email-otp/send-verification-otp", map[string]string{"email": email, "type": "forget-password"}, false)
	return err
}

func (c *Cloud) ResetPassword(email, otp, password string) error {
	_, _, err := c.authPost("/email-otp/reset-password", map[string]string{"email": email, "otp": otp, "password": password}, false)
	return err
}

// Token returns a short-lived (15 min) JWT for the Data API and storage Function.
func (c *Cloud) Token() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.jwt != "" && time.Now().Before(c.jwtExp.Add(-time.Minute)) {
		return c.jwt, nil
	}
	s := c.store.LoadSession()
	if s == nil {
		return "", Errf("AUTH", "Please sign in.")
	}
	req, _ := http.NewRequest("GET", c.authURL+"/token", nil)
	req.Header.Set("cookie", s.Cookie)
	req.Header.Set("origin", c.origin)
	res, err := c.http.Do(req)
	if err != nil {
		return "", Errf("NETWORK", "Cannot reach Neon Auth: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 403 {
		c.store.SaveSession(nil)
		return "", Errf("AUTH", "Your session expired. Please sign in again.")
	}
	if res.StatusCode >= 300 {
		return "", Errf("CLOUD", "Token refresh failed (HTTP %d)", res.StatusCode)
	}
	if updated := mergeCookies(s.Cookie, res); updated != s.Cookie && updated != "" {
		c.store.SaveSession(&Session{Cookie: updated, User: s.User})
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil || body.Token == "" {
		return "", Errf("CLOUD", "Token refresh returned no token")
	}
	parts := strings.Split(body.Token, ".")
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if len(parts) == 3 {
		if raw, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
			_ = json.Unmarshal(raw, &claims)
		}
	}
	c.jwt = body.Token
	c.jwtExp = time.Unix(claims.Exp, 0)
	return c.jwt, nil
}

func (c *Cloud) dropToken() {
	c.mu.Lock()
	c.jwt = ""
	c.mu.Unlock()
}

// ---------- retries ----------

// retryable errors are network/5xx problems; everything with a specific code is final.
func withRetry[T any](label string, attempts int, fn func() (T, error)) (T, error) {
	var last error
	var zero T
	for i := 0; i < attempts; i++ {
		v, err := fn()
		if err == nil {
			return v, nil
		}
		last = err
		if code := CodeOf(err); code != "" && code != "CLOUD" && code != "RETRY" && code != "NETWORK" {
			return zero, err
		}
		if i < attempts-1 {
			time.Sleep(time.Second << i)
		}
	}
	return zero, Errf("NETWORK", "%s failed after %d tries: %v", label, attempts, last)
}

// ---------- Data API ----------

var (
	lockedRe   = regexp.MustCompile(`LOCKED:(.*) on (.*)$`)
	conflictRe = regexp.MustCompile(`CONFLICT:(\d+)`)
)

// Access errors raised by the functions in neon/setup.sql.
var accessRe = regexp.MustCompile(`^(NO_ACCESS|DELETED|ADMIN_ONLY)\b`)

var accessMessages = map[string]string{
	"NO_ACCESS":  "You do not have access to this profile.",
	"DELETED":    "This profile was deleted. Ask an admin to restore it.",
	"ADMIN_ONLY": "Only an admin can do this.",
}

func translateDBError(status int, body map[string]any) error {
	msg, _ := body["message"].(string)
	code, _ := body["code"].(string)
	if msg == "" {
		msg = fmt.Sprintf("HTTP %d", status)
	}
	if m := lockedRe.FindStringSubmatch(msg); m != nil {
		return &SyncError{Code: "LOCKED", Message: fmt.Sprintf("In use by %s on %s.", m[1], m[2]), Data: map[string]any{"email": m[1], "machine": m[2]}}
	}
	if m := conflictRe.FindStringSubmatch(msg); m != nil {
		return &SyncError{Code: "CONFLICT", Message: fmt.Sprintf("Someone uploaded a newer version (v%s) while you were working.", m[1])}
	}
	if m := accessRe.FindStringSubmatch(msg); m != nil {
		return Errf(m[1], "%s", accessMessages[m[1]])
	}
	if code == "42P01" || code == "PGRST202" || code == "PGRST205" || strings.Contains(msg, "does not exist") || strings.Contains(msg, "Could not find") {
		return Errf("SETUP", "The Neon database is not set up yet. Run neon/setup.sql on it.")
	}
	if status == 401 {
		return Errf("AUTH", "Your session expired. Please sign in again.")
	}
	if status >= 500 {
		return Errf("CLOUD", "%s", msg)
	}
	return Errf("DB", "%s", msg)
}

func (c *Cloud) db(method, path string, body any, out any) error {
	_, err := withRetry("Database "+method+" "+strings.SplitN(path, "?", 2)[0], 3, func() (struct{}, error) {
		tok, err := c.Token()
		if err != nil {
			return struct{}{}, err
		}
		var rdr io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, c.restURL+path, rdr)
		req.Header.Set("content-type", "application/json")
		req.Header.Set("authorization", "Bearer "+tok)
		res, err := c.http.Do(req)
		if err != nil {
			return struct{}{}, Errf("NETWORK", "%v", err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode < 300 {
			if out != nil && len(raw) > 0 {
				return struct{}{}, json.Unmarshal(raw, out)
			}
			return struct{}{}, nil
		}
		var eb map[string]any
		_ = json.Unmarshal(raw, &eb)
		// A cold database connection occasionally misses the JWT on the first call. Retry it.
		if m, _ := eb["message"].(string); m == "not signed in" {
			return struct{}{}, Errf("RETRY", "not signed in")
		}
		if res.StatusCode == 401 {
			c.dropToken()
		}
		return struct{}{}, translateDBError(res.StatusCode, eb)
	})
	return err
}

func (c *Cloud) ListProfiles() ([]Row, error) {
	var rows []Row
	err := c.db("GET", "/synced_profiles?select=*&order=name.asc", nil, &rows)
	return rows, err
}

func (c *Cloud) GetProfile(id string) (*Row, error) {
	var rows []Row
	if err := c.db("GET", "/synced_profiles?select=*&id=eq."+url.QueryEscape(id), nil, &rows); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

func (c *Cloud) rpc(fn string, args map[string]any) (*Row, error) {
	var r Row
	if err := c.db("POST", "/rpc/"+fn, args, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (c *Cloud) ClaimLock(id, machine string, force bool) (*Row, error) {
	return c.rpc("claim_lock", map[string]any{"p_id": id, "p_machine": machine, "p_force": force})
}

func (c *Cloud) ReleaseLock(id, machine string, force bool) (*Row, error) {
	return c.rpc("release_lock", map[string]any{"p_id": id, "p_machine": machine, "p_force": force})
}

func (c *Cloud) CommitVersion(a CommitArgs) (*Row, error) {
	return c.rpc("commit_version", map[string]any{
		"p_id": a.ID, "p_expected": a.Expected, "p_name": a.Name, "p_folder": a.Folder, "p_parts": a.Parts,
		"p_size": a.Size, "p_sha": a.Sha256, "p_prefix": a.Prefix, "p_machine": a.Machine, "p_os": a.OS, "p_keep_lock": a.KeepLock,
	})
}

// DeleteProfile is a soft delete for members and a permanent one for admins. The server decides by role.
func (c *Cloud) DeleteProfile(id string) error {
	return c.db("POST", "/rpc/delete_profile", map[string]any{"p_id": id}, nil)
}

// RestoreProfile brings back a soft-deleted profile. Admins only.
func (c *Cloud) RestoreProfile(id string) error {
	return c.db("POST", "/rpc/restore_profile", map[string]any{"p_id": id}, nil)
}

// MyRole returns the signed-in user's role. A user the server has not seen yet is a member.
func (c *Cloud) MyRole() (string, error) {
	var role string
	err := c.db("POST", "/rpc/my_role", map[string]any{}, &role)
	return role, err
}

// Register records the signed-in user in the team member list so an admin can grant them access.
func (c *Cloud) Register() (string, error) {
	var role string
	err := c.db("POST", "/rpc/register_me", map[string]any{}, &role)
	return role, err
}

// Member is one person on the team, as the admin sees them.
type Member struct {
	UserID  string `json:"user_id"`
	Email   string `json:"email"`
	Role    string `json:"role"`
	Status  string `json:"status"`   // active | removed
	WipedAt string `json:"wiped_at"` // when a removed member's app finished deleting its copies
}

func (c *Cloud) ListMembers() ([]Member, error) {
	var out []Member
	err := c.db("GET", "/team_members?select=user_id,email,role,status,wiped_at&order=email.asc", nil, &out)
	return out, err
}

// SetMemberStatus offboards a member ("removed") or brings them back ("active"). Admins only.
func (c *Cloud) SetMemberStatus(userID, status string) error {
	return c.db("POST", "/rpc/set_member_status", map[string]any{"p_user_id": userID, "p_status": status}, nil)
}

// ReportWipe tells admins that this removed member's app has deleted its copies.
func (c *Cloud) ReportWipe() error {
	return c.db("POST", "/rpc/report_wipe", map[string]any{}, nil)
}

// ProfileVerdicts asks what to do with each profile on this computer: "keep", "remove" (access ended or
// deleted) or "unknown" (never uploaded).
func (c *Cloud) ProfileVerdicts(ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []struct {
		ID      string `json:"id"`
		Verdict string `json:"verdict"`
	}
	if err := c.db("POST", "/rpc/profile_verdicts", map[string]any{"p_ids": ids}, &rows); err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ID] = r.Verdict
	}
	return out, nil
}

// Grants lists who can see which profiles directly and which folders. Keys are profile ids and folder names.
type Grants struct {
	Profiles map[string][]string `json:"profiles"`
	Folders  map[string][]string `json:"folders"`
}

func (c *Cloud) ListGrants() (Grants, error) {
	g := Grants{Profiles: map[string][]string{}, Folders: map[string][]string{}}
	var profiles []struct {
		ProfileID string `json:"profile_id"`
		UserID    string `json:"user_id"`
	}
	if err := c.db("GET", "/profile_grants?select=profile_id,user_id", nil, &profiles); err != nil {
		return g, err
	}
	for _, p := range profiles {
		g.Profiles[p.ProfileID] = append(g.Profiles[p.ProfileID], p.UserID)
	}
	var folders []struct {
		Folder string `json:"folder"`
		UserID string `json:"user_id"`
	}
	if err := c.db("GET", "/folder_grants?select=folder,user_id", nil, &folders); err != nil {
		return g, err
	}
	for _, f := range folders {
		g.Folders[f.Folder] = append(g.Folders[f.Folder], f.UserID)
	}
	return g, nil
}

// SetProfileAccess replaces who can see one profile directly. Admins only.
func (c *Cloud) SetProfileAccess(id string, userIDs []string) error {
	return c.db("POST", "/rpc/set_profile_access", map[string]any{"p_id": id, "p_user_ids": nonNil(userIDs)}, nil)
}

// SetFolderAccess replaces who can see every profile in one folder. Admins only.
func (c *Cloud) SetFolderAccess(folder string, userIDs []string) error {
	return c.db("POST", "/rpc/set_folder_access", map[string]any{"p_folder": folder, "p_user_ids": nonNil(userIDs)}, nil)
}

// SetMemberRole makes a member an admin or back to a member. Admins only.
func (c *Cloud) SetMemberRole(userID, role string) error {
	return c.db("POST", "/rpc/set_member_role", map[string]any{"p_user_id": userID, "p_role": role}, nil)
}

// nonNil keeps an empty list an empty JSON array, not null, so the server clears the grants.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (c *Cloud) GetTeamSetting(key string) (string, bool, error) {
	var rows []struct {
		Value string `json:"value"`
	}
	if err := c.db("GET", "/team_settings?select=value&key=eq."+url.QueryEscape(key), nil, &rows); err != nil {
		return "", false, err
	}
	if len(rows) == 0 {
		return "", false, nil
	}
	return rows[0].Value, true, nil
}

func (c *Cloud) InsertTeamSetting(key, value string) error {
	return c.db("POST", "/team_settings", map[string]string{"key": key, "value": value}, nil)
}

// ---------- Object Storage via the storage Function ----------

func (c *Cloud) fn(path string, body any, out any) error {
	tok, err := c.Token()
	if err != nil {
		return err
	}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", c.fnURL+path, bytes.NewReader(b))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+tok)
	res, err := c.http.Do(req)
	if err != nil {
		return Errf("CLOUD", "Storage service unreachable: %v", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode == 401 {
		c.dropToken()
		return Errf("CLOUD", "storage token rejected")
	}
	if res.StatusCode >= 300 {
		var eb struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &eb)
		code := "STORAGE"
		if res.StatusCode >= 500 {
			code = "CLOUD"
		}
		if eb.Error == "" {
			eb.Error = fmt.Sprintf("Storage error (HTTP %d)", res.StatusCode)
		}
		return Errf(code, "%s", eb.Error)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (c *Cloud) sign(op, key string) (string, error) {
	var r struct {
		URLs []string `json:"urls"`
	}
	if err := c.fn("/sign", map[string]any{"items": []map[string]string{{"op": op, "key": key}}}, &r); err != nil {
		return "", err
	}
	if len(r.URLs) != 1 {
		return "", Errf("CLOUD", "storage returned no URL")
	}
	return r.URLs[0], nil
}

func (c *Cloud) Upload(key string, data []byte) error {
	_, err := withRetry("Upload of "+key, 4, func() (struct{}, error) {
		u, err := c.sign("put", key)
		if err != nil {
			return struct{}{}, err
		}
		req, _ := http.NewRequest("PUT", u, bytes.NewReader(data))
		req.Header.Set("content-type", "application/octet-stream")
		req.ContentLength = int64(len(data))
		res, err := c.http.Do(req)
		if err != nil {
			return struct{}{}, Errf("NETWORK", "%v", err)
		}
		defer res.Body.Close()
		if res.StatusCode >= 300 {
			b, _ := io.ReadAll(io.LimitReader(res.Body, 300))
			return struct{}{}, Errf("CLOUD", "Upload rejected (HTTP %d): %s", res.StatusCode, b)
		}
		return struct{}{}, nil
	})
	return err
}

func (c *Cloud) Download(key string) ([]byte, error) {
	return withRetry("Download of "+key, 4, func() ([]byte, error) {
		u, err := c.sign("get", key)
		if err != nil {
			return nil, err
		}
		res, err := c.http.Get(u)
		if err != nil {
			return nil, Errf("NETWORK", "%v", err)
		}
		defer res.Body.Close()
		if res.StatusCode >= 300 {
			return nil, Errf("CLOUD", "Download failed (HTTP %d)", res.StatusCode)
		}
		return io.ReadAll(res.Body)
	})
}

func (c *Cloud) listKeys(prefix string) ([]string, error) {
	return withRetry("List "+prefix, 4, func() ([]string, error) {
		var r struct {
			Keys []string `json:"keys"`
		}
		err := c.fn("/list", map[string]string{"prefix": prefix}, &r)
		return r.Keys, err
	})
}

func (c *Cloud) RemoveFolder(prefix string) error {
	keys, err := c.listKeys(prefix + "/")
	if err != nil {
		return err
	}
	for i := 0; i < len(keys); i += 1000 {
		batch := keys[i:min(i+1000, len(keys))]
		if _, err := withRetry("Delete in "+prefix, 4, func() (struct{}, error) {
			return struct{}{}, c.fn("/delete", map[string]any{"keys": batch}, nil)
		}); err != nil {
			return err
		}
	}
	return nil
}

func (c *Cloud) ListFolders(id string) ([]string, error) {
	keys, err := c.listKeys(id + "/")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, k := range keys {
		parts := strings.SplitN(k, "/", 3)
		if len(parts) >= 2 {
			f := parts[0] + "/" + parts[1]
			if !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
	}
	return out, nil
}
