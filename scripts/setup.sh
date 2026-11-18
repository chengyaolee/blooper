#!/usr/bin/env bash
# Set up blooper on this computer, and optionally this computer as a runner.
#
#   scripts/setup.sh            interactive; Enter accepts each default
#   scripts/setup.sh --lan      skip Tailscale; serve plain HTTP on the LAN
#   scripts/setup.sh --runner   also set this computer up as a runner
#   scripts/setup.sh --no-runner, --no-ios   skip those steps
#
# Builds ./blooper and writes config/blooper.local.yaml, plus runner.yaml and
# .env when you choose. Re-running rebuilds and asks before overwriting config.
# Without a terminal (for example in CI) it takes every default and asks nothing.
set -euo pipefail

cd "$(dirname "$0")/.."
ROOT=$(pwd)

TAILSCALE=1
RUNNER=ask
IOS=1
for arg in "$@"; do
  case "$arg" in
    --lan) TAILSCALE=0 ;;
    --runner) RUNNER=yes ;;
    --no-runner) RUNNER=no ;;
    --no-ios) IOS=0 ;;
    -h|--help) sed -n '2,9p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown option: $arg (try --help)" >&2; exit 2 ;;
  esac
done

say()  { printf '\n==> %s\n' "$*"; }
ok()   { printf '    %s\n' "$*"; }
warn() { printf '    ! %s\n' "$*"; }

# ask PROMPT DEFAULT sets REPLY to the answer, or to DEFAULT without a terminal.
ask() {
  REPLY=$2
  if [ -t 0 ]; then
    read -r -p "    $1 [$2]: " REPLY || true
    REPLY=${REPLY:-$2}
  fi
}

# yes_no PROMPT y|n returns success for yes.
yes_no() {
  ask "$1 (y/n)" "$2"
  case "$REPLY" in y|Y|yes|YES) return 0 ;; *) return 1 ;; esac
}

# name_ok rejects names that would break YAML keys or the runner's allow-list.
name_ok() {
  case "$1" in
    ""|*[!A-Za-z0-9_-]*) warn "use only letters, digits, '-' and '_'"; return 1 ;;
  esac
}

# argv_yaml "go test ./..." prints ["go", "test", "./..."]
argv_yaml() {
  local out="" w
  for w in $1; do out="$out, \"$w\""; done
  printf '[%s]' "${out#, }"
}

say "Checking prerequisites"
if ! command -v go >/dev/null 2>&1; then
  echo "    Go is not installed. Get it from https://go.dev/dl/ and run this again."
  exit 1
fi
want=$(sed -n 's/^go \(.*\)$/\1/p' go.mod)
have=$(go env GOVERSION | sed 's/^go//')
if [ "$(printf '%s\n%s\n' "$want" "$have" | sort -V | head -n1)" != "$want" ]; then
  echo "    go.mod needs Go $want or newer; you have $have. Update Go and run this again."
  exit 1
fi
ok "Go $have"

if command -v ollama >/dev/null 2>&1; then
  ok "Ollama found"
  if ! ollama list 2>/dev/null | grep -q '^qwen2.5:3b'; then
    warn "the default local planner model is missing: run 'ollama pull qwen2.5:3b' (or use OpenAI, below)"
  fi
else
  warn "Ollama not found. The local planner needs it: https://ollama.com (or use OpenAI, below)"
fi

say "Building blooper"
go build -o blooper ./cmd/blooper
ok "./blooper"

say "Network"
ask "Port for the server" 8080
PORT=$REPLY
SCHEME=http
TLS_CERT=""
TLS_KEY=""
PUBLIC_URL=""

if [ "$TAILSCALE" = 1 ]; then
  DNS=""
  if command -v tailscale >/dev/null 2>&1 && tailscale status >/dev/null 2>&1; then
    DNS=$(tailscale status --json 2>/dev/null | sed -n 's/.*"DNSName": *"\([^"]*\)".*/\1/p' | head -n1 | sed 's/\.$//')
  fi
  if [ -n "$DNS" ]; then
    mkdir -p data/tls
    if tailscale cert --cert-file data/tls/server.crt --key-file data/tls/server.key "$DNS" >/dev/null 2>&1; then
      SCHEME=https
      TLS_CERT="$ROOT/data/tls/server.crt"
      TLS_KEY="$ROOT/data/tls/server.key"
      PUBLIC_URL="https://$DNS:$PORT"
      ok "Tailscale: $DNS, with a TLS certificate"
    else
      warn "Tailscale is up, but no certificate was issued. Turn on HTTPS certificates in the admin console (DNS page), then run this again."
    fi
  else
    warn "Tailscale is not installed or not signed in."
  fi
fi

if [ "$SCHEME" = http ]; then
  ip=$(ipconfig getifaddr en0 2>/dev/null || hostname -I 2>/dev/null | awk '{print $1}' || true)
  ip=${ip:-$(hostname)}
  ask "Address phones will use" "http://$ip:$PORT"
  PUBLIC_URL=$REPLY
  case "$PUBLIC_URL" in
    https://*) ;;
    *) warn "plain HTTP: requests are signed but not encrypted. Use this only on a network you trust." ;;
  esac
fi

say "Writing config/blooper.local.yaml"
if [ -f config/blooper.local.yaml ] && ! yes_no "    Overwrite the existing config/blooper.local.yaml?" n; then
  ok "kept the existing file"
else
  cat > config/blooper.local.yaml <<EOF
