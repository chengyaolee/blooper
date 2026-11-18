// Package desktop hosts chat, the coordinator and an automatically enrolled
// local runner. Only the signed phone API is exposed to the LAN; all settings
// and credential operations require a per-launch capability on loopback.
package desktop

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	resources "github.com/chengyaolee/blooper"
	"github.com/chengyaolee/blooper/internal/agent"
	"github.com/chengyaolee/blooper/internal/core"
	"github.com/chengyaolee/blooper/internal/llm"
	"github.com/chengyaolee/blooper/internal/memory"
	"github.com/chengyaolee/blooper/internal/remote"
	"github.com/chengyaolee/blooper/internal/runner"
	"github.com/chengyaolee/blooper/internal/tools"
	"github.com/chengyaolee/blooper/internal/ui"
	"github.com/grandcat/zeroconf"
	qrcode "github.com/skip2/go-qrcode"
	"gopkg.in/yaml.v3"
)

type providerSettings struct {
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	OpenAIKey   string `json:"openaiKey"`
	DeepgramKey string `json:"deepgramKey"`
}

type Runtime struct {
	dir, token, pin, runnerURL string
	port                       int
	provider                   *llm.Provider
	coord                      *remote.Coordinator
	mu                         sync.Mutex
	deepgram                   bool
	runnerCancel               context.CancelFunc
	runnerDone                 chan struct{}
	runnerConfig               runner.Config
	client                     *http.Client
	ctx                        context.Context
	permissionsMu              sync.Mutex
	chat                       *ui.Server
}

func installResources(dir string) error {
	return fs.WalkDir(resources.Resources, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		target := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		data, err := resources.Resources.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0600)
	})
}

func Run(ctx context.Context, dir string) error {
	if dir == "" {
		return fmt.Errorf("desktop requires --data-dir")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := os.Chdir(dir); err != nil {
		return err
	}
	if err := os.MkdirAll("user_agents", 0700); err != nil {
		return err
	}
	if err := installResources("builtin"); err != nil {
		return err
	}
	token := os.Getenv("BLOOPER_DESKTOP_TOKEN")
	if len(token) < 32 {
		return fmt.Errorf("desktop must be launched by the Blooper desktop app")
	}
	cert, pin, err := localCertificate(dir)
	if err != nil {
		return err
	}
	listenPort := 0
	if data, readErr := os.ReadFile(filepath.Join(dir, "lan.port")); readErr == nil {
		listenPort, err = strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil || listenPort < 1 || listenPort > 65535 {
			return fmt.Errorf("Invalid saved local-network port")
		}
	}
	lan, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", listenPort))
	if err != nil {
		return err
	}
	defer lan.Close()
	admin, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer admin.Close()
	port := lan.Addr().(*net.TCPAddr).Port
	if err := os.WriteFile(filepath.Join(dir, "lan.port"), []byte(strconv.Itoa(port)), 0600); err != nil {
		return err
	}
	advertisement, advertiseErr := zeroconf.Register("Blooper-"+pin[:8], "_blooper._tcp", "local.", port, []string{"pin=" + pin}, nil)
	if advertiseErr != nil {
		log.Printf("Local discovery unavailable; QR pairing still works: %v", advertiseErr)
	} else {
		defer advertisement.Shutdown()
	}
	roots := x509.NewCertPool()
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return err
	}
	roots.AddCert(leaf)
	d := &Runtime{dir: dir, token: token, pin: pin, port: port, provider: &llm.Provider{}, ctx: ctx,
		runnerURL: fmt.Sprintf("https://127.0.0.1:%d", port), client: &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}}
	d.coord, err = remote.Open(remote.Config{DBPath: filepath.Join(dir, "remote.db"), Engine: d.provider, ProviderStatus: d.status})
	if err != nil {
		return err
	}
	defer d.coord.Close()
	chat, closeFleet, err := d.fleet()
	if err != nil {
		return err
	}
	defer closeFleet()
	d.chat = chat
	api := d.coord.Routes()
	// LAN clients cannot reach /admin or any desktop/chat route.
	lanMux := http.NewServeMux()
	lanMux.Handle("/v1/", api)
	lanServer := &http.Server{Handler: localNetworkOnly(lanMux), ReadHeaderTimeout: 10 * time.Second}
	defer lanServer.Close()
	control := http.NewServeMux()
	control.HandleFunc("GET /desktop/status", d.handleStatus)
	control.HandleFunc("POST /desktop/configure", d.handleConfigure)
	control.HandleFunc("POST /desktop/validate", d.handleValidate)
	control.HandleFunc("POST /desktop/pair", d.handlePair)
	control.HandleFunc("POST /desktop/revoke", d.handleRevoke)
	control.HandleFunc("GET /desktop/permissions", d.handlePermissions)
	control.HandleFunc("POST /desktop/permissions", d.handlePermissions)
	control.Handle("/", chat.Routes())
	adminServer := &http.Server{Handler: d.authorize(control), ReadHeaderTimeout: 10 * time.Second}
	defer adminServer.Close()
	errs := make(chan error, 2)
	go func() {
		errs <- lanServer.Serve(tls.NewListener(lan, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}))
	}()
	go func() { errs <- adminServer.Serve(admin) }()
	if err := d.loadRunner(); err != nil {
		return err
	}
	defer d.stopRunner()
	// Only the parent process sees this handshake. No API keys are in it.
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"ready": true, "url": "http://" + admin.Addr().String()}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
	case err := <-errs:
		if err != http.ErrServerClosed {
			return err
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = lanServer.Shutdown(shutdown)
	_ = adminServer.Shutdown(shutdown)
	return nil
}

