#!/usr/bin/env python3
"""Exercise source setup without installing packages, opening apps, or changing credentials."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile


source = Path(__file__).resolve().with_name("setup-desktop.sh")
node = shutil.which("node")
assert node, "Node.js is required for setup version checks"

with tempfile.TemporaryDirectory(prefix="blooper setup checks ") as directory:
    root = Path(directory)
    (root / "scripts").mkdir()
    (root / "ios/BlooperRemote").mkdir(parents=True)
    (root / "config").mkdir()
    (root / "unrelated working directory").mkdir()
    shutil.copy2(source, root / "scripts/setup-desktop.sh")
    (root / "go.mod").write_text("module setup-check\n\ngo 1.27.1\n")
    preserved = [root / ".env", root / "runner.yaml", root / "config/blooper.local.yaml"]
    for path in preserved:
        path.write_text("existing settings: keep me\n")
    log = root / "calls.log"

    def executable(relative, body):
        path = root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("#!/bin/bash\nset -eu\n" + body + "\n")
        path.chmod(0o755)

    executable("mock-bin/uname", 'printf "%s\\n" "${SETUP_OS:-Darwin}"')
    executable("mock-bin/sw_vers", 'printf "27.0\\n"')
    executable("mock-bin/go", 'printf "%s\\n" "${SETUP_GO:-go1.27.1}"')
    executable("mock-bin/npm", 'printf "npm %s\\n" "$*" >> "$SETUP_LOG"')
    executable("mock-bin/xcodegen", 'printf "xcodegen %s\\n" "$*" >> "$SETUP_LOG"')
    executable("mock-bin/xcrun", '''
if [ "${SETUP_NO_SWIFT:-0}" = 1 ] && [ "$*" = "--find swiftc" ]; then exit 1; fi
if [ "${SETUP_NO_IOS:-0}" = 1 ] && [ "$*" = "--sdk iphoneos --show-sdk-path" ]; then exit 1; fi
printf "/mock/developer/tool-or-sdk\\n"
''')
    (root / "mock-bin/node").symlink_to(node)
    executable("desktop/bin/blooper-computer", 'printf "helper %s\\n" "$*" >> "$SETUP_LOG"')
    executable("desktop/node_modules/.bin/electron", 'printf "electron %s in %s\\n" "$*" "$PWD" >> "$SETUP_LOG"')

    def run(*arguments, **overrides):
        log.write_text("")
        environment = dict(os.environ, PATH=str(root / "mock-bin") + os.pathsep + os.defpath,
                           SETUP_LOG=str(log), **overrides)
        result = subprocess.run([str(root / "scripts/setup-desktop.sh"), *arguments],
                                cwd=root / "unrelated working directory", env=environment,
                                capture_output=True, text=True, timeout=20)
        for path in preserved:
            assert path.read_text() == "existing settings: keep me\n", f"Setup modified {path.name}"
        return result, log.read_text().splitlines()

    result, calls = run("--help")
    assert result.returncode == 0 and "--launch" in result.stdout and not calls
    result, calls = run("--unknown")
    assert result.returncode == 2 and "Unknown option" in result.stderr and not calls
    result, calls = run("--check", "--ios", "--launch")
    assert result.returncode == 0 and "Prerequisites ready" in result.stdout and not calls
    result, calls = run()
    assert result.returncode == 0, result.stderr
    assert calls == ["npm ci", "npm run build:runtime", "helper --self-test"], calls
    result, calls = run("--ios", "--launch")
    assert result.returncode == 0, result.stderr
    assert calls == ["npm ci", "npm run build:runtime", "helper --self-test", "xcodegen generate",
                     f"electron . in {root / 'desktop'}"], calls
    for version in ["go1.26.9", "go1.27.0"]:
        result, calls = run(SETUP_GO=version)
        assert result.returncode == 1 and "Go 1.27.1 or newer" in result.stderr and not calls
    result, calls = run("--check", SETUP_GO="go1.28.0")
    assert result.returncode == 0 and not calls, result.stderr
    result, calls = run(SETUP_OS="Linux")
    assert result.returncode == 1 and "supports macOS" in result.stderr and not calls
    result, calls = run(SETUP_NO_SWIFT="1")
    assert result.returncode == 1 and "Swift compiler unavailable" in result.stderr and not calls
    result, calls = run("--ios", SETUP_NO_IOS="1")
    assert result.returncode == 1 and "full Xcode" in result.stderr and not calls

print("PASS: desktop setup flags, tool versions, builds, launch, iOS prerequisites, paths with spaces, and existing settings")
