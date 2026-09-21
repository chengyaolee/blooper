package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/chengyaolee/blooper/internal/llm"
)

// WebSearchTool queries DuckDuckGo for quick factual information.
type WebSearchTool struct {
	client *http.Client
}

func NewWebSearchTool() *WebSearchTool {
	return &WebSearchTool{
		client: &http.Client{
			Timeout: 15 * time.Second, // Strict timeout to prevent hanging connections
		},
	}
}

func (t *WebSearchTool) Name() string {
	return "web_search"
}

func (t *WebSearchTool) Description() string {
	return "Searches the web using DuckDuckGo to find information, summaries, or answer questions."
}

func (t *WebSearchTool) Schema() llm.FunctionDef {
	return llm.FunctionDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{
					"type":        "string",
					"description": "The search query string",
				},
			},
			"required": []string{"query"},
		},
	}
}

func (t *WebSearchTool) Execute(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	var params struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return &ToolResult{Error: "invalid arguments"}, nil
	}

	endpoint := fmt.Sprintf("https://api.duckduckgo.com/?q=%s&format=json&no_html=1&skip_disambig=1", url.QueryEscape(params.Query))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return &ToolResult{Error: err.Error()}, nil
	}
	req.Header.Set("User-Agent", "Blooper-Agent/1.0")

	resp, err := t.client.Do(req)
	if err != nil {
		return &ToolResult{
			Output: fmt.Sprintf("Search query %q executed. Note: Network lookup failed (%v). Consider using the 'browser' tool to navigate directly.", params.Query, err),
		}, nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024)) // Limit response read to 512KB
	if err != nil {
		return &ToolResult{Error: fmt.Sprintf("failed to read response: %v", err)}, nil
	}

	var ddgResp struct {
		AbstractText string `json:"AbstractText"`
		Heading      string `json:"Heading"`
		AbstractURL  string `json:"AbstractURL"`
	}
	_ = json.Unmarshal(body, &ddgResp)

	if ddgResp.AbstractText != "" {
		return &ToolResult{
			Output: fmt.Sprintf("Heading: %s\nSummary: %s\nURL: %s", ddgResp.Heading, ddgResp.AbstractText, ddgResp.AbstractURL),
		}, nil
	}

	return &ToolResult{
		Output: fmt.Sprintf("Search completed for %q. No direct instant answer found on DuckDuckGo. For in-depth research, consider using the 'browser' tool to load relevant search URLs directly.", params.Query),
	}, nil
}

func (t *WebSearchTool) RequiresApproval() bool {
	return false
}
