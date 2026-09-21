package core

import (
	"fmt"
	"sync"

	"github.com/chengyaolee/blooper/internal/agent"
)

type Router struct {
	mu             sync.RWMutex
	agentRegistry  map[string]*agent.AgentConfig
	defaultAgent   string
}

func NewRouter(defaultAgent string) *Router {
	return &Router{
		agentRegistry: make(map[string]*agent.AgentConfig),
		defaultAgent:  defaultAgent,
	}
}

func (r *Router) Register(name string, config *agent.AgentConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agentRegistry[name] = config
}

func (r *Router) Route(agentName string) (*agent.AgentConfig, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if cfg, ok := r.agentRegistry[agentName]; ok {
		return cfg, nil
	}
	if cfg, ok := r.agentRegistry[r.defaultAgent]; ok {
		return cfg, nil
	}

	return nil, fmt.Errorf("agent %s and default agent %s not found", agentName, r.defaultAgent)
}