# Written by scripts/setup.sh. Re-run the script to regenerate it.
server:
  host: "0.0.0.0"
  port: $PORT
  tls_cert: "$TLS_CERT"
  tls_key: "$TLS_KEY"
ollama:
  base_url: "http://localhost:11434"
hardware_profile: "custom"
agent_dirs: ["agents", "user_agents"]
default_agent: "agent_creator"
memory:
  db_path: "data/blooper.db"
remote:
  public_url: "$PUBLIC_URL"
  db_path: "data/remote.db"
  planner: "auto"
  openai_model: "gpt-5.4-mini"
  ollama_model: "qwen2.5:3b"
  deepgram_model: "nova-3"
  job_timeout: "1h"
push:
  topic: "com.blooper.remote"
  environment: "sandbox"
EOF
  ok "written"
fi

say "Hosted services (optional)"
if [ -f .env ]; then
  ok ".env exists; keeping it"
else
  (
    umask 077
    : > .env.new
    if [ -t 0 ]; then
      echo "    Press Enter to skip any key. Input is hidden."
      read -r -s -p "    OPENAI_API_KEY (planner; blank = local Ollama): " key || true; echo
      [ -n "$key" ] && printf 'OPENAI_API_KEY=%s\n' "$key" >> .env.new
      read -r -s -p "    DEEPGRAM_API_KEY (phone transcription; blank = on-device): " key || true; echo
      [ -n "$key" ] && printf 'DEEPGRAM_API_KEY=%s\n' "$key" >> .env.new
    fi
    mv .env.new .env
  )
  ok ".env written (git-ignored, mode 600)"
fi

if [ "$RUNNER" = ask ]; then
  if yes_no "Set up this computer as a runner, so your phone can run tests and apps here?" n; then
    RUNNER=yes
  else
    RUNNER=no
  fi
fi

if [ "$RUNNER" = yes ]; then
  say "Runner setup"
  if [ -f runner.yaml ]; then
    ok "runner.yaml exists; keeping it (delete it to reconfigure)"
  else
    ask "Coordinator URL (the server, as phones see it)" "$PUBLIC_URL"
    COORD=$REPLY
    ask "Name shown on your phone" "$(hostname -s)"
    NAME=$REPLY

    INSECURE=""
    case "$COORD" in
      https://*) ;;
      *) INSECURE="allow_insecure: true" ;;
    esac

    WS=""
    echo "    Workspaces the phone may run tests in. Leave the name blank to finish."
    while :; do
      ask "Workspace name" ""
      wname=$REPLY
      [ -n "$wname" ] || break
      name_ok "$wname" || continue
      ask "Path to the repository" "$ROOT"
      wpath=$REPLY
      ask "Test command" "go test ./..."
      wcmd=$REPLY
      WS="$WS  $wname:
    path: \"$wpath\"
    test_cmd: $(argv_yaml "$wcmd")
    timeout: 15m
"
      ok "added $wname"
    done

    APPS=""
    echo "    Apps the phone may open on this desktop. Leave the name blank to finish."
    while :; do
      ask "App name" ""
      aname=$REPLY
      [ -n "$aname" ] || break
      name_ok "$aname" || continue
      if [ "$(uname -s)" = Darwin ]; then def="open -a $aname"; else def="$aname"; fi
      ask "Command that launches it" "$def"
      APPS="$APPS  $aname: $(argv_yaml "$REPLY")
"
      ok "added $aname"
    done

    {
      echo "# Written by scripts/setup.sh. Edit freely."
      echo "coordinator: \"$COORD\""
      echo "name: \"$NAME\""
      echo "kind: desktop"
      if [ -n "$INSECURE" ]; then echo "$INSECURE"; fi
      if [ -n "$WS" ]; then printf 'workspaces:\n%s' "$WS"; else echo "workspaces: {}"; fi
      if [ -n "$APPS" ]; then printf 'apps:\n%s' "$APPS"; else echo "apps: {}"; fi
      echo "web_search: false"
    } > runner.yaml
    ok "runner.yaml written"
    if [ -z "$WS" ] && [ -z "$APPS" ]; then
      warn "no workspaces or apps: the phone will have nothing to run here. Edit runner.yaml or re-run."
    fi
  fi
fi

if [ "$IOS" = 1 ] && [ -d ios/BlooperRemote ]; then
  say "iOS app"
  if ! command -v xcodegen >/dev/null 2>&1; then
    warn "xcodegen not found: brew install xcodegen, then run this again (or skip with --no-ios)"
  elif ! xcodebuild -version >/dev/null 2>&1; then
    warn "Xcode is not installed or not selected. Install it from the App Store, then run: sudo xcode-select -s /Applications/Xcode.app"
  elif yes_no "    Generate the iOS Xcode project now?" y; then
    if (cd ios/BlooperRemote && xcodegen generate >/dev/null); then
      ok "ios/BlooperRemote/BlooperRemote.xcodeproj"
    else
      warn "xcodegen failed; see the output above"
    fi
  fi
fi

say "Done"
echo "    Start the server (run from this directory):"
echo "      make serve"
echo "    It prints a pairing QR. Scan it in the blooper Remote app."
if [ "$RUNNER" = yes ] && [ -f runner.yaml ]; then
  echo
  echo "    Start the runner on this computer, in your logged-in desktop session:"
  echo "      make runner"
fi
if [ "$SCHEME" = http ]; then
  echo
  echo "    Plain HTTP on the LAN. Phones on other networks cannot reach this server."
fi
