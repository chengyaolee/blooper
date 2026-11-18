const fs = require('node:fs');
const path = require('node:path');

// safeStorage uses Keychain on macOS and DPAPI on Windows. Never fall back to
// plaintext storage or include credentials in renderer state or diagnostics.
class Vault {
  constructor(file, safeStorage) { this.file = file; this.safeStorage = safeStorage; }
  read() {
    if (!fs.existsSync(this.file)) return {provider: 'openai', model: 'gpt-5.4-mini', launchAtLogin: false, autoUpdates: true};
    if (!this.safeStorage.isEncryptionAvailable()) throw new Error('Unlock your system credential store, then reopen blooper.');
    return JSON.parse(this.safeStorage.decryptString(fs.readFileSync(this.file)));
  }
  write(value) {
    if (!this.safeStorage.isEncryptionAvailable()) throw new Error('Secure storage is unavailable. No keys were saved.');
    const encrypted = this.safeStorage.encryptString(JSON.stringify(value));
    fs.mkdirSync(path.dirname(this.file), {recursive: true, mode: 0o700});
    const temporary = this.file + '.new';
    fs.writeFileSync(temporary, encrypted, {mode: 0o600});
    fs.renameSync(temporary, this.file);
  }
}
module.exports = {Vault};
