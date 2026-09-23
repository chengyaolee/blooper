package llm

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// A slot is a reserved chunk of VRAM for context
type KVSlot struct {
	ID int
	SessionID string
	Tokens int
	LastUsed time.Time
	Active bool
}

// Manages a fixed pool of KV context slots
type SlotManager struct {
	mu sync.Mutex
	slots []*KVSlot
	maxSlots int
	bytesPerToken int64
}

// Initialises an allocator with fixed slot capacity
func NewSlotManager(maxSlots int, bytesPerToken int64) *SlotManager {
	slots := make([]*KVSlot, maxSlots)
	for i := 0; i < maxSlots; i ++ {
		slots[i] = &KVSlot{
			ID: i,
			Active: false,
		}
	}
	return &SlotManager{
		slots: slots,
		maxSlots: maxSlots,
		bytesPerToken: bytesPerToken,
	}
}

//  Assigns a slot to a session ID, or evicts the least recently used slot if full
func (m *SlotManager) AllocateSlot(ctx context.Context, sessionID string, expectedToken int) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Case 1: Session already has a slot
	for _, slot := range m.slots {
		if slot.SessionID == sessionID && slot.Active {
			slot.LastUsed = time.Now()
			slot.Tokens = expectedToken
			return slot.ID, nil
		}
	}

	// Case 2: An empty slot is available
	for _, slot := range m.slots {
		if !slot.Active {
			slot.SessionID = sessionID
			slot.LastUsed = time.Now()
			slot.Tokens = expectedToken
			slot.Active = true
			return slot.ID, nil
		}
	}

	// Case 3: No empty slots, evict the least recently used slot
	oldestIndex := -1
	var oldestTime time.Time
	// loop through all slots and find the oldest one
	for i, slot:= range m.slots {
		if oldestIndex == -1 || slot.LastUsed.Before(oldestTime) {
			oldestIndex = i
			oldestTime = slot.LastUsed
		}
	}
	// if oldest found, evict it
	if oldestIndex != -1 {
		slot := m.slots[oldestIndex]
		slot.SessionID = sessionID
		slot.Tokens = expectedToken
		slot.LastUsed = time.Now()
		return slot.ID, nil
	}

	// No slots available, return error
	return 0, fmt.Errorf("no slots available")
}


// When agent completes its conversation, free the slot
func (m *SlotManager) FreeSlot(ctx context.Context, slotID int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// check if slotID is valid
	if slotID < 0 || slotID >= len(m.slots) {
		return fmt.Errorf("invalid slot ID")
	}

	// check if slot is active
	slot := m.slots[slotID]
	if !slot.Active {
		return fmt.Errorf("slot is not active")
	}

	// free the slot
	slot.Active = false
	slot.SessionID = ""
	slot.Tokens = 0
	return nil
}

// Stats returns the active memory usage.
func (m *SlotManager) Stats() (*KVMetrics, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	active := 0
	var usedBytes int64

	for _, slot := range m.slots {
		if slot.Active {
			active++
			usedBytes += int64(slot.Tokens) * m.bytesPerToken
		}
	}

	return &KVMetrics{
		ActiveSlots:     active,
		TotalSlots:      m.maxSlots,
		UsedMemoryBytes: usedBytes,
	}, nil
}