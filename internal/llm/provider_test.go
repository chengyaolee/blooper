package llm

import (
	"context"
	"testing"
)

type echoModel struct{}

func (echoModel) Generate(_ context.Context, r *ChatRequest) (*ChatResponse, error) {
	return &ChatResponse{Message: ChatMessage{Content: r.Model}}, nil
}
func (echoModel) Stream(ctx context.Context, r *ChatRequest, out chan<- StreamChunk) error {
	out <- StreamChunk{Content: r.Model}
	return nil
}
func (echoModel) KVStats() (*KVMetrics, error)                 { return &KVMetrics{}, nil }
func (echoModel) ListModels(context.Context) ([]string, error) { return nil, nil }

func TestProviderPinsWholeJobAcrossCredentialChanges(t *testing.T) {
	p := &Provider{}
	if _, err := p.Generate(context.Background(), &ChatRequest{}); err == nil {
		t.Fatal("unconfigured provider accepted work")
	}
	p.Set("openai", "hosted", echoModel{})
	ctx := Pin(context.Background(), p)
	p.Set("ollama", "local", echoModel{})
	for _, c := range []struct {
		ctx  context.Context
		want string
	}{{ctx, "hosted"}, {Pin(ctx, p), "hosted"}, {context.Background(), "local"}} {
		got, err := p.Generate(c.ctx, &ChatRequest{Model: "agent-preference"})
		if err != nil || got.Message.Content != c.want {
			t.Fatalf("got %+v %v; want %s", got, err, c.want)
		}
	}
	p.Set("openai", "hosted", nil)
	if _, err := p.Generate(context.Background(), &ChatRequest{}); err == nil {
		t.Fatal("deleted credentials remained active for new work")
	}
	if _, err := p.Generate(ctx, &ChatRequest{}); err != nil {
		t.Fatal("settings change interrupted in-flight work")
	}
}
