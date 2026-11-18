const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const crypto = require('node:crypto');
const {Vault} = require('../vault.cjs');

test('vault persists encrypted keys, replaces and deletes them, and fails closed', t => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'blooper-vault-'));
  t.after(() => fs.rmSync(dir, {recursive:true,force:true}));
  // Exercise persistence with real encryption without changing the user's
  // system keychain. Production injects Electron safeStorage instead.
  const key = crypto.randomBytes(32), iv = crypto.randomBytes(12);
  const storage = {
    isEncryptionAvailable: () => true,
    encryptString: value => { const c = crypto.createCipheriv('aes-256-gcm',key,iv); const body = Buffer.concat([c.update(value,'utf8'),c.final()]); return Buffer.concat([c.getAuthTag(),body]); },
    decryptString: value => { const c = crypto.createDecipheriv('aes-256-gcm',key,iv); c.setAuthTag(value.subarray(0,16)); return Buffer.concat([c.update(value.subarray(16)),c.final()]).toString('utf8'); }
  };
  const file = path.join(dir,'providers.enc'), vault = new Vault(file, storage);
  assert.equal(vault.read().provider,'openai');
  vault.write({provider:'openai',openaiKey:'test-secret-one'});
  assert.equal(fs.readFileSync(file).includes(Buffer.from('test-secret-one')), false);
  assert.equal(vault.read().openaiKey,'test-secret-one');
  vault.write({provider:'openai',openaiKey:'test-secret-two'});
  assert.equal(vault.read().openaiKey,'test-secret-two');
  vault.write({provider:'openai'});
  assert.equal(vault.read().openaiKey,undefined);
  storage.isEncryptionAvailable = () => false;
  assert.throws(() => vault.write({openaiKey:'do-not-save'}),/Secure storage/);
  assert.throws(() => vault.read(),/credential store/);
  assert.equal(fs.existsSync(file + '.new'),false);
});
