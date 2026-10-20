package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/chengyaolee/blooper/internal/agent"
	"github.com/chengyaolee/blooper/internal/core"
	"github.com/chengyaolee/blooper/internal/llm"
	"github.com/chengyaolee/blooper/internal/memory"
	"github.com/chengyaolee/blooper/internal/tools"
	"github.com/chengyaolee/blooper/internal/trace"
	"github.com/chengyaolee/blooper/internal/ui"
	"gopkg.in/yaml.v3"
)

type ConfigFile struct {
	Server struct {
		Host string `yaml:"host"`
		Port int    `yaml:"port"`
	} `yaml:"server"`
	Ollama struct {
		BaseURL string `yaml:"base_url"`
	} `yaml:"ollama"`
	HardwareProfile string   `yaml:"hardware_profile"`
	AgentDirs       []string `yaml:"agent_dirs"`
	DefaultAgent    string   `yaml:"default_agent"`
	Memory          struct {
		DBPath string `yaml:"db_path"`
	} `yaml:"memory"`
}

func main() {
	portFlag := flag.Int("port", 0, "Server port (overrides config)")
	hostFlag := flag.String("host", "", "Server host (overrides config)")
	ollamaFlag := flag.String("ollama", "", "Ollama base URL (overrides config)")
	profileFlag := flag.String("profile", "", "Hardware profile: m5_pro_24gb or linux_1080ti")
	configFlag := flag.String("config", "config/blooper.yaml", "Path to config file")
	exportFlag := flag.String("export", "", "Export all recorded trajectories as JSONL to this file and exit")
	flag.Parse()

	// 1. Load config file
	var cfg ConfigFile
	if data, err := os.ReadFile(*configFlag); err == nil {
		_ = yaml.Unmarshal(data, &cfg)
	}

	// Apply defaults and CLI flag overrides
	host := "127.0.0.1"
	if cfg.Server.Host != "" {
		host = cfg.Server.Host
	}
	if *hostFlag != "" {
		host = *hostFlag
	}

	port := 8080
	if cfg.Server.Port != 0 {
		port = cfg.Server.Port
	}
	if *portFlag != 0 {
		port = *portFlag
	}

	ollamaURL := "http://localhost:11434"
	if cfg.Ollama.BaseURL != "" {
		ollamaURL = cfg.Ollama.BaseURL
	}
	if *ollamaFlag != "" {
		ollamaURL = *ollamaFlag
	}

	profileName := "m5_pro_24gb"
	if cfg.HardwareProfile != "" {
		profileName = cfg.HardwareProfile
	}
	if *profileFlag != "" {
		profileName = *profileFlag
	}

	defaultAgent := "agent_creator"
	if cfg.DefaultAgent != "" {
		defaultAgent = cfg.DefaultAgent
	}

	dbPath := "data/blooper.db"
	if cfg.Memory.DBPath != "" {
		dbPath = cfg.Memory.DBPath
	}

	agentDirs := cfg.AgentDirs
	if len(agentDirs) == 0 {
		agentDirs = []string{"agents", "user_agents"}
	}

	// 2. Hardware profile lookup
	profile, exists := llm.HardwareProfiles[profileName]
	if !exists {
		profile = llm.HardwareProfiles["m5_pro_24gb"]
	}

	// 3. Print Banner
	fmt.Println("╔══════════════════════════════════════════════════════════════╗")
	fmt.Println("║               🤖 BLOOPER AGENT FLEET (GO)                   ║")
	fmt.Println("╚══════════════════════════════════════════════════════════════╝")
	fmt.Printf(" Target Profile:    %s (%s)\n", profile.Name, profileName)
	fmt.Printf(" Default Model:     %s (Fallback: %s)\n", profile.DefaultModel, profile.FallbackModel)
	fmt.Printf(" Max Context Len:   %d tokens\n", profile.MaxContextLen)
	fmt.Printf(" Max Parallel:      %d task slot(s)\n", profile.MaxParallelReqs)
	fmt.Printf(" Inference Server:  %s\n", ollamaURL)
	fmt.Printf(" Memory Overhead:   ~10 MB RAM (Persistent memory: %s)\n", dbPath)
	fmt.Println("──────────────────────────────────────────────────────────────")

	// 4. Initialize Core Subsystems
	var engine llm.InferenceEngine = llm.NewOllamaClient(ollamaURL)

	memStore, err := memory.Open(dbPath)
	if err != nil {
		fmt.Printf("⚠️  Warning: could not open memory store (%v). Memory persistence disabled.\n", err)
	} else {
		defer memStore.Close()
		// Record every agent run: LLM calls, tool calls, task-graph decisions, human approvals.
		engine = trace.Engine(engine)
	}

	if *exportFlag != "" {
		if memStore == nil {
			fmt.Println("❌ Export needs the memory store.")
			os.Exit(1)
		}
		f, err := os.Create(*exportFlag)
		if err != nil {
			fmt.Printf("❌ Export failed: %v\n", err)
			os.Exit(1)
		}
		n, err := memStore.ExportJSONL(f)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			fmt.Printf("❌ Export failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("📦 Exported %d episode(s) to %s\n", n, *exportFlag)
		return
	}

	approvalQueue := core.NewApprovalQueue()

	agentRegistry := agent.NewAgentRegistry()
	for _, dir := range agentDirs {
		if err := agentRegistry.LoadDir(dir); err != nil {
			fmt.Printf("⚠️  Warning loading agents from %s: %v\n", dir, err)
		}
	}

	// Hot-reload watcher for agent configs
	watcherCtx, cancelWatcher := context.WithCancel(context.Background())
	defer cancelWatcher()

	watcher, err := agent.NewWatcher(agentRegistry, agentDirs)
	if err == nil {
		_ = watcher.Start(watcherCtx)
		fmt.Printf("🔄 Hot-reload enabled for directories: %v\n", agentDirs)
	}

	// Tool Registry initialization
	toolRegistry := tools.NewRegistry()
	toolRegistry.Register(agent.NewCreateAgentConfigTool("user_agents"))
	toolRegistry.Register(tools.NewWebSearchTool())
	toolRegistry.Register(tools.NewFileWriteTool("data/outputs"))
	toolRegistry.Register(tools.NewFileReadTool("data/outputs", "agents", "user_agents"))

	if memStore != nil {
		toolRegistry.Register(tools.NewRecallTool(memStore))
	}

	// Browser tool with resource cleanup
	browserTool := tools.NewBrowserTool(true)
	defer browserTool.Close()
	toolRegistry.Register(browserTool)

	// Delegation engine
	delegator := core.NewDelegator(agentRegistry, engine, toolRegistry, memStore)
	toolRegistry.Register(tools.NewDelegateTool("system", delegator))

	// Multi-agent goals: controller splits, workers self-claim via the shared task graph
	if memStore != nil {
		swarm := core.NewSwarm(memStore, agentRegistry, engine, toolRegistry, approvalQueue, defaultAgent, profile.MaxParallelReqs)
		toolRegistry.Register(tools.NewRunGoalTool(swarm))
		trace.WrapRegistry(toolRegistry)
	}

	loadedAgents := agentRegistry.List()
	fmt.Printf(" Loaded %d Agent(s):\n", len(loadedAgents))
	for _, a := range loadedAgents {
		tag := "user"
		if a.BuiltIn {
			tag = "built-in"
		}
		fmt.Printf("   • %-18s [%s] - %s\n", a.Name, tag, a.DisplayName)
	}
	fmt.Println("──────────────────────────────────────────────────────────────")

	// 5. Start UI Server
	serverCfg := ui.ServerConfig{
		Host:            host,
		Port:            port,
		HardwareProfile: profile.Name,
		DefaultAgent:    defaultAgent,
		DefaultModel:    profile.DefaultModel,
		MaxConcurrent:   profile.MaxParallelReqs,
		StaticDir:       "static",
		TemplateDir:     "internal/ui/templates",
	}

	srv := ui.NewServer(serverCfg, engine, agentRegistry, toolRegistry, approvalQueue, memStore)

	// Graceful shutdown channel
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		if err := srv.Start(); err != nil && err != context.Canceled {
			fmt.Printf("Server stopped: %v\n", err)
		}
	}()

	<-stop
	fmt.Println("\n🛑 Shutting down Blooper fleet...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	fmt.Println("👋 Goodbye!")
}
