package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/chengyaolee/blooper/internal/agent"
	"github.com/chengyaolee/blooper/internal/core"
	"github.com/chengyaolee/blooper/internal/llm"
	"github.com/chengyaolee/blooper/internal/memory"
	"github.com/chengyaolee/blooper/internal/tools"
)

// ServerConfig holds the configuration for the UI server.
type ServerConfig struct {
	Host            string
	Port            int
	HardwareProfile string
	DefaultAgent    string
	DefaultModel    string
	MaxConcurrent   int
	StaticDir       string
	TemplateDir     string
}

// Server handles UI requests and SSE streaming.
type Server struct {
	config        ServerConfig
	engine        llm.InferenceEngine
	agentRegistry *agent.AgentRegistry
	toolRegistry  *tools.ToolRegistry
	approvalQueue *core.ApprovalQueue
	memoryStore   *memory.Store
	router        *core.Router
	scheduler     *core.Scheduler
	summarizer    *memory.Summarizer
	httpServer    *http.Server
}

// NewServer creates a new UI server instance.
func NewServer(
	cfg ServerConfig,
	engine llm.InferenceEngine,
	agentRegistry *agent.AgentRegistry,
	toolRegistry *tools.ToolRegistry,
	approvalQueue *core.ApprovalQueue,
	memoryStore *memory.Store,
) *Server {
	if cfg.StaticDir == "" {
		cfg.StaticDir = "static"
	}
	if cfg.TemplateDir == "" {
		cfg.TemplateDir = "internal/ui/templates"
	}
	if cfg.DefaultAgent == "" {
		cfg.DefaultAgent = "agent_creator"
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 2
	}

	router := core.NewRouter(cfg.DefaultAgent)
	for _, a := range agentRegistry.List() {
		router.Register(a.Name, a)
	}

	scheduler := core.NewScheduler(cfg.MaxConcurrent)
	summarizer := memory.NewSummarizer(cfg.DefaultModel)

	return &Server{
		config:        cfg,
		engine:        engine,
		agentRegistry: agentRegistry,
		toolRegistry:  toolRegistry,
		approvalQueue: approvalQueue,
		memoryStore:   memoryStore,
		router:        router,
		scheduler:     scheduler,
		summarizer:    summarizer,
	}
}

// Routes initializes the HTTP mux.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// Static files
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(s.config.StaticDir))))

	// Web pages
	mux.HandleFunc("/", s.handleIndex)

	// API endpoints
	mux.HandleFunc("/api/chat", s.handleChat)
	mux.HandleFunc("/api/agents", s.handleAgents)
	mux.HandleFunc("/api/approvals", s.handleApprovals)
	mux.HandleFunc("/api/approvals/", s.handleApprovalAction)
	mux.HandleFunc("/api/health", s.handleHealth)

	return mux
}

// Start runs the HTTP server.
func (s *Server) Start() error {
	addr := fmt.Sprintf("%s:%d", s.config.Host, s.config.Port)
	s.httpServer = &http.Server{
		Addr:    addr,
		Handler: s.Routes(),
	}
	fmt.Printf("🚀 blooper UI server running at http://%s\n", addr)
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer != nil {
		return s.httpServer.Shutdown(ctx)
	}
	return nil
}

type indexViewData struct {
	Agents                 []*agent.AgentConfig
	DefaultAgent           string
	ActiveAgentDisplayName string
	HardwareProfile        string
	ActiveModel            string
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	tmplPath := filepath.Join(s.config.TemplateDir, "index.html")
	tmplBytes, err := os.ReadFile(tmplPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to load template: %v", err), http.StatusInternalServerError)
		return
	}

	tmpl, err := template.New("index").Parse(string(tmplBytes))
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to parse template: %v", err), http.StatusInternalServerError)
		return
	}

	agents := s.agentRegistry.List()
	activeDisplayName := "Agent Creator"
	activeModel := s.config.DefaultModel
	if activeModel == "" {
		activeModel = "qwen2.5:3b"
	}

	for _, a := range agents {
		if a.Name == s.config.DefaultAgent {
			activeDisplayName = a.DisplayName
			if a.Model.Preferred != "" {
				activeModel = a.Model.Preferred
			}
			break
		}
	}

	if provider, ok := s.engine.(*llm.Provider); ok {
		_, activeModel, _ = provider.Status()
	}
	data := indexViewData{
		Agents:                 agents,
		DefaultAgent:           s.config.DefaultAgent,
		ActiveAgentDisplayName: activeDisplayName,
		HardwareProfile:        s.config.HardwareProfile,
		ActiveModel:            activeModel,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = tmpl.Execute(w, data)
}