func (d *Runtime) fleet() (*ui.Server, func(), error) {
	store, err := memory.Open(filepath.Join(d.dir, "memory.db"), memory.WithoutEmbeddings())
	if err != nil {
		return nil, nil, err
	}
	browser := tools.NewBrowserTool(true)
	closeFleet := func() { browser.Close(); store.Close() }
	agents := agent.NewAgentRegistry()
	reg := tools.NewRegistry()
	reg.Register(tools.NewWebSearchTool())
	reg.Register(browser)
	reg.Register(tools.NewFileWriteTool(filepath.Join(d.dir, "outputs")))
	reg.Register(tools.NewFileReadTool(filepath.Join(d.dir, "outputs"), filepath.Join(d.dir, "user_agents")))
	creator := agent.NewCreateAgentConfigTool(filepath.Join(d.dir, "user_agents"))
	creator.Registry = reg
	reg.Register(creator)
	reg.Register(tools.NewRecallTool(store))
	reg.Register(tools.NewDelegateTool("", core.NewDelegator(agents, d.provider, reg)))
	agents.Tools = reg
	dirs := []string{filepath.Join(d.dir, "builtin/agents"), filepath.Join(d.dir, "user_agents")}
	for _, dir := range dirs {
		if err := agents.LoadDir(dir); err != nil {
			closeFleet()
			return nil, nil, err
		}
	}
	if watcher, err := agent.NewWatcher(agents, dirs); err == nil {
		_ = watcher.Start(d.ctx)
	}
	chat := ui.NewServer(ui.ServerConfig{DefaultAgent: "web_researcher", StaticDir: filepath.Join(d.dir, "builtin/static"), TemplateDir: filepath.Join(d.dir, "builtin/internal/ui/templates")}, d.provider, agents, reg, core.NewApprovalQueue(), store)
	return chat, closeFleet, nil
}

