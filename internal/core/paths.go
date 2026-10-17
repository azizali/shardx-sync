package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
)

// DefaultShardxRoot is where ShardX Launcher keeps its data on each OS.
func DefaultShardxRoot() string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "shardx-launcher")
	case "windows":
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			return filepath.Join(appdata, "shardx-launcher")
		}
		return filepath.Join(home, "AppData", "Roaming", "shardx-launcher")
	default:
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			return filepath.Join(xdg, "shardx-launcher")
		}
		return filepath.Join(home, ".config", "shardx-launcher")
	}
}

// ResolveShardxRoot respects a manual override, then ShardX's own "data_root" setting.
func ResolveShardxRoot(override string) string {
	if override != "" {
		return override
	}
	base := DefaultShardxRoot()
	var s struct {
		DataRoot *string `json:"data_root"`
	}
	if b, err := os.ReadFile(filepath.Join(base, "settings.json")); err == nil && json.Unmarshal(b, &s) == nil && s.DataRoot != nil {
		if IsValidShardxRoot(*s.DataRoot) {
			return *s.DataRoot
		}
	}
	return base
}

func IsValidShardxRoot(root string) bool {
	if root == "" {
		return false
	}
	st, err := os.Stat(filepath.Join(root, "profiles"))
	return err == nil && st.IsDir()
}
