package core

import (
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
	Status    string // "pending", "approved", "rejected"
	CreatedAt time.Time
}

type ApprovalQueue struct {
	mu    sync.Mutex
	items map[string]*ApprovalItem
}

func NewApprovalQueue() *ApprovalQueue {
	return &ApprovalQueue{
		items: make(map[string]*ApprovalItem),
	}
}

func (q *ApprovalQueue) Submit(agentName, toolName, arguments string) string {
	q.mu.Lock()
	defer q.mu.Unlock()

	id := uuid.New().String()
	item := &ApprovalItem{
		ID:        id,
		AgentName: agentName,
		ToolName:  toolName,
		Arguments: arguments,
		Status:    "pending",
		CreatedAt: time.Now(),
	}
	q.items[id] = item
	return id
}

func (q *ApprovalQueue) Get(id string) *ApprovalItem {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.items[id]
}

func (q *ApprovalQueue) Approve(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	item, ok := q.items[id]
	if !ok {
		return fmt.Errorf("approval item %s not found", id)
	}
	item.Status = "approved"
	return nil
}

func (q *ApprovalQueue) Reject(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	item, ok := q.items[id]
	if !ok {
		return fmt.Errorf("approval item %s not found", id)
	}
	item.Status = "rejected"
	return nil
}

func (q *ApprovalQueue) ListPending() []*ApprovalItem {
	q.mu.Lock()
	defer q.mu.Unlock()

	var pending []*ApprovalItem
	for _, item := range q.items {
		if item.Status == "pending" {
			pending = append(pending, item)
		}
	}
	return pending
}
