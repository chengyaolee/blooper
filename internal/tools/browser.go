package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/chengyaolee/blooper/internal/llm"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// isSafeURL checks if a target URL is safe to visit, preventing SSRF to private/internal networks.
func isSafeURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("unsupported URL scheme %q (only http/https allowed)", scheme)
	}

	hostname := u.Hostname()
	if hostname == "" {
		return fmt.Errorf("missing hostname in URL")
	}

	// Block obvious localhost / link-local aliases
	lowerHost := strings.ToLower(hostname)
	if lowerHost == "localhost" || lowerHost == "127.0.0.1" || lowerHost == "::1" || lowerHost == "169.254.169.254" {
		return fmt.Errorf("access to internal/localhost network address %q is forbidden", hostname)
	}

	// Resolve IP addresses to prevent DNS rebinding or private subnet access
	ips, err := net.LookupIP(hostname)
	if err == nil {
		for _, ip := range ips {
			if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
				return fmt.Errorf("access to private network address (%s -> %s) is forbidden", hostname, ip.String())
			}
		}
	}

	return nil
}

// BrowserTool provides controlled web browsing through Scrapling's MCP server (scrapling-mcp),
// which keeps one stealth Chrome session open across calls.
type BrowserTool struct {
	mu            sync.Mutex
	client        *mcp.ClientSession
	sessionID     string
	screenshotDir string
	headless      bool
	xvfb          bool      // run headful Chrome on a virtual display (headless servers)
	xvfbCmd       *exec.Cmd // our Xvfb process while a session is open
	display       string    // its DISPLAY, e.g. ":99"
}

// NewBrowserTool creates a managed BrowserTool. headless is honored on desktops. On a Linux box with no
// display (a server) Cloudflare Turnstile fails for headless Chrome, so if Xvfb is installed Chrome runs
// headful on a virtual display; without it we fall back to plain headless (works, but Turnstile pages won't solve).
func NewBrowserTool(headless bool) *BrowserTool {
	dir := "data/screenshots"
	_ = os.MkdirAll(dir, 0755)
	t := &BrowserTool{screenshotDir: dir, headless: headless}
	if runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		if _, err := exec.LookPath("Xvfb"); err == nil {
			t.xvfb, t.headless = true, false
		} else {
			t.headless = true
		}
	}
	return t
}

// hasSystemChrome reports whether Google Chrome is installed. If so Scrapling drives it (real
// fingerprint); otherwise it uses its bundled Chromium (`scrapling install`), which is what a bare server has.
func hasSystemChrome() bool {
	switch runtime.GOOS {
	case "darwin":
		_, err := os.Stat("/Applications/Google Chrome.app")
		return err == nil
	case "windows":
		_, err := os.Stat(filepath.Join(os.Getenv("ProgramFiles"), "Google", "Chrome", "Application", "chrome.exe"))
		return err == nil
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable"} {
		if _, err := exec.LookPath(name); err == nil {
			return true
		}
	}
	return false
}

// scraplingBin returns the scrapling-mcp executable: $SCRAPLING_MCP_BIN, else PATH.
func scraplingBin() (string, error) {
	bin := os.Getenv("SCRAPLING_MCP_BIN")
	if bin == "" {
		bin = "scrapling-mcp"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return "", fmt.Errorf(`%q not found; install with: pip install "scrapling[ai]" && scrapling install (Python 3.10+) and put it on PATH or set SCRAPLING_MCP_BIN`, bin)
	}
	return path, nil
}

// getSession lazily starts scrapling-mcp and opens one reusable browser session. Caller holds t.mu.
func (t *BrowserTool) getSession(ctx context.Context) error {
	if t.sessionID != "" {
		return nil
	}
	bin, err := scraplingBin()
	if err != nil {
		return err
	}

	cmd := exec.Command(bin)
	if t.xvfb {
		if err := t.startXvfb(); err != nil {
			return err
		}
		cmd.Env = append(os.Environ(), "DISPLAY="+t.display)
	}

	// WithoutCancel: the subprocess must outlive this call's per-action timeout.
	client := mcp.NewClient(&mcp.Implementation{Name: "blooper", Version: "0"}, nil)
	session, err := client.Connect(context.WithoutCancel(ctx), &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.stopXvfb()
		return fmt.Errorf("failed to start scrapling-mcp: %w", err)
	}
	t.client = session

	res, err := t.call(ctx, "browser_open", map[string]any{"real_chrome": hasSystemChrome(), "headless": t.headless})
	if err != nil {
		t.shutdown(ctx)
		return err
	}
	var opened struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(textOf(res)), &opened); err != nil || opened.SessionID == "" {
		t.shutdown(ctx)
		return fmt.Errorf("unexpected browser_open reply: %q", textOf(res))
	}
	t.sessionID = opened.SessionID
	return nil
}

