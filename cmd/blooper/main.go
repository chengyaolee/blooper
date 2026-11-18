// Command blooper runs the agent fleet.
//
//	blooper [serve]        chat UI + remote coordinator
//	blooper runner         execute jobs on this computer for a coordinator
//	blooper pair           print a QR code to pair a phone
//	blooper devices        list enrolled devices
//	blooper revoke <id>    revoke a phone or runner
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/chengyaolee/blooper/internal/agent"
	"github.com/chengyaolee/blooper/internal/core"
	"github.com/chengyaolee/blooper/internal/llm"
	"github.com/chengyaolee/blooper/internal/memory"
	"github.com/chengyaolee/blooper/internal/remote"
	"github.com/chengyaolee/blooper/internal/runner"
	"github.com/chengyaolee/blooper/internal/tools"
	"github.com/chengyaolee/blooper/internal/ui"
	"gopkg.in/yaml.v3"
)

type config struct {
	Server struct {
		Host    string `yaml:"host"`
		Port    int    `yaml:"port"`
		TLSCert string `yaml:"tls_cert"`
		TLSKey  string `yaml:"tls_key"`
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
	Push struct {
		Topic       string `yaml:"topic"`
		Environment string `yaml:"environment"`
	} `yaml:"push"`
	Remote struct {
		PublicURL     string `yaml:"public_url"` // how phones and runners reach this server
		DBPath        string `yaml:"db_path"`
		Planner       string `yaml:"planner"` // auto (default), openai, or ollama
		OpenAIBaseURL string `yaml:"openai_base_url"`
		OpenAIModel   string `yaml:"openai_model"`
		OllamaModel   string `yaml:"ollama_model"`
		DeepgramModel string `yaml:"deepgram_model"`
		JobTimeout    string `yaml:"job_timeout"`
	} `yaml:"remote"`
}

func loadConfig(path string) (*config, error) {
	cfg := &config{}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}
	if cfg.Remote.DBPath == "" {
		cfg.Remote.DBPath = "data/remote.db"
	}
	if cfg.Remote.PublicURL == "" {
		host, _ := os.Hostname()
		scheme := "http"
		if cfg.Server.TLSCert != "" {
			scheme = "https"
		}
		cfg.Remote.PublicURL = fmt.Sprintf("%s://%s:%d", scheme, host, cfg.Server.Port)
	}
	cfg.Remote.PublicURL = strings.TrimRight(cfg.Remote.PublicURL, "/")
	return cfg, nil
}

