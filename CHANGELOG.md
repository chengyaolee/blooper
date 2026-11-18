# Changelog

A day-by-day log of what was committed to blooper and what changed. Newest day first; within a day, commits are listed oldest first. Each entry is `short-hash` followed by the commit subject, then a summary of the change.

## Unreleased

- Adopt the supplied blooper wordmark and app artwork for the desktop, web chat and iPhone apps; use lowercase product labels and preserve the desktop credential-storage identity.

- Fix iPhone QR scanning startup: request camera permission and start recognition after presentation, process updated detections, and report camera errors. Add a visible Cancel button, retry controls, and a shared scanner for pairing and enrollment.

- Add macOS/Windows desktop application packaging with a bundled coordinator, local runner, tray/menu-bar access, optional login startup, and update checks/downloads. Production signing and update publication remain release steps.
- Add OS-protected provider settings with key validation, replacement and deletion. OpenAI runs the whole agent fleet without Ollama; recall has a local text-search mode. Phones receive connection status, never provider keys.
- Add same-Wi-Fi QR pairing with certificate pinning, a private settings listener, stable ports, local discovery, active/offline device indicators, and iPhone switching between separately paired Mac and Windows computers.
- Add desktop runtime/security integration checks, native shell smoke checks and a real TLS-pinning check for the shared iOS client. Defer Tailscale and hosted relay connectivity.

- Enforce per-agent tool allowlists and validate configured approval tools. Web Researcher explicitly has `delegate`; remove approval names for tools that do not exist.
- Pause chat and delegated calls for single-use approval decisions, preserving caller and cancellation context. Fix fragmented SSE parsing so approval cards survive network chunk boundaries.
- Use the installed `qwen2.5:3b` for shipped agents and the local planner; validate startup model availability and configured fallbacks.
- Replace the hosted DeepSeek adapter with OpenAI Chat Completions (`gpt-5.4-mini` by default), including provider-compatible token limits and opt-in live checks for OpenAI and Deepgram.
- Add optional APNs alerts for completed jobs and jobs needing input, signed token registration/removal, persistent token storage, invalid-token cleanup, and iOS job navigation from notifications. Document Apple signing and credential setup.
- Verified a real Ollama chat delegation chain, OpenAI `gpt-5.4-mini` tool calls and streaming, and Deepgram `nova-3` transcription. Validated the local APNs key as P-256 and the configured bundle ID/environment; real APNs delivery and iOS build/device testing remain manual.

## 2026-10-21

- `7745822` **Replace FTS5 conversation recall with ragout hybrid search**
  - Conversation recall is now indexed and searched by ragout (BM25 plus vector search via Ollama `nomic-embed-text`, fused with RRF) instead of SQLite FTS5.
  - SQLite stays the record of conversations. ragout owns indexing and search, with snapshots stored next to the database (`<db>.vec`, `<db>.bm25`).
  - The store API (`Open`, `SaveConversation`, `Recall`) is unchanged apart from an optional `WithEmbedder`. Recall dedupes multi-chunk hits per conversation.
  - Existing conversations are indexed on first open if no snapshot exists. The `conversations_fts` table is dropped.
  - Fix: recall parsed `created_at` with the wrong layout and ignored the error, so `CreatedAt` was always zero. It now uses RFC3339.
  - Known gaps: no relevance cutoff on vector hits (unrelated queries still return the nearest records), and a failed embedding leaves a saved conversation unindexed.
  - Touched: `internal/memory/{recall,store,store_test}.go`, `internal/tools/recall.go`, `go.mod`, `go.sum`.
- `8ac27e4` **Move browser tool from go-rod to scrapling-mcp**
  - The browser tool now drives Chrome through Scrapling's MCP server over stdio instead of go-rod/stealth.
  - One stealth session is kept open across calls and is restarted if the subprocess dies.
  - On Linux servers without a display, the browser runs headful on Xvfb when it is available.
  - Added `TestBrowserRecovers`, which kills the child process mid-session and checks that the next call recovers.
  - Touched: `internal/tools/browser.go`, `internal/tools/browser_test.go`, `go.mod`, `go.sum`.
