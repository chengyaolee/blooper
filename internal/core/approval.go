package core

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

type ApprovalItem struct {
	ID        string
	AgentName string
	ToolName  string
	Arguments string
	Status    string // pending, approved, rejected, cancelled
	CreatedAt time.Time
	done      chan struct{}
}

type ApprovalQueue struct {
	mu    sync.Mutex
	items map[string]*ApprovalItem
}

func NewApprovalQueue() *ApprovalQueue { return &ApprovalQueue{items: map[string]*ApprovalItem{}} }

func (q *ApprovalQueue) Submit(agentName, toolName, arguments string) string {
	q.mu.Lock()
	defer q.mu.Unlock()
	id := uuid.New().String()
	q.items[id] = &ApprovalItem{ID: id, AgentName: agentName, ToolName: toolName, Arguments: arguments, Status: "pending", CreatedAt: time.Now(), done: make(chan struct{})}
	return id
}

func (q *ApprovalQueue) Get(id string) *ApprovalItem {
	q.mu.Lock()
	defer q.mu.Unlock()
	if item := q.items[id]; item != nil {
		copy := *item
		return &copy
	}
	return nil
}

func (q *ApprovalQueue) resolve(id, status string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	item := q.items[id]
	if item == nil {
		return fmt.Errorf("approval item %s not found", id)
	}
	if item.Status != "pending" {
		return fmt.Errorf("approval item %s is already %s", id, item.Status)
	}
	item.Status = status
	close(item.done)
	return nil
}

func (q *ApprovalQueue) Approve(id string) error { return q.resolve(id, "approved") }
func (q *ApprovalQueue) Reject(id string) error  { return q.resolve(id, "rejected") }

// Wait pauses the original agent so the approved call executes once, with its
// original caller, delegation depth and cancellation context intact.
func (q *ApprovalQueue) Wait(ctx context.Context, id string) error {
	item := q.Get(id)
	if item == nil {
		return fmt.Errorf("approval item %s not found", id)
	}
	select {
	case <-ctx.Done():
		_ = q.resolve(id, "cancelled")
		return ctx.Err()
	case <-item.done:
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if q.Get(id).Status != "approved" {
		return fmt.Errorf("action rejected by the user; do not retry")
	}
	return nil
}

func (q *ApprovalQueue) ListPending() []*ApprovalItem {
	q.mu.Lock()
	defer q.mu.Unlock()
	var pending []*ApprovalItem
	for _, item := range q.items {
		if item.Status == "pending" {
			copy := *item
			pending = append(pending, &copy)
		}
	}
	return pending
}
