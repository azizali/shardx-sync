package core

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// CookiesFile is the archive entry that carries a profile's cookies as plain JSON.
// The Cookies database stores values encrypted with a key that is tied to the computer
// (DPAPI on Windows), so the database alone cannot be read on another computer. The archive is
// encrypted with the team key before upload, so this file never leaves the team in clear text.
const CookiesFile = "cookies.json"

// Cookie uses the same field names as ShardBrowser's cookie export, so a file made here imports there.
type Cookie struct {
	Domain   string   `json:"domain"`
	Name     string   `json:"name"`
	Value    string   `json:"value"`
	Path     string   `json:"path"`
	Expires  *float64 `json:"expires"` // unix seconds; nil = session cookie
	Secure   bool     `json:"secure"`
	HTTPOnly bool     `json:"http_only"`
	SameSite string   `json:"same_site"`
}

// Chromium stores times as microseconds since 1601-01-01.
const winEpochDeltaSecs = 11644473600

// cookiesSchema is Chromium's v24 cookie table, as ShardBrowser creates it for a fresh profile.
const cookiesSchema = `
CREATE TABLE IF NOT EXISTS meta (key LONGVARCHAR NOT NULL UNIQUE PRIMARY KEY, value LONGVARCHAR);
INSERT OR IGNORE INTO meta (key, value) VALUES ('version', '24');
INSERT OR IGNORE INTO meta (key, value) VALUES ('last_compatible_version', '24');
CREATE TABLE IF NOT EXISTS cookies (
	creation_utc INTEGER NOT NULL, host_key TEXT NOT NULL,
	top_frame_site_key TEXT NOT NULL, name TEXT NOT NULL, value TEXT NOT NULL,
	encrypted_value BLOB NOT NULL, path TEXT NOT NULL, expires_utc INTEGER NOT NULL,
	is_secure INTEGER NOT NULL, is_httponly INTEGER NOT NULL, last_access_utc INTEGER NOT NULL,
	has_expires INTEGER NOT NULL, is_persistent INTEGER NOT NULL, priority INTEGER NOT NULL,
	samesite INTEGER NOT NULL, source_scheme INTEGER NOT NULL, source_port INTEGER NOT NULL,
	last_update_utc INTEGER NOT NULL, source_type INTEGER NOT NULL,
	has_cross_site_ancestor INTEGER NOT NULL);
CREATE UNIQUE INDEX IF NOT EXISTS cookies_unique_index ON cookies (
	host_key, top_frame_site_key, has_cross_site_ancestor, name, path, source_scheme, source_port);`

// cookiesDB is the Cookies file of a user-data dir: Default/Cookies, or Default/Network/Cookies on newer Chromium.
func cookiesDB(udd string) string {
	primary := filepath.Join(udd, "Default", "Cookies")
	if exists(primary) {
		return primary
	}
	if alt := filepath.Join(udd, "Default", "Network", "Cookies"); exists(alt) {
		return alt
	}
	return primary
}

// ExportCookies decrypts every cookie in a profile's store. It works on a copy in tmpDir, so the
// original files are never opened for writing. Cookies that cannot be decrypted are skipped.
func ExportCookies(udd, tmpDir string) ([]Cookie, error) {
	src := cookiesDB(udd)
	if !exists(src) {
		return nil, nil
	}
	key, err := osCryptKey(udd)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(tmpDir, "cookies-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	dst := filepath.Join(dir, "Cookies")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if exists(src + suffix) {
			if err := copyFile(src+suffix, dst+suffix); err != nil {
				return nil, err
			}
		}
	}
	db, err := sql.Open("sqlite", dst)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT host_key, name, encrypted_value, path, expires_utc, is_secure, is_httponly, has_expires, samesite FROM cookies`)
	if err != nil {
		return nil, fmt.Errorf("read cookies: %w", err)
	}
	defer rows.Close()
	crypt := cookieCrypt{key: key}
	out := []Cookie{}
	for rows.Next() {
		var host, name, path string
		var enc []byte
		var expires, secure, httpOnly, hasExpires, sameSite int64
		if err := rows.Scan(&host, &name, &enc, &path, &expires, &secure, &httpOnly, &hasExpires, &sameSite); err != nil {
			return nil, err
		}
		plain, ok := crypt.open(host, enc)
		if !ok {
			continue
		}
		c := Cookie{Domain: host, Name: name, Value: string(plain), Path: path, Secure: secure != 0, HTTPOnly: httpOnly != 0, SameSite: samesiteName(sameSite)}
		if hasExpires != 0 {
			secs := float64(expires)/1_000_000 - winEpochDeltaSecs
			c.Expires = &secs
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ImportCookies replaces a profile's cookie store with cookies, encrypting each value with this computer's key.
// The profile must not be running: the browser would overwrite the store when it exits.
func ImportCookies(udd string, cookies []Cookie) error {
	key, err := osCryptKey(udd)
	if err != nil {
		return err
	}
	file := cookiesDB(udd)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", file)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(cookiesSchema); err != nil {
		return fmt.Errorf("prepare cookies: %w", err)
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM cookies`); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO cookies (
		creation_utc, host_key, top_frame_site_key, name, value, encrypted_value, path, expires_utc,
		is_secure, is_httponly, last_access_utc, has_expires, is_persistent, priority, samesite,
		source_scheme, source_port, last_update_utc, source_type, has_cross_site_ancestor)
		VALUES (?, ?, '', ?, '', ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, 0, 1)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	crypt := cookieCrypt{key: key}
	now := chromiumNow()
	for _, c := range cookies {
		var expires int64
		hasExpires := int64(0)
		if c.Expires != nil {
			hasExpires = 1
			expires = int64((*c.Expires + winEpochDeltaSecs) * 1_000_000)
		}
		scheme, port := int64(1), int64(80)
		if c.Secure {
			scheme, port = 2, 443
		}
		if _, err := stmt.Exec(now, c.Domain, c.Name, crypt.seal(c.Domain, []byte(c.Value)), c.Path, expires,
			boolInt(c.Secure), boolInt(c.HTTPOnly), now, hasExpires, hasExpires, samesiteValue(c.SameSite),
			scheme, port, now); err != nil {
			return fmt.Errorf("write cookie %s: %w", c.Name, err)
		}
	}
	return tx.Commit()
}

// readCookiesFile loads the cookies.json entry of an archive, if it has one.
func readCookiesFile(path string) ([]Cookie, bool, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var cookies []Cookie
	if err := json.Unmarshal(b, &cookies); err != nil {
		return nil, false, Errf("CORRUPT", "Cookie file is damaged.")
	}
	return cookies, true, nil
}

// cookieCrypt wraps the v10 cipher. The plaintext is SHA256(host) + value since Chromium 130.
type cookieCrypt struct{ key []byte }

func (c cookieCrypt) open(host string, enc []byte) ([]byte, bool) {
	if len(enc) < 3 || string(enc[:3]) != "v10" {
		return nil, false
	}
	pt, ok := c.decrypt(enc[3:])
	if !ok || len(pt) < sha256.Size {
		return nil, false
	}
	return pt[sha256.Size:], true
}

func (c cookieCrypt) seal(host string, value []byte) []byte {
	h := sha256.Sum256([]byte(host))
	plain := append(h[:], value...)
	return append([]byte("v10"), c.encrypt(plain)...)
}

func samesiteName(v int64) string {
	switch v {
	case 0:
		return "None"
	case 1:
		return "Lax"
	case 2:
		return "Strict"
	}
	return "unspecified"
}

func samesiteValue(s string) int64 {
	switch s {
	case "None", "none":
		return 0
	case "Lax", "lax":
		return 1
	case "Strict", "strict":
		return 2
	}
	return -1
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func chromiumNow() int64 {
	return int64((float64(time.Now().UnixNano())/1e9 + winEpochDeltaSecs) * 1_000_000)
}
