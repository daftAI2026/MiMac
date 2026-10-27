// Standalone Electron verification only; never loaded by MiMac.
const {app, BrowserWindow} = require("electron");
const fs = require("node:fs");
const path = require("node:path");
const [url, directory] = process.argv.slice(2);
if (!url || !directory || !url.startsWith("http://127.0.0.1:")) process.exit(2);
app.setPath("userData", path.join(directory, "profile"));
app.whenReady().then(async () => {
  // Publish ownership before any renderer/network await so the host can clean
  // up this exact app even when the fixture page fails to load.
  fs.writeFileSync(path.join(directory, "pid"), String(process.pid));
  const window = new BrowserWindow({width: 760, height: 720,
    webPreferences: {nodeIntegration: false, contextIsolation: true, sandbox: true}});
  await window.loadURL(url);
  window.show(); window.focus(); app.focus({steal: true});
}).catch(error => {
  console.error("BrowserMatrix fixture failed to load:", error);
  app.exit(1);
});
app.on("window-all-closed", () => app.quit());
