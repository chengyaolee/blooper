package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Ollama speaks tool-call arguments as JSON objects; the rest of blooper uses
// JSON-encoded strings. Both directions must be converted.
func TestOllamaToolCallArguments(t *testing.T) {
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&sent)
		io.WriteString(w, `{"message":{"role":"assistant","tool_calls":[{"id":"c1","function":{"name":"f","arguments":{"workspace":"demo"}}}]},"done":true}`)
	}))
	defer srv.Close()

	resp, err := NewOllamaClient(srv.URL).Generate(context.Background(), &ChatRequest{Model: "m", Messages: []ChatMessage{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c0", Function: ToolCallFunction{Name: "f", Arguments: `{"a":1}`}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Message.ToolCalls[0].Function.Arguments; got != `{"workspace":"demo"}` {
		t.Errorf("arguments decoded as %q", got)
	}
	args := sent["messages"].([]any)[0].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["arguments"]
	if _, ok := args.(map[string]any); !ok {
		t.Errorf("arguments sent to Ollama as %T, want a JSON object", args)
	}
}

func TestOpenAIGenerate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ChatRequest
		json.NewDecoder(r.Body).Decode(&req)
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer k" || req.Model != OpenAIModel ||
			req.Messages[0].Content != "hello" {
			http.Error(w, "unexpected request", 400)
			return
		}
		io.WriteString(w, `{"model":"gpt-5.4-mini","choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{\"x\":1}"}}]}}]}`)
	}))
	defer srv.Close()

	resp, err := NewOpenAIClient(srv.URL, "k").Generate(context.Background(), &ChatRequest{Model: OpenAIModel, Messages: []ChatMessage{
		{Role: "assistant", Content: "hello"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	m := resp.Message
	if len(m.ToolCalls) != 1 || m.ToolCalls[0].Function.Arguments != `{"x":1}` {
		t.Errorf("message %+v", m)
	}
}

func TestOpenAIWireStreamingAndErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			io.WriteString(w, `{"data":[{"id":"gpt-5.4-mini"}]}`)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["model"] == "forbidden" {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":{"message":"invalid key"}}`)
			return
		}
		if body["max_completion_tokens"] != float64(256) || body["store"] != false || body["max_tokens"] != nil || body["temperature"] != nil {
			t.Errorf("incompatible OpenAI request: %v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: {\"choices\":[]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	client := NewOpenAIClient(srv.URL+"/v1/", "test")
	chunks := make(chan StreamChunk, 4)
	if err := client.Stream(context.Background(), &ChatRequest{Model: OpenAIModel, MaxTokens: 256, Temperature: 0.3}, chunks); err != nil {
		t.Fatal(err)
	}
	if got := (<-chunks).Content; got != "hello" {
		t.Fatal(got)
	}
	models, err := client.ListModels(context.Background())
	if err != nil || len(models) != 1 || models[0] != OpenAIModel {
		t.Fatalf("models: %v %v", models, err)
	}
	if _, err := client.Generate(context.Background(), &ChatRequest{Model: "forbidden"}); err == nil || !strings.Contains(err.Error(), "invalid key") {
		t.Fatalf("provider error lost: %v", err)
	}
}

func TestOpenAIStreamRejectsBrokenResponses(t *testing.T) {
	for _, body := range []string{
		"data: not-json\n\n",
		"data: {\"error\":{\"message\":\"provider failed\"}}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
		err := NewOpenAIClient(srv.URL, "test").Stream(context.Background(), &ChatRequest{Model: OpenAIModel}, make(chan StreamChunk, 2))
		srv.Close()
		if err == nil {
			t.Fatalf("broken stream accepted: %q", body)
		}
	}
}
