package llm

import "context"

// KVManager is a PLACEHOLDER interface for manual implementation.
// The user intends to implement custom KV cache management as a learning exercise.
//
// KV Cache sizing formula:
//   KVCacheSize = 2 × layers × heads × head_dim × precision_bytes × seq_len
//
// For example, Qwen3 8B (32 layers, 32 heads, 128 head_dim, FP16):
//   KV per token = 2 × 32 × 32 × 128 × 2 = 1,048,576 bytes (~1 MB/token)
//   At 4096 context: ~4 GB KV cache
//
// Possible implementations:
//   - Prefix caching: share system prompt KV blocks across agents
//   - Paged attention: allocate KV in fixed-size pages to avoid fragmentation
//   - LRU eviction: free oldest context blocks under VRAM pressure
type KVManager interface {
    AllocateSlot(ctx context.Context, sessionID string, expectedTokens int) (slotID int, err error)
    FreeSlot(ctx context.Context, slotID int) error
    EvictLRU(ctx context.Context) error
    SharePrefix(ctx context.Context, prefixHash string, targetSlotID int) error
    Stats() (*KVMetrics, error)
}

// DefaultKVManager is a no-op stub. Ollama manages KV cache internally.
type DefaultKVManager struct{}

func (d *DefaultKVManager) AllocateSlot(_ context.Context, _ string, _ int) (int, error) { return 0, nil }
func (d *DefaultKVManager) FreeSlot(_ context.Context, _ int) error { return nil }
func (d *DefaultKVManager) EvictLRU(_ context.Context) error { return nil }
func (d *DefaultKVManager) SharePrefix(_ context.Context, _ string, _ int) error { return nil }
func (d *DefaultKVManager) Stats() (*KVMetrics, error) { return &KVMetrics{}, nil }
