const { contextBridge, ipcRenderer } = require('electron');
const invoke = name => (...args) => ipcRenderer.invoke('blooper:' + name, ...args);
contextBridge.exposeInMainWorld('blooper', {
  state: invoke('state'), pair: invoke('pair'), revoke: invoke('revoke'),
  permissions: invoke('permissions'), savePermissions: invoke('save-permissions'),
  computerPermissions: invoke('computer-permissions'),
  chooseFolder: invoke('choose-folder'), chooseApp: invoke('choose-app'),
  saveProvider: invoke('save-provider'), deleteProvider: invoke('delete-provider'),
  preferences: invoke('preferences'), checkUpdates: invoke('check-updates'), installUpdate: invoke('install-update'),
  providerConsole: invoke('provider-console'),
  onSettings: callback => ipcRenderer.on('blooper:settings', () => callback()),
  onStopped: callback => ipcRenderer.on('blooper:runtime-stopped', () => callback())
});
