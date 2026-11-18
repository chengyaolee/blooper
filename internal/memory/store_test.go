package memory

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chengyaolee/ragout/embedder"
)

func testEmbedder(t *testing.T) Option {
	t.Helper()
	e, err := embedder.NewMockEmbedder(8, 0)
	if err != nil {
		t.Fatal(err)
	}
	return WithEmbedder(e)
}

func TestMemoryStore(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_memory.db")

	store, err := Open(dbPath, testEmbedder(t))
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
	if err := store.SaveConversation("email_drafter", "Draft a lunch invite", "Wrote a short invite for Friday.", "completed", ""); err != nil {
		t.Fatal(err)
	}

	results, err := store.Recall("Acme Corp", 5)
	if err != nil {
		t.Fatalf("recall query failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected at least 1 result for 'Acme Corp', got 0")
	}
	if results[0].AgentName != "sales_researcher" || results[0].CreatedAt.IsZero() {
		t.Errorf("expected full 'sales_researcher' record first, got %+v", results[0])
	}
	if len(results) > 1 && results[0].ID == results[1].ID {
		t.Error("same conversation returned twice")
	}
	if got, _ := store.Recall("Acme Corp", 1); len(got) != 1 {
		t.Errorf("limit 1 returned %d records", len(got))
	}
}

func TestIndexSurvivesRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "m.db")
	s, err := Open(dbPath, testEmbedder(t))
	if err != nil {
		t.Fatal(err)
	}
	s.SaveConversation("a", "Find Acme Corp contact", "Jane Doe", "completed", "")
	s.Close()

	s, err = Open(dbPath, testEmbedder(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, err := s.Recall("Acme", 5); err != nil || len(got) == 0 || got[0].UserQuery != "Find Acme Corp contact" {
		t.Fatalf("recall after reopen: %v %v", got, err)
	}
}

// Conversations saved before the ragout index existed are indexed on first open.
func TestLegacyRowsAreIndexed(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "m.db")
	s, err := Open(dbPath, testEmbedder(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO conversations (agent_name, user_query, summary) VALUES ('a', 'Quarterly Zorblax report', 'done')`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	os.Remove(dbPath + ".vec")
	os.Remove(dbPath + ".bm25")

	s, err = Open(dbPath, testEmbedder(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, _ := s.Recall("Zorblax", 5); len(got) == 0 || got[0].UserQuery != "Quarterly Zorblax report" {
		t.Fatalf("legacy row not recalled: %v", got)
	}
}

func TestRecallOnEmptyStore(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "m.db"), testEmbedder(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, err := s.Recall("anything", 5); err != nil || len(got) != 0 {
		t.Fatalf("want no results and no error, got %v %v", got, err)
	}
}

func TestRecallWithoutOllama(t *testing.T) {
	path := filepath.Join(t.TempDir(), "local.db")
	store, err := Open(path, WithoutEmbeddings())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveConversation("researcher", "observatory telescope schedule", "telescope observations Friday", "completed", ""); err != nil {
		t.Fatal(err)
	}
	records, err := store.Recall("telescope", 3)
	if err != nil || len(records) == 0 {
		t.Fatalf("local recall: %v %v", records, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, WithoutEmbeddings())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	records, err = store.Recall("telescope", 3)
	if err != nil || len(records) == 0 {
		t.Fatalf("persisted local recall: %v %v", records, err)
	}
}
