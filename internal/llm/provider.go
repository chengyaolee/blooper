package llm

import (
	"context"
	"fmt"
	"sync"
)

// Provider switches credentials for new jobs. Pin preserves one provider/model
// for a whole agent/delegation chain, even if settings change while it runs.
type Provider struct {
	mu     sync.RWMutex
	engine InferenceEngine
	model  string
	name   string
}
type providerKey struct{ p *Provider }
type providerSnapshot struct {
	engine InferenceEngine
	model  string
}

func (p *Provider) Set(name, model string, engine InferenceEngine) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.name, p.model, p.engine = name, model, engine
}
func (p *Provider) Status() (name, model string, connected bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.name, p.model, p.engine != nil
}
func Pin(ctx context.Context, engine InferenceEngine) context.Context {
	p, ok := engine.(*Provider)
	if !ok || ctx.Value(providerKey{p}) != nil {
		return ctx
	}
	p.mu.RLock()
	snapshot := providerSnapshot{p.engine, p.model}
	p.mu.RUnlock()
	return context.WithValue(ctx, providerKey{p}, snapshot)
}
func (p *Provider) snapshot(ctx context.Context) (providerSnapshot, error) {
	s, ok := ctx.Value(providerKey{p}).(providerSnapshot)
	if !ok {
		p.mu.RLock()
		s = providerSnapshot{p.engine, p.model}
		p.mu.RUnlock()
	}
	if s.engine == nil {
		return s, fmt.Errorf("Connect OpenAI or choose an installed Ollama model in Settings")
	}
	return s, nil
}
func (p *Provider) Generate(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	s, err := p.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	r := *req
	r.Model = s.model
	return s.engine.Generate(ctx, &r)
}
func (p *Provider) Stream(ctx context.Context, req *ChatRequest, out chan<- StreamChunk) error {
	s, err := p.snapshot(ctx)
	if err != nil {
		return err
	}
	r := *req
	r.Model = s.model
	return s.engine.Stream(ctx, &r, out)
}
func (p *Provider) KVStats() (*KVMetrics, error) {
	s, err := p.snapshot(context.Background())
	if err != nil {
		return &KVMetrics{}, nil
	}
	return s.engine.KVStats()
}
func (p *Provider) ListModels(ctx context.Context) ([]string, error) {
	s, err := p.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return []string{s.model}, nil
}
