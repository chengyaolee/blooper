const { app, BrowserWindow, ipcMain, safeStorage, dialog, Menu, shell, session, Tray, nativeImage } = require('electron');
const { autoUpdater } = require('electron-updater');
const { spawn } = require('node:child_process');
const { randomBytes } = require('node:crypto');
const readline = require('node:readline');
const fs = require('node:fs');
const path = require('node:path');
const { pathToFileURL } = require('node:url');
const { Vault } = require('./vault.cjs');

let window, tray, runtime, endpoint, vault, settings, stopping = false, updateReady = false;
let updateStatus = 'Updates are available in installed release builds.';
const token = randomBytes(32).toString('hex');
const uiURL = pathToFileURL(path.join(__dirname, 'ui/index.html')).href;

async function api(route, body) {
  if (!endpoint) throw new Error('Blooper is starting. Try again shortly.');
  const response = await fetch(endpoint + route, {
    method: body === undefined ? 'GET' : 'POST',
    headers: {'Content-Type': 'application/json', 'X-Blooper-Desktop': token},
    body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(40000)
  });
  const data = await response.json();
  if (!response.ok) throw new Error(data.error || 'The local runtime could not complete this request.');
  return data;
}
function publicSettings() {
  return {provider: settings.provider, model: settings.model,
    openaiSaved: Boolean(settings.openaiKey), deepgramSaved: Boolean(settings.deepgramKey),
    launchAtLogin: settings.launchAtLogin, autoUpdates: settings.autoUpdates, updateStatus, updateReady};
}
function trusted(event) {
  if (!window || event.sender !== window.webContents || event.senderFrame !== window.webContents.mainFrame || event.senderFrame.url !== uiURL) throw new Error('Untrusted application window.');
}
function handle(name, action) { ipcMain.handle('blooper:' + name, async (event, ...args) => { trusted(event); return action(...args); }); }

async function startRuntime() {
  const executable = path.join(app.isPackaged ? process.resourcesPath : __dirname, app.isPackaged ? 'runtime' : 'bin', process.platform === 'win32' ? 'blooper.exe' : 'blooper');
  const dataDir = path.join(app.getPath('userData'), 'runtime');
  // Credentials never appear in argv, files or logs of the Go process.
  const env = {...process.env, BLOOPER_DESKTOP_TOKEN: token};
  for (const name of ['OPENAI_API_KEY', 'DEEPGRAM_API_KEY', 'APNS_KEY_PATH', 'APNS_KEY_ID', 'APNS_TEAM_ID']) delete env[name];
  runtime = spawn(executable, ['desktop', '--data-dir', dataDir], {env, stdio: ['pipe', 'pipe', 'pipe'], windowsHide: true});
  const lines = readline.createInterface({input: runtime.stdout});
  // Keep a bounded runtime log in memory. Never forward provider settings to it.
  let diagnostics = '';
  runtime.stderr.on('data', chunk => { diagnostics = (diagnostics + chunk.toString()).slice(-4000); });
  await new Promise((resolve, reject) => {
    const timeout = setTimeout(() => { runtime.kill(); reject(new Error('The local runtime did not start.')); }, 20000);
    runtime.once('error', error => { clearTimeout(timeout); reject(error); });
    runtime.once('exit', code => { clearTimeout(timeout); reject(new Error(`The local runtime stopped (${code}). ${diagnostics}`)); });
    lines.on('line', line => {
      try {
        const ready = JSON.parse(line);
        if (ready.ready && /^http:\/\/127\.0\.0\.1:\d+$/.test(ready.url)) { endpoint = ready.url; clearTimeout(timeout); resolve(); }
      } catch {}
    });
  });
  runtime.on('exit', () => {
    endpoint = undefined;
    if (!stopping && window) window.webContents.send('blooper:runtime-stopped');
  });
  await api('/desktop/configure', settings);
}

