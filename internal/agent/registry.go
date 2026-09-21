package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// AgentRegistry manages agent configurations loaded from files.
type AgentRegistry struct {
	mu     sync.RWMutex
	agents map[string]*AgentConfig
}

// NewAgentRegistry creates a new, empty AgentRegistry.
func NewAgentRegistry() *AgentRegistry {
	return &AgentRegistry{
		agents: make(map[string]*AgentConfig),
	}
}

// Register adds an agent configuration to the registry.
func (r *AgentRegistry) Register(cfg *AgentConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[cfg.Name] = cfg
}

// Get retrieves an agent configuration by name.
func (r *AgentRegistry) Get(name string) (*AgentConfig, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cfg, ok := r.agents[name]
	return cfg, ok
}

// List returns a list of all registered agent configurations.
func (r *AgentRegistry) List() []*AgentConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var list []*AgentConfig
	for _, cfg := range r.agents {
		list = append(list, cfg)
	}
	return list
}

// LoadDir reads all .yaml files in the specified directory and registers them.
func (r *AgentRegistry) LoadDir(dirPath string) error {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Ignore non-existent directories
		}
		return fmt.Errorf("failed to read directory %s: %w", dirPath, err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}

		path := filepath.Join(dirPath, entry.Name())
		cfg, err := LoadConfig(path)
		if err != nil {
			return fmt.Errorf("failed to load agent config %s: %w", path, err)
		}

		r.Register(cfg)
	}

	return nil
}
