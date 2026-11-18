package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// OllamaClient implements InferenceEngine for Ollama.
type OllamaClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewOllamaClient creates a new Ollama HTTP client.
func NewOllamaClient(baseURL string) *OllamaClient {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	return &OllamaClient{
		BaseURL:    baseURL,
		HTTPClient: http.DefaultClient,
	}
}

type ollamaMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
}

// Ollama sends and expects tool-call arguments as a JSON object, while
// llm.ToolCall carries them as a JSON-encoded string (the OpenAI shape).
type ollamaToolCall struct {
	ID       string `json:"id,omitempty"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

func toOllamaCalls(calls []ToolCall) []ollamaToolCall {
	out := make([]ollamaToolCall, len(calls))
	for i, tc := range calls {
		out[i].ID = tc.ID
		out[i].Function.Name = tc.Function.Name
		out[i].Function.Arguments = json.RawMessage(tc.Function.Arguments)
		if !json.Valid(out[i].Function.Arguments) {
			out[i].Function.Arguments = json.RawMessage("{}")
		}
	}
	return out
}

func fromOllamaCalls(calls []ollamaToolCall) []ToolCall {
	var out []ToolCall
	for _, tc := range calls {
		out = append(out, ToolCall{ID: tc.ID, Type: "function", Function: ToolCallFunction{Name: tc.Function.Name, Arguments: string(tc.Function.Arguments)}})
	}
	return out
}

type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Tools    []ToolDef       `json:"tools,omitempty"`
	Stream   bool            `json:"stream"`
	Options  map[string]any  `json:"options,omitempty"`
}

type ollamaChatResponse struct {
	Model   string        `json:"model"`
	Message ollamaMessage `json:"message"`
	Done    bool          `json:"done"`
}

func (c *OllamaClient) mapMessages(messages []ChatMessage) []ollamaMessage {
	res := make([]ollamaMessage, len(messages))
	for i, m := range messages {
		res[i] = ollamaMessage{
			Role:      m.Role,
			Content:   m.Content,
			ToolCalls: toOllamaCalls(m.ToolCalls),
		}
	}
	return res
}

// Generate implements InferenceEngine.Generate.
func (c *OllamaClient) Generate(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	apiURL, err := url.JoinPath(c.BaseURL, "/api/chat")
	if err != nil {
		return nil, err
	}

	options := make(map[string]any)
	if req.Temperature != 0 {
		options["temperature"] = req.Temperature
	}
	if req.MaxTokens != 0 {
		options["num_predict"] = req.MaxTokens
	}

	ollamaReq := ollamaChatRequest{
		Model:    req.Model,
		Messages: c.mapMessages(req.Messages),
		Tools:    req.Tools,
		Stream:   false,
		Options:  options,
	}

	bodyBytes, err := json.Marshal(ollamaReq)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("ollama API error: status %d: %s", resp.StatusCode, msg)
	}

	var ollamaResp ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		return nil, err
	}

	return &ChatResponse{
		Message: ChatMessage{
			Role:      ollamaResp.Message.Role,
			Content:   ollamaResp.Message.Content,
			ToolCalls: fromOllamaCalls(ollamaResp.Message.ToolCalls),
		},
		Done:  ollamaResp.Done,
		Model: ollamaResp.Model,
	}, nil
}

// Stream implements InferenceEngine.Stream.
// The caller owns out and closes it (agent.RunStream does).
func (c *OllamaClient) Stream(ctx context.Context, req *ChatRequest, out chan<- StreamChunk) error {
	apiURL, err := url.JoinPath(c.BaseURL, "/api/chat")
	if err != nil {
		return err
	}

	options := make(map[string]any)
	if req.Temperature != 0 {
		options["temperature"] = req.Temperature
	}
	if req.MaxTokens != 0 {
		options["num_predict"] = req.MaxTokens
	}

	ollamaReq := ollamaChatRequest{
		Model:    req.Model,
		Messages: c.mapMessages(req.Messages),
		Tools:    req.Tools,
		Stream:   true,
		Options:  options,
	}

	bodyBytes, err := json.Marshal(ollamaReq)
	if err != nil {
		return err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("ollama API error: status %d: %s", resp.StatusCode, msg)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Bytes()
		var ollamaResp ollamaChatResponse
		if err := json.Unmarshal(line, &ollamaResp); err != nil {
			out <- StreamChunk{Error: err}
			return err
		}

		out <- StreamChunk{
			Content:   ollamaResp.Message.Content,
			ToolCalls: fromOllamaCalls(ollamaResp.Message.ToolCalls),
			Done:      ollamaResp.Done,
		}

		if ollamaResp.Done {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		out <- StreamChunk{Error: err}
		return err
	}
	return nil
}

// KVStats implements InferenceEngine.KVStats.
func (c *OllamaClient) KVStats() (*KVMetrics, error) {
	return &KVMetrics{}, nil
}

type ollamaTagsResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

// ListModels implements InferenceEngine.ListModels.
func (c *OllamaClient) ListModels(ctx context.Context) ([]string, error) {
	apiURL, err := url.JoinPath(c.BaseURL, "/api/tags")
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("ollama API error: status %d: %s", resp.StatusCode, msg)
	}

	var tagsResp ollamaTagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&tagsResp); err != nil {
		return nil, err
	}

	var models []string
	for _, m := range tagsResp.Models {
		models = append(models, m.Name)
	}
	return models, nil
}