function configureUpdates() {
  autoUpdater.autoDownload = true;
  autoUpdater.autoInstallOnAppQuit = false;
  autoUpdater.on('checking-for-update', () => { updateStatus = 'Checking for updates…'; });
  autoUpdater.on('update-not-available', () => { updateStatus = 'Blooper is up to date.'; });
  autoUpdater.on('download-progress', progress => { updateStatus = `Downloading update · ${Math.round(progress.percent)}%`; });
  autoUpdater.on('update-downloaded', () => { updateReady = true; updateStatus = 'An update is ready. Restart when your jobs are finished.'; });
  autoUpdater.on('error', () => { updateStatus = 'Could not check for updates. Try again later.'; });
  if (app.isPackaged && settings.autoUpdates) {
    autoUpdater.checkForUpdates().catch(() => {});
    setInterval(() => { if (settings.autoUpdates) autoUpdater.checkForUpdates().catch(() => {}); }, 6 * 60 * 60 * 1000).unref();
  }
}

function deviceTrayIcon(connected = false) {
  const size = 20, pixels = Buffer.alloc(size * size * 4);
  const paint = (x, y) => { const i = (y * size + x) * 4; const color = connected ? [35, 155, 85] : [120, 128, 139]; pixels[i] = color[2]; pixels[i+1] = color[1]; pixels[i+2] = color[0]; pixels[i+3] = 255; };
  for (let y=3; y<=13; y++) for (let x=2; x<=17; x++) if (x<=3 || x>=16 || y<=4 || y>=12) paint(x,y);
  for (let y=14; y<=16; y++) for (let x=9; x<=10; x++) paint(x,y);
  for (let x=5; x<=14; x++) paint(x,17);
  return nativeImage.createFromBitmap(pixels, {width:size, height:size, scaleFactor:1});
}
async function createWindow() {
  window = new BrowserWindow({width: 1120, height: 780, minWidth: 780, minHeight: 580,
    title: 'Blooper', show: false, webPreferences: {preload: path.join(__dirname, 'preload.cjs'), contextIsolation: true, nodeIntegration: false, sandbox: true}});
  window.webContents.setWindowOpenHandler(() => ({action: 'deny'}));
  window.webContents.on('will-navigate', (event, url) => { if (url !== uiURL) event.preventDefault(); });
  window.webContents.session.setPermissionRequestHandler((_contents, _permission, callback) => callback(false));
  // Inject the runtime capability only into requests to this exact loopback
  // origin. It is not placed in the renderer's DOM, URL, localStorage or JS.
  session.defaultSession.webRequest.onBeforeSendHeaders((details, callback) => {
    if (endpoint && details.url.startsWith(endpoint + '/') && details.webContentsId === window.webContents.id) details.requestHeaders['X-Blooper-Desktop'] = token;
    callback({requestHeaders: details.requestHeaders});
  });
  window.on('close', event => { if (!stopping) { event.preventDefault(); window.hide(); } });
  window.once('ready-to-show', () => window.show());
  await window.loadFile(path.join(__dirname, 'ui/index.html'));
  tray = new Tray(deviceTrayIcon());
  tray.setToolTip('Blooper · Local devices');
  tray.setContextMenu(Menu.buildFromTemplate([{label:'Open Blooper',click:()=>window.show()},{label:'Quit Blooper',click:()=>app.quit()}]));
  tray.on('click',()=>window.show());
  Menu.setApplicationMenu(Menu.buildFromTemplate([
    ...(process.platform === 'darwin' ? [{label: 'Blooper', submenu: [{role: 'about'}, {label: 'Settings…', accelerator: 'CmdOrCtrl+,', click: () => { window.show(); window.webContents.send('blooper:settings'); }}, {type: 'separator'}, {label: 'Quit Blooper', accelerator: 'CmdOrCtrl+Q', click: () => app.quit()}]}] : []),
    {label: 'File', submenu: [{label: 'Show Blooper', click: () => window.show()}, {label: 'Quit Blooper', click: () => app.quit()}]},
    {role: 'editMenu'}, {role: 'viewMenu'}
  ]));
}

