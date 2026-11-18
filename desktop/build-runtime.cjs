const { spawnSync } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');
const target = process.env.GOOS || ({darwin: 'darwin', win32: 'windows'})[process.platform];
if (!['darwin', 'windows'].includes(target)) throw new Error('Desktop releases support macOS and Windows.');
fs.rmSync(path.join(__dirname, 'bin'), {recursive:true,force:true});
fs.mkdirSync(path.join(__dirname, 'bin'), {recursive: true});
const output = path.join(__dirname, 'bin', target === 'windows' ? 'blooper.exe' : 'blooper');
const result = spawnSync('go', ['build', '-trimpath', '-ldflags=-s -w', '-o', output, './cmd/blooper'], {
  cwd: path.join(__dirname, '..'), stdio: 'inherit', env: {...process.env, GOOS: target, CGO_ENABLED: '0'}
});
process.exit(result.status ?? 1);
