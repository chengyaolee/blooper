# blooper apps: local first

The desktop app hosts the coordinator, agents, chat and a local runner in one process tree.
Install it on a Mac or Windows computer, add provider credentials in Settings, and pair an iPhone
on the same Wi-Fi. There is no blooper account, hosted relay, Tailscale dependency, port forwarding,
or customer-managed backend in this phase.

## Everyday setup

1. Open blooper Desktop. Go to **Settings**, choose OpenAI, enter your API key and select
   **Validate and save**. The default model is `gpt-5.4-mini`. This works without Ollama.
   To use local inference instead, select Ollama and an installed model.
2. Optionally connect Deepgram. Its validation sends generated silence, not recorded user audio.
   Without it, the iPhone can continue using on-device transcription.
3. In **Permissions**, choose workspace folders and their test commands, and optionally select
   applications. Save the changes. Remote app launches still require explicit phone approval.
4. Open **Devices**, choose the Wi-Fi interface if several are listed, and create a QR code.
   Open the iPhone app, choose **Scan pairing code**, and scan the desktop's code.
5. Pair another Mac or Windows computer from the iPhone's **Computers** screen. Choose the
   computer you want to control; each computer keeps its own keys, permissions, and job history.

A green device icon and **Connected** label indicate recent contact. An offline icon and
**Offline** label indicate missing contact. Phones heartbeat every five seconds while active;
the desktop expires their presence after 60 seconds. A backgrounded iPhone can therefore appear
offline; this is not evidence that the app was uninstalled or unpaired. The iPhone marks the
selected computer offline after a failed connection, and shows provider connection status only.

The desktop maintains a stable local listening port across launches and advertises
`_blooper._tcp` using Bonjour/mDNS. A previously paired iPhone can rediscover a changed address,
but it must verify the saved certificate identity and existing enrollment before storing that
address. Guest Wi-Fi or corporate access-point isolation can prevent connections between devices.
If discovery is blocked, scanning a fresh desktop QR reconnects the same trusted computer.
Allow the desktop application through the operating system firewall on private networks.

Closing the desktop window keeps its runtime running; reopen it through the menu bar/system tray.
**Quit blooper** stops it and its running commands. Enable **Launch blooper when I sign in** in
Settings for automatic startup in installed builds. It is off until explicitly enabled.
The computer must be awake and running blooper to execute jobs.

## Local credentials and transport

- The Electron main process encrypts provider settings with `safeStorage`: macOS Keychain or
  Windows DPAPI protects the encryption key. The application stores only ciphertext in
  `providers.enc` under its per-user application-data directory. There is no plaintext fallback.
- Password fields are never filled from saved credentials. The renderer receives saved/not-saved
  flags; paired phones receive provider name, selected model, and connected/not-connected flags.
- Keys pass to the local Go runtime through an authenticated loopback request and remain in
  runtime memory. They are not written to YAML, logs, subprocess arguments, or runner command
  environments. They are sent directly to the relevant provider for API authentication.
- Validate-and-save supports initial setup and replacement. Delete removes the saved credential
  and disables it for new work. Already-running agent chains retain their original provider
  snapshot so settings changes do not switch models partway through execution.
- The desktop shell protects chat and settings using a random per-launch capability. Neither
  these routes nor the existing administrative CLI API are exposed on the LAN listener.
- LAN traffic uses TLS 1.2 or later. The single-use QR includes the exact certificate's SHA-256
  identity. The phone stores trusted computer identities in its device-only Keychain, rejects
  certificate changes and redirects, and signs its API requests using its existing Ed25519 key.
  Discovery records are address hints, not trusted replacements for the scanned identity.
- The local TLS private key and runner identity are owner-readable files in the runtime data
  directory. Provider credentials are independently protected by the OS-backed vault.

The runtime installs its bundled agents and chat assets into its own application-data directory.
User-created agents and output files remain separate from those built-in assets. Conversation
recall uses local BM25 text search, with no model download or embedding API required.
The optional Scrapling browser tool still requires its separately installed browser integration;
chat, delegation, web search, drafting, and configured runner actions do not require it.

Push delivery via a blooper-operated APNs service is outside this LAN phase. The existing advanced
self-hosted server still supports owner-configured APNs. Public desktop installers never contain
an APNs signing key. Relay connectivity, cross-network access and cloud execution are deferred.

## Develop from a clone

Requires Go matching `go.mod`, Node.js 22 or later, and macOS or Windows:

```sh
make desktop
```

Or run these commands from `desktop/`:

```sh
npm ci
npm start
```

The runtime and all mandatory UI/agent resources are bundled during the build. Installed users
need neither Go nor Node.js. The desktop app does not silently import `.env` into its credential
vault. Existing command-line users can continue using environment variables and
`go run ./cmd/blooper serve -config config/blooper.yaml`.

