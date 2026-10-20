package core

import (
	"context"
	"testing"
)

func TestApprovalQueue(t *testing.T) {
	q := NewApprovalQueue()

	id := q.Submit(context.Background(), "agent_creator", "create_agent_config", `{"name":"lead_agent"}`)
	if id == "" {
		t.Fatalf("expected non-empty approval ID")
	}

	item := q.Get(id)
	if item == nil {
		t.Fatalf("expected to find submitted approval item")
	}
	if item.Status != "pending" {
		t.Errorf("expected status 'pending', got %q", item.Status)
	}

	pending := q.ListPending()
	if len(pending) != 1 {
		t.Errorf("expected 1 pending item, got %d", len(pending))
	}

	if err := q.Approve(id); err != nil {
		t.Fatalf("Approve failed: %v", err)
	}

	if item.Status != "approved" {
		t.Errorf("expected status 'approved', got %q", item.Status)
	}

	pending = q.ListPending()
	if len(pending) != 0 {
		t.Errorf("expected 0 pending items after approval, got %d", len(pending))
	}
}
