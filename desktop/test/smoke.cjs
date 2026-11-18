// Run with: desktop/node_modules/.bin/electron desktop/test/smoke.cjs
// Exercises the actual desktop shell + bundled Go runtime using a temporary
// profile. It never reads or modifies the user's provider vault or API keys.
const {app} = require('electron');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const assert = require('node:assert/strict');
const dir = fs.mkdtempSync(path.join(os.tmpdir(),'blooper-desktop-smoke-'));
app.setPath('userData',dir);
let finished = false;
app.on('browser-window-created',(_event,window) => {
  window.webContents.once('did-finish-load',async()=>{
    try {
      await new Promise(r=>setTimeout(r,1200));
      const state = await window.webContents.executeJavaScript('window.blooper.state()');
      const chatFrame = window.webContents.mainFrame.frames.find(frame => frame.url.startsWith(state.chatURL));
      assert.ok(chatFrame, 'Chat frame did not load');
      const chatText = await chatFrame.executeJavaScript('document.body.innerText');
      assert.equal(chatText.includes('Desktop authorization required'),false);
      assert.ok(chatText.includes('Agent'));
      assert.equal(state.openaiSaved,false);
      assert.equal(state.runtime.providers.connected,false);
      assert.equal(state.runtime.devices.some(d=>d.kind==='desktop' && d.reachable),true);
      assert.equal(await window.webContents.executeJavaScript('document.getElementById("settings").hidden'),false);
      await window.webContents.executeJavaScript('document.querySelector("[data-page=devices]").click()');
      if (state.runtime.addresses.length) {
        await window.webContents.executeJavaScript('document.getElementById("new-pair").click()');
        await new Promise(r=>setTimeout(r,400));
        assert.equal(await window.webContents.executeJavaScript('document.getElementById("pair-qr").src.startsWith("data:image/png;base64,")'),true);
      }
      await window.webContents.executeJavaScript('document.querySelector("[data-page=permissions]").click()');
      await new Promise(r=>setTimeout(r,200));
      assert.equal(await window.webContents.executeJavaScript('document.getElementById("permissions").hidden'),false);
      const permissions = await window.webContents.executeJavaScript('window.blooper.permissions()');
      assert.equal(permissions.computer,false);
      assert.equal(permissions.autoApproveRoutine,false);
      assert.equal(permissions.computerSupported,process.platform==='darwin');
      assert.equal(await window.webContents.executeJavaScript('document.getElementById("computer-permissions").hidden'),process.platform!=='darwin');
      assert.equal(await window.webContents.executeJavaScript('document.getElementById("allow-computer").checked'),false);
      assert.equal(await window.webContents.executeJavaScript('document.getElementById("auto-approve-routine").disabled'),true);
      if (process.platform==='darwin') {
        // Enabling the capability does not capture screens or deliver input.
        await window.webContents.executeJavaScript('window.blooper.savePermissions({workspaces:{},apps:{},computer:true,autoApproveRoutine:true})');
        const enabled = await window.webContents.executeJavaScript('window.blooper.state()');
        assert.ok(enabled.runtime.devices.some(d=>d.kind==='desktop' && d.tools.includes('computer')));
        assert.ok(enabled.runtime.devices.some(d=>d.kind==='desktop' && d.auto_approve_routine===true));
        assert.equal((await window.webContents.executeJavaScript('window.blooper.permissions()')).autoApproveRoutine,true);
        assert.equal(await window.webContents.executeJavaScript('window.blooper.savePermissions({workspaces:{},apps:{},computer:false,autoApproveRoutine:true}).then(()=>false,()=>true)'),true);
        await window.webContents.executeJavaScript('window.blooper.savePermissions({workspaces:{},apps:{},computer:false})');
        const disabled = await window.webContents.executeJavaScript('window.blooper.state()');
        assert.equal(disabled.runtime.devices.some(d=>d.tools && d.tools.includes('computer')),false);
        assert.equal(disabled.runtime.devices.some(d=>d.auto_approve_routine===true),false);
      }
      await window.webContents.executeJavaScript('document.querySelector("[data-page=settings]").click()');
      await new Promise(r=>setTimeout(r,200));
      fs.writeFileSync(path.join(os.tmpdir(),'blooper-desktop-settings.png'),(await window.capturePage()).toPNG());
      console.log('PASS: desktop onboarding, private runtime, local runner, QR pairing, settings and computer-control opt-in');
      finished = true; app.quit();
    } catch(error) { console.error(error); process.exitCode=1; finished=true; app.quit(); }
  });
});
setTimeout(()=>{ if(!finished) { console.error('Desktop smoke test timed out'); process.exitCode=1; app.quit(); } },25000).unref();
app.on('will-quit',()=>{ fs.rmSync(dir,{recursive:true,force:true}); });
require('../main.cjs');
