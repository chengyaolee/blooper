# Blooper

Run an agent fleet on your computer and control it from your iPhone. Blooper starts with
**devices on the same Wi-Fi**, with provider keys stored locally on the computer doing the work.
The desktop app bundles the coordinator, runner, chat, provider settings, and device pairing.
OpenAI works throughout the fleet without Ollama; local models are optional.

**Current status:** macOS Apple Silicon and Windows x64 development installers can be built
from this repository. The iPhone companion is provided as SwiftUI source. Production signing,
App Store distribution, and publication of automatic-update releases are separate release steps.
Tailscale, a hosted relay, and cross-network connectivity are deferred.

## Set up the desktop app

If you have a built installer, open the macOS DMG and copy Blooper to Applications, or run the
Windows installer. Installed users do not need Go, Node.js, a terminal, or a configuration file.
Development installers are unsigned and are intended for testing, not public distribution.

1. Open **Blooper → Settings**.
2. Select **OpenAI**, enter an API key from the [OpenAI platform](https://platform.openai.com/api-keys),
   and choose **Validate and save**. The default model is `gpt-5.4-mini`.
3. Optionally add a [Deepgram](https://console.deepgram.com/) key for hosted voice transcription.
   Without one, the iPhone can use on-device transcription.
4. Open **Permissions**, select the workspace folders and test commands you want to allow,
   and optionally select applications. Choose **Save permissions**.
5. Open **Devices → Create pairing code**. Keep this screen open while pairing your phone.

Keys are encrypted using the computer's OS-backed credential store: macOS Keychain or Windows
DPAPI. Settings supports validation, replacement, and deletion. Saved key values are never
returned to the desktop renderer or a paired phone. The selected provider receives the API key
for authentication and the request data needed to do the work; local key storage does not imply
local inference. Provider validation can use a small amount of API credit.

To use **Ollama** instead, install and start it on the computer, pull a model such as
`qwen2.5:3b`, then select **Ollama** and that model in Settings. Conversation recall uses local
text search and does not require an embedding model.

Closing the window keeps Blooper running in the menu bar/system tray. Use **Quit Blooper** to
stop it. Enable **Launch Blooper when I sign in** for automatic startup in installed builds.
Update settings can automatically check for and download published releases; the app offers a
restart to install an update after active jobs finish. Production updates require signed releases.

## Run the desktop app from a clone

Requires Git, **Go 1.27.1 or newer** (see `go.mod`), **Node.js 22 or newer**, and macOS or Windows.
From Terminal or PowerShell:

```sh
git clone https://github.com/chengyaolee/blooper.git
cd blooper
cd desktop
npm ci
npm start
```

This builds the Go runtime and starts the desktop interface. Enter API keys in Settings; the
desktop app does not silently import credentials from `.env`. No separate coordinator or runner
command is needed.

On macOS, `make desktop` from the repository root runs the same setup. On Windows, the npm
commands above work without installing Make.

To build an installer on the target desktop platform:

```sh
cd desktop
npm ci
npm run dist
```

Build output goes into `desktop/dist/`: a DMG/ZIP on macOS or an NSIS installer on Windows.
For an unpacked macOS development bundle without automatically selecting a signing identity:

```sh
CSC_IDENTITY_AUTO_DISCOVERY=false npm run pack
```

See [desktop setup and release instructions](docs/DESKTOP.md#build-and-release) for signing,
notarization, CI build artifacts, update publication, and the remaining platform tests.

## Build and pair the iPhone app

Building the iPhone app requires a Mac with **full Xcode**, XcodeGen, and an Apple signing team.
The project targets iOS 17 or newer. Command Line Tools alone do not include the iOS SDK.

```sh
cd ios/BlooperRemote
xcodegen generate
open BlooperRemote.xcodeproj
```

1. In Xcode, select your development team and a unique bundle identifier in `project.yml`, then
   regenerate if you change the project settings. The notification entitlement requires a signing
   profile that supports Push Notifications; see [APNs setup](docs/REMOTE.md#push-notifications).
2. Build and run on your iPhone. Allow camera access when scanning and local-network access
   when connecting.
3. Keep the phone and computer on the **same Wi-Fi**. Open Blooper on the phone, choose
   **Scan pairing code**, and scan the desktop's QR code.
4. The QR verifies the computer's TLS certificate. Requests are encrypted and signed; the phone
   stores the trusted computer identity in its device-only Keychain.
5. Use **Computers → Pair another computer** to add a Mac or Windows computer. Select which
   paired computer to control. Each computer keeps its own provider keys, permissions, and jobs.

A green device icon with **Connected** indicates recent contact. An offline icon with **Offline**
indicates missing contact. A backgrounded phone can appear offline because its heartbeat stops.
The desktop must be awake and running Blooper to execute work.

Previously paired devices use Bonjour/mDNS to rediscover their computer. If pairing fails,
check local-network permission and the desktop firewall's private-network access. Guest Wi-Fi
and access-point isolation can block device-to-device connections. If discovery is unavailable,
create and scan a fresh QR code. See [local connectivity details](docs/DESKTOP.md).

## Advanced: command-line and self-hosted setup

The CLI remains available for separate coordinators and runners. From the repository root,
set the provider key in the shell that starts Blooper:

```sh
# macOS/Linux shell
export OPENAI_API_KEY="your-key"
export DEEPGRAM_API_KEY="your-key"  # optional

go run ./cmd/blooper serve -config config/blooper.yaml
```

In PowerShell:

```powershell
$env:OPENAI_API_KEY = "your-key"
$env:DEEPGRAM_API_KEY = "your-key"  # optional

go run ./cmd/blooper serve -config config/blooper.yaml
```

With the default `remote.planner: auto`, an OpenAI key selects hosted inference for chat,
delegation, and remote planning. To run locally, select `remote.planner: ollama` and install the
models configured in `agents/` and `config/blooper.yaml`.

The direct `go run` command does not load `.env`. The existing macOS/Linux setup script
(`./scripts/setup.sh`, or `make setup`) creates a local configuration; `make serve` loads `.env`
and starts that configuration. Never commit `.env`, API keys, APNs keys, or runner identities.

For separately enrolled computers, copy `config/runner.example.yaml` to `runner.yaml`, configure
its coordinator URL and permitted actions, then run:

```sh
go run ./cmd/blooper runner -config runner.yaml
```

Use HTTPS for network connections. The desktop app handles local certificates and pairing
itself; advanced self-hosted installations manage their own transport. The iPhone's Advanced
connection section supports manual server addresses with publicly trusted TLS certificates.
See [remote coordinator, runner, TLS, and APNs instructions](docs/REMOTE.md).

Push delivery through a Blooper-operated notification service is outside this local-first phase.
The advanced self-hosted server supports owner-configured APNs. Customers do not need an APNs
signing key for local desktop operation, and public desktop installers must never contain one.

## Optional browser tool

Basic chat, delegation, web search, drafting, and runner actions do not require this integration.
The `browser` tool uses [Scrapling](https://github.com/D4Vinci/Scrapling)'s MCP server. To enable
it on macOS/Linux, install Python 3.10 or newer and run:

```sh
python3 -m venv ~/.venvs/scrapling
~/.venvs/scrapling/bin/pip install "scrapling[ai]"
~/.venvs/scrapling/bin/scrapling install
export SCRAPLING_MCP_BIN=~/.venvs/scrapling/bin/scrapling-mcp
```

Launch Blooper with that environment, or put `scrapling-mcp` on its PATH. For GUI launches,
ensure the desktop process can find the integration. Headless Linux installations may also
need Xvfb. A missing browser integration returns an installation hint.

## Agent permissions and approvals

An agent's YAML `tools` list is its allowlist: unlisted shared tools are neither exposed nor
executable. Empty means no shared tools. Memory helpers (`read_artifact`, `remember_rule`)
remain managed by the agent's memory orchestrator. Unknown tool names fail validation.

`require_approval_for` must name tools in the same allowlist. An intrinsic tool approval
requirement always applies. The agent pauses until approval or rejection; disconnecting the
chat cancels pending approval, and a decision cannot be reused.

Delegation requires `delegate` in `tools` plus permitted targets in `can_delegate_to`. Target
restrictions and depth limits apply, including approvals inside delegated agents. Chat agent
delegation and the phone's runner-job tools remain separate execution paths.

## Verification

From the repository root:

```sh
go test -race ./...
go build ./...
go vet ./...
```

Desktop credential-storage checks:

```sh
cd desktop
npm ci
npm test
```

On macOS, after building the desktop runtime, the shell and TLS-pinning checks run from the
repository root with isolated temporary profiles:

```sh
./desktop/node_modules/.bin/electron desktop/test/smoke.cjs
python3 scripts/test-local-trust.py
```

[Desktop verification instructions](docs/DESKTOP.md#checks) include opt-in live OpenAI and Ollama
delegation checks. [Hosted provider checks](docs/REMOTE.md#hosted-service-keys-and-checks) cover
OpenAI and Deepgram. These live checks require keys and can incur provider charges. Full iOS
builds, physical-device pairing, Windows installation, and production update cycles still need
platform validation.
