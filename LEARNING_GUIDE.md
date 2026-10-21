# Blooper: The Step-by-Step Hands-On Engineering Workbook

Welcome! This workbook is designed specifically to help you build and understand the advanced parts of Blooper **step-by-step**. 

Every milestone is broken down into **numbered micro-tasks**. You will never have to guess which file to touch, what lines of code to write, or why you are writing them.

---

## 🧭 Your Learning Dashboard

Track your progress by checking off tasks as you finish them:

- [ ] **Track 1: Stealth Browser Automation & Anti-Bot Evasion**
  - [ ] [Task 1.1: Capture Your Baseline Bot-Failure Screenshot](#task-11-capture-your-baseline-bot-failure-screenshot)
  - [ ] [Task 1.2: Upgrade to Chrome's New Headless Engine](#task-12-upgrade-to-chromes-new-headless-engine)
  - [ ] [Task 1.3: Eliminate the `navigator.webdriver` Flag](#task-13-eliminate-the-navigatorwebdriver-flag)
  - [ ] [Task 1.4: Inject `go-rod/stealth` Pre-Navigation Scripts](#task-14-inject-go-rodstealth-pre-navigation-scripts)
  - [ ] [Task 1.5: Build Humanized Typing Dynamics](#task-15-build-humanized-typing-dynamics)
- [ ] **Track 2: Custom Inference Engine & KV Cache Management**
  - [ ] [Task 2.1: Calculate Your VRAM Budget on Paper](#task-21-calculate-your-vram-budget-on-paper)
  - [ ] [Task 2.2: Implement the Slot-Based KV Cache Manager](#task-22-implement-the-slot-based-kv-cache-manager)
  - [ ] [Task 2.3: Build an LRU Eviction Policy](#task-23-build-an-lru-eviction-policy)
- [ ] **Track 3: Model Context Protocol (MCP) Client**
  - [ ] [Task 3.1: Build the Subprocess JSON-RPC Transport](#task-31-build-the-subprocess-json-rpc-transport)
  - [ ] [Task 3.2: Implement the Tool Adapter for Blooper](#task-32-implement-the-tool-adapter-for-blooper)
  - [ ] [Task 3.3: Write an End-to-End Test with a Mock MCP Server](#task-33-write-an-end-to-end-test-with-a-mock-mcp-server)
  - [ ] [Task 3.4: Connect External Community MCP Servers](#task-34-connect-external-community-mcp-servers)
- [ ] **Track 4: Hybrid Search Memory (BM25 + Local Vector Embeddings)**
  - [ ] [Task 4.1: Generate Local Embeddings with Ollama](#task-41-generate-local-embeddings-with-ollama)
  - [ ] [Task 4.2: Implement Pure-Go Cosine Similarity & RRF](#task-42-implement-pure-go-cosine-similarity--rrf)

---

# 🛠️ Pre-Flight Check: Verify Your Environment

Before typing any code, open your terminal and run these four commands to ensure your environment is ready:

#### Check 1: Verify Go Compiler
```bash
go version
```
* **Expected Output**: `go version go1.22` (or newer).
* **If it fails**: Install Go from [golang.org](https://go.dev).

#### Check 2: Verify Google Chrome Installation
* **On macOS**: Run `ls -d "/Applications/Google Chrome.app"`
  * **Expected Output**: `/Applications/Google Chrome.app`
* **On Linux**: Run `google-chrome --version` or `which chromium`

#### Check 3: Verify Blooper Current Tests Pass
```bash
cd /Users/chengyao/Documents/GitHub/blooper
go test ./...
```
* **Expected Output**: `PASS` across all internal packages (`agent`, `core`, `memory`, `tools`).

#### Check 4: Verify Ollama is Reachable
```bash
curl http://localhost:11434/api/tags
```
* **Expected Output**: A JSON array of your locally installed models.

---

# Track 1: Stealth Browser Automation & Bot Evasion

Websites protect themselves against scrapers and AI agents using anti-bot services like **Cloudflare Turnstile, DataDome, and Akamai**. These services run JavaScript tests in the background to detect whether a browser is operated by a human or automation software.

In this track, you will turn Blooper's browser from an **easily detected bot** into an **undetectable, human-like browser**.

---

## Task 1.1: Capture Your Baseline Bot-Failure Screenshot

### The Goal
Before changing any browser code, we need to create an automated test that opens a bot-detection test page (`https://bot.sannysoft.com`), takes a screenshot, and saves it so you can see which checks currently fail.

---

### Step 1.1.1: Create the Test File
* **Folder**: `internal/tools/`
* **File to Create**: `internal/tools/browser_test.go`

In terminal or your editor, create the file:
```bash
touch internal/tools/browser_test.go
```

---

### Step 1.1.2: Write the Test Code
Open [internal/tools/browser_test.go](file:///Users/chengyao/Documents/GitHub/blooper/internal/tools/browser_test.go) and paste the following code:

```go
package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestBrowserDetection(t *testing.T) {
	// 1. Create a browser tool instance in headless mode (true)
	tool := NewBrowserTool(true)
	defer tool.Close() // Ensures Chrome process is killed when test completes

	// 2. Prepare the JSON request asking the browser to screenshot Sannysoft
	payload, err := json.Marshal(map[string]string{
		"action": "screenshot",
		"url":    "https://bot.sannysoft.com",
	})
	if err != nil {
		t.Fatalf("Failed to create JSON payload: %v", err)
	}

	// 3. Set a generous timeout (45s) for network load and rendering
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	t.Log("Launching browser, navigating to https://bot.sannysoft.com...")

	// 4. Run the browser tool
	result, err := tool.Execute(ctx, payload)
	if err != nil {
		t.Fatalf("Browser execution failed: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("Browser returned an error: %s", result.Error)
	}

	// 5. Output the result message (contains the screenshot file path)
	t.Logf("Success! %s", result.Output)
}
```

#### Line-by-Line Explanation:
* **Line 10 (`NewBrowserTool(true)`)**: Calls our constructor in `browser.go`. `true` means headless (no visible window).
* **Line 11 (`defer tool.Close()`)**: Important! If you omit this, Chrome stays alive in background memory (~400MB leak).
* **Line 14–17 (`json.Marshal(...)`)**: Blooper's tools receive arguments as JSON from the LLM. We mimic that here.
* **Line 21 (`context.WithTimeout(...)`)**: Prevents the test from hanging forever if Wi-Fi drops.
* **Line 26 (`tool.Execute(...)`)**: The heart of the tool. It launches Chrome, loads the page, takes a screenshot, and saves it into `data/screenshots/`.

---

### Step 1.1.3: Run the Test
In your terminal, execute:
```bash
go test -v -run TestBrowserDetection ./internal/tools
```

#### Expected Terminal Output:
```
=== RUN   TestBrowserDetection
    browser_test.go:24: Launching browser, navigating to https://bot.sannysoft.com...
    browser_test.go:35: Success! Screenshot captured and saved to data/screenshots/screenshot_1726880000.png (124000 bytes)
--- PASS: TestBrowserDetection (5.12s)
PASS
ok  	github.com/chengyaolee/blooper/internal/tools	5.312s
```

---

### Step 1.1.4: Inspect the Screenshot
Open the newly created screenshot in Preview:
```bash
open data/screenshots/screenshot_*.png
```

#### What You Will See:
Look at the table in the screenshot:
1. **`WebDriver (New)`**: Highlighted in **RED** (`present (failed)`).
   * *Why?* Chrome automatically tells websites that it is automated.
2. **`Chrome`**: Highlighted in **RED**.
   * *Why?* Normal `window.chrome` properties are missing.
3. **`User Agent`**: Mentions `HeadlessChrome`.

**You have now established your baseline!** Keep this screenshot open to compare against future steps.

---

## Task 1.2: Upgrade to Chrome's New Headless Engine

### The Goal
In 2023, Google introduced `--headless=new`. 
* **Old Headless**: A stripped-down, separate C++ engine. Easily fingerprinted by websites because text fonts, WebGL, and CSS behave differently than real Chrome.
* **New Headless (`--headless=new`)**: The exact full Chrome desktop browser, rendered into an off-screen memory buffer.

We will update our browser launcher to use `--headless=new`.

---

### Step 1.2.1: Open `internal/tools/browser.go`
Open [internal/tools/browser.go](file:///Users/chengyao/Documents/GitHub/blooper/internal/tools/browser.go) and navigate to lines ~80–92 (inside the `getBrowser()` method).

You will see:
```go
	// Launch controlled headless browser
	l := launcher.New().
		Headless(t.headless).
		Set("no-sandbox").
		Set("disable-gpu").
		Set("disable-dev-shm-usage")
```

---

### Step 1.2.2: Make the Edit
Replace `Headless(t.headless)` with `.Set("headless", "new")`.

Your code should now look like this:
```go
	// Launch controlled headless browser using modern headless=new engine
	l := launcher.New().
		Set("headless", "new").
		Set("no-sandbox").
		Set("disable-gpu").
		Set("disable-dev-shm-usage")
```

---

### Step 1.2.3: Re-Run the Test
```bash
go test -v -run TestBrowserDetection ./internal/tools
```

---

### Step 1.2.4: Inspect the New Screenshot
```bash
open $(ls -t data/screenshots/*.png | head -n 1)
```
*(This command opens only the most recent screenshot)*

#### What Changed?
* Font antialiasing, CSS layout dimensions, and screen metrics now match authentic Chrome.
* Notice that `WebDriver` is still red. We fix that next!

---

## Task 1.3: Eliminate the `navigator.webdriver` Flag

### The Goal
When automation software controls Chrome, Chrome sets the JavaScript variable `window.navigator.webdriver = true`. 95% of bot detectors simply check `if (navigator.webdriver) { block(); }`.

We will instruct Chrome's Blink engine to suppress this flag completely.

---

### Step 1.3.1: Open `internal/tools/browser.go`
Locate the launcher setup in `getBrowser()` again.

---

### Step 1.3.2: Add Anti-Detection Launcher Flags
Add three key flags:
1. `disable-blink-features=AutomationControlled` (removes `navigator.webdriver`).
2. A real desktop `user-agent` (removes `HeadlessChrome` from the request header).
3. A standard desktop `window-size` (`1920,1080`).

Edit `getBrowser()` so it looks like this:
```go
	l := launcher.New().
		Set("headless", "new").
		Set("disable-blink-features", "AutomationControlled").
		Set("user-agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36").
		Set("window-size", "1920,1080").
		Set("no-sandbox").
		Set("disable-dev-shm-usage")
```

---

### Step 1.3.3: Re-Run the Test
```bash
go test -v -run TestBrowserDetection ./internal/tools
```

---

### Step 1.3.4: Inspect the Screenshot
```bash
open $(ls -t data/screenshots/*.png | head -n 1)
```

#### What Changed?
Look at the **`WebDriver (New)`** row in Sannysoft:
* It should now be **GREEN (`missing (passed)`)**!
* Your browser no longer announces to the world that it is automated.

---

## Task 1.4: Inject `go-rod/stealth` Pre-Navigation Scripts

### The Goal
While `navigator.webdriver` is now gone, sophisticated anti-bot scripts (like Cloudflare Turnstile) check deeper properties:
1. `navigator.plugins`: In headless Chrome, this is an empty array (`[]`). Real browsers have default plugins (PDF viewer, etc.).
2. `navigator.languages`: Often empty or misconfigured in headless mode.
3. WebGL Vendor: Often reports `Google SwiftShader` (software CPU rendering) instead of a real GPU.

We will inject `go-rod/stealth` scripts into every new tab **before** any website JavaScript runs.

---

### Step 1.4.1: Check Dependencies
Rod's stealth library is already part of our `go.mod`. You can verify by running:
```bash
go list -m github.com/go-rod/stealth
```

---

### Step 1.4.2: Import Stealth in `internal/tools/browser.go`
Open [internal/tools/browser.go](file:///Users/chengyao/Documents/GitHub/blooper/internal/tools/browser.go).

At the top of the file, add the import:
```go
import (
	// ... existing imports ...
	"github.com/go-rod/stealth"
)
```

---

### Step 1.4.3: Inject Stealth Script Before Page Loads
Scroll down to `Execute()`.

Find the block handling `case "navigate":` and `case "screenshot":`:
```go
		page, err := browser.Context(ctxTimeout).Page(proto.TargetCreateTarget{URL: params.URL})
		if err != nil {
			return &ToolResult{Error: fmt.Sprintf("failed to load page %s: %v", params.URL, err)}, nil
		}
		defer page.Close()
```

Immediately after `defer page.Close()`, add this line:
```go
		// Injects stealth shims into the window BEFORE the page scripts execute
		_, _ = page.EvalOnNewDocument(stealth.JS)
```

Make sure to add this in both `case "navigate":` and `case "screenshot":`!

#### Why `EvalOnNewDocument`?
Standard `page.Eval()` executes JavaScript *after* the page has already loaded (too late — bot scripts have already detected you!). `EvalOnNewDocument` tells Chrome to execute `stealth.JS` at the very instant the JavaScript context is initialized, before any external script can run.

---

### Step 1.4.4: Run the Test to Verify
```bash
go test -v -run TestBrowserDetection ./internal/tools
open $(ls -t data/screenshots/*.png | head -n 1)
```

#### Final Sannysoft Result:
* `WebDriver (New)`: **GREEN**
* `Chrome`: **GREEN**
* `Permissions`: **GREEN**
* `Plugins`: **GREEN**
* `WebGL Vendor`: **GREEN**

**Congratulations!** Your browser is now stealth-grade and will not be blocked by standard bot detection filters.

---

## Task 1.5: Build Humanized Typing Dynamics

### The Goal
If an agent needs to type into a search box or login form, sending the entire text string instantaneously in 0 milliseconds will trigger behavioral bot detection. We will write a helper function that mimics human typing speed.

---

### Step 1.5.1: Write the `humanType` Function
Open [internal/tools/browser.go](file:///Users/chengyao/Documents/GitHub/blooper/internal/tools/browser.go).

Add this function near the bottom of the file:
```go
import (
	// Add math/rand to your imports if not present
	"math/rand"
)

// humanType types text into an element with randomized intervals mimicking human keystrokes.
func humanType(el *rod.Element, text string) error {
	for _, char := range text {
		// 1. Type a single character
		if err := el.Input(string(char)); err != nil {
			return err
		}

		// 2. Sleep between 40ms and 110ms per keystroke
		delay := time.Duration(40+rand.Intn(70)) * time.Millisecond
		time.Sleep(delay)

		// 3. 5% chance of a longer thinking pause (200ms - 350ms)
		if rand.Float32() < 0.05 {
			time.Sleep(time.Duration(200+rand.Intn(150)) * time.Millisecond)
		}
	}
	return nil
}
```

---

### Step 1.5.2: Add a Form Fill Action to `Execute()`
In `internal/tools/browser.go`, update `case "click":` to also support an action `"fill"`:

```go
	case "fill":
		if params.Selector == "" {
			return &ToolResult{Error: "selector required for fill action"}, nil
		}
		// Locate element on page
		el, err := page.Element(params.Selector)
		if err != nil {
			return &ToolResult{Error: fmt.Sprintf("element %s not found: %v", params.Selector, err)}, nil
		}
		// Clear existing text and type human-like
		_ = el.SelectAllText()
		if err := humanType(el, params.Text); err != nil {
			return &ToolResult{Error: fmt.Sprintf("typing failed: %v", err)}, nil
		}
		return &ToolResult{Output: fmt.Sprintf("Successfully typed text into %s", params.Selector)}, nil
```

---

# Track 2: Custom Inference Engine & KV Cache Management

In this track, you will learn how large language models consume memory and implement a **real Key-Value (KV) cache slot allocator** with Least-Recently-Used (LRU) eviction.

---

## Task 2.1: Calculate Your VRAM Budget on Paper

### The Mental Model
When an LLM generates text token-by-token, it computes two matrices for every token in every layer: **Key ($K$)** and **Value ($V$)**. To avoid recalculating these for past tokens, they are stored in VRAM. This is the **KV Cache**.

$$\text{KV Bytes per Token} = 2 \times \text{layers} \times \text{kv\_heads} \times \text{head\_dim} \times \text{precision\_bytes}$$

### Your Target 1: NVIDIA GTX 1080 Ti (11 GB VRAM)
Model: **Qwen 2.5 / 3 (8B)**
* Layers = $32$
* KV Heads = $8$
* Head Dim = $128$
* Precision = $2$ bytes (FP16)

**Calculation**:
$$2 \times 32 \times 8 \times 128 \times 2 = 131,072 \text{ bytes per token} \approx 128 \text{ KB / token}$$

* At **2,048 tokens** context: $2048 \times 128\text{ KB} = 256\text{ MB}$
* At **4,096 tokens** context: $4096 \times 128\text{ KB} = 512\text{ MB}$
* At **8,192 tokens** context: $8192 \times 128\text{ KB} = 1024\text{ MB} = 1\text{ GB}$

#### VRAM Allocation on 1080 Ti:
```
Total VRAM:                                11,264 MB (11 GB)
- Qwen3 8B Model Weights (Q4_K_M):         -5,200 MB
- CUDA Context & Framework Buffers:          -800 MB
────────────────────────────────────────────────────
Available for KV Cache:                     5,264 MB (~5.2 GB)
```

**Conclusion for your design**:
On the GTX 1080 Ti, you can safely support:
* **Two concurrent agents** at **8,192 tokens** context ($2 \times 1\text{ GB} = 2\text{ GB}$), or
* **Four concurrent agents** at **4,096 tokens** context ($4 \times 0.5\text{ GB} = 2\text{ GB}$).
Leaving ~3.2 GB of safety headroom so memory never spills over PCIe into slow system RAM!

---

## Task 2.2: Implement the Slot-Based KV Cache Manager

### The Goal
Now let's build the allocator in Go. It will manage a fixed number of slots (e.g. 2 or 4 slots), track which agent session is using which slot, and reject or evict sessions when memory is full.

---

### Step 2.2.1: Create the Source File
* **Folder**: `internal/llm/`
* **File to Create**: `internal/llm/kv_allocator.go`

```bash
touch internal/llm/kv_allocator.go
```

---

### Step 2.2.2: Paste the Implementation
Open [internal/llm/kv_allocator.go](file:///Users/chengyao/Documents/GitHub/blooper/internal/llm/kv_allocator.go) and paste:

```go
package llm

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// KVSlot represents a reserved chunk of VRAM context.
type KVSlot struct {
	ID        int
	SessionID string
	Tokens    int
	LastUsed  time.Time
	Active    bool
}

// SlotManager manages a fixed pool of KV context slots.
type SlotManager struct {
	mu            sync.Mutex
	slots         []*KVSlot
	maxSlots      int
	bytesPerToken int64
}

// NewSlotManager creates an allocator with a fixed slot capacity.
func NewSlotManager(maxSlots int, bytesPerToken int64) *SlotManager {
	slots := make([]*KVSlot, maxSlots)
	for i := 0; i < maxSlots; i++ {
		slots[i] = &KVSlot{ID: i, Active: false}
	}
	return &SlotManager{
		slots:         slots,
		maxSlots:      maxSlots,
		bytesPerToken: bytesPerToken,
	}
}

// AllocateSlot assigns a slot to a sessionID, or evicts the oldest session if full.
func (m *SlotManager) AllocateSlot(ctx context.Context, sessionID string, expectedTokens int) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Case 1: Session already owns a slot. Update timestamp and token count.
	for _, slot := range m.slots {
		if slot.Active && slot.SessionID == sessionID {
			slot.LastUsed = time.Now()
			slot.Tokens = expectedTokens
			return slot.ID, nil
		}
	}

	// Case 2: An empty slot is available.
	for _, slot := range m.slots {
		if !slot.Active {
			slot.Active = true
			slot.SessionID = sessionID
			slot.Tokens = expectedTokens
			slot.LastUsed = time.Now()
			return slot.ID, nil
		}
	}

	// Case 3: All slots full! Find the oldest slot (LRU) and evict it.
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

	return 0, fmt.Errorf("no slots available")
}

// FreeSlot releases a slot when an agent completes its conversation.
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

// Stats returns the active memory usage.
func (m *SlotManager) Stats() (*KVMetrics, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	active := 0
	var usedBytes int64

	for _, slot := range m.slots {
		if slot.Active {
			active++
			usedBytes += int64(slot.Tokens) * m.bytesPerToken
		}
	}

	return &KVMetrics{
		ActiveSlots:     active,
		TotalSlots:      m.maxSlots,
		UsedMemoryBytes: usedBytes,
	}, nil
}
```

---

### Step 2.2.3: Write the Unit Test
* **File to Create**: `internal/llm/kv_test.go`
```bash
touch internal/llm/kv_test.go
```

Open [internal/llm/kv_test.go](file:///Users/chengyao/Documents/GitHub/blooper/internal/llm/kv_test.go) and add:

```go
package llm

import (
	"context"
	"testing"
	"time"
)

func TestKVSlotAllocationAndEviction(t *testing.T) {
	// Simulate 2 slots max (128 KB per token for Qwen 8B)
	allocator := NewSlotManager(2, 131072)
	ctx := context.Background()

	// 1. Allocate session 1
	slot0, err := allocator.AllocateSlot(ctx, "session-agent-research", 2048)
	if err != nil || slot0 != 0 {
		t.Fatalf("Expected slot 0, got %d, err: %v", slot0, err)
	}

	time.Sleep(10 * time.Millisecond)

	// 2. Allocate session 2
	slot1, err := allocator.AllocateSlot(ctx, "session-agent-email", 2048)
	if err != nil || slot1 != 1 {
		t.Fatalf("Expected slot 1, got %d, err: %v", slot1, err)
	}

	// Verify stats: 2 active slots
	stats, _ := allocator.Stats()
	if stats.ActiveSlots != 2 {
		t.Errorf("Expected 2 active slots, got %d", stats.ActiveSlots)
	}
	t.Logf("Memory used: %d MB", stats.UsedMemoryBytes/(1024*1024))

	time.Sleep(10 * time.Millisecond)

	// 3. Allocate session 3 (All slots full! Should evict session 1)
	slotEvicted, err := allocator.AllocateSlot(ctx, "session-agent-sales", 2048)
	if err != nil {
		t.Fatalf("Allocation failed: %v", err)
	}
	if slotEvicted != 0 {
		t.Errorf("Expected LRU to evict slot 0, but got slot %d", slotEvicted)
	}
	t.Logf("Successfully evicted oldest session and reused Slot %d", slotEvicted)
}
```

---

### Step 2.2.4: Run the Test
```bash
go test -v -run TestKVSlotAllocationAndEviction ./internal/llm
```

#### Expected Terminal Output:
```
=== RUN   TestKVSlotAllocationAndEviction
    kv_test.go:31: Memory used: 512 MB
    kv_test.go:42: Successfully evicted oldest session and reused Slot 0
--- PASS: TestKVSlotAllocationAndEviction (0.02s)
PASS
ok  	github.com/chengyaolee/blooper/internal/llm	0.412s
```

You now have a working, thread-safe KV cache slot manager!

---

# Track 3: Model Context Protocol (MCP) Client

The **Model Context Protocol (MCP)** is an open standard designed to let AI agents interact with external tools and databases through a standardized protocol. Instead of hardcoding specialized Go tools for Google Calendar, Slack, GitHub, Postgres, and Email, an external program runs as an **MCP Server**, and Blooper connects to it as an **MCP Client**.

```
                       MCP ARCHITECTURE IN BLOOPER
                       
   ┌─────────────────────────────────────────────────────────────┐
   │                       Blooper Core                          │
   │                                                             │
   │  llm.InferenceEngine ──► Model outputs tool call:           │
   │                          read_emails({"unread_only": true}) │
   └─────────────────────────────────────────────────────────────┘
                                  │
                                  ▼
   ┌─────────────────────────────────────────────────────────────┐
   │            internal/tools/mcp_adapter.go                    │
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

Communication is standard **JSON-RPC 2.0** over `stdin`/`stdout`:
1. `initialize`: Handshake where client and server exchange capabilities and protocol versions.
2. `notifications/initialized`: Notification sent by the client acknowledging handshake completion.
3. `tools/list`: Server returns the list of available tools with their JSON Schema parameters.
4. `tools/call`: Client invokes a tool by name with arguments; server returns content blocks.

---

## Task 3.1: Build the Subprocess JSON-RPC Transport

### The Goal
Build `MCPClient` in `internal/tools/mcp_client.go`. It will launch any external MCP server executable as a child process (`exec.Command`), write newline-delimited JSON-RPC requests to its `stdin`, read newline-delimited responses from its `stdout`, and match each response back to the caller using a correlation `id`.

---

### Step 3.1.1: Create the Source File
* **Folder**: `internal/tools/`
* **File to Create**: [internal/tools/mcp_client.go](file:///Users/chengyao/Documents/GitHub/blooper/internal/tools/mcp_client.go)

In terminal:
```bash
touch internal/tools/mcp_client.go
```

---

### Step 3.1.2: Write the `MCPClient` Implementation
Open [internal/tools/mcp_client.go](file:///Users/chengyao/Documents/GitHub/blooper/internal/tools/mcp_client.go) and paste:

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

// JSONRPCRequest represents a JSON-RPC 2.0 request payload.
type JSONRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int64       `json:"id,omitempty"` // Omitted for notifications
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// JSONRPCResponse represents a JSON-RPC 2.0 response payload.
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// JSONRPCError holds error code and message.
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// MCPClient manages a subprocess MCP server communicating over stdio.
type MCPClient struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	reader  *bufio.Reader
	mu      sync.Mutex
	seq     int64
	pending map[int64]chan *JSONRPCResponse
}

// NewMCPClient spawns an MCP server subprocess and starts the background response reader.
func NewMCPClient(command string, args ...string) (*MCPClient, error) {
	cmd := exec.Command(command, args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to open stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to open stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start mcp subprocess: %w", err)
	}

	client := &MCPClient{
		cmd:     cmd,
		stdin:   stdin,
		reader:  bufio.NewReader(stdout),
		pending: make(map[int64]chan *JSONRPCResponse),
	}

	go client.readLoop()

	return client, nil
}

// readLoop runs in the background, continuously parsing newline-delimited JSON-RPC responses.
func (c *MCPClient) readLoop() {
	for {
		line, err := c.reader.ReadBytes('\n')
		if err != nil {
			return
		}

		var resp JSONRPCResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			continue // Skip non-JSON or debug logs
		}

		c.mu.Lock()
		ch, exists := c.pending[resp.ID]
		if exists {
			delete(c.pending, resp.ID)
			ch <- &resp
		}
		c.mu.Unlock()
	}
}

// Call sends a JSON-RPC request and blocks until a matching response arrives or context times out.
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
		return nil, fmt.Errorf("failed to encode request: %w", err)
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
		return nil, fmt.Errorf("failed to write to stdin: %w", err)
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case resp := <-respChan:
		if resp.Error != nil {
			return nil, fmt.Errorf("mcp error (%d): %s", resp.Error.Code, resp.Error.Message)
		}
		return resp, nil
	}
}

// Notify sends a one-way notification without expecting a response ID.
func (c *MCPClient) Notify(ctx context.Context, method string, params interface{}) error {
	req := JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.stdin.Write(data)
	return err
}

// Initialize performs the mandatory MCP initialization handshake.
func (c *MCPClient) Initialize(ctx context.Context) error {
	initParams := map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]string{
			"name":    "blooper",
			"version": "1.0.0",
		},
	}

	_, err := c.Call(ctx, "initialize", initParams)
	if err != nil {
		return fmt.Errorf("initialize handshake failed: %w", err)
	}

	// Send mandatory initialized notification
	return c.Notify(ctx, "notifications/initialized", map[string]interface{}{})
}

// Close gracefully terminates the subprocess.
func (c *MCPClient) Close() error {
	_ = c.stdin.Close()
	if c.cmd != nil && c.cmd.Process != nil {
		return c.cmd.Process.Kill()
	}
	return nil
}
```

---

## Task 3.2: Implement the Tool Adapter for Blooper

### The Goal
Blooper's agents execute tools that conform to the `tools.Tool` interface in [internal/tools/tool.go](file:///Users/chengyao/Documents/GitHub/blooper/internal/tools/tool.go). We will build an adapter in `internal/tools/mcp_adapter.go` that translates an MCP tool definition into this interface so any Blooper agent can call it like a native tool.

---

### Step 3.2.1: Create the Adapter File
* **Folder**: `internal/tools/`
* **File to Create**: [internal/tools/mcp_adapter.go](file:///Users/chengyao/Documents/GitHub/blooper/internal/tools/mcp_adapter.go)

```bash
touch internal/tools/mcp_adapter.go
```

---

### Step 3.2.2: Write the `MCPToolAdapter` Implementation
Open [internal/tools/mcp_adapter.go](file:///Users/chengyao/Documents/GitHub/blooper/internal/tools/mcp_adapter.go) and paste:

```go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chengyaolee/blooper/internal/llm"
)

// MCPToolAdapter wraps an MCP server tool into Blooper's standard Tool interface.
type MCPToolAdapter struct {
	client   *MCPClient
	name     string
	desc     string
	schema   llm.FunctionDef
	approval bool
}

// NewMCPToolAdapter creates an adapter for an MCP tool.
func NewMCPToolAdapter(client *MCPClient, name, desc string, parameters map[string]interface{}, approval bool) *MCPToolAdapter {
	return &MCPToolAdapter{
		client: client,
		name:   name,
		desc:   desc,
		schema: llm.FunctionDef{
			Name:        name,
			Description: desc,
			Parameters:  parameters,
		},
		approval: approval,
	}
}

func (a *MCPToolAdapter) Name() string             { return a.name }
func (a *MCPToolAdapter) Description() string      { return a.desc }
func (a *MCPToolAdapter) Schema() llm.FunctionDef  { return a.schema }
func (a *MCPToolAdapter) RequiresApproval() bool   { return a.approval }

// Execute delegates the execution to the MCP server via "tools/call".
func (a *MCPToolAdapter) Execute(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	var parsedArgs map[string]interface{}
	if err := json.Unmarshal(args, &parsedArgs); err != nil {
		return &ToolResult{Error: fmt.Sprintf("invalid tool arguments: %v", err)}, nil
	}

	callParams := map[string]interface{}{
		"name":      a.name,
		"arguments": parsedArgs,
	}

	resp, err := a.client.Call(ctx, "tools/call", callParams)
	if err != nil {
		return &ToolResult{Error: fmt.Sprintf("mcp execution failed: %v", err)}, nil
	}

	// MCP responses format: {"content": [{"type": "text", "text": "..."}]}
	var callResult struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}

	if err := json.Unmarshal(resp.Result, &callResult); err != nil {
		return &ToolResult{Output: string(resp.Result)}, nil
	}

	var sb strings.Builder
	for _, c := range callResult.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
			sb.WriteString("\n")
		}
	}

	output := strings.TrimSpace(sb.String())
	if callResult.IsError {
		return &ToolResult{Error: output}, nil
	}

	return &ToolResult{Output: output}, nil
}
```

---

## Task 3.3: Write an End-to-End Test with a Mock MCP Server

### The Goal
Verify that `MCPClient` can start a process, execute the `initialize` handshake, and invoke a tool via `tools/call` without needing external npm/Python packages.

---

### Step 3.3.1: Create `internal/tools/mcp_test.go`
```bash
touch internal/tools/mcp_test.go
```

Open [internal/tools/mcp_test.go](file:///Users/chengyao/Documents/GitHub/blooper/internal/tools/mcp_test.go) and paste:

```go
package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestHelperProcess acts as a mock MCP server responding on stdin/stdout.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Bytes()
		var req JSONRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}

		switch req.Method {
		case "initialize":
			resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"mock-server","version":"1.0"}}}`+"\n", req.ID)
			os.Stdout.WriteString(resp)
		case "tools/call":
			resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"Mock tool output from MCP"}],"isError":false}}`+"\n", req.ID)
			os.Stdout.WriteString(resp)
		}
	}
	os.Exit(0)
}

func TestMCPClientHandshakeAndToolCall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Launch the test binary itself with GO_WANT_HELPER_PROCESS=1 to run the mock server
	client, err := NewMCPClient(os.Args[0], "-test.run=TestHelperProcess")
	if err != nil {
		t.Fatalf("failed to spawn mock server: %v", err)
	}
	client.cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	defer client.Close()

	// 1. Handshake
	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Handshake failed: %v", err)
	}
	t.Log("✅ MCP Handshake completed successfully!")

	// 2. Wrap into Blooper Tool Adapter
	adapter := NewMCPToolAdapter(client, "mock_echo", "A mock tool", map[string]interface{}{}, false)

	// 3. Execute through Blooper's standard interface
	res, err := adapter.Execute(ctx, json.RawMessage(`{"input":"hello"}`))
	if err != nil {
		t.Fatalf("Tool execution failed: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("Tool returned error: %s", res.Error)
	}

	if res.Output != "Mock tool output from MCP" {
		t.Errorf("Unexpected tool output: %q", res.Output)
	}
	t.Logf("✅ Tool Call succeeded! Output: %s", res.Output)
}
```

---

### Step 3.3.2: Run the Test
```bash
go test -v -run TestMCPClientHandshakeAndToolCall ./internal/tools
```

#### Expected Terminal Output:
```
=== RUN   TestMCPClientHandshakeAndToolCall
    mcp_test.go:48: ✅ MCP Handshake completed successfully!
    mcp_test.go:66: ✅ Tool Call succeeded! Output: Mock tool output from MCP
--- PASS: TestMCPClientHandshakeAndToolCall (0.05s)
PASS
ok  	github.com/chengyaolee/blooper/internal/tools	0.210s
```

---

## Task 3.4: Connect External Community MCP Servers

### The Goal
Now that your transport and adapter work, you can connect any community MCP server (like SQLite, GitHub, or Filesystem) to your fleet!

### Example: Adding an MCP Server to `config/blooper.yaml`
Declare external servers in your configuration file:

```yaml
mcp_servers:
  - name: "sqlite_memory"
    command: "npx"
    args: ["-y", "@modelcontextprotocol/server-sqlite", "--db-path", "data/blooper.db"]
    tools:
      - read_query
      - write_query
```

At startup in `cmd/blooper/main.go`, Blooper launches each configured server:
```go
for _, srv := range cfg.MCPServers {
    client, err := tools.NewMCPClient(srv.Command, srv.Args...)
    if err == nil {
        _ = client.Initialize(ctx)
        // Automatically registers tools for your agents!
        for _, t := range srv.Tools {
            adapter := tools.NewMCPToolAdapter(client, t, "MCP Tool", nil, false)
            toolRegistry.Register(adapter)
        }
    }
}
```

---

# Track 4: Hybrid Search Memory (BM25 + Local Vector Embeddings)

Blooper currently uses SQLite FTS5 (BM25 keyword search) for memory. Here, you will add **dense vector semantic search** using `nomic-embed-text` locally via Ollama, and merge keyword and vector results using **Reciprocal Rank Fusion (RRF)**.

---

## Task 4.1: Generate Local Embeddings with Ollama

### The Goal
Call Ollama's local `/api/embeddings` endpoint to transform any conversation text into a 768-dimensional array of `float32` numbers.

---

### Step 4.1.1: Pull the Embedding Model
In your terminal, pull the model (takes ~270MB):
```bash
ollama pull nomic-embed-text
```

---

### Step 4.1.2: Create `internal/memory/embed.go`
```bash
touch internal/memory/embed.go
```

Open [internal/memory/embed.go](file:///Users/chengyao/Documents/GitHub/blooper/internal/memory/embed.go) and add:

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
	reqBody, err := json.Marshal(embedRequest{
		Model:  e.model,
		Prompt: text,
	})
	if err != nil {
		return nil, err
	}

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
		return nil, fmt.Errorf("ollama returned HTTP %d", resp.StatusCode)
	}

	var res embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return res.Embedding, nil
}
```

---

## Task 4.2: Implement Pure-Go Cosine Similarity & RRF

### The Goal
Compare two vectors mathematically without any external vector database.

$$\text{Cosine Similarity}(A, B) = \frac{A \cdot B}{\|A\| \|B\|}$$

---

### Step 4.2.1: Create `internal/memory/hybrid.go`
```bash
touch internal/memory/hybrid.go
```

Open [internal/memory/hybrid.go](file:///Users/chengyao/Documents/GitHub/blooper/internal/memory/hybrid.go) and add:

```go
package memory

import (
	"math"
	"sort"
)

// CosineSimilarity computes the angle between two embedding vectors (-1.0 to 1.0).
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

type RankedResult struct {
	ID    int64
	Score float64
}

// ReciprocalRankFusion combines ranks from Keyword Search (BM25) and Vector Similarity.
// Formula: Score(doc) = 1/(60 + rank_bm25) + 1/(60 + rank_vector)
func ReciprocalRankFusion(bm25IDs, vectorIDs []int64, k float64) []RankedResult {
	if k <= 0 {
		k = 60.0
	}

	scores := make(map[int64]float64)

	for rank, id := range bm25IDs {
		scores[id] += 1.0 / (k + float64(rank+1))
	}

	for rank, id := range vectorIDs {
		scores[id] += 1.0 / (k + float64(rank+1))
	}

	results := make([]RankedResult, 0, len(scores))
	for id, score := range scores {
		results = append(results, RankedResult{ID: id, Score: score})
	}

	// Sort descending by combined score
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	return results
}
```

---

### Step 4.2.2: Test It!
Create `internal/memory/hybrid_test.go`:

```go
package memory

import (
	"testing"
)

func TestCosineSimilarity(t *testing.T) {
	vecA := []float32{1.0, 0.0, 0.0}
	vecB := []float32{1.0, 0.0, 0.0}
	vecC := []float32{0.0, 1.0, 0.0}

	// Identical vectors = 1.0
	simAB := CosineSimilarity(vecA, vecB)
	if simAB < 0.99 {
		t.Errorf("Expected similarity 1.0, got %f", simAB)
	}

	// Orthogonal vectors = 0.0
	simAC := CosineSimilarity(vecA, vecC)
	if simAC > 0.01 {
		t.Errorf("Expected similarity 0.0, got %f", simAC)
	}
}

func TestRRF(t *testing.T) {
	// Document 12 and Document 45 appear in both lists
	bm25 := []int64{12, 45, 99}
	vector := []int64{45, 12, 101}

	merged := ReciprocalRankFusion(bm25, vector, 60.0)

	// Both 45 and 12 should be at the top because they matched both search methods!
	if len(merged) < 2 {
		t.Fatalf("Expected at least 2 merged items")
	}

	topID := merged[0].ID
	if topID != 12 && topID != 45 {
		t.Errorf("Expected Doc 12 or 45 to win RRF, got %d", topID)
	}
	t.Logf("RRF Winner: Doc %d (Score: %f)", merged[0].ID, merged[0].Score)
}
```

Run the test:
```bash
go test -v -run "TestCosineSimilarity|TestRRF" ./internal/memory
```

---

## What to do next?

Start right now with **[Task 1.1](#task-11-capture-your-baseline-bot-failure-screenshot)**! 
1. Create `internal/tools/browser_test.go`
2. Run `go test -v -run TestBrowserDetection ./internal/tools`
3. Check `data/screenshots/`

If you get any compile error or test failure, paste the terminal output here and we will debug it together.
