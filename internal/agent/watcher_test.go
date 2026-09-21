package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatcherHotReload(t *testing.T) {
	tmpDir := t.TempDir()
	reg := NewAgentRegistry()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watcher, err := NewWatcher(reg, []string{tmpDir})
	if err != nil {
		t.Fatalf("failed to create watcher: %v", err)
	}

	if err := watcher.Start(ctx); err != nil {
		t.Fatalf("failed to start watcher: %v", err)
	}

	// Initially empty
	if len(reg.List()) != 0 {
		t.Errorf("expected 0 agents initially, got %d", len(reg.List()))
	}

	// Create a new agent YAML file
	newAgentYAML := `
name: hot_reloaded_agent
display_name: "Hot Reloaded Agent"
description: "Created dynamically"
persona:
  role: "Dynamic Agent"
  system_prompt: "Hello from dynamic agent"
model:
  preferred: "qwen3:8b"
`
	filePath := filepath.Join(tmpDir, "dynamic.yaml")
	if err := os.WriteFile(filePath, []byte(newAgentYAML), 0644); err != nil {
		t.Fatalf("failed to write dynamic yaml: %v", err)
	}

	// Wait for debounce and reload
	time.Sleep(350 * time.Millisecond)

	agent, found := reg.Get("hot_reloaded_agent")
	if !found || agent == nil {
		t.Errorf("expected hot_reloaded_agent to be detected and loaded by watcher")
	} else if agent.DisplayName != "Hot Reloaded Agent" {
		t.Errorf("expected display name 'Hot Reloaded Agent', got %q", agent.DisplayName)
	}
}
