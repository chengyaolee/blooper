const $ = id => document.getElementById(id);
let state, permissions = {workspaces: {}, apps: {}}, activePage = 'chat', lastModel = '';
const commands = {go: ['go', 'test', './...'], npm: ['npm', 'test'], python: ['python', '-m', 'pytest'], cargo: ['cargo', 'test']};
function message(text, error = false) { $('message').textContent = text; $('message').classList.toggle('error', error); $('message').hidden = !text; }
async function act(button, work) {
  if (button) button.disabled = true;
  try { await work(); } catch (error) { message(error.message, true); }
  finally { if (button) button.disabled = false; }
}
function page(name) {
  activePage = name;
  document.querySelectorAll('.page').forEach(el => { el.hidden = el.id !== name; });
  document.querySelectorAll('nav button').forEach(el => { if (el.dataset.page === name) el.setAttribute('aria-current', 'page'); else el.removeAttribute('aria-current'); });
  if (name === 'permissions') act(null, loadPermissions);
}
function text(tag, value, className) { const el = document.createElement(tag); el.textContent = value; if (className) el.className = className; return el; }
function deviceIcon(online, kind) {
  const icon = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  icon.setAttribute('viewBox','0 0 24 24'); icon.setAttribute('aria-hidden','true');
  icon.setAttribute('class', 'device-icon' + (online ? ' connected' : ''));
  icon.innerHTML = kind === 'phone' ? '<rect x="6" y="2" width="12" height="20" rx="3"/><path d="M10 18h4"/>' : '<rect x="3" y="4" width="18" height="13" rx="2"/><path d="M8 21h8M12 17v4"/>';
  if (!online) { const line = document.createElementNS('http://www.w3.org/2000/svg', 'path'); line.setAttribute('d','M3 3l18 18'); icon.append(line); }
  return icon;
}
function renderDevices(devices) {
  const list = $('devices-list'); list.replaceChildren();
  if (!devices.length) list.append(text('p', 'No devices paired yet. Create a code to connect your iPhone.', 'secondary'));
  for (const device of devices) {
    const row = text('div', '', 'device-row');
    row.append(deviceIcon(device.reachable, device.kind));
    const detail = text('div', '', 'detail'); detail.append(text('strong', device.name), text('small', device.kind === 'phone' ? 'iPhone' : device.os === 'darwin' ? 'Mac' : device.os === 'windows' ? 'Windows' : 'Computer'));
    row.append(detail, text('span', device.revoked ? 'Revoked' : device.reachable ? 'Connected' : 'Offline', `status-text${device.reachable ? ' connected' : ''}`));
    if (device.kind === 'phone' && !device.revoked) {
      const remove = text('button', 'Disconnect');
      remove.onclick = () => { if (confirm(`Disconnect ${device.name}? Pair it again to restore access.`)) act(remove, async () => { await window.blooper.revoke(device.id); await refresh(); }); };
      row.append(remove);
    }
    list.append(row);
  }
}
async function refresh(initial = false) {
  state = await window.blooper.state();
  const {runtime} = state;
  $('host-status').textContent = runtime.providers.connected ? 'Ready for your devices' : 'Connect a model in Settings';
  $('device-count').textContent = runtime.devices.filter(d => d.kind === 'phone' && d.reachable && !d.revoked).length || '';
  renderDevices(runtime.devices);
  const selection = $('network').value;
  $('network').replaceChildren(...runtime.addresses.map(url => { const option = text('option', url.replace('https://', '')); option.value = url; return option; }));
  if (runtime.addresses.includes(selection)) $('network').value = selection;
  $('new-pair').disabled = runtime.addresses.length === 0;
  $('chat-empty').hidden = runtime.providers.connected; $('chat-frame').hidden = !runtime.providers.connected;
  if ($('chat-frame').getAttribute('src') !== state.chatURL || lastModel !== runtime.providers.model) $('chat-frame').src = state.chatURL;
  lastModel = runtime.providers.model;
  $('openai-saved').textContent = state.openaiSaved ? 'A key is saved on this computer. Enter a new key to replace it.' : 'No OpenAI key saved.';
  $('deepgram-saved').textContent = state.deepgramSaved ? 'Connected · key saved on this computer.' : 'Not connected · on-device transcription remains available.';
  $('delete-openai').disabled = !state.openaiSaved; $('delete-deepgram').disabled = !state.deepgramSaved;
  $('update-status').textContent = state.updateStatus; $('install-update').hidden = !state.updateReady;
  if (initial) {
    $('provider').value = state.provider; $('model').value = state.model;
    $('launch-at-login').checked = state.launchAtLogin; $('auto-updates').checked = state.autoUpdates;
    showProvider(); if (!runtime.providers.connected) page('settings');
  }
}
function showProvider() {
  const local = $('provider').value === 'ollama';
  $('openai-fields').hidden = local; $('openai-console').hidden = local; $('delete-openai').hidden = local;
  $('provider-info').textContent = local ? 'Uses Ollama on this computer at localhost:11434. Install Ollama and pull your chosen model first.' : 'OpenAI receives prompts and tool output. Your API key stays on this computer.';
}
$('provider').onchange = () => { $('model').value = $('provider').value === 'openai' ? 'gpt-5.4-mini' : 'qwen2.5:3b'; showProvider(); };
document.querySelectorAll('[data-page]').forEach(el => el.onclick = () => { message(''); page(el.dataset.page); });
document.querySelectorAll('[data-go]').forEach(el => el.onclick = () => page(el.dataset.go));
$('connect-device').onclick = () => page('devices');
$('refresh-devices').onclick = () => act($('refresh-devices'), refresh);
let pairingTimer;
$('network').onchange = () => { $('pair-qr').hidden = true; $('pair-expiry').textContent = ''; clearTimeout(pairingTimer); };
$('new-pair').onclick = () => act($('new-pair'), async () => {
  const result = await window.blooper.pair($('network').value);
  $('pair-qr').src = 'data:image/png;base64,' + result.qr; $('pair-qr').hidden = false;
  $('pair-expiry').textContent = 'Expires in 10 minutes. Scan only in the Blooper app.';
  clearTimeout(pairingTimer); pairingTimer = setTimeout(() => { $('pair-qr').hidden = true; $('pair-expiry').textContent = 'Code expired. Create a new pairing code.'; }, result.expires_in * 1000);
});
$('model-form').onsubmit = event => {
  event.preventDefault(); const key = $('openai-key').value.trim(); $('openai-key').value = '';
  act($('model-form').querySelector('button[type=submit]'), async () => {
    await window.blooper.saveProvider({provider: $('provider').value, model: $('model').value.trim(), key});
    await refresh(); message('Model connected. Your key is stored securely on this computer.');
  });
};
$('deepgram-form').onsubmit = event => {
  event.preventDefault(); const key = $('deepgram-key').value.trim(); $('deepgram-key').value = '';
  act($('deepgram-form').querySelector('button[type=submit]'), async () => { await window.blooper.saveProvider({provider: 'deepgram', key}); await refresh(); message('Deepgram connected.'); });
};
for (const provider of ['openai', 'deepgram']) {
  $('delete-' + provider).onclick = () => { if (confirm(`Delete the saved ${provider === 'openai' ? 'OpenAI' : 'Deepgram'} key from this computer?`)) act($('delete-' + provider), async () => { await window.blooper.deleteProvider(provider); await refresh(); message('Saved key deleted.'); }); };
  $(provider + '-console').onclick = () => window.blooper.providerConsole(provider);
}
function permissionRows(container, entries, describe, remove) {
  $(container).replaceChildren();
  for (const [name,value] of Object.entries(entries || {})) {
    const row = text('div', '', 'permission-row'); const detail = text('div', '', 'detail'); detail.append(text('strong', name), text('small', describe(value))); row.append(detail);
    const button = text('button', 'Remove'); button.onclick = () => { remove(name); renderPermissions(); }; row.append(button); $(container).append(row);
  }
  if (!Object.keys(entries || {}).length) $(container).append(text('p', 'None allowed yet.', 'secondary'));
}
function renderPermissions() {
  permissionRows('workspace-list', permissions.workspaces, ws => `${ws.Path} · ${ws.TestCmd.join(' ')}`, name => delete permissions.workspaces[name]);
  permissionRows('apps-list', permissions.apps, argv => argv.join(' '), name => delete permissions.apps[name]);
}
async function loadPermissions() { const value = await window.blooper.permissions(); permissions = {workspaces: value.workspaces || {}, apps: value.apps || {}}; renderPermissions(); }
$('choose-workspace').onclick = () => act($('choose-workspace'), async () => {
  const folder = await window.blooper.chooseFolder(); if (!folder) return;
  const name = folder.split(/[\\/]/).filter(Boolean).at(-1);
  if (permissions.workspaces[name] && !confirm(`Replace the workspace named ${name}?`)) return;
  const argv = state.runtime.os === 'windows' && $('test-preset').value === 'npm' ? ['cmd.exe', '/d', '/s', '/c', 'npm test'] : commands[$('test-preset').value];
  permissions.workspaces[name] = {Path: folder, TestCmd: argv, Timeout: '15m'}; renderPermissions();
});
$('choose-app').onclick = () => act($('choose-app'), async () => { const app = await window.blooper.chooseApp(); if (app) { permissions.apps[app.name] = app.argv; renderPermissions(); } });
$('save-permissions').onclick = () => act($('save-permissions'), async () => { await window.blooper.savePermissions(permissions); message('Permissions saved.'); });
$('save-preferences').onclick = () => act($('save-preferences'), async () => { await window.blooper.preferences({launchAtLogin: $('launch-at-login').checked, autoUpdates: $('auto-updates').checked}); await refresh(); message('Preferences saved.'); });
$('check-updates').onclick = () => act($('check-updates'), async () => { await window.blooper.checkUpdates(); await refresh(); });
$('install-update').onclick = () => act($('install-update'), () => window.blooper.installUpdate());
window.blooper.onSettings(() => page('settings'));
window.blooper.onStopped(() => { message('The local runtime stopped. Quit and reopen Blooper to reconnect.', true); $('host-status').textContent = 'Offline'; document.querySelector('.host-status .device-icon').classList.remove('connected'); });
act(null, () => refresh(true));
setInterval(() => { refresh().catch(() => { $('host-status').textContent = 'Offline'; document.querySelector('.host-status .device-icon').classList.remove('connected'); }); }, 5000);