if (!app.requestSingleInstanceLock()) app.quit();
else {
  app.on('second-instance', () => { if (window) { window.show(); window.focus(); } });
  app.whenReady().then(async () => {
    if (!['darwin', 'win32'].includes(process.platform)) throw new Error('This desktop app supports macOS and Windows.');
    vault = new Vault(path.join(app.getPath('userData'), 'providers.enc'), safeStorage);
    settings = vault.read();
    handle('state', async () => {
      const status = await api('/desktop/status');
      if (tray) tray.setImage(deviceTrayIcon(status.devices.some(d => d.kind === 'phone' && d.reachable && !d.revoked)));
      return {...publicSettings(), runtime:status, chatURL:endpoint};
    });
    handle('pair', address => api('/desktop/pair', {address}));
    handle('revoke', id => api('/desktop/revoke', {id}));
    handle('permissions', () => api('/desktop/permissions'));
    handle('save-permissions', value => api('/desktop/permissions', value));
    handle('choose-folder', async () => {
      const result = await dialog.showOpenDialog(window, {title: 'Choose a workspace', properties: ['openDirectory']});
      return result.canceled ? null : result.filePaths[0];
    });
    handle('choose-app', async () => {
      const result = await dialog.showOpenDialog(window, {title: 'Choose an app to allow', properties: ['openFile'], filters: process.platform === 'win32' ? [{name: 'Applications', extensions: ['exe']}] : [{name: 'Applications', extensions: ['app']}]});
      if (result.canceled) return null;
      const selected = result.filePaths[0];
      return {name: path.basename(selected, path.extname(selected)), argv: process.platform === 'darwin' ? ['/usr/bin/open', '-a', selected] : [selected]};
    });
    handle('save-provider', async change => {
      if (!['openai', 'ollama', 'deepgram'].includes(change.provider)) throw new Error('Unknown provider.');
      const next = {...settings};
      if (change.provider === 'deepgram') {
        await api('/desktop/validate', {provider: 'deepgram', deepgramKey: change.key}); next.deepgramKey = change.key;
      } else {
        next.provider = change.provider;
        next.model = String(change.model || (change.provider === 'openai' ? 'gpt-5.4-mini' : 'qwen2.5:3b')).trim();
        if (change.provider === 'openai' && change.key) next.openaiKey = change.key;
        await api('/desktop/validate', next);
      }
      vault.write(next); await api('/desktop/configure', next); settings = next;
      return publicSettings();
    });
    handle('delete-provider', async provider => {
      const next = {...settings};
      if (provider === 'openai') delete next.openaiKey;
      else if (provider === 'deepgram') delete next.deepgramKey;
      else throw new Error('Unknown provider.');
      vault.write(next); await api('/desktop/configure', next); settings = next; return publicSettings();
    });
    handle('preferences', async value => {
      const next = {...settings, launchAtLogin: Boolean(value.launchAtLogin), autoUpdates: Boolean(value.autoUpdates)};
      if (app.isPackaged) app.setLoginItemSettings({openAtLogin: next.launchAtLogin});
      else if (next.launchAtLogin) throw new Error('Launch at login is available in the installed app.');
      vault.write(next); settings = next;
      if (app.isPackaged && next.autoUpdates) autoUpdater.checkForUpdates().catch(() => {});
      return publicSettings();
    });
    handle('check-updates', async () => { if (!app.isPackaged) return publicSettings(); await autoUpdater.checkForUpdates(); return publicSettings(); });
    handle('install-update', async () => {
      if (!updateReady) throw new Error('No update is ready.');
      const state = await api('/desktop/status');
      if (state.busy) throw new Error('Finish or cancel active jobs before restarting.');
      // This process owns the runtime. Close it before replacing application files.
      stopping = true; await stopRuntime(); autoUpdater.quitAndInstall(false, true);
    });
    handle('provider-console', async provider => {
      const url = {openai: 'https://platform.openai.com/api-keys', deepgram: 'https://console.deepgram.com/'}[provider];
      if (url) await shell.openExternal(url);
    });
    await startRuntime(); configureUpdates(); await createWindow();
  }).catch(error => { dialog.showErrorBox('Blooper could not start', error.message); app.exit(1); });
  app.on('activate', () => { if (window) window.show(); });
  app.on('window-all-closed', () => {}); // Keep the local runtime available to phones.
  app.on('before-quit', event => {
    if (stopping) return;
    event.preventDefault(); stopping = true;
    stopRuntime().finally(() => app.quit());
  });
}
async function stopRuntime() {
  if (!runtime || runtime.exitCode !== null) return;
  // EOF asks the runtime to stop, including on Windows where SIGINT is not a
  // portable child-process shutdown mechanism.
  runtime.stdin.end();
  await new Promise(resolve => {
    const timer = setTimeout(() => { runtime.kill(); resolve(); }, 8000);
    runtime.once('exit', () => { clearTimeout(timer); resolve(); });
  });
}
