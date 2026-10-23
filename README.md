# ShardX Sync

Desktop app (macOS, Windows, Linux) that shares ShardX browser profiles between teammates through Neon.
Built with Go and Wails, so each installer is small: it uses the webview that is already part of the OS.

A synced profile includes the fingerprint file, the full browser folder (cookies, saved logins, local storage,
IndexedDB, bookmarks, history, sessions, Web Store extensions), the ShardX extensions it uses and its proxy.
Browser caches are skipped by default because Chromium rebuilds them.

Everything is encrypted on your computer with the team passphrase (AES-256-GCM) before upload.
Neon only stores ciphertext.

## How the Neon backend fits together

| Neon service | Used for |
| --- | --- |
| Managed Better Auth | Email and password sign-in. Public sign-up is disabled. |
| Data API + Postgres | The profile list, versions and check-out locks (`neon/setup.sql`). Row-level security admits signed-in users only. |
| Object Storage | Encrypted profile archives, bucket `profiles`, private. |
| Neon Function `shardxstorage` | Checks the user's login token, then returns 15-minute presigned upload and download links (`neon/storage-fn`). The app never holds storage keys. |

The project is `shardx-sync` (`frosty-water-08741847`), branch `main`. Its public URLs are built into
`internal/core/defaults.go`, so teammates only need a login and the team passphrase.

## Adding a teammate

1. Create their account. Either use Neon Console > your project > **Auth > Users > Add user**, or run:

   ```bash
   neon neon-auth user create --project-id frosty-water-08741847 --branch main --email teammate@example.com
   ```

2. They install the app, click **Set or reset password** on the sign-in screen, and enter the code Neon emails them.
3. Give them the team passphrase through a private channel.

To remove someone, delete their user in **Auth > Users**. Their session stops working within 15 minutes.

## Roles and access

| Role | Can do |
| --- | --- |
| Admin | See and manage every profile. Grant access. Restore deleted profiles. Delete for good. |
| Member | See only the profiles shared with them, directly or through a folder. Upload, download and check out those. Delete hides a profile for the team (a soft delete). |

Admins open **Team access** in the top bar to set roles and folder access. On a profile's page, admins tick the members who can see it.
Folder access follows the ShardBrowser folder. Tags do not sync yet, so they cannot be used for access.

A new teammate can only be granted access after they have signed in once, because the app records them on sign-in.
The team's first admin is set once, in the SQL Editor, after they have signed in:

```sql
select public.bootstrap_admin('you@example.com');
```

Upgrading an existing team: run `neon/setup.sql` again. It is safe to repeat. Afterwards only the admin sees profiles until they are shared.

If the emailed code does not arrive, a fallback is to open sign-ups briefly, let them create the account
from the password screen, then close sign-ups again:

```bash
neon neon-auth config email-password update --project-id frosty-water-08741847 --branch main --disable-sign-up false
neon neon-auth config email-password update --project-id frosty-water-08741847 --branch main --disable-sign-up true
```

## Setting up a new Neon project from scratch

Only needed to move to a different project. Run these against the new project's `main` branch:

