#!/usr/bin/env bash
# Prepare the macOS desktop app from a clone while installers are unpublished.
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: ./scripts/setup-desktop.sh [--ios] [--launch] [--check]

  --ios     Also generate the iPhone Xcode project (full Xcode + XcodeGen required).
  --launch  Start the desktop app after setup; keep this terminal open.
  --check   Only check prerequisites; do not install, build, generate, or launch.
  --help    Show this help.

Requires macOS, Go matching go.mod, Node.js 22+, npm, and Xcode Command Line Tools.
Computer control requires macOS 14+. API keys and permissions are configured in
the desktop app. This script does not change .env, CLI configs, or OS permissions.
EOF
}

prepare_ios=0
launch_desktop=0
check_only=0
for argument in "$@"; do
  case "$argument" in
    --ios) prepare_ios=1 ;;
    --launch) launch_desktop=1 ;;
    --check) check_only=1 ;;
    -h|--help) usage; exit 0 ;;
    *) printf 'Unknown option: %s (try --help)\n' "$argument" >&2; exit 2 ;;
  esac
done

cd "$(dirname "$0")/.."
if [ "$(uname -s)" != Darwin ]; then
  printf 'This setup script supports macOS. For Windows, use the npm commands in README.md.\n' >&2
  exit 1
fi

printf 'Checking desktop prerequisites…\n'
for prerequisite in node npm go xcrun; do
  if ! command -v "$prerequisite" >/dev/null 2>&1; then
    printf 'Missing %s. Install Go and Node.js (brew install go node), and Xcode Command Line Tools (xcode-select --install), then retry.\n' "$prerequisite" >&2
    exit 1
  fi
done
node -e 'if (Number(process.versions.node.split(".")[0]) < 22) { console.error("Node.js 22 or newer is required; update Node.js and retry."); process.exit(1); }'
required_go=$(sed -n 's/^go //p' go.mod)
installed_go=$(GOTOOLCHAIN=local go env GOVERSION)
node -e '
  const required = process.argv[1].split(".").map(Number);
  const match = /^go(\d+)\.(\d+)(?:\.(\d+))?$/.exec(process.argv[2]);
  const installed = match && match.slice(1).map(value => Number(value || 0));
  const difference = installed && required.map((value, i) => installed[i] - value).find(value => value !== 0);
  if (!installed || difference < 0) {
    console.error(`Go ${process.argv[1]} or newer is required; found ${process.argv[2]}. Update Go and retry.`);
    process.exit(1);
  }
' "$required_go" "$installed_go"

# Select an installed full Xcode for this process without changing xcode-select.
if [ -z "${DEVELOPER_DIR:-}" ] && [ -d /Applications/Xcode.app/Contents/Developer ]; then
  export DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer
fi
if ! xcrun --find swiftc >/dev/null 2>&1; then
  printf 'Swift compiler unavailable. Install Xcode Command Line Tools (xcode-select --install), then retry.\n' >&2
  exit 1
fi
macos_version=$(sw_vers -productVersion)
if [ "${macos_version%%.*}" -lt 14 ]; then
  printf 'macOS %s: desktop setup can continue, but computer control requires macOS 14+.\n' "$macos_version"
fi

if [ "$prepare_ios" = 1 ]; then
  if ! command -v xcodegen >/dev/null 2>&1; then
    printf 'iPhone setup requires XcodeGen: brew install xcodegen\n' >&2
    exit 1
  fi
  if ! xcrun --find xcodebuild >/dev/null 2>&1 || ! xcrun --sdk iphoneos --show-sdk-path >/dev/null 2>&1; then
    printf 'iPhone setup requires full Xcode and the iOS platform SDK. Install them in Xcode, then retry. Command Line Tools alone are insufficient.\n' >&2
    exit 1
  fi
fi
printf 'Prerequisites ready: %s, Node.js %s, macOS %s.\n' "$installed_go" "$(node --version)" "$macos_version"
if [ "$check_only" = 1 ]; then exit 0; fi

printf '\nInstalling desktop dependencies and building the bundled runtime…\n'
(
  cd desktop
  npm ci
  npm run build:runtime
  ./bin/blooper-computer --self-test
)

if [ "$prepare_ios" = 1 ]; then
  printf '\nGenerating the iPhone project…\n'
  (cd ios/BlooperRemote && xcodegen generate)
fi

cat <<'EOF'

Desktop setup complete.
  1. Open Settings in blooper and connect OpenAI or a local vision model.
  2. Choose allowed workspaces/apps in Permissions and save.
  3. For computer control, set up Mac permissions, reopen blooper, and enable it.
     Optionally enable Auto-approve routine steps and save permissions.
  4. Create a pairing code in Devices and scan it with the iPhone app on the same Wi-Fi.

To launch again: cd desktop && npm start
Closing the window keeps blooper running; use Quit blooper before closing its terminal.
EOF
if [ "$prepare_ios" = 1 ]; then
  printf '\nOpen ios/BlooperRemote/BlooperRemote.xcodeproj in Xcode. Configure your signing team\nand bundle identifier in project.yml, regenerate, and build on your iPhone (iOS 17+).\nSee README.md for signing and pairing instructions.\n'
fi
if [ "$launch_desktop" = 1 ]; then
  cd desktop
  exec ./node_modules/.bin/electron .
fi
