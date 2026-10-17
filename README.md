# ShardX Sync

Desktop app (macOS, Windows, Linux) that shares ShardX browser profiles between teammates through Neon.

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
`src/core/defaults.js`, so teammates only need a login and the team passphrase.

## Adding a teammate

1. Create their account. Either use Neon Console > your project > **Auth > Users > Add user**, or run:

   ```bash
   neon neon-auth user create --project-id frosty-water-08741847 --branch main --email teammate@example.com
   ```

2. They install the app, click **Set or reset password** on the sign-in screen, and enter the code Neon emails them.
3. Give them the team passphrase through a private channel.

To remove someone, delete their user in **Auth > Users**. Their session stops working within 15 minutes.

If the emailed code does not arrive, a fallback is to open sign-ups briefly, let them create the account
from the password screen, then close sign-ups again:

```bash
neon neon-auth config email-password update --project-id frosty-water-08741847 --branch main --disable-sign-up false
neon neon-auth config email-password update --project-id frosty-water-08741847 --branch main --disable-sign-up true
```

## Setting up a new Neon project from scratch

Only needed to move to a different project. Run these against the new project's `main` branch:

1. Enable the Data API with Neon Auth (Console > Data API > Enable), on database `neondb`.
2. Run `neon/setup.sql` in the SQL Editor.
3. Create a private Object Storage bucket named `profiles`.
4. Deploy the storage function: `cd neon/storage-fn && npm install && neon functions deploy shardxstorage --src index.ts`.
5. Auth settings: disable sign-up, remove OAuth providers you don't use, and add `https://shardx-sync.app` as a trusted domain.
6. Put the new database URL and function URL in `src/core/defaults.js` (or in the app's Settings > Neon connection).

## Using the app

1. Install from `dist/` (or build, see below). On first run enter the team passphrase and a name for this computer.
2. Sign in. The first time, use **Set or reset password** to set your password with an emailed code.
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
# live test against the real Neon project (needs two team accounts; cleans up after itself)
LIVE_A_EMAIL=.. LIVE_A_PASS=.. LIVE_B_EMAIL=.. LIVE_B_PASS=.. node test/live-neon.js
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