// startXvfb launches a virtual display on a free display number (-displayfd) and returns once it is ready.
func (t *BrowserTool) startXvfb() error {
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer r.Close()
	cmd := exec.Command("Xvfb", "-displayfd", "3", "-screen", "0", "1920x1080x24", "-nolisten", "tcp")
	cmd.ExtraFiles = []*os.File{w} // becomes fd 3 in the child
	if err := cmd.Start(); err != nil {
		w.Close()
		return fmt.Errorf("failed to start Xvfb: %w", err)
	}
	w.Close() // keep only the child's copy so a dead Xvfb gives EOF instead of a hang
	num, err := bufio.NewReader(r).ReadString('\n')
	if err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return fmt.Errorf("Xvfb did not report a display: %w", err)
	}
	t.xvfbCmd, t.display = cmd, ":"+strings.TrimSpace(num)
	return nil
}

// stopXvfb kills our Xvfb, if any.
func (t *BrowserTool) stopXvfb() {
	if t.xvfbCmd != nil {
		_ = t.xvfbCmd.Process.Kill()
		_, _ = t.xvfbCmd.Process.Wait()
		t.xvfbCmd, t.display = nil, ""
	}
}

// call invokes an MCP tool and converts a tool-level error into a Go error. If the scrapling-mcp
// process or Chrome has died, it drops the dead session so the next Execute starts a fresh one
// (a long-running server must not stay broken until restart). Caller holds t.mu.
func (t *BrowserTool) call(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, error) {
	if t.client == nil {
		return nil, fmt.Errorf("%s: browser session is not running", name)
	}
	res, err := t.client.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		if ctx.Err() == nil { // a plain timeout leaves the session usable; anything else means the subprocess is gone
			t.reset()
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if res.IsError {
		msg := textOf(res)
		if strings.Contains(msg, "has been closed") || strings.Contains(msg, "Target closed") || strings.Contains(msg, "browser has disconnected") {
			t.reset() // Chrome crashed: session unusable
		}
		return nil, fmt.Errorf("%s: %s", name, msg)
	}
	return res, nil
}

// reset discards the client and session without talking to them. Caller holds t.mu.
func (t *BrowserTool) reset() {
	if t.client != nil {
		_ = t.client.Close()
	}
	t.client, t.sessionID = nil, ""
	t.stopXvfb()
}

// textOf joins the text parts of a tool result.
func textOf(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

// shutdown closes the session and the subprocess. Caller holds t.mu.
func (t *BrowserTool) shutdown(ctx context.Context) error {
	if t.client == nil {
		return nil
	}
	if t.sessionID != "" {
		_, _ = t.call(ctx, "close_session", map[string]any{"session_id": t.sessionID})
	}
	err := t.client.Close()
	t.client, t.sessionID = nil, ""
	t.stopXvfb()
	return err
}

// Close gracefully terminates the browser session and scrapling-mcp process.
func (t *BrowserTool) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return t.shutdown(ctx)
}

func (t *BrowserTool) Name() string {
	return "browser"
}

func (t *BrowserTool) Description() string {
	return "Browses a web page, extracts readable text, clicks elements, or captures screenshots. Actions: 'navigate', 'screenshot', 'click', 'fill'."
}

func (t *BrowserTool) Schema() llm.FunctionDef {
	return llm.FunctionDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"action": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"navigate", "screenshot", "click", "fill"},
					"description": "Browser action to perform",
				},
				"url": map[string]interface{}{
					"type":        "string",
					"description": "The URL to navigate to (required for 'navigate' and 'screenshot')",
				},
				"selector": map[string]interface{}{
					"type":        "string",
					"description": "CSS selector to click or fill (for 'click' and 'fill' actions)",
				},
				"text": map[string]interface{}{
					"type":        "string",
					"description": "Text to fill into the element (for 'fill' action)",
				},
			},
			"required": []string{"action"},
		},
	}
}

