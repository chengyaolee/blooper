package agent

import (
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// AgentConfig represents the YAML configuration for an agent.
type AgentConfig struct {
	Name        string           `yaml:"name"`
	DisplayName string           `yaml:"display_name"`
	Description string           `yaml:"description"`
	BuiltIn     bool             `yaml:"built_in,omitempty"`
	Persona     PersonaConfig    `yaml:"persona"`
	Model       ModelConfig      `yaml:"model"`
	Tools       []string         `yaml:"tools"`
	Delegation  DelegationConfig `yaml:"delegation,omitempty"`
	Approval    ApprovalConfig   `yaml:"approval,omitempty"`
}

type PersonaConfig struct {
	Role         string `yaml:"role"`
	Tone         string `yaml:"tone,omitempty"`
	SystemPrompt string `yaml:"system_prompt"`
}

type ModelConfig struct {
	Preferred     string  `yaml:"preferred"`
	Fallback      string  `yaml:"fallback,omitempty"`
	Temperature   float64 `yaml:"temperature,omitempty"`
	ContextLength int     `yaml:"context_length,omitempty"`
}

type DelegationConfig struct {
	CanDelegateTo      []string `yaml:"can_delegate_to,omitempty"`
	CanBeDelegatedBy   []string `yaml:"can_be_delegated_by,omitempty"`
	MaxDelegationDepth int      `yaml:"max_delegation_depth,omitempty"`
}

type ApprovalConfig struct {
	RequireApprovalFor []string `yaml:"require_approval_for,omitempty"`
}

// LoadConfig reads and parses a YAML file into an AgentConfig.
func LoadConfig(path string) (*AgentConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", path, err)
	}

	var config AgentConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config %s: %w", path, err)
	}

	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config in %s: %w", path, err)
	}

	return &config, nil
}

// Validate checks the agent configuration for required fields.
func (c *AgentConfig) Validate() error {
	if c.Name == "" {
		return errors.New("name is required")
	}
	if c.Persona.Role == "" {
		return errors.New("persona.role is required")
	}
	if c.Persona.SystemPrompt == "" {
		return errors.New("persona.system_prompt is required")
	}
	if c.Model.Preferred == "" {
		return errors.New("model.preferred is required")
	}
	return nil
}
