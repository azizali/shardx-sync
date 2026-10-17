# ShardX Sync

Desktop app (macOS, Windows, Linux) that shares ShardX browser profiles between teammates through Supabase.

A synced profile includes the fingerprint file, the full browser folder (cookies, saved logins, local storage,
IndexedDB, bookmarks, history, sessions, Web Store extensions), the ShardX extensions it uses and its proxy.
Browser caches are skipped by default because Chromium rebuilds them.

Everything is encrypted on your computer with the team passphrase (AES-256-GCM) before upload.
Supabase only stores ciphertext.

## One-time Supabase setup (about 5 minutes)

1. Create a free project at https://supabase.com. Pick the region closest to you both.
2. Open **SQL Editor > New query**, paste `supabase/setup.sql`, and click **Run**.
3. Open **Authentication > Sign In / Providers**. Turn **off** "Allow new users to sign up".
4. Open **Authentication > Users > Add user** and create one account per teammate (email + password).
5. Open **Project Settings > API** and copy the **Project URL** and the **anon / publishable key**.

The free plan has 1 GB storage and a 50 MB per-file limit. The app uploads in 40 MB parts, so both are fine
for a handful of profiles. Free projects pause after a week with no use. Click "Restore" in the dashboard
or move to the Pro plan to avoid that.

## Using the app

1. Install from `dist/` (or build, see below). On first run enter the Supabase URL, key, a team passphrase
   and a name for this computer. Your teammate uses the **same** URL, key and passphrase.
2. Sign in.
3. Pick a profile on the left. The right side shows its status, the last sync, who has it checked out, and the buttons.

| Action | What it does |
| --- | --- |
| Upload | Sends this computer's copy and releases your check-out (tick the box to keep it). |
| Download | Replaces this computer's copy with the cloud version and checks it out to you. Your old copy goes to the backups folder (last 3 kept). |
| Release / Force unlock | Frees the check-out without uploading. |
| Remove from cloud | Deletes the cloud copy only. |

Typical flow: **Download → use the profile in ShardX → close its window → Upload.**

Rules the app enforces:

- A profile whose browser window is open cannot be synced.
- While your teammate has a profile checked out, you cannot upload it.
- If both of you changed a profile, the app asks which copy wins. It never silently overwrites.
- Quit ShardX Launcher before downloading and reopen it afterwards, so it loads the new profile.

## Moving profiles between different operating systems

Fingerprint, bookmarks, history, local storage, extensions and proxy all move across macOS, Windows and Linux.
Chromium encrypts **cookies and saved passwords** with a key tied to the operating system's keychain.
Whether those survive a move depends on how ShardX stores that key, which has not been verified yet.
Test with one profile first: upload on one computer, download on the other, and check you are still logged in to a site.
If logins do not carry over, sites will simply ask you to sign in again. Nothing else is affected.

## Development

```bash
npm install
npm start          # run the app
npm test           # end-to-end sync tests against an in-memory cloud
npm run dist:mac   # or dist:win / dist:linux -> installers in dist/
```

From VS Code's terminal, run `env -u ELECTRON_RUN_AS_NODE npm start`.
The GitHub workflow in `.github/workflows/build.yml` builds all three installers on each `v*` tag.
Installers are unsigned. On macOS, right-click the app and choose Open the first time.
On Windows, choose "More info > Run anyway" in SmartScreen.

## Where things live

| | macOS | Windows | Linux |
| --- | --- | --- | --- |
| ShardX data (auto-detected) | `~/Library/Application Support/shardx-launcher` | `%APPDATA%\shardx-launcher` | `~/.config/shardx-launcher` |
| ShardX Sync settings and backups | `~/Library/Application Support/ShardX Sync` | `%APPDATA%\ShardX Sync` | `~/.config/ShardX Sync` |

The passphrase and login are stored encrypted with the OS keychain (Electron safeStorage).
