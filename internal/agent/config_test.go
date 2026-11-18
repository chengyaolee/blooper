package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	yamlContent := `
name: test_agent
display_name: "Test Agent"
description: "A test agent"
persona:
  role: "Tester"
  system_prompt: "You are a test agent."
model:
  preferred: "qwen3:8b"
tools:
  - web_search
`
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test_agent.yaml")
	if err := os.WriteFile(filePath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed to write temp yaml: %v", err)
	}

	cfg, err := LoadConfig(filePath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if cfg.Name != "test_agent" {
		t.Errorf("expected name 'test_agent', got %q", cfg.Name)
	}
	if cfg.Model.Preferred != "qwen3:8b" {
		t.Errorf("expected model 'qwen3:8b', got %q", cfg.Model.Preferred)
	}
}

func TestRegistryLoadAgents(t *testing.T) {
	reg := NewAgentRegistry()
	if err := reg.LoadDir("../../agents"); err != nil {
		t.Fatalf("failed to load agents dir: %v", err)
	}

	list := reg.List()
	if len(list) < 3 {
		t.Errorf("expected at least 3 agents loaded, got %d", len(list))
	}

	creator, found := reg.Get("agent_creator")
	if !found || creator == nil {
		t.Fatalf("expected agent_creator to be registered")
	}
	if !creator.BuiltIn {
		t.Errorf("expected agent_creator to be marked built_in")
	}
}

func TestApprovalMustNameAnAllowedTool(t *testing.T) {
	cfg := &AgentConfig{Approval: ApprovalConfig{RequireApprovalFor: []string{"email_send"}}}
	if err := cfg.ValidateTools(nil); err == nil {
		t.Fatal("unavailable approval tool accepted")
	}
}
