package memory

import (
	"database/sql"
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

func TestOpenMigratesOldTaskGraph(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// Schema as shipped before run linking, acceptance checks and verified_by existed.
	if _, err := old.Exec(`
		CREATE TABLE tasks (goal_id TEXT NOT NULL, id TEXT NOT NULL, parent_id TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', status TEXT NOT NULL,
			assignee TEXT NOT NULL DEFAULT '', owner TEXT NOT NULL DEFAULT '', result TEXT NOT NULL DEFAULT '',
			feedback TEXT NOT NULL DEFAULT '', attempts INTEGER NOT NULL DEFAULT 0, created_by TEXT NOT NULL,
			updated_at INTEGER NOT NULL, PRIMARY KEY (goal_id, id));
		CREATE TABLE task_events (id INTEGER PRIMARY KEY AUTOINCREMENT, goal_id TEXT NOT NULL, ts INTEGER NOT NULL,
			src TEXT NOT NULL, dst TEXT NOT NULL, kind TEXT NOT NULL, reward REAL, payload TEXT NOT NULL DEFAULT '{}');`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open old db: %v", err)
	}
	defer store.Close()
	goal, err := store.CreateGoal("g", Actor{Agent: "boss", RunID: "r1"}, nil, nil)
	if err != nil {
		t.Fatalf("create goal on migrated db: %v", err)
	}
	if n, err := store.RunEventCount("r1"); err != nil || n != 1 {
		t.Fatalf("run_id column should work after migration: n=%d err=%v (goal %s)", n, err, goal)
	}
}