func (t *BrowserTool) Execute(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	var params struct {
		Action   string `json:"action"`
		URL      string `json:"url"`
		Selector string `json:"selector"`
		Text     string `json:"text"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return &ToolResult{Error: "invalid arguments"}, nil
	}

	// One shared browser session: serialize actions.
	t.mu.Lock()
	defer t.mu.Unlock()

	// Per-task timeout: a cold server start plus a Cloudflare solve measured ~100s.
	ctx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()

	if err := t.getSession(ctx); err != nil {
		return &ToolResult{Error: fmt.Sprintf("browser unavailable: %v", err)}, nil
	}

	switch params.Action {
	case "navigate":
		if err := isSafeURL(params.URL); err != nil {
			return &ToolResult{Error: fmt.Sprintf("security block: %v", err)}, nil
		}

		res, err := t.call(ctx, "browser_fetch", map[string]any{
			"session_id":       t.sessionID,
			"url":              params.URL,
			"solve_cloudflare": true,
			"extraction_type":  "markdown",
		})
		if err != nil {
			return &ToolResult{Error: fmt.Sprintf("failed to navigate to %s: %v", params.URL, err)}, nil
		}

		// browser_fetch wraps the page as {"status", "url", "content": [markdown chunks]}.
		var page struct {
			URL     string   `json:"url"`
			Content []string `json:"content"`
		}
		raw := textOf(res)
		if err := json.Unmarshal([]byte(raw), &page); err != nil {
			page.Content = []string{raw}
		}
		if page.URL == "" {
			page.URL = params.URL
		}

		// Clean up whitespace
		var cleanedLines []string
		for _, line := range strings.Split(strings.Join(page.Content, "\n"), "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				cleanedLines = append(cleanedLines, trimmed)
			}
		}
		resultText := strings.Join(cleanedLines, "\n")

		// Truncate to 4000 characters to protect LLM context window
		if len(resultText) > 4000 {
			resultText = resultText[:4000] + "\n\n... [Content truncated to 4,000 characters for context efficiency]"
		}

		// Best-effort title; the page is already read, so a failure here is ignored.
		title := ""
		if tres, err := t.call(ctx, "browser_evaluate", map[string]any{"session_id": t.sessionID, "expression": "document.title"}); err == nil {
			title = strings.Trim(strings.TrimSpace(textOf(tres)), `"`)
		}

		return &ToolResult{
			Output: fmt.Sprintf("Page Title: %s\nURL: %s\n\nPage Content:\n%s", title, page.URL, resultText),
		}, nil

	case "screenshot":
		if err := isSafeURL(params.URL); err != nil {
			return &ToolResult{Error: fmt.Sprintf("security block: %v", err)}, nil
		}

		if _, err := t.call(ctx, "browser_fetch", map[string]any{
			"session_id":       t.sessionID,
			"url":              params.URL,
			"solve_cloudflare": true,
		}); err != nil {
			return &ToolResult{Error: fmt.Sprintf("failed to navigate to %s: %v", params.URL, err)}, nil
		}

		res, err := t.call(ctx, "browser_screenshot", map[string]any{"session_id": t.sessionID, "full_page": true})
		if err != nil {
			return &ToolResult{Error: fmt.Sprintf("screenshot failed: %v", err)}, nil
		}
		var imgData []byte
		for _, c := range res.Content {
			if ic, ok := c.(*mcp.ImageContent); ok {
				imgData = ic.Data
				break
			}
		}
		if len(imgData) == 0 {
			return &ToolResult{Error: "screenshot failed: no image returned"}, nil
		}

		filename := fmt.Sprintf("screenshot_%d.png", time.Now().Unix())
		targetPath := filepath.Join(t.screenshotDir, filename)
		if err := os.WriteFile(targetPath, imgData, 0644); err != nil {
			return &ToolResult{Error: fmt.Sprintf("failed to save screenshot: %v", err)}, nil
		}

		return &ToolResult{
			Output: fmt.Sprintf("Screenshot captured and saved to %s (%d bytes)", targetPath, len(imgData)),
		}, nil

	case "click":
		if params.Selector == "" {
			return &ToolResult{Error: "selector required for click action"}, nil
		}
		if _, err := t.call(ctx, "browser_actions", map[string]any{
			"session_id": t.sessionID,
			"actions":    []map[string]any{{"type": "click", "target": params.Selector}},
		}); err != nil {
			return &ToolResult{Error: fmt.Sprintf("click failed: %v", err)}, nil
		}
		return &ToolResult{Output: fmt.Sprintf("Clicked %s", params.Selector)}, nil

	case "fill":
		if params.Selector == "" {
			return &ToolResult{Error: "selector required for fill action"}, nil
		}
		// slowly: random per-keystroke delays, replaces the old hand-rolled humanType.
		if _, err := t.call(ctx, "browser_actions", map[string]any{
			"session_id": t.sessionID,
			"slowly":     true,
			"actions":    []map[string]any{{"type": "textbox", "target": params.Selector, "value": params.Text}},
		}); err != nil {
			return &ToolResult{Error: fmt.Sprintf("typing failed: %v", err)}, nil
		}
		return &ToolResult{Output: fmt.Sprintf("Successfully typed text into %s", params.Selector)}, nil

	default:
		return &ToolResult{Error: fmt.Sprintf("unknown action %q", params.Action)}, nil
	}
}

func (t *BrowserTool) RequiresApproval() bool {
	return false
}