type chatRequestPayload struct {
	Agent     string `json:"agent"`
	Message   string `json:"message"`
	SessionID string `json:"session_id"`
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload chatRequestPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	agentName := payload.Agent
	if agentName == "" {
		agentName = s.config.DefaultAgent
	}

	cfg, ok := s.agentRegistry.Get(agentName)
	if !ok {
		var err error
		cfg, err = s.router.Route(agentName)
		if err != nil {
			http.Error(w, fmt.Sprintf("Agent %q not found", agentName), http.StatusNotFound)
			return
		}
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	agentInstance := agent.NewAgent(cfg, s.engine, s.toolRegistry)
	agentInstance.SetApprovalHandler(s.approvalQueue)

	chunkChan := make(chan llm.StreamChunk, 20)
	ctx := r.Context()

	// Run agent via scheduler to respect concurrency boundaries
	err := s.scheduler.Submit(ctx, func(taskCtx context.Context) {
		_ = agentInstance.RunStream(taskCtx, payload.Message, chunkChan)
	})
	if err != nil {
		data, _ := json.Marshal(map[string]string{"error": fmt.Sprintf("Server at capacity: %v", err)})
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
		return
	}

	var fullResponse strings.Builder

	for chunk := range chunkChan {
		if chunk.Error != nil {
			data, _ := json.Marshal(map[string]string{"error": chunk.Error.Error()})
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
			break
		}

		if chunk.Content != "" {
			fullResponse.WriteString(chunk.Content)
			data, _ := json.Marshal(map[string]string{"content": chunk.Content})
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}

		// The agent has already queued the action; show that item. Queuing it here too
		// would create a second approval that runs the action twice if both are approved.
		if item := s.approvalQueue.Get(chunk.ApprovalID); chunk.ApprovalID != "" && item != nil {
			appData, _ := json.Marshal(map[string]interface{}{
				"approval": map[string]string{
					"id":        item.ID,
					"tool_name": item.ToolName,
					"arguments": item.Arguments,
				},
			})
			fmt.Fprintf(w, "data: %s\n\n", appData)
			flusher.Flush()
		}
	}

	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()

	// Async conversation memory saving
	if s.memoryStore != nil && fullResponse.Len() > 0 {
		go func(q, a string) {
			summary := a
			if len(summary) > 200 {
				summary = summary[:200] + "..."
			}
			_ = s.memoryStore.SaveConversation(agentName, q, summary, "completed", "")
		}(payload.Message, fullResponse.String())
	}
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	agents := s.agentRegistry.List()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(agents)
}

func (s *Server) handleApprovals(w http.ResponseWriter, r *http.Request) {
	pending := s.approvalQueue.ListPending()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(pending)
}

func (s *Server) handleApprovalAction(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/approvals/"), "/")
	if len(parts) < 2 {
		http.Error(w, "Invalid approval path", http.StatusBadRequest)
		return
	}

	id := parts[0]
	action := parts[1]

	item := s.approvalQueue.Get(id)
	if item == nil {
		http.Error(w, "Approval item not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var err error
	switch action {
	case "approve":
		err = s.approvalQueue.Approve(id)
	case "reject":
		err = s.approvalQueue.Reject(id)
	default:
		http.Error(w, "Unknown action", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"status": s.approvalQueue.Get(id).Status})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	stats, _ := s.engine.KVStats()
	models, _ := s.engine.ListModels(r.Context())
	activeTasks, totalSlots := s.scheduler.Stats()

	status := map[string]interface{}{
		"status":           "ok",
		"hardware_profile": s.config.HardwareProfile,
		"default_agent":    s.config.DefaultAgent,
		"available_agents": len(s.agentRegistry.List()),
		"available_models": models,
		"kv_metrics":       stats,
		"scheduler": map[string]int{
			"active_tasks": activeTasks,
			"total_slots":  totalSlots,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(status)
}

// Busy reports active chat work so application updates do not interrupt it.
func (s *Server) Busy() bool { active, _ := s.scheduler.Stats(); return active > 0 }
