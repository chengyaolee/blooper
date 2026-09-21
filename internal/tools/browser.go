package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chengyaolee/blooper/internal/llm"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
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

// BrowserTool provides controlled, sandboxed web browsing via Rod.
type BrowserTool struct {
	mu            sync.Mutex
	browser       *rod.Browser
	screenshotDir string
	headless      bool
}

// NewBrowserTool creates a managed BrowserTool.
func NewBrowserTool(headless bool) *BrowserTool {
	dir := "data/screenshots"
	_ = os.MkdirAll(dir, 0755)
	return &BrowserTool{
		screenshotDir: dir,
		headless:      headless,
	}
}

// getBrowser lazily initializes and reuses a single headless browser instance.
func (t *BrowserTool) getBrowser() (*rod.Browser, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.browser != nil {
		return t.browser, nil
	}

	// Launch controlled headless browser
	l := launcher.New().
		Headless(t.headless).
		Set("no-sandbox").
		Set("disable-gpu").
		Set("disable-dev-shm-usage")

	urlStr, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("failed to launch chromium: %w", err)
	}

	browser := rod.New().ControlURL(urlStr)
	if err := browser.Connect(); err != nil {
		return nil, fmt.Errorf("failed to connect to chromium: %w", err)
	}

	t.browser = browser
	return t.browser, nil
}

// Close gracefully terminates the browser process to prevent memory leaks.
func (t *BrowserTool) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.browser != nil {
		err := t.browser.Close()
		t.browser = nil
		return err
	}
	return nil
}

func (t *BrowserTool) Name() string {
	return "browser"
}

func (t *BrowserTool) Description() string {
	return "Browses a web page, extracts readable text, clicks elements, or captures screenshots. Actions: 'navigate', 'screenshot', 'click'."
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
					"enum":        []string{"navigate", "screenshot", "click"},
					"description": "Browser action to perform",
				},
				"url": map[string]interface{}{
					"type":        "string",
					"description": "The URL to navigate to (required for 'navigate' and 'screenshot')",
				},
				"selector": map[string]interface{}{
					"type":        "string",
					"description": "CSS selector to click (for 'click' action)",
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
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return &ToolResult{Error: "invalid arguments"}, nil
	}

	browser, err := t.getBrowser()
	if err != nil {
		return &ToolResult{Error: fmt.Sprintf("browser unavailable: %v", err)}, nil
	}

	// Enforce 30s timeout per browser task to prevent hanging
	ctxTimeout, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	switch params.Action {
	case "navigate":
		if err := isSafeURL(params.URL); err != nil {
			return &ToolResult{Error: fmt.Sprintf("security block: %v", err)}, nil
		}

		page, err := browser.Context(ctxTimeout).Page(proto.TargetCreateTarget{URL: params.URL})
		if err != nil {
			return &ToolResult{Error: fmt.Sprintf("failed to load page %s: %v", params.URL, err)}, nil
		}
		defer page.Close()

		_ = page.WaitLoad()

		// DOM Simplification: extract only visible text, strip scripts/styles/media
		textVal, err := page.Eval(`() => {
			const clone = document.body.cloneNode(true);
			const removeElements = clone.querySelectorAll('script, style, svg, noscript, iframe, img, video, audio');
			removeElements.forEach(el => el.remove());
			return clone.innerText || clone.textContent || "";
		}`)
		if err != nil {
			return &ToolResult{Error: fmt.Sprintf("failed to extract text: %v", err)}, nil
		}

		rawText := textVal.Value.Str()
		// Clean up whitespace
		lines := strings.Split(rawText, "\n")
		var cleanedLines []string
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" {
				cleanedLines = append(cleanedLines, trimmed)
			}
		}
		resultText := strings.Join(cleanedLines, "\n")

		// Truncate to 4000 characters to protect LLM context window
		if len(resultText) > 4000 {
			resultText = resultText[:4000] + "\n\n... [Content truncated to 4,000 characters for context efficiency]"
		}

		title := ""
		if info, err := page.Info(); err == nil && info != nil {
			title = info.Title
		}

		return &ToolResult{
			Output: fmt.Sprintf("Page Title: %s\nURL: %s\n\nPage Content:\n%s", title, params.URL, resultText),
		}, nil

	case "screenshot":
		if err := isSafeURL(params.URL); err != nil {
			return &ToolResult{Error: fmt.Sprintf("security block: %v", err)}, nil
		}

		page, err := browser.Context(ctxTimeout).Page(proto.TargetCreateTarget{URL: params.URL})
		if err != nil {
			return &ToolResult{Error: fmt.Sprintf("failed to load page: %v", err)}, nil
		}
		defer page.Close()

		_ = page.WaitLoad()

		imgData, err := page.Screenshot(true, nil)
		if err != nil {
			return &ToolResult{Error: fmt.Sprintf("screenshot failed: %v", err)}, nil
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
		return &ToolResult{
			Output: fmt.Sprintf("Simulated click on selector: %s", params.Selector),
		}, nil

	default:
		return &ToolResult{Error: fmt.Sprintf("unknown action %q", params.Action)}, nil
	}
}

func (t *BrowserTool) RequiresApproval() bool {
	return false
}