func (d *Runtime) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		// Capability authentication also applies to HTML/chat: another local web page
		// cannot issue unauthenticated commands or obtain a new pairing QR.
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Blooper-Desktop")), []byte(d.token)) != 1 {
			http.Error(w, "Desktop authorization required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		respond(w, 400, map[string]string{"error": "Invalid settings"})
		return false
	}
	return true
}
func (d *Runtime) status() remote.ProviderStatus {
	name, model, connected := d.provider.Status()
	d.mu.Lock()
	deepgram := d.deepgram
	d.mu.Unlock()
	return remote.ProviderStatus{Provider: name, Model: model, Connected: connected, Deepgram: deepgram}
}
func (d *Runtime) handleStatus(w http.ResponseWriter, r *http.Request) {
	respond(w, 200, map[string]any{"os": runtime.GOOS, "providers": d.status(), "devices": d.coord.Devices(), "addresses": lanAddresses(d.port), "busy": d.coord.HasActiveJobs() || (d.chat != nil && d.chat.Busy())})
}
func (d *Runtime) handleConfigure(w http.ResponseWriter, r *http.Request) {
	var cfg providerSettings
	if !decode(w, r, &cfg) {
		return
	}
	var engine llm.InferenceEngine
	switch cfg.Provider {
	case "openai":
		if cfg.OpenAIKey != "" {
			engine = llm.NewOpenAIClient("", cfg.OpenAIKey)
		}
		if cfg.Model == "" {
			cfg.Model = llm.OpenAIModel
		}
	case "ollama":
		engine = llm.NewOllamaClient("")
		if cfg.Model == "" {
			cfg.Model = "qwen2.5:3b"
		}
	default:
		respond(w, 400, map[string]string{"error": "Choose OpenAI or Ollama"})
		return
	}
	d.provider.Set(cfg.Provider, cfg.Model, engine)
	d.coord.SetTranscriptionKey(cfg.DeepgramKey)
	d.mu.Lock()
	d.deepgram = cfg.DeepgramKey != ""
	d.mu.Unlock()
	respond(w, 200, d.status())
}
func (d *Runtime) handleValidate(w http.ResponseWriter, r *http.Request) {
	var cfg providerSettings
	if !decode(w, r, &cfg) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var err error
	switch cfg.Provider {
	case "openai":
		if cfg.OpenAIKey == "" {
			err = fmt.Errorf("Enter an OpenAI API key")
		} else {
			if cfg.Model == "" {
				cfg.Model = llm.OpenAIModel
			}
			_, err = llm.NewOpenAIClient("", cfg.OpenAIKey).Generate(ctx, &llm.ChatRequest{Model: cfg.Model, MaxTokens: 256, Messages: []llm.ChatMessage{{Role: "user", Content: "Reply OK."}}})
		}
	case "ollama":
		var models []string
		models, err = llm.NewOllamaClient("").ListModels(ctx)
		if err == nil {
			_, err = llm.SelectModel(models, cfg.Model, "")
		}
	case "deepgram":
		err = remote.ValidateDeepgramKey(ctx, cfg.DeepgramKey)
	default:
		err = fmt.Errorf("Unknown provider")
	}
	if err != nil {
		respond(w, 400, map[string]string{"error": err.Error()})
		return
	}
	respond(w, 200, map[string]bool{"valid": true})
}
func (d *Runtime) handlePair(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Address string `json:"address"`
	}
	if !decode(w, r, &req) {
		return
	}
	addresses := lanAddresses(d.port)
	found := false
	for _, address := range addresses {
		if req.Address == address {
			found = true
		}
	}
	if !found {
		respond(w, 400, map[string]string{"error": "Choose an available local network"})
		return
	}
	code := d.coord.NewPairCode()
	qr := "blooper://pair?" + url.Values{"url": {req.Address}, "code": {code}, "pin": {d.pin}, "name": {computerName()}}.Encode()
	png, err := qrcode.Encode(qr, qrcode.Medium, 320)
	if err != nil {
		respond(w, 500, map[string]string{"error": "Could not create pairing QR"})
		return
	}
	respond(w, 200, map[string]any{"qr": png, "payload": qr, "expires_in": 600})
}
func (d *Runtime) handleRevoke(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := d.coord.Revoke(req.ID); err != nil {
		respond(w, 400, map[string]string{"error": err.Error()})
		return
	}
	respond(w, 200, map[string]bool{"revoked": true})
}

