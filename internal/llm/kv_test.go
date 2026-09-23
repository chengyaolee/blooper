package llm

import (
	"context"
	"testing"
	"time"
)

func TestKVSlotAllocationAndEviction(t *testing.T) {
	// Simulate 2 slots with 128KB per token (Qwen 8B)
	allocator := NewSlotManager(2, 131072) // 128KB per token
	ctx := context.Background()

	// 1. Allocate session 1
	slot0, err := allocator.AllocateSlot(ctx, "session-agent-research", 2048)
	if err != nil || slot0 != 0 {
		t.Fatalf("Expected slot 0, got %d: %v", slot0, err)
	}

	time.Sleep(100 * time.Millisecond) // simulate some work

	// 2. Allocate session 2
	slot1, err := allocator.AllocateSlot(ctx, "session-agent-email", 1024)
	if err != nil || slot1 != 1 {
		t.Fatalf("Expected slot 1, got %d: %v", slot1, err)
	}

	// Verify stats (2 active slots)
	stats, _ := allocator.Stats()
	if stats.ActiveSlots != 2 {
		t.Errorf("Expected 2 active slots, got %d", stats.ActiveSlots)
	}
	t.Logf("Memory used: %d MB", stats.UsedMemoryBytes/(1024*1024))

	time.Sleep(100 * time.Millisecond)

	// 3. Allocate session 3 again (Should evict session 1 - oldest)
	slotEvicted, err := allocator.AllocateSlot(ctx, "session-agent-sales", 2048)
	if err != nil {
		t.Fatalf("Allocation failed: %v", err)
	}
	if slotEvicted != 0 {
		t.Errorf("Expected LRU to evict slot 0, but got slot %d", slotEvicted)
	}
	t.Logf("Successfully evicted oldest session and reused Slot %d", slotEvicted)
}