package memory

import (
	"path/filepath"
	"testing"
)

func TestMemoryStore(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_memory.db")

	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open memory store: %v", err)
	}
	defer store.Close()

	err = store.SaveConversation(
		"sales_researcher",
		"Find VP of engineering at Acme Corp",
		"Jane Doe is VP of Engineering at Acme Corp. Email: jane@acmecorp.com",
		"completed",
		`{"company":"Acme Corp"}`,
	)
	if err != nil {
		t.Fatalf("failed to save conversation: %v", err)
	}

	results, err := store.Recall("Acme Corp", 5)
	if err != nil {
		t.Fatalf("recall query failed: %v", err)
	}

	if len(results) == 0 {
		t.Fatalf("expected at least 1 result for 'Acme Corp', got 0")
	}

	if results[0].AgentName != "sales_researcher" {
		t.Errorf("expected agent 'sales_researcher', got %q", results[0].AgentName)
	}
}
