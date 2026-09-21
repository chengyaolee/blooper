package tools

import (
	"sync"

	"github.com/chengyaolee/blooper/internal/llm"
)

// ToolRegistry manages the available tools.
type ToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry creates a new ToolRegistry.
func NewRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools: make(map[string]Tool),
	}
}

// Register adds a tool to the registry.
func (r *ToolRegistry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name()] = t
}

// Get retrieves a tool by name. Returns the tool and whether it was found.
func (r *ToolRegistry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List returns all registered tools.
func (r *ToolRegistry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	
	var list []Tool
	for _, t := range r.tools {
		list = append(list, t)
	}
	return list
}

// ToToolDefs returns the tools as LLM tool definitions.
func (r *ToolRegistry) ToToolDefs() []llm.ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	
	var defs []llm.ToolDef
	for _, t := range r.tools {
		defs = append(defs, llm.ToolDef{
			Type:     "function",
			Function: t.Schema(),
		})
	}
	return defs
}
