// Launches the real app with a throwaway data folder and saves a screenshot. Usage: npx electron test/smoke-main.js out.png
const { app, BrowserWindow } = require('electron');
const fs = require('fs'); const os = require('os'); const path = require('path');
const out = process.argv[process.argv.length - 1];
app.setPath('userData', fs.mkdtempSync(path.join(os.tmpdir(), 'sxs-smoke-')));
const errors = [];
app.on('browser-window-created', (_e, w) => {
  w.webContents.on('console-message', (_ev, level, msg) => { if (level >= 2) errors.push(msg); });
  w.webContents.on('did-finish-load', () => setTimeout(async () => {
    const img = await w.webContents.capturePage();
    fs.writeFileSync(out, img.toPNG());
    console.log('SMOKE errors:', JSON.stringify(errors));
    app.exit(0);
  }, 1200));
});
require('../src/main.js');