func main() {
	log.SetFlags(log.Ltime)
	cmd, args := "serve", os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	cfgDefault := "config/blooper.yaml"
	if cmd == "runner" {
		cfgDefault = "runner.yaml"
	}
	cfgPath := fs.String("config", cfgDefault, "config file")
	fs.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var err error
	switch cmd {
	case "serve":
		err = serve(ctx, *cfgPath)
	case "runner":
		var r *runner.Runner
		if r, err = runner.Load(*cfgPath); err == nil {
			err = r.Run(ctx)
		}
	case "pair", "devices", "revoke":
		err = admin(cmd, *cfgPath, fs.Arg(0))
	default:
		err = fmt.Errorf("unknown command %q (serve, runner, pair, devices, revoke)", cmd)
	}
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func serve(ctx context.Context, cfgPath string) error {
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return err
	}

	// Hosted OpenAI works across chat, delegation and remote planning. Ollama is optional.
	ollama := llm.NewOllamaClient(cfg.Ollama.BaseURL)
	var fleetEngine llm.InferenceEngine = ollama
	hostedKey := os.Getenv("OPENAI_API_KEY")
	if cfg.Remote.Planner != "ollama" && hostedKey != "" {
		provider := &llm.Provider{}
		provider.Set("openai", cmp(cfg.Remote.OpenAIModel, llm.OpenAIModel), llm.NewOpenAIClient(cfg.Remote.OpenAIBaseURL, hostedKey))
		fleetEngine = provider
	}
	agents := agent.NewAgentRegistry()
	store, err := memory.Open(cfg.Memory.DBPath, memory.WithoutEmbeddings())
	if err != nil {
		log.Printf("memory disabled: %v", err)
		store = nil
	}
	browser := tools.NewBrowserTool(true)
	defer browser.Close()
	toolReg := tools.NewRegistry()
	toolReg.Register(tools.NewWebSearchTool())
	toolReg.Register(tools.NewFileWriteTool(""))
	toolReg.Register(tools.NewFileReadTool())
	toolReg.Register(browser)
	creator := agent.NewCreateAgentConfigTool("user_agents")
	creator.Registry = toolReg
	toolReg.Register(creator)
	toolReg.Register(tools.NewDelegateTool("", core.NewDelegator(agents, fleetEngine, toolReg)))
	if store != nil {
		toolReg.Register(tools.NewRecallTool(store))
	}
	agents.Tools = toolReg
	for _, dir := range cfg.AgentDirs {
		if err := agents.LoadDir(dir); err != nil {
			return err
		}
	}
	var available []string
	if fleetEngine == ollama {
		checkCtx, cancelCheck := context.WithTimeout(ctx, 10*time.Second)
		available, err = ollama.ListModels(checkCtx)
		cancelCheck()
		if err != nil {
			return fmt.Errorf("Choose OpenAI with OPENAI_API_KEY, or start Ollama at %s: %w", ollama.BaseURL, err)
		}
		for _, a := range agents.List() {
			model, err := llm.SelectModel(available, a.Model.Preferred, a.Model.Fallback)
			if err != nil {
				return fmt.Errorf("agent %s: %w", a.Name, err)
			}
			a.Model.Preferred = model
		}
	}
	if w, err := agent.NewWatcher(agents, cfg.AgentDirs); err == nil {
		w.Start(ctx)
	}
	chat := ui.NewServer(ui.ServerConfig{
		Host: cfg.Server.Host, Port: cfg.Server.Port,
		HardwareProfile: cfg.HardwareProfile, DefaultAgent: cfg.DefaultAgent,
	}, fleetEngine, agents, toolReg, core.NewApprovalQueue(), store)

	// The remote coordinator. Its planner may use a hosted model.
	rc := remote.Config{
		DBPath: cfg.Remote.DBPath, Engine: ollama, Model: cmp(cfg.Remote.OllamaModel, "qwen2.5:3b"),
		DeepgramKey: os.Getenv("DEEPGRAM_API_KEY"), DeepgramModel: cfg.Remote.DeepgramModel,
	}
	keyPath, keyID, teamID := os.Getenv("APNS_KEY_PATH"), os.Getenv("APNS_KEY_ID"), os.Getenv("APNS_TEAM_ID")
	if keyPath != "" || keyID != "" || teamID != "" {
		if cfg.Push.Environment != "sandbox" && cfg.Push.Environment != "production" {
			return errors.New("push.environment must be sandbox or production")
		}
		rc.APNS, err = remote.NewAPNSClient(keyPath, keyID, teamID, cfg.Push.Topic, cfg.Push.Environment == "sandbox")
		if err != nil {
			return err
		}
	}
	if cfg.Remote.JobTimeout != "" {
		if rc.JobTimeout, err = time.ParseDuration(cfg.Remote.JobTimeout); err != nil {
			return fmt.Errorf("remote.job_timeout: %w", err)
		}
	}
	if cfg.Remote.Planner != "" && cfg.Remote.Planner != "auto" && cfg.Remote.Planner != "openai" && cfg.Remote.Planner != "ollama" {
		return fmt.Errorf("remote.planner must be auto, openai, or ollama")
	}
	key := os.Getenv("OPENAI_API_KEY")
	switch {
	case cfg.Remote.Planner == "openai" && key == "":
		return errors.New("remote.planner is openai but OPENAI_API_KEY is not set")
	case cfg.Remote.Planner != "ollama" && key != "":
		hosted := llm.NewOpenAIClient(cfg.Remote.OpenAIBaseURL, key)
		rc.Engine, rc.Model = hosted, cmp(cfg.Remote.OpenAIModel, llm.OpenAIModel)
		log.Printf("remote planner: OpenAI %s. Transcripts and tool output are sent to %s", rc.Model, hosted.BaseURL)
	default:
		if _, err := llm.SelectModel(available, rc.Model, ""); err != nil {
			return fmt.Errorf("remote planner: %w", err)
		}
		log.Printf("remote planner: Ollama %s (local; set OPENAI_API_KEY to use OpenAI)", rc.Model)
	}
	if rc.DeepgramKey == "" {
		log.Printf("transcription: on-device only (set DEEPGRAM_API_KEY to offer Deepgram)")
	}
	rc.ProviderStatus = func() remote.ProviderStatus {
		name := "ollama"
		if fleetEngine != ollama {
			name = "openai"
		}
		return remote.ProviderStatus{Provider: name, Model: rc.Model, Connected: true, Deepgram: rc.DeepgramKey != ""}
	}
	coord, err := remote.Open(rc)
	if err != nil {
		return fmt.Errorf("remote coordinator: %w", err)
	}
	defer coord.Close()

	mux := http.NewServeMux()
	api := coord.Routes()
	mux.Handle("/v1/", api)
	mux.Handle("/admin/", api)
	mux.Handle("/", loopbackOnly(chat.Routes()))

	if !coord.HasPhone() {
		printPairing(cfg.Remote.PublicURL, coord.NewPairCode())
	}
	srv := &http.Server{Addr: fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port), Handler: mux}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	log.Printf("blooper listening on %s (phones and runners use %s)", srv.Addr, cfg.Remote.PublicURL)
	if cfg.Server.TLSCert != "" {
		return srv.ListenAndServeTLS(cfg.Server.TLSCert, cfg.Server.TLSKey)
	}
	log.Printf("warning: serving plain HTTP. Requests are signed but not encrypted; use tls_cert/tls_key or a tailnet.")
	return srv.ListenAndServe()
}

