// Screenshot the login and "set password" screens with a configured passphrase. Usage: npx electron test/smoke-login.js out-prefix
const { app } = require('electron'); const fs = require('fs'); const os = require('os'); const path = require('path');
const out = process.argv[process.argv.length - 1];
const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'sxs-smoke-')); app.setPath('userData', dir);
fs.writeFileSync(path.join(dir, 'secrets.bin'), JSON.stringify({ passphrase: 'x' })); // plain fallback is readable when safeStorage fails
const errors = [];
app.on('browser-window-created', (_e, w) => {
  w.webContents.on('console-message', (_ev, level, msg) => { if (level >= 2) errors.push(msg); });
  w.webContents.on('did-finish-load', () => setTimeout(async () => {
    fs.writeFileSync(`${out}-1.png`, (await w.webContents.capturePage()).toPNG());
    await w.webContents.executeJavaScript("document.querySelector('#login-reset').click()");
    await new Promise((r) => setTimeout(r, 300));
    fs.writeFileSync(`${out}-2.png`, (await w.webContents.capturePage()).toPNG());
    await w.webContents.executeJavaScript("document.querySelector('#reset-back').click(); document.querySelector('#login-settings').click(); document.querySelector('details.adv').open = true");
    await new Promise((r) => setTimeout(r, 300));
    fs.writeFileSync(`${out}-3.png`, (await w.webContents.capturePage()).toPNG());
    console.log('SMOKE errors:', JSON.stringify(errors)); app.exit(0);
  }, 1200));
});
require('../src/main.js');
