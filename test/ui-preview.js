// Renders the main screen with sample data (no cloud). Usage: npx electron test/ui-preview.js out.png
const { app, BrowserWindow } = require('electron'); const fs = require('fs'); const path = require('path');
const out = process.argv[process.argv.length - 1];
app.whenReady().then(() => {
  const w = new BrowserWindow({ width: 1120, height: 720, show: false, webPreferences: { preload: path.join(__dirname, 'ui-preview-preload.js'), contextIsolation: true } });
  w.loadFile(path.join(__dirname, '../src/ui/index.html'));
  w.webContents.on('did-finish-load', () => setTimeout(async () => {
    await w.webContents.executeJavaScript("document.querySelector('.item:nth-child(2)').click()");
    await new Promise((r) => setTimeout(r, 300));
    fs.writeFileSync(out, (await w.webContents.capturePage()).toPNG()); app.exit(0);
  }, 800));
});