func cmp(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// loopbackOnly keeps the chat UI and its /api routes, which have no
// authentication, off the network now that the server is reachable by phones.
// If a reverse proxy fronts this server, forward only /v1/ through it.
func loopbackOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			http.Error(w, "The chat UI is only available from the machine running blooper.", http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func printPairing(publicURL, code string) {
	fmt.Printf("\nPair your phone: open Blooper Remote and scan this code.\n\n")
	remote.PrintQR(os.Stdout, "blooper://pair?url="+url.QueryEscape(publicURL)+"&code="+code)
	fmt.Printf("\nOr enter the server %s and the code %s (valid for 10 minutes, single use).\n\n", publicURL, code)
}

// admin talks to the running server's /admin API. The token file it reads is
// written by the server and readable only by the same OS user.
func admin(cmd, cfgPath, arg string) error {
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return err
	}
	token, err := os.ReadFile(remote.AdminTokenPath(cfg.Remote.DBPath))
	if err != nil {
		return fmt.Errorf("is the server running? %w", err)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", cfg.Server.Port)
	if cfg.Server.TLSCert != "" {
		base = cfg.Remote.PublicURL
	}
	method, path := http.MethodPost, "/admin/pair"
	switch cmd {
	case "devices":
		method, path = http.MethodGet, "/admin/devices"
	case "revoke":
		if arg == "" {
			return errors.New("usage: blooper revoke <device-id> (see `blooper devices`)")
		}
		path = "/admin/revoke/" + url.PathEscape(arg)
	}
	req, _ := http.NewRequest(method, base+path, nil)
	req.Header.Set("Authorization", "Bearer "+string(token))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", resp.Status, body)
	}
	if cmd == "pair" {
		var ch remote.EnrollChallenge
		json.Unmarshal(body, &ch)
		printPairing(cfg.Remote.PublicURL, ch.Code)
		return nil
	}
	var devices []remote.Device
	json.Unmarshal(body, &devices)
	for _, d := range devices {
		status := "offline"
		switch {
		case d.Revoked:
			status = "REVOKED"
		case d.Kind == remote.KindPhone:
			status = ""
		case d.Reachable:
			status = "online"
		}
		fmt.Printf("%s  %-8s %-24s %s\n", d.ID, d.Kind, d.Name, status)
	}
	return nil
}
