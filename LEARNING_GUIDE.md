# Blooper: Complete Hands-On Engineering & Learning Guide

Welcome to the comprehensive, hands-on engineering guide for **Blooper**. 

This guide is designed for developers who want to learn how modern AI agent infrastructure, anti-bot browser automation, KV cache management, and protocol adapters work under the hood.

Every milestone below contains:
1. **The Objective**: What you are building and why.
2. **File Map**: Exactly which files to edit, create, or test.
3. **Architecture Blueprint**: Concrete code skeletons, imports, and data structures.
4. **Execution Command**: The exact terminal command to run and verify.
5. **Expected Output**: What success and failure look like.
6. **Common Pitfalls**: What bugs to avoid.

---

## Table of Contents
* [Track 1: Stealth Browser Automation & Bot Evasion](#track-1-stealth-browser-automation--bot-evasion)
  * [Milestone 1.1: Establishing the Baseline Benchmark](#milestone-11-establishing-the-baseline-benchmark)
  * [Milestone 1.2: Upgrading to Modern Headless (`--headless=new`)](#milestone-12-upgrading-to-modern-headless---headlessnew)
  * [Milestone 1.3: Suppressing Automation Flags & Webdriver](#milestone-13-suppressing-automation-flags--webdriver)
  * [Milestone 1.4: Pre-Navigation Script Injection (`go-rod/stealth`)](#milestone-14-pre-navigation-script-injection-go-rodstealth)
  * [Milestone 1.5: Humanizing Input Dynamics & Typing Jitter](#milestone-15-humanizing-input-dynamics--typing-jitter)
* [Track 2: Custom Inference Engine & KV Cache Management](#track-2-custom-inference-engine--kv-cache-management)
  * [Milestone 2.1: Mathematical Sizing & Hardware VRAM Budgeting](#milestone-21-mathematical-sizing--hardware-vram-budgeting)
  * [Milestone 2.2: Building the Slot-Based KV Cache Manager](#milestone-22-building-the-slot-based-kv-cache-manager)
  * [Milestone 2.3: Implementing Prefix Caching for Shared Agent Prompts](#milestone-23-implementing-prefix-caching-for-shared-agent-prompts)
* [Track 3: Model Context Protocol (MCP) Client](#track-3-model-context-protocol-mcp-client)
  * [Milestone 3.1: Building the Stdio JSON-RPC 2.0 Transport](#milestone-31-building-the-stdio-json-rpc-20-transport)
  * [Milestone 3.2: Wrapping MCP Tools into Blooper's Tool Registry](#milestone-32-wrapping-mcp-tools-into-bloopers-tool-registry)
* [Track 4: Hybrid Search Memory (BM25 + Local Vector Embeddings)](#track-4-hybrid-search-memory-bm25--local-vector-embeddings)
  * [Milestone 4.1: Generating Local Embeddings with Ollama](#milestone-41-generating-local-embeddings-with-ollama)
  * [Milestone 4.2: Vector Similarity & Reciprocal Rank Fusion (RRF)](#milestone-42-vector-similarity--reciprocal-rank-fusion-rrf)

---

# Track 1: Stealth Browser Automation & Bot Evasion

```
                    HOW DETECTORS SPOT YOUR BROWSER
                    
   Default Headless Rod                         Stealth-Patched Rod
  ┌──────────────────────────────┐             ┌──────────────────────────────┐
  │ navigator.webdriver: true   │             │ navigator.webdriver: undef   │
  │ WebGL: SwiftShader (CPU)    │    ───►     │ WebGL: Apple M5 / GTX 1080 Ti│
  │ Plugins length: 0            │             │ Plugins length: 3 (PDF/etc)  │
  │ Keystroke delay: 0ms         │             │ Keystroke delay: 40-110ms    │
  └──────────────────────────────┘             └──────────────────────────────┘
             [BLOCKED]                                    [PASSED]
```

---

### Milestone 1.1: Establishing the Baseline Benchmark

#### Objective
Run our existing browser tool against an anti-bot test site (`https://bot.sannysoft.com`) and produce a screenshot showing which browser features fail the bot test.

#### File Map
* **Existing File**: `internal/tools/browser.go` (the browser implementation)
* **New File to Add**: `internal/tools/browser_test.go` (the test harness)
* **Output Artifact**: `data/screenshots/screenshot_*.png` (visual evidence)

#### Implementation Steps
1. Create `internal/tools/browser_test.go`.
2. Instantiate `NewBrowserTool(true)` (runs in headless mode).
3. Call `Execute(ctx, payload)` asking for a screenshot of `https://bot.sannysoft.com`.

#### Code Blueprint (`internal/tools/browser_test.go`)
```go
package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestBrowserDetection(t *testing.T) {
	// Step 1: Create the browser tool in headless mode
	tool := NewBrowserTool(true)
	defer tool.Close()

	// Step 2: Prepare the screenshot action payload
	payload, err := json.Marshal(map[string]string{
		"action": "screenshot",
		"url":    "https://bot.sannysoft.com",
	})
	if err != nil {
		t.Fatalf("failed to marshal payload: %v", err)
	}

	// Step 3: Allow up to 45 seconds for page load and asset rendering
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	t.Log("Navigating to https://bot.sannysoft.com and taking screenshot...")

	// Step 4: Execute the browser tool
	result, err := tool.Execute(ctx, payload)
	if err != nil {
		t.Fatalf("tool execution failed: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("tool returned error: %s", result.Error)
	}

	t.Logf("Result: %s", result.Output)
}
```

#### How to Run & Verify
```bash
go test -v -run TestBrowserDetection ./internal/tools
```
Open the screenshot:
```bash
open data/screenshots/screenshot_*.png
```

#### What to Look For
* `WebDriver (New)`: **RED (Failed - present)**
* `Chrome`: **RED**
* `User Agent`: Contains `HeadlessChrome` or generic headless markers.

---

### Milestone 1.2: Upgrading to Modern Headless (`--headless=new`)

#### Objective
Legacy `--headless` runs a stripped-down browser shell that has distinct rendering quirks. Chrome 112+ introduced `--headless=new`, which uses the full desktop Chrome browser pipeline rendered into a memory buffer.

#### File Map
* **File to Edit**: `internal/tools/browser.go` (inside `getBrowser()`, lines ~80–95)

#### Implementation Steps
1. Locate `l := launcher.New()` in `getBrowser()`.
2. Notice `Headless(t.headless)`. In Rod, `Headless(true)` sets the old `--headless` flag.
3. Replace it by explicitly setting `--headless=new`.

#### Code Blueprint (`internal/tools/browser.go`)
```go
// Inside getBrowser():
l := launcher.New().
	Set("headless", "new"). // <-- Replaces Headless(true)
	Set("no-sandbox").
	Set("disable-gpu").
	Set("disable-dev-shm-usage")
```

#### How to Run & Verify
```bash
go test -v -run TestBrowserDetection ./internal/tools
open data/screenshots/screenshot_*.png
```
Compare this screenshot with Milestone 1.1. Notice how font smoothing, window dimensions, and media rendering now match genuine desktop Chrome.

---

### Milestone 1.3: Suppressing Automation Flags & Webdriver

#### Objective
Chrome sets `window.navigator.webdriver = true` whenever DevTools or automation controls are connected. We must tell the Blink engine to disable automation indicators.

#### File Map
* **File to Edit**: `internal/tools/browser.go` (inside `getBrowser()`)

#### Implementation Steps
1. Add Chrome launcher flag `disable-blink-features=AutomationControlled`.
2. Add a realistic desktop User-Agent string (matching macOS or Linux).
3. Set a standard desktop window size (e.g. `1920,1080`).

#### Code Blueprint (`internal/tools/browser.go`)
```go
// Inside getBrowser():
l := launcher.New().
	Set("headless", "new").
	Set("disable-blink-features", "AutomationControlled"). // <-- Key anti-detection flag
	Set("user-agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36").
	Set("window-size", "1920,1080").
	Set("no-sandbox").
	Set("disable-dev-shm-usage")
```

#### How to Run & Verify
```bash
go test -v -run TestBrowserDetection ./internal/tools
open data/screenshots/screenshot_*.png
```
Look at the **`WebDriver`** row in Sannysoft. It should now show **green** (`missing (passed)`)!

---

### Milestone 1.4: Pre-Navigation Script Injection (`go-rod/stealth`)

#### Objective
Even with flags, anti-bot scripts query deep browser properties: `navigator.plugins`, `navigator.languages`, WebGL vendor (`SwiftShader` vs hardware GPU), and `Notification.permission`. 
We inject `go-rod/stealth` before any page script executes.

#### File Map
* **File to Edit**: `internal/tools/browser.go`
* **Dependency**: `github.com/go-rod/stealth` (already fetched)

#### Implementation Steps
1. Import `"github.com/go-rod/stealth"`.
2. When creating a new page in `Execute()` (`case "navigate"` and `case "screenshot"`), evaluate `stealth.JS` using `page.EvalOnNewDocument()`.
3. `EvalOnNewDocument` guarantees the JavaScript shims run **before** Sannysoft or Cloudflare scripts can query the environment.

#### Code Blueprint (`internal/tools/browser.go`)
```go
import (
	// ... existing imports ...
	"github.com/go-rod/stealth"
)

// Inside Execute(), right after page is created:
page, err := browser.Context(ctxTimeout).Page(proto.TargetCreateTarget{URL: params.URL})
if err != nil {
	return &ToolResult{Error: err.Error()}, nil
}
defer page.Close()

// Inject stealth scripts before page loads!
_, err = page.EvalOnNewDocument(stealth.JS)
if err != nil {
	// Log warning or continue
}

_ = page.WaitLoad()
```

#### How to Run & Verify
```bash
go test -v -run TestBrowserDetection ./internal/tools
open data/screenshots/screenshot_*.png
```
**The Milestone Goal**: All major rows on Sannysoft (`WebDriver`, `Chrome`, `Permissions`, `Plugins`, `WebGL`) should now be **GREEN**!

---

### Milestone 1.5: Humanizing Input Dynamics & Typing Jitter

#### Objective
Bot detectors record typing cadences in forms. If 30 characters appear in 2 milliseconds, the request gets challenged with a CAPTCHA. We create a helper that types with natural randomized pauses.

#### File Map
* **File to Edit**: `internal/tools/browser.go`

#### Implementation Steps
1. Write a private helper function `humanType(el *rod.Element, text string) error`.
2. Loop over each character, call `el.Input(string(char))`, and sleep a randomized duration between 40ms and 110ms.
3. In `Execute()` for a new action `"fill"`, invoke `humanType`.

#### Code Blueprint (`internal/tools/browser.go`)
```go
import (
	"math/rand"
	"time"
)

func humanType(el *rod.Element, text string) error {
	for _, char := range text {
		if err := el.Input(string(char)); err != nil {
			return err
		}
		// Random delay between 40ms and 110ms per keystroke
		delay := time.Duration(40+rand.Intn(70)) * time.Millisecond
		time.Sleep(delay)
		
		// 5% chance of a longer pause (mimicking human hesitation)
		if rand.Float32() < 0.05 {
			time.Sleep(250 * time.Millisecond)
		}
	}
	return nil
}
```

---

# Track 2: Custom Inference Engine & KV Cache Management

```
               THE KV CACHE LIFECYCLE FOR AGENTS
               
   [Agent System Prompt: 1,200 tokens] 
                  │
                  ▼ Prefill Phase (Computed Once)
   ┌────────────────────────────────────────────────────────┐
   │  Slot 0: Cached Key/Value Matrices (~150 MB in VRAM)   │
   └────────────────────────────────────────────────────────┘
                  │
         Shared Prefix Link (Copy-on-Write)
        ┌─────────┴─────────┐
        ▼                   ▼
   Agent A (Research)   Agent B (Email)
   Appends Turn 1       Appends Turn 1
   (+15 MB)             (+15 MB)
```

---

### Milestone 2.1: Mathematical Sizing & Hardware VRAM Budgeting

#### Objective
Understand the exact formula governing Key-Value (KV) cache memory consumption so you can configure slot boundaries for both your M5 Pro (24GB) and Linux 1080 Ti (11GB).

#### The Formula
$$\text{KV Bytes per Token} = 2 \times \text{layers} \times \text{kv\_heads} \times \text{head\_dim} \times \text{precision\_bytes}$$

Where:
* $2$ accounts for both the **Key** and **Value** tensors.
* $\text{layers}$: Number of transformer layers in the model.
* $\text{kv\_heads}$: Number of Key-Value attention heads (in Grouped-Query Attention models like Qwen and Llama 3, this is typically 8, whereas query heads are 32).
* $\text{head\_dim}$: Dimension per head (usually 128).
* $\text{precision\_bytes}$: $2$ bytes for FP16 or BF16.

#### Sizing Exercise
For **Qwen3 8B**:
* Layers: 32
* KV Heads: 8
* Head Dim: 128
* Precision: 2 bytes (FP16)
$$\text{Per Token} = 2 \times 32 \times 8 \times 128 \times 2 = 131,072 \text{ bytes} \approx 128 \text{ KB / token}$$

#### Budget Table:
| Context Window | KV Cache per Agent | 2 Concurrent Agents | VRAM Headroom on GTX 1080 Ti (11GB)* |
| :--- | :--- | :--- | :--- |
| **2,048 tokens** | ~256 MB | ~512 MB | ~5.5 GB free (Very Safe) |
| **4,096 tokens** | ~512 MB | ~1.0 GB | ~5.0 GB free (Recommended) |
| **8,192 tokens** | ~1.0 GB | ~2.0 GB | ~4.0 GB free (Upper Limit) |
| **16,384 tokens** | ~2.0 GB | ~4.0 GB | ~2.0 GB free (Risk of OOM) |

*\*Assuming 8B model weights take ~5.0 GB in Q4_K_M.*

---

### Milestone 2.2: Building the Slot-Based KV Cache Manager

#### Objective
Implement a real memory allocator in Go that manages context slots, tracks token usage, and evicts least-recently-used (LRU) sessions when VRAM capacity is reached.

#### File Map
* **Interface**: `internal/llm/kv_placeholder.go`
* **New File to Add**: `internal/llm/kv_allocator.go`
* **Test File to Add**: `internal/llm/kv_test.go`

#### Implementation Steps
1. Create a `KVSlot` struct representing a VRAM context buffer.
2. Create a `SlotManager` struct holding `maxSlots`, `maxTokensPerSlot`, and an active slot map.
3. Implement `AllocateSlot(ctx, sessionID, tokens)`:
   * If `sessionID` already has a slot, update its `lastUsed` timestamp and return.
   * If an empty slot exists, reserve it.
   * If all slots are full, invoke `EvictLRU()`.
4. Implement `EvictLRU()`: finds the slot with the oldest `lastUsed` time and frees it.

#### Code Blueprint (`internal/llm/kv_allocator.go`)
```go
package llm

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type KVSlot struct {
	ID        int
	SessionID string
	Tokens    int
	LastUsed  time.Time
	Active    bool
}

type SlotManager struct {
	mu               sync.Mutex
	slots            []*KVSlot
	maxSlots         int
	maxTokensPerSlot int
	bytesPerToken    int64
}

func NewSlotManager(maxSlots, maxTokensPerSlot int, bytesPerToken int64) *SlotManager {
	slots := make([]*KVSlot, maxSlots)
	for i := 0; i < maxSlots; i++ {
		slots[i] = &KVSlot{ID: i, Active: false}
	}
	return &SlotManager{
		slots:            slots,
		maxSlots:         maxSlots,
		maxTokensPerSlot: maxTokensPerSlot,
		bytesPerToken:    bytesPerToken,
	}
}

func (m *SlotManager) AllocateSlot(ctx context.Context, sessionID string, expectedTokens int) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 1. Check if session already owns a slot
	for _, slot := range m.slots {
		if slot.Active && slot.SessionID == sessionID {
			slot.LastUsed = time.Now()
			slot.Tokens = expectedTokens
			return slot.ID, nil
		}
	}

	// 2. Find an empty slot
	for _, slot := range m.slots {
		if !slot.Active {
			slot.Active = true
			slot.SessionID = sessionID
			slot.Tokens = expectedTokens
			slot.LastUsed = time.Now()
			return slot.ID, nil
		}
	}

	// 3. No empty slots — evict oldest (LRU)
	oldestIdx := -1
	var oldestTime time.Time

	for i, slot := range m.slots {
		if oldestIdx == -1 || slot.LastUsed.Before(oldestTime) {
			oldestIdx = i
			oldestTime = slot.LastUsed
		}
	}

	if oldestIdx != -1 {
		slot := m.slots[oldestIdx]
		slot.SessionID = sessionID
		slot.Tokens = expectedTokens
		slot.LastUsed = time.Now()
		return slot.ID, nil
	}

	return 0, fmt.Errorf("failed to allocate KV slot")
}

func (m *SlotManager) FreeSlot(ctx context.Context, slotID int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if slotID < 0 || slotID >= len(m.slots) {
		return fmt.Errorf("invalid slot ID: %d", slotID)
	}

	m.slots[slotID].Active = false
	m.slots[slotID].SessionID = ""
	m.slots[slotID].Tokens = 0
	return nil
}

func (m *SlotManager) Stats() (*KVMetrics, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	active := 0
	var totalMemory int64

	for _, s := range m.slots {
		if s.Active {
			active++
			totalMemory += int64(s.Tokens) * m.bytesPerToken
		}
	}

	return &KVMetrics{
		ActiveSlots:     active,
		TotalSlots:      m.maxSlots,
		UsedMemoryBytes: totalMemory,
	}, nil
}
```

#### Verification Test (`internal/llm/kv_test.go`)
```go
package llm

import (
	"context"
	"testing"
)

func TestSlotManagerAllocationAndEviction(t *testing.T) {
	// Create manager with 2 slots max (simulating GTX 1080 Ti limits)
	mgr := NewSlotManager(2, 4096, 131072) // 128 KB per token

	ctx := context.Background()

	// Allocate Slot 0
	s0, err := mgr.AllocateSlot(ctx, "session-A", 1000)
	if err != nil || s0 != 0 {
		t.Fatalf("expected slot 0, got %d, err: %v", s0, err)
	}

	// Allocate Slot 1
	s1, err := mgr.AllocateSlot(ctx, "session-B", 2000)
	if err != nil || s1 != 1 {
		t.Fatalf("expected slot 1, got %d, err: %v", s1, err)
	}

	// Allocate Slot 2 (Capacity full! Should evict session-A)
	s2, err := mgr.AllocateSlot(ctx, "session-C", 1500)
	if err != nil {
		t.Fatalf("allocation with LRU eviction failed: %v", err)
	}
	t.Logf("Session-C assigned slot: %d (evicted oldest)", s2)

	stats, _ := mgr.Stats()
	if stats.ActiveSlots != 2 {
		t.Errorf("expected 2 active slots, got %d", stats.ActiveSlots)
	}
}
```

#### How to Run
```bash
go test -v -run TestSlotManager ./internal/llm
```

---

### Milestone 2.3: Implementing Prefix Caching for Shared Agent Prompts

#### Objective
Fleet agents share identical base tool schemas (~1,200 tokens). Prefix caching hashes the system prompt tokens and points multiple slots to the same physical memory block until an agent appends custom messages.

#### Guiding Architectural Concept
```go
// Concept:
// prefixHash = sha256(systemPrompt + toolDefinitions)
// If cache[prefixHash] exists in GPU memory:
//   Don't re-compute prompt attention!
//   Directly copy KV pointer into slot starting at offset N.
```

---

# Track 3: Model Context Protocol (MCP) Client

```
                       MCP ARCHITECTURE IN BLOOPER
                       
   ┌─────────────────────────────────────────────────────────────┐
   │                       Blooper Core                          │
   │                                                             │
   │  llm.InferenceEngine ──► Model outputs:                     │
   │                          call_tool("read_emails", {...})    │
   └─────────────────────────────────────────────────────────────┘
                                  │
                                  ▼
   ┌─────────────────────────────────────────────────────────────┐
   │           internal/tools/mcp_adapter.go                     │
   │                                                             │
   │  Translates Blooper Tool.Execute() into JSON-RPC 2.0:       │
   │  {"jsonrpc":"2.0","method":"tools/call","params":{...}}    │
   └─────────────────────────────────────────────────────────────┘
                                  │ (stdin/stdout pipe)
                                  ▼
   ┌─────────────────────────────────────────────────────────────┐
   │             External MCP Server (Subprocess)                │
   │                                                             │
   │  e.g. Email Server (Go / Python / Node)                     │
   │  Returns: {"jsonrpc":"2.0","result":{"content":[...]}}      │
   └─────────────────────────────────────────────────────────────┘
```

---

### Milestone 3.1: Building the Stdio JSON-RPC 2.0 Transport

#### Objective
Launch an MCP server process, establish communication over `stdin` and `stdout`, and complete the JSON-RPC `initialize` handshake.

#### File Map
* **New File to Add**: `internal/tools/mcp_client.go`
* **Test File to Add**: `internal/tools/mcp_test.go`

#### Code Blueprint (`internal/tools/mcp_client.go`)
```go
package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
)

type JSONRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int64       `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type MCPClient struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	reader    *bufio.Reader
	mu        sync.Mutex
	seq       int64
	pending   map[int64]chan *JSONRPCResponse
}

func NewMCPClient(command string, args ...string) (*MCPClient, error) {
	cmd := exec.Command(command, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start mcp process: %w", err)
	}

	client := &MCPClient{
		cmd:     cmd,
		stdin:   stdin,
		reader:  bufio.NewReader(stdout),
		pending: make(map[int64]chan *JSONRPCResponse),
	}

	// Start background reader loop
	go client.readLoop()

	return client, nil
}

func (c *MCPClient) readLoop() {
	for {
		line, err := c.reader.ReadBytes('\n')
		if err != nil {
			return
		}

		var resp JSONRPCResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			continue
		}

		c.mu.Lock()
		ch, found := c.pending[resp.ID]
		if found {
			delete(c.pending, resp.ID)
			ch <- &resp
		}
		c.mu.Unlock()
	}
}

func (c *MCPClient) Call(ctx context.Context, method string, params interface{}) (*JSONRPCResponse, error) {
	id := atomic.AddInt64(&c.seq, 1)

	req := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')

	respChan := make(chan *JSONRPCResponse, 1)
	c.mu.Lock()
	c.pending[id] = respChan
	c.mu.Unlock()

	c.mu.Lock()
	_, err = c.stdin.Write(data)
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp := <-respChan:
		if resp.Error != nil {
			return nil, fmt.Errorf("mcp error (%d): %s", resp.Error.Code, resp.Error.Message)
		}
		return resp, nil
	}
}

func (c *MCPClient) Close() error {
	_ = c.stdin.Close()
	return c.cmd.Process.Kill()
}
```

---

### Milestone 3.2: Wrapping MCP Tools into Blooper's Tool Registry

#### Objective
Expose tools discovered from an MCP server as standard Blooper `tools.Tool` instances so any agent can use them natively.

#### File Map
* **New File to Add**: `internal/tools/mcp_adapter.go`

#### Implementation Steps
1. Call `tools/list` on the MCP client to retrieve all tool definitions.
2. For each discovered tool, instantiate an `MCPToolAdapter`.
3. Register the adapters in Blooper's `ToolRegistry`.

#### Code Blueprint (`internal/tools/mcp_adapter.go`)
```go
package tools

import (
	"context"
	"encoding/json"
	"github.com/chengyaolee/blooper/internal/llm"
)

type MCPToolAdapter struct {
	client   *MCPClient
	name     string
	desc     string
	schema   llm.FunctionDef
	approval bool
}

func NewMCPToolAdapter(client *MCPClient, name, desc string, schema llm.FunctionDef, approval bool) *MCPToolAdapter {
	return &MCPToolAdapter{
		client:   client,
		name:     name,
		desc:     desc,
		schema:   schema,
		approval: approval,
	}
}

func (a *MCPToolAdapter) Name() string             { return a.name }
func (a *MCPToolAdapter) Description() string      { return a.desc }
func (a *MCPToolAdapter) Schema() llm.FunctionDef  { return a.schema }
func (a *MCPToolAdapter) RequiresApproval() bool   { return a.approval }

func (a *MCPToolAdapter) Execute(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	params := map[string]interface{}{
		"name":      a.name,
		"arguments": args,
	}

	resp, err := a.client.Call(ctx, "tools/call", params)
	if err != nil {
		return &ToolResult{Error: err.Error()}, nil
	}

	return &ToolResult{Output: string(resp.Result)}, nil
}
```

---

# Track 4: Hybrid Search Memory (BM25 + Local Vector Embeddings)

```
                       HYBRID MEMORY ARCHITECTURE
                       
   User Query: "How should we structure outreach to engineering leaders?"
                       │
          ┌────────────┴────────────┐
          ▼                         ▼
   FTS5 BM25 Search          Ollama nomic-embed-text
   Exact keywords:           Vector Cosine Similarity:
   "outreach", "leaders"     Finds "CTO messaging strategy"
   Rank 1: Doc 12            Rank 1: Doc 45
   Rank 2: Doc 45            Rank 2: Doc 12
          │                         │
          └────────────┬────────────┘
                       ▼
         Reciprocal Rank Fusion (RRF)
         Merged Rank 1: Doc 45 (HighScore)
         Merged Rank 2: Doc 12 (HighScore)
```

---

### Milestone 4.1: Generating Local Embeddings with Ollama

#### Objective
Generate 768-dimensional dense vector embeddings using `nomic-embed-text` hosted locally on your Ollama server (~270MB VRAM footprint).

#### Setup
In your terminal, pull the embedding model:
```bash
ollama pull nomic-embed-text
```

#### File Map
* **New File to Add**: `internal/memory/embed.go`

#### Code Blueprint (`internal/memory/embed.go`)
```go
package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Embedder struct {
	baseURL string
	model   string
	client  *http.Client
}

func NewEmbedder(baseURL, model string) *Embedder {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	if model == "" {
		model = "nomic-embed-text"
	}
	return &Embedder{
		baseURL: baseURL,
		model:   model,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

type embedRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type embedResponse struct {
	Embedding []float32 `json:"embedding"`
}

func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	reqBody, _ := json.Marshal(embedRequest{
		Model:  e.model,
		Prompt: text,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/api/embeddings", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama embed request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama embed error code: %d", resp.StatusCode)
	}

	var res embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}

	return res.Embedding, nil
}
```

---

### Milestone 4.2: Vector Similarity & Reciprocal Rank Fusion (RRF)

#### Objective
Compute cosine similarity between queries and stored memories, then blend vector ranks with FTS5 BM25 ranks using the Reciprocal Rank Fusion (RRF) algorithm.

#### Mathematical Formulation
$$\text{RRF Score}(d) = \frac{1}{60 + \text{rank}_{\text{BM25}}(d)} + \frac{1}{60 + \text{rank}_{\text{Vector}}(d)}$$

#### File Map
* **New File to Add**: `internal/memory/hybrid.go`
* **Test File to Add**: `internal/memory/hybrid_test.go`

#### Code Blueprint (`internal/memory/hybrid.go`)
```go
package memory

import (
	"math"
	"sort"
)

// CosineSimilarity calculates the cosine angle between two normalized vectors.
func CosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}

	var dot, normA, normB float64
	for i := 0; i < len(a); i++ {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return float32(dot / (math.Sqrt(normA) * math.Sqrt(normB)))
}

type RankedItem struct {
	ID    int64
	Score float64
}

// ReciprocalRankFusion merges two ranked lists of IDs into a unified scored ranking.
func ReciprocalRankFusion(bm25RankedIDs, vectorRankedIDs []int64, k float64) []RankedItem {
	if k <= 0 {
		k = 60.0 // Standard constant
	}

	scores := make(map[int64]float64)

	for rank, id := range bm25RankedIDs {
		scores[id] += 1.0 / (k + float64(rank+1))
	}

	for rank, id := range vectorRankedIDs {
		scores[id] += 1.0 / (k + float64(rank+1))
	}

	results := make([]RankedItem, 0, len(scores))
	for id, score := range scores {
		results = append(results, RankedItem{ID: id, Score: score})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	return results
}
```

---

## Suggested Execution Roadmap

1. **Start with Milestone 1.1 & 1.2 (Stealth Browser)**:
   * Create `internal/tools/browser_test.go` and run `go test -v -run TestBrowserDetection ./internal/tools`.
   * Update `internal/tools/browser.go` with `--headless=new` and watch your test screenshot change.
2. **Implement Milestone 2.2 (KV Allocator)**:
   * Add `internal/llm/kv_allocator.go` and test slot allocation/eviction with `internal/llm/kv_test.go`.
3. **Implement Milestone 4.2 (Hybrid RRF Memory)**:
   * Add `internal/memory/hybrid.go` and run unit tests for Cosine Similarity and RRF.
4. **Implement Milestone 3.1 (MCP Client)**:
   * Wire a subprocess JSON-RPC client to connect to external email/calendar tools.

Whenever you work on a specific milestone and want a hint or code review, feel free to ask!