Hosted OpenAI now works across command-line chat and delegated agents as well. With the default
`remote.planner: auto`, setting `OPENAI_API_KEY` selects it throughout the fleet; `ollama` keeps
local inference. Custom self-hosted server URLs and runner enrollment remain available in the
CLI, and the iPhone's Advanced connection section accepts servers with publicly trusted TLS.

## Build and release

Run `npm run dist` from `desktop/` on the target platform. macOS builds produce a DMG and ZIP;
Windows builds produce an NSIS installer. `npm run pack` produces an unpacked application.
For a local macOS development bundle without selecting a personal signing identity:

```sh
CSC_IDENTITY_AUTO_DISCOVERY=false npm run pack
```

`.github/workflows/desktop.yml` builds macOS Apple Silicon, macOS Intel and Windows x64 artifacts.
It can be run manually or triggered with a `desktop-v*` tag. It uploads build artifacts for review;
it does not publish a release automatically.

Before distributing an install-and-run production release:

- Set the package version in `desktop/package.json` to the intended release version. The
  configured release feed is the `chengyaolee/blooper` GitHub repository; change it if needed.
- Sign and notarize macOS builds using a Developer ID Application certificate. Supply
  `MAC_CSC_LINK`, `MAC_CSC_KEY_PASSWORD`, `APPLE_ID`, `APPLE_APP_SPECIFIC_PASSWORD`, and
  `APPLE_TEAM_ID` in CI. Local development or ad-hoc signing is not production notarization.
- Sign Windows installers with your Windows signing identity. The workflow uses
  `WINDOWS_CSC_LINK` and `WINDOWS_CSC_KEY_PASSWORD` for that platform.
- Publish the installers, macOS update ZIPs, blockmaps, and generated `latest*.yml` metadata to a
  non-draft GitHub release. Keep signing identities consistent between versions.
- Test installation, OS credential storage, login startup, firewall consent, and an upgrade from
  a previous installed version on both desktop platforms. macOS automatic updates need a signed
  application. Cross-compilation does not replace a Windows installation test.
- Review build-tool dependency advisories before release. The shipped updater dependencies pass
  `npm audit --omit=dev`; the current stable packaging toolchain still reports development-only
  advisories through its glob matcher, for which npm has no published patched picomatch version.

Automatic updates check and download in installed builds when enabled. The UI offers
**Restart and install update** and refuses that restart while chat or remote jobs are active.
Development bundles and unpublished versions cannot demonstrate a production update cycle.

The SwiftUI iPhone app remains under `ios/BlooperRemote`. Regenerate with `xcodegen generate`,
then build using full Xcode, your bundle ID, and your team. The app declares local-network access
and `_blooper._tcp` discovery. App Store signing and submission are separate release steps.
Full Xcode and an installed iOS SDK are required. Camera recognition and Wi-Fi pairing must
also be exercised on a physical iPhone.

## Checks

```sh
go test -race ./internal/... ./cmd/...
go vet ./internal/... ./cmd/...
cd desktop && npm test
```

On macOS, after `npm run build:runtime`, run the actual shell smoke test from the repository root:

```sh
./desktop/node_modules/.bin/electron desktop/test/smoke.cjs
python3 scripts/test-local-trust.py
```

The shell check uses a temporary profile and never touches your saved provider credentials.
The TLS check exercises the shared iOS delegate against a real local HTTPS server, confirming
that a scanned identity succeeds and a mismatched certificate fails.

For a real OpenAI fleet delegation check, export the key into the test process and run:

```sh
BLOOPER_LIVE_OPENAI_FLEET=1 go test ./internal/ui -run '^TestLiveChatDelegation$' -count=1 -v
```

The existing `BLOOPER_LIVE_OLLAMA=1` check remains available for local inference. Neither check
needs a remote runner or relay. The OpenAI check makes billable API requests.

For the iPhone scanner cancellation/reopening UI check, use an available simulator from
`xcrun simctl list devices available`:

```sh
DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer python3 scripts/test-ios-scanner.py "SIMULATOR_UDID"
```

This creates an isolated test application and leaves your Xcode project and paired-device
credentials untouched. The simulator checks cancellation and reopening; real camera QR
recognition needs a physical iPhone. The scanner shows errors and offers retry or Settings
when camera access is denied, and both pairing and enrollment have an explicit Cancel button.

## Brand assets

The supplied `blooper-logo.png` is the wordmark used by desktop, web chat and iOS navigation.
`blooper-full-icon-512.png` supplies the macOS, Windows and iOS app icons. Packaged icons
place the artwork on white; iOS uses a 1024px version scaled from the supplied 512px source.
The display name is **blooper**. Bundle IDs, pairing protocols and the desktop storage identity
remain stable so existing credentials and paired devices keep working.
