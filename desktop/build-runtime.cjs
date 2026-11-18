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
if (result.status !== 0) process.exit(result.status ?? 1);
if (target === 'darwin') {
  if (process.platform !== 'darwin') throw new Error('Build the macOS computer helper on macOS.');
  const arch = process.env.GOARCH || ({arm64: 'arm64', x64: 'amd64'})[process.arch];
  const triple = ({arm64: 'arm64-apple-macosx12.0', amd64: 'x86_64-apple-macosx12.0'})[arch];
  if (!triple) throw new Error('Unsupported macOS architecture.');
  const helper = spawnSync('xcrun', ['swiftc', '-O', '-target', triple, path.join(__dirname, 'native/Computer.swift'), '-o', path.join(__dirname, 'bin/blooper-computer')], {stdio: 'inherit'});
  process.exit(helper.status ?? 1);
}