1. Enable the Data API with Neon Auth (Console > Data API > Enable), on database `neondb`.
2. Run `neon/setup.sql` in the SQL Editor. Sign in to the app once, then run `select public.bootstrap_admin('you@example.com');`.
3. Create a private Object Storage bucket named `profiles`.
4. Deploy the storage function: `pnpm install && pnpm deploy:storage` (edit the project id in `neon/storage-fn/package.json` first). Set its `NEON_DATA_API_URL` setting to the project's Data API URL ending in `/rest/v1`, next to `NEON_AUTH_JWKS_URL`. The function checks each request against the caller's access through this URL.
5. Auth settings: disable sign-up, remove OAuth providers you don't use, and add `https://shardx-sync.app` as a trusted domain.
6. Put the new database URL and function URL in `internal/core/defaults.go` (or in the app's Settings > Neon connection).

## Using the app

1. Download the installer for your OS from the repo's GitHub Releases page (or build it, see below). On first run enter the team passphrase and a name for this computer.
2. Sign in. The first time, use **Set or reset password** to set your password with an emailed code.
3. Pick a profile on the left. The right side shows its status, the last sync, who has it checked out, and the buttons.

| Action | What it does |
| --- | --- |
| Upload | Sends this computer's copy and releases your check-out (tick the box to keep it). |
| Download | Replaces this computer's copy with the cloud version and checks it out to you. Your old copy goes to the backups folder (last 3 kept). |
| Release / Force unlock | Frees the check-out without uploading. |
| Delete | Members: hides the profile for the team, and an admin can restore it. Admins: deletes the cloud copy and every version for good. The copy on this computer stays. |
| Restore | Admins only. Brings back a deleted profile. |

Typical flow: **Download → use the profile in ShardX → close its window → Upload.**

Rules the app enforces:

- A profile whose browser window is open cannot be synced.
- While your teammate has a profile checked out, you cannot upload it.
- If both of you changed a profile, the app asks which copy wins. It never silently overwrites.
- Quit ShardX Launcher before downloading and reopen it afterwards, so it loads the new profile.

## Moving profiles between different operating systems

Fingerprint, bookmarks, history, local storage, extensions and proxy all move across macOS, Windows and Linux.
Cookies are exported with each upload and restored on download, so you stay logged in to sites when a profile moves.
They travel inside the encrypted archive, and each computer re-encrypts them with its own key. This was checked on
macOS with ShardBrowser 2.1 data. The Windows path has not been tested on a Windows machine yet.
Saved passwords sync with the rest of the browser data, unchanged. Their key is tied to the operating system, so they may not open on another OS. That is not handled yet.

## Development

| Path | What it is |
| --- | --- |
| `main.go`, `app.go`, `secrets.go` | Wails app: window, methods the UI calls, OS keychain storage |
| `internal/core` | Sync engine: profile scanning, archive, encryption, Neon client, locks and conflicts |
| `frontend/dist` | The UI (plain HTML, CSS and JS, no build step) |
| `build/` | Icons, macOS Info.plist, Windows installer script, Linux `.deb` config |
| `neon/` | Database schema and the Neon storage Function (TypeScript, managed with pnpm) |

Requirements: Go 1.26+ and the Wails CLI (`go install github.com/wailsapp/wails/v2/cmd/wails@v2.16.0`).
On Linux also `libgtk-3-dev` and `libwebkit2gtk-4.1-dev`.

```bash
wails dev                                   # run the app with live reload
go test ./...                               # sync engine tests against an in-memory cloud
wails build -platform darwin/universal      # macOS app in build/bin
wails build -platform windows/amd64 -nsis   # Windows exe + installer (needs makensis)
wails build -platform linux/amd64 -tags webkit2_41   # on Linux only
pnpm install && pnpm deploy:storage         # redeploy the Neon storage function
# live test against the real Neon project (needs two team accounts; cleans up after itself)
LIVE_A_EMAIL=.. LIVE_A_PASS=.. LIVE_B_EMAIL=.. LIVE_B_PASS=.. go test -tags live -run TestLive -v ./internal/core
```

## Releasing

Pushing a version tag builds every installer on GitHub Actions and attaches them to a GitHub Release.
The version comes from the tag, so there is no file to bump.

```bash
git tag v1.2.0
git push origin v1.2.0
```

The release contains:

| File | For |
| --- | --- |
| `ShardX-Sync-<version>-mac-universal.dmg` | Any Mac (Apple Silicon and Intel), macOS 12 or later |
| `ShardX-Sync-<version>-win-x64-setup.exe` | Windows installer |
| `ShardX-Sync-<version>-win-x64-portable.exe` | Windows, no install needed |
| `ShardX-Sync-<version>-linux-amd64.deb` | Ubuntu 24.04+ and Debian 13+ |
| `ShardX-Sync-<version>-linux-x64.tar.gz` | Other Linux; needs GTK 3 and WebKitGTK 4.1 installed |

To build without releasing, run the workflow by hand from the Actions tab and download the files from the run.

Installers are unsigned. On macOS, right-click the app and choose Open the first time.
On Windows, choose "More info > Run anyway" in SmartScreen.
Windows needs the WebView2 runtime, which Windows 10 and 11 normally include. The setup installer fetches it if missing.

## Where things live

| | macOS | Windows | Linux |
| --- | --- | --- | --- |
| ShardX data (auto-detected) | `~/Library/Application Support/shardx-launcher` | `%APPDATA%\shardx-launcher` | `~/.config/shardx-launcher` |
| ShardX Sync settings and backups | `~/Library/Application Support/ShardX Sync` | `%APPDATA%\ShardX Sync` | `~/.config/ShardX Sync` |

The passphrase and login session are stored in the OS keychain: macOS Keychain, Windows Credential Manager,
or the Linux Secret Service. On a Linux desktop without a keyring they fall back to a private file
(`secrets.json`, readable only by you) in the settings folder.