- `65190a1` **Document browser tool setup for scrapling-mcp**
  - README now covers the venv install, `SCRAPLING_MCP_BIN`, and the Chrome requirement.
- `ad6b3f9` **Revise learning guide tasks and checklist formatting**
  - Rewrote the Track 1 and Track 3 sections of `LEARNING_GUIDE.md` and converted the table of contents to task checkboxes.
  - The Task 1.4 go-rod/stealth material is unchanged and still needs updating for Scrapling.
- `0c3dfa9` **Add completion-reviewer agent**
  - Added a read-only subagent (`.claude/agents/completion-reviewer.md`) that checks whether an implementation delivers the original request, using the plan file and the diff as evidence.

## 2026-10-20

- `d9cb817` **Add hierarchical memory layer with persistent user rules**
  - New `internal/memory.Orchestrator` decides what enters the model's context instead of leaving that to the agent loop. Task state, long-term summaries, artifact pointers, and a verbatim scratchpad of recent steps are assembled into layered prompts.
  - Tool output over 2000 characters is stored as an artifact outside the context. The model gets a digest (sample lines plus error and warning lines) and can fetch more through a `read_artifact` tool, capped per call.
  - Compaction evicts the oldest scratchpad steps once a size or step threshold is hit, summarizing them into a structured record (findings, queries tried, ruled-out hypotheses, artifact IDs). Long-term memory is itself re-compacted when it grows too large. If summarization fails, a deterministic record is used so tool calls and artifact pointers are never silently dropped.
  - Persistent user rules (`MEMORY.md`): a `remember_rule` tool saves standing instructions, but only ones grounded in words the user typed in the conversation, so a fetched web page or log cannot plant a rule via prompt injection. Standing instructions ("always/never/don't...") repeated across `RepeatThreshold` separate user messages are auto-promoted, with counts tracked in `data/instructions.json`. Rules are injected into every prompt right after the persona.
  - `internal/agent/agent.go`: `Run` and `RunStream` now route all prompt building and tool-result handling through the orchestrator. The previously duplicated tool-execution code in both is now one shared helper.
  - Touched: `internal/agent/{agent,agent_test}.go`, `internal/memory/{context,context_test,rules,rules_test}.go`.

## 2026-09-23

- `dce84f0` **Update Go module dependencies, add browser testing functionality, and enhance BrowserTool capabilities**
  - Added a `fill` action for typing text into page elements and improved browser page management, with stealth mode integration.
  - Added `internal/tools/browser_test.go` (`TestBrowserDetection`, which screenshots a bot-detection page).
  - Updated `go.mod` and `go.sum`.
- `e8b5373` **KV cache manager**
  - Added `KVSlot` and `SlotManager` for managing VRAM context slots: allocation, eviction, and memory usage statistics.
  - Added unit tests for slot allocation and eviction logic.
  - Touched: `internal/llm/kv_allocator.go`, `internal/llm/kv_test.go`.

## 2026-09-21

- `32d2a5e` **Add initial project structure for blooper**
  - Go module setup, `.gitignore`, `Makefile`, `PLAN.md`, and `LEARNING_GUIDE.md`.
  - Agent configs: `agents/{agent_creator,email_drafter,web_researcher}.yaml`. Runtime config: `config/{blooper,hardware}.yaml`.
  - `internal/agent`: agent loop, config loading, creator, registry, and a file watcher, with tests.
  - `internal/core`: approval flow, delegation, router, and scheduler.
  - `internal/llm`: engine interface, Ollama client, streaming, model definitions, and a KV cache placeholder.
  - `internal/memory`: conversation store, recall, and summarizer, with tests.
  - `internal/tools`: browser (go-rod), delegate, files, recall, search, and the tool registry, with tests.
  - `internal/ui`: web server, HTML template, and static assets (`static/app.js`, `static/style.css`).
  - `experiments/custom_engine`: README and stub client.

## 2026-09-18

- `f062ec7` **Initial commit**
  - Repository created with a `.gitignore` and README.
