package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// OpenAIClient implements InferenceEngine using OpenAI Chat Completions.
//
// Everything in a request (transcripts, tool output) leaves this machine.
type OpenAIClient struct {
	BaseURL    string // default https://api.openai.com/v1
	APIKey     string
	HTTPClient *http.Client
}

// OpenAIModel is the default hosted planner model, with streaming and function calling.
const OpenAIModel = "gpt-5.4-mini"

func NewOpenAIClient(baseURL, apiKey string) *OpenAIClient {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	return &OpenAIClient{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, HTTPClient: http.DefaultClient}
}

// Keep provider-specific fields out of the shared Ollama request. Reasoning
// models accept max_completion_tokens and may reject custom temperature.
func openAIRequest(req *ChatRequest, stream bool) map[string]any {
	body := map[string]any{"model": req.Model, "messages": req.Messages, "stream": stream, "store": false}
	if len(req.Tools) > 0 {
		body["tools"] = req.Tools
	}
	if req.MaxTokens > 0 {
		body["max_completion_tokens"] = req.MaxTokens
	}
	return body
}

type openAIResponse struct {
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
	Model   string `json:"model"`
	Choices []struct {
		Message ChatMessage `json:"message"` // non-streaming
		Delta   ChatMessage `json:"delta"`   // streaming
	} `json:"choices"`
}

func (c *OpenAIClient) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		return nil, fmt.Errorf("openai API error: status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return resp, nil
}

// Generate implements InferenceEngine.Generate.
func (c *OpenAIClient) Generate(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	r := openAIRequest(req, false)
	resp, err := c.do(ctx, http.MethodPost, "/chat/completions", r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.Error != nil {
		return nil, fmt.Errorf("openai API error: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("openai API error: response had no choices")
	}
	return &ChatResponse{Message: out.Choices[0].Message, Done: true, Model: out.Model}, nil
}

// Stream implements InferenceEngine.Stream. The caller owns out and closes it.
func (c *OpenAIClient) Stream(ctx context.Context, req *ChatRequest, out chan<- StreamChunk) error {
	r := openAIRequest(req, true)
	resp, err := c.do(ctx, http.MethodPost, "/chat/completions", r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
		}
		if data == "[DONE]" {
			return nil
		}
		var chunk openAIResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return fmt.Errorf("openai stream: %w", err)
		}
		if chunk.Error != nil {
			return fmt.Errorf("openai stream: %s", chunk.Error.Message)
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		if content := chunk.Choices[0].Delta.Content; content != "" {
			select {
			case out <- StreamChunk{Content: content}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return fmt.Errorf("openai stream ended before [DONE]")
}

// KVStats implements InferenceEngine.KVStats. The KV cache is OpenAI's, not ours.
func (c *OpenAIClient) KVStats() (*KVMetrics, error) { return &KVMetrics{}, nil }

// ListModels implements InferenceEngine.ListModels.
func (c *OpenAIClient) ListModels(ctx context.Context) ([]string, error) {
	resp, err := c.do(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	var models []string
	for _, m := range out.Data {
		models = append(models, m.ID)
	}
	return models, nil
}
