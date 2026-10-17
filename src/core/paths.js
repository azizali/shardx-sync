'use strict';
const os = require('os');
const path = require('path');
const fs = require('fs');

// Where ShardX Launcher keeps its data on each OS.
function defaultShardxRoot() {
  const home = os.homedir();
  if (process.platform === 'darwin') return path.join(home, 'Library', 'Application Support', 'shardx-launcher');
  if (process.platform === 'win32') return path.join(process.env.APPDATA || path.join(home, 'AppData', 'Roaming'), 'shardx-launcher');
  return path.join(process.env.XDG_CONFIG_HOME || path.join(home, '.config'), 'shardx-launcher');
}

// ShardX lets users move its data (settings.json "data_root"). Respect that.
function resolveShardxRoot(override) {
  if (override) return override;
  const base = defaultShardxRoot();
  try {
    const s = JSON.parse(fs.readFileSync(path.join(base, 'settings.json'), 'utf8'));
    if (s.data_root && fs.existsSync(path.join(s.data_root, 'profiles'))) return s.data_root;
  } catch { /* no settings yet */ }
  return base;
}

function isValidShardxRoot(root) {
  return !!root && fs.existsSync(path.join(root, 'profiles'));
}

module.exports = { defaultShardxRoot, resolveShardxRoot, isValidShardxRoot };