func (d *Runtime) loadRunner() error {
	d.runnerConfig = runner.Config{Coordinator: d.runnerURL, WebSearch: true, Workspaces: map[string]runner.Workspace{}, Apps: map[string][]string{}}
	path := filepath.Join(d.dir, "permissions.yaml")
	if data, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(data, &d.runnerConfig); err != nil {
			return fmt.Errorf("desktop permissions: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return d.startRunner()
}
func (d *Runtime) stopRunner() {
	if d.runnerCancel != nil {
		d.runnerCancel()
		<-d.runnerDone
		d.runnerCancel = nil
	}
}
func (d *Runtime) startRunner() error {
	d.runnerConfig.Coordinator = d.runnerURL
	d.runnerConfig.KeyFile = filepath.Join(d.dir, "runner.key")
	data, err := yaml.Marshal(d.runnerConfig)
	if err != nil {
		return err
	}
	path := filepath.Join(d.dir, "permissions.yaml")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	local, err := runner.Load(path)
	if err != nil {
		return err
	}
	local.SetHTTPClient(d.client)
	if err := d.coord.RegisterLocalRunner(local.Capabilities()); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(d.ctx)
	d.runnerCancel = cancel
	d.runnerDone = make(chan struct{})
	go func() {
		defer close(d.runnerDone)
		if err := local.Run(ctx); err != nil && ctx.Err() == nil {
			log.Printf("local runner stopped: %v", err)
		}
	}()
	return nil
}
func (d *Runtime) handlePermissions(w http.ResponseWriter, r *http.Request) {
	d.permissionsMu.Lock()
	defer d.permissionsMu.Unlock()
	if r.Method == http.MethodGet {
		respond(w, 200, map[string]any{"workspaces": d.runnerConfig.Workspaces, "apps": d.runnerConfig.Apps})
		return
	}
	if d.coord.HasActiveJobs() {
		respond(w, 409, map[string]string{"error": "Finish or cancel active jobs before changing permissions"})
		return
	}
	var cfg struct {
		Workspaces map[string]runner.Workspace `json:"workspaces"`
		Apps       map[string][]string         `json:"apps"`
	}
	if !decode(w, r, &cfg) {
		return
	}
	for name, workspace := range cfg.Workspaces {
		if strings.TrimSpace(name) == "" || !filepath.IsAbs(workspace.Path) || len(workspace.TestCmd) == 0 {
			respond(w, 400, map[string]string{"error": "Each workspace needs a name, selected folder, and test command"})
			return
		}
		stat, err := os.Stat(workspace.Path)
		if err != nil || !stat.IsDir() {
			respond(w, 400, map[string]string{"error": "Workspace folder does not exist"})
			return
		}
	}
	for name, argv := range cfg.Apps {
		if name == "" || len(argv) == 0 {
			respond(w, 400, map[string]string{"error": "Each allowed app needs a name and executable"})
			return
		}
	}
	old := d.runnerConfig
	d.stopRunner()
	d.runnerConfig.Workspaces, d.runnerConfig.Apps = cfg.Workspaces, cfg.Apps
	if err := d.startRunner(); err != nil {
		d.runnerConfig = old
		_ = d.startRunner()
		respond(w, 400, map[string]string{"error": err.Error()})
		return
	}
	respond(w, 200, map[string]bool{"saved": true})
}

func computerName() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "My computer"
	}
	return name
}

func localNetworkOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		ip := net.ParseIP(host)
		if ip == nil || (!ip.IsPrivate() && !ip.IsLoopback()) {
			http.Error(w, "Connect on the same local network", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
