package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/chengyaolee/blooper/internal/llm"
	"github.com/chengyaolee/blooper/internal/tools"
	"gopkg.in/yaml.v3"
)

// CreateAgentConfigTool is a built-in tool to create new agents.
type CreateAgentConfigTool struct {
	OutputDir string
}

func NewCreateAgentConfigTool(outputDir string) *CreateAgentConfigTool {
	if outputDir == "" {
		outputDir = "user_agents"
	}
	return &CreateAgentConfigTool{OutputDir: outputDir}
}

// Name returns the tool's name.
func (t *CreateAgentConfigTool) Name() string {
	return "create_agent_config"
}

// Description returns the tool's description.
func (t *CreateAgentConfigTool) Description() string {
	return "Creates a new agent by writing a YAML configuration file. Provide the agent's name, display_name, description, persona (role, tone, system_prompt), preferred model, list of tools, and approval rules."
}

// Schema returns the FunctionDef for the tool arguments.
func (t *CreateAgentConfigTool) Schema() llm.FunctionDef {
	return llm.FunctionDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name":                 map[string]interface{}{"type": "string"},
				"display_name":         map[string]interface{}{"type": "string"},
				"description":          map[string]interface{}{"type": "string"},
				"role":                 map[string]interface{}{"type": "string"},
				"tone":                 map[string]interface{}{"type": "string"},
				"system_prompt":        map[string]interface{}{"type": "string"},
				"preferred_model":      map[string]interface{}{"type": "string"},
				"tools":                map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
				"require_approval_for": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
			},
			"required": []string{"name", "display_name", "description", "role", "system_prompt", "preferred_model"},
		},
	}
}

// Execute performs the tool's action.
func (t *CreateAgentConfigTool) Execute(ctx context.Context, args json.RawMessage) (*tools.ToolResult, error) {
	var params struct {
		Name               string   `json:"name"`
		DisplayName        string   `json:"display_name"`
		Description        string   `json:"description"`
		Role               string   `json:"role"`
		Tone               string   `json:"tone"`
		SystemPrompt       string   `json:"system_prompt"`
		PreferredModel     string   `json:"preferred_model"`
		Tools              []string `json:"tools"`
		RequireApprovalFor []string `json:"require_approval_for"`
	}

	if err := json.Unmarshal(args, &params); err != nil {
		return &tools.ToolResult{Error: fmt.Sprintf("failed to parse arguments: %v", err)}, nil
	}

	cfg := &AgentConfig{
		Name:        params.Name,
		DisplayName: params.DisplayName,
		Description: params.Description,
		Persona: PersonaConfig{
			Role:         params.Role,
			Tone:         params.Tone,
			SystemPrompt: params.SystemPrompt,
		},
		Model: ModelConfig{
			Preferred: params.PreferredModel,
		},
		Tools: params.Tools,
		Approval: ApprovalConfig{
			RequireApprovalFor: params.RequireApprovalFor,
		},
	}

	if err := cfg.Validate(); err != nil {
		return &tools.ToolResult{Error: fmt.Sprintf("invalid agent configuration: %v", err)}, nil
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal config: %w", err)
	}

	dir := t.OutputDir
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	path := filepath.Join(dir, fmt.Sprintf("%s.yaml", cfg.Name))
	if err := os.WriteFile(path, data, 0644); err != nil {
		return nil, fmt.Errorf("failed to write config file %s: %w", path, err)
	}

	return &tools.ToolResult{
		Output: fmt.Sprintf("Successfully created agent config at %s", path),
	}, nil
}

// RequiresApproval indicates if this tool needs user approval before execution.
func (t *CreateAgentConfigTool) RequiresApproval() bool {
	return true
}
