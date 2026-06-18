package workercontrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cmesh/cmesh/internal/cluster"
	"github.com/cmesh/cmesh/internal/resources"
	"github.com/cmesh/cmesh/internal/runtimes"
	"github.com/cmesh/cmesh/internal/workerstatus"
)

var inferredRPCAdvertiseHosts sync.Map

type Config struct {
	ManagerURL       string `json:"manager_url"`
	JoinToken        string `json:"join_token"`
	NodeName         string `json:"node_name"`
	CPU              int    `json:"cpu"`
	MemoryGB         int    `json:"memory_gb"`
	DiskGB           int    `json:"disk_gb"`
	GPUEnabled       bool   `json:"gpu_enabled"`
	VRAMGB           int    `json:"vram_gb"`
	JobSlots         int    `json:"job_slots"`
	Benchmark        bool   `json:"benchmark"`
	RPCHost          string `json:"rpc_host"`
	RPCAdvertiseHost string `json:"rpc_advertise_host"`
	RPCPort          int    `json:"rpc_port"`
	RPCCache         bool   `json:"rpc_cache"`
	WorkerBinary     string `json:"worker_binary"`
	WorkerCacheDir   string `json:"worker_cache_dir"`
}

type ProcessStatus struct {
	Running   bool       `json:"running"`
	PID       int        `json:"pid,omitempty"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	ExitCode  *int       `json:"exit_code,omitempty"`
	LastError string     `json:"last_error,omitempty"`
}

type RPCRuntimeStatus struct {
	ProcessStatus
	Runtime  runtimes.RPCRuntimeProbe `json:"runtime"`
	Endpoint string                   `json:"endpoint"`
}

type Status struct {
	Running    bool                    `json:"running"`
	PID        int                     `json:"pid,omitempty"`
	StartedAt  *time.Time              `json:"started_at,omitempty"`
	ExitCode   *int                    `json:"exit_code,omitempty"`
	LastError  string                  `json:"last_error,omitempty"`
	LogTail    string                  `json:"log_tail"`
	JobStatus  *workerstatus.JobStatus `json:"job_status,omitempty"`
	Runtime    runtimes.RuntimeStatus  `json:"runtime_status"`
	RPC        RPCRuntimeStatus        `json:"rpc_status"`
	Models     []cluster.ModelResource `json:"models,omitempty"`
	Config     Config                  `json:"config"`
	ConfigPath string                  `json:"config_path"`
}

type Server struct {
	addr       string
	configPath string
	token      string

	mu           sync.Mutex
	config       Config
	cmd          *exec.Cmd
	startedAt    *time.Time
	exitCode     *int
	lastError    string
	rpcCmd       *exec.Cmd
	rpcStartedAt *time.Time
	rpcExitCode  *int
	rpcLastError string
	logTail      *tailBuffer
}

func NewServer(addr string, configPath string) (*Server, error) {
	return NewServerWithToken(addr, configPath, os.Getenv("CMESH_WORKER_CONTROL_TOKEN"))
}

func NewServerWithToken(addr string, configPath string, token string) (*Server, error) {
	if addr == "" {
		addr = "127.0.0.1:9781"
	}
	if configPath == "" {
		configPath = defaultConfigPath()
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		return nil, err
	}
	return &Server{
		addr:       addr,
		configPath: configPath,
		token:      strings.TrimSpace(token),
		config:     cfg,
		logTail:    newTailBuffer(32 * 1024),
	}, nil
}

func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/v1/config", s.handleConfig)
	mux.HandleFunc("/v1/status", s.handleStatus)
	mux.HandleFunc("/v1/start", s.handleStart)
	mux.HandleFunc("/v1/stop", s.handleStop)
	mux.HandleFunc("/v1/restart", s.handleRestart)
	mux.HandleFunc("/v1/disconnect", s.handleDisconnect)
	mux.HandleFunc("/v1/runtime/llama.cpp/ensure", s.handleEnsureLlamaCPP)
	mux.HandleFunc("/v1/runtime/llama.cpp/repair", s.handleEnsureLlamaCPP)
	mux.HandleFunc("/v1/runtime/llama.cpp/rpc/status", s.handleLlamaCPPRPCStatus)
	mux.HandleFunc("/v1/runtime/llama.cpp/rpc/start", s.handleStartLlamaCPPRPC)
	mux.HandleFunc("/v1/runtime/llama.cpp/rpc/stop", s.handleStopLlamaCPPRPC)
	mux.HandleFunc("/v1/runtime/llama.cpp/rpc/restart", s.handleRestartLlamaCPPRPC)

	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	fmt.Printf("worker control API: http://%s\n", listener.Addr().String())
	err = httpServer.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.mu.Lock()
		cfg := s.config
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, cfg)
	case http.MethodPut:
		var cfg Config
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		cfg = normalizeConfig(cfg)
		if err := validateConfig(cfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := saveConfig(s.configPath, cfg); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.mu.Lock()
		s.config = cfg
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, cfg)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, s.status())
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.startWorker(); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, s.status())
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.stopWorker(); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, s.status())
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_ = s.stopWorker()
	if err := s.startWorker(); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, s.status())
}

func (s *Server) handleEnsureLlamaCPP(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	cacheDir := s.config.WorkerCacheDir
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	_, runtimeStatus, err := runtimes.EnsureLlamaCPP(ctx, cacheDir)
	if err != nil {
		status := s.status()
		status.Runtime = runtimeStatus
		writeJSON(w, http.StatusConflict, status)
		return
	}
	writeJSON(w, http.StatusOK, s.status())
}

func (s *Server) handleLlamaCPPRPCStatus(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, s.status().RPC)
}

func (s *Server) handleStartLlamaCPPRPC(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.startLlamaCPPRPC(); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, s.status().RPC)
}

func (s *Server) handleStopLlamaCPPRPC(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.stopLlamaCPPRPC(); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, s.status().RPC)
}

func (s *Server) handleRestartLlamaCPPRPC(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_ = s.stopLlamaCPPRPC()
	if err := s.startLlamaCPPRPC(); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, s.status().RPC)
}

func (s *Server) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.stopWorker(); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	_ = s.stopLlamaCPPRPC()

	s.mu.Lock()
	cfg := s.config
	cfg.JoinToken = ""
	s.config = cfg
	s.logTail.WriteString("disconnected worker from cluster\n")
	s.mu.Unlock()

	if err := saveConfig(s.configPath, cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, s.status())
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) bool {
	if s.token == "" {
		return true
	}
	if r != nil && r.Header.Get("X-CMesh-Control-Token") == s.token {
		return true
	}
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return false
}

func (s *Server) startWorker() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil && s.cmd.Process != nil {
		return fmt.Errorf("worker is already running")
	}
	cfg := normalizeConfig(s.config)
	if err := validateConfig(cfg); err != nil {
		return err
	}
	if err := validateRunnableConfig(cfg); err != nil {
		return err
	}
	binary, err := resolveWorkerBinary(cfg.WorkerBinary)
	if err != nil {
		return err
	}
	args := workerArgs(cfg)
	cmd := exec.Command(binary, args...)
	configureWorkerCommand(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	now := time.Now().UTC()
	s.cmd = cmd
	s.startedAt = &now
	s.exitCode = nil
	s.lastError = ""
	s.logTail.WriteString(fmt.Sprintf("started worker pid=%d\n", cmd.Process.Pid))
	go s.copyOutput(stdout)
	go s.copyOutput(stderr)
	go s.waitWorker(cmd)
	return nil
}

func (s *Server) stopWorker() error {
	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := interruptWorkerProcess(cmd); err != nil {
		_ = killWorkerProcess(cmd)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		running := s.cmd == cmd
		s.mu.Unlock()
		if !running {
			return nil
		}
		if time.Now().After(deadline) {
			_ = killWorkerProcess(cmd)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (s *Server) startLlamaCPPRPC() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rpcCmd != nil && s.rpcCmd.Process != nil {
		return fmt.Errorf("llama.cpp rpc-server is already running")
	}
	cfg := normalizeConfig(s.config)
	if err := validateRPCConfig(cfg); err != nil {
		return err
	}
	runtimeStatus := runtimes.LlamaCPPStatus(cfg.WorkerCacheDir)
	if !runtimeStatus.Ready {
		if runtimeStatus.Error != "" {
			return fmt.Errorf("llama.cpp runtime is not ready: %s", runtimeStatus.Error)
		}
		return fmt.Errorf("llama.cpp runtime is not ready")
	}
	probe := runtimes.NewLlamaCPPRPCRuntime(runtimeStatus.BinaryPath, rpcBindEndpoint(cfg)).Probe(context.Background())
	if !probe.Ready {
		return fmt.Errorf("llama.cpp rpc runtime is not ready: %s", strings.Join(probe.Blockers, "; "))
	}
	args := llamaCPPRPCArgs(cfg)
	cmd := exec.Command(probe.ServerPath, args...)
	configureWorkerCommand(cmd)
	if cfg.RPCCache {
		cmd.Env = append(os.Environ(), "LLAMA_CACHE="+filepath.Join(cfg.WorkerCacheDir, "runtimes", "llama.cpp-rpc-cache"))
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	now := time.Now().UTC()
	endpoint := rpcAdvertiseEndpoint(cfg)
	s.rpcCmd = cmd
	s.rpcStartedAt = &now
	s.rpcExitCode = nil
	s.rpcLastError = ""
	if err := resources.WriteLlamaCPPRPCState(cfg.WorkerCacheDir, resources.LlamaCPPRPCState{
		Running:           true,
		Endpoint:          endpoint,
		BindEndpoint:      rpcBindEndpoint(cfg),
		AdvertiseEndpoint: endpoint,
		PID:               cmd.Process.Pid,
		StartedAt:         now,
	}); err != nil {
		_ = killWorkerProcess(cmd)
		_ = cmd.Wait()
		s.rpcCmd = nil
		s.rpcStartedAt = nil
		return err
	}
	s.logTail.WriteString(fmt.Sprintf("started llama.cpp rpc-server pid=%d endpoint=%s\n", cmd.Process.Pid, endpoint))
	go s.copyOutput(stdout)
	go s.copyOutput(stderr)
	go s.waitLlamaCPPRPC(cmd)
	return nil
}

func (s *Server) stopLlamaCPPRPC() error {
	s.mu.Lock()
	cmd := s.rpcCmd
	cacheDir := s.config.WorkerCacheDir
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		_ = resources.ClearLlamaCPPRPCState(cacheDir)
		return nil
	}
	if err := interruptWorkerProcess(cmd); err != nil {
		_ = killWorkerProcess(cmd)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		running := s.rpcCmd == cmd
		s.mu.Unlock()
		if !running {
			_ = resources.ClearLlamaCPPRPCState(cacheDir)
			return nil
		}
		if time.Now().After(deadline) {
			_ = killWorkerProcess(cmd)
			_ = resources.ClearLlamaCPPRPCState(cacheDir)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (s *Server) waitWorker(cmd *exec.Cmd) {
	err := cmd.Wait()
	exitCode := 0
	if err != nil {
		exitCode = 1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd == cmd {
		s.cmd = nil
		s.startedAt = nil
		s.exitCode = &exitCode
		if err != nil {
			s.lastError = err.Error()
		}
		s.logTail.WriteString(fmt.Sprintf("worker exited code=%d\n", exitCode))
	}
}

func (s *Server) waitLlamaCPPRPC(cmd *exec.Cmd) {
	err := cmd.Wait()
	exitCode := 0
	if err != nil {
		exitCode = 1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rpcCmd == cmd {
		_ = resources.ClearLlamaCPPRPCState(s.config.WorkerCacheDir)
		s.rpcCmd = nil
		s.rpcStartedAt = nil
		s.rpcExitCode = &exitCode
		if err != nil {
			s.rpcLastError = err.Error()
		}
		s.logTail.WriteString(fmt.Sprintf("llama.cpp rpc-server exited code=%d\n", exitCode))
	}
}

func (s *Server) copyOutput(reader io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			s.mu.Lock()
			s.logTail.Write(buf[:n])
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (s *Server) status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg := normalizeConfig(s.config)
	runtimeStatus := runtimes.LlamaCPPStatus(cfg.WorkerCacheDir)
	rpcRuntime := runtimes.NewLlamaCPPRPCRuntime(runtimeStatus.BinaryPath, rpcBindEndpoint(cfg)).Probe(context.Background())
	status := Status{
		Running:   s.cmd != nil && s.cmd.Process != nil,
		StartedAt: s.startedAt,
		ExitCode:  s.exitCode,
		LastError: s.lastError,
		LogTail:   s.logTail.String(),
		Runtime:   runtimeStatus,
		RPC: RPCRuntimeStatus{
			ProcessStatus: ProcessStatus{
				Running:   s.rpcCmd != nil && s.rpcCmd.Process != nil,
				StartedAt: s.rpcStartedAt,
				ExitCode:  s.rpcExitCode,
				LastError: s.rpcLastError,
			},
			Runtime:  rpcRuntime,
			Endpoint: rpcAdvertiseEndpoint(cfg),
		},
		Models:     resources.DiscoverInstalledModels(cfg.WorkerCacheDir),
		Config:     cfg,
		ConfigPath: s.configPath,
	}
	if jobStatus, ok := workerstatus.Read(cfg.WorkerCacheDir); ok {
		status.JobStatus = &jobStatus
	}
	if status.Running {
		status.PID = s.cmd.Process.Pid
	}
	if status.RPC.Running {
		status.RPC.PID = s.rpcCmd.Process.Pid
	}
	return status
}

func loadConfig(path string) (Config, error) {
	cfg := defaultConfig()
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, err
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		return Config{}, err
	}
	return normalizeConfig(cfg), nil
}

func saveConfig(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(normalizeConfig(cfg), "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	return os.WriteFile(path, body, 0o600)
}

func defaultConfig() Config {
	return Config{
		ManagerURL:     "http://127.0.0.1:8080",
		NodeName:       defaultNodeName(),
		CPU:            runtime.NumCPU(),
		MemoryGB:       8,
		DiskGB:         50,
		GPUEnabled:     true,
		VRAMGB:         0,
		JobSlots:       1,
		Benchmark:      true,
		RPCHost:        "0.0.0.0",
		RPCPort:        50052,
		RPCCache:       true,
		WorkerCacheDir: defaultCacheDir(),
	}
}

func normalizeConfig(cfg Config) Config {
	defaults := defaultConfig()
	cfg.ManagerURL = strings.TrimRight(strings.TrimSpace(cfg.ManagerURL), "/")
	cfg.JoinToken = strings.TrimSpace(cfg.JoinToken)
	cfg.NodeName = strings.TrimSpace(cfg.NodeName)
	cfg.WorkerBinary = strings.TrimSpace(cfg.WorkerBinary)
	cfg.WorkerCacheDir = strings.TrimSpace(cfg.WorkerCacheDir)
	cfg.RPCHost = strings.TrimSpace(cfg.RPCHost)
	cfg.RPCAdvertiseHost = strings.TrimSpace(cfg.RPCAdvertiseHost)
	if cfg.ManagerURL == "" {
		cfg.ManagerURL = defaults.ManagerURL
	}
	if cfg.NodeName == "" {
		cfg.NodeName = defaults.NodeName
	}
	if cfg.CPU == 0 {
		cfg.CPU = defaults.CPU
	}
	if cfg.MemoryGB == 0 {
		cfg.MemoryGB = defaults.MemoryGB
	}
	if cfg.DiskGB == 0 {
		cfg.DiskGB = defaults.DiskGB
	}
	if cfg.JobSlots == 0 {
		cfg.JobSlots = defaults.JobSlots
	}
	if cfg.RPCHost == "" {
		cfg.RPCHost = defaults.RPCHost
	}
	if cfg.RPCAdvertiseHost == "" {
		cfg.RPCAdvertiseHost = inferRPCAdvertiseHost(cfg.ManagerURL, cfg.RPCHost)
	}
	if cfg.RPCPort == 0 {
		cfg.RPCPort = defaults.RPCPort
	}
	if cfg.WorkerCacheDir == "" {
		cfg.WorkerCacheDir = defaults.WorkerCacheDir
	}
	return cfg
}

func validateConfig(cfg Config) error {
	if cfg.ManagerURL == "" {
		return fmt.Errorf("manager_url is required")
	}
	if cfg.NodeName == "" {
		return fmt.Errorf("node_name is required")
	}
	if cfg.CPU <= 0 {
		return fmt.Errorf("cpu must be greater than zero")
	}
	if cfg.MemoryGB <= 0 {
		return fmt.Errorf("memory_gb must be greater than zero")
	}
	if cfg.DiskGB <= 0 {
		return fmt.Errorf("disk_gb must be greater than zero")
	}
	if cfg.JobSlots <= 0 {
		return fmt.Errorf("job_slots must be greater than zero")
	}
	return validateRPCConfig(cfg)
}

func validateRPCConfig(cfg Config) error {
	if strings.TrimSpace(cfg.RPCHost) == "" {
		return fmt.Errorf("rpc_host is required")
	}
	if strings.TrimSpace(cfg.RPCAdvertiseHost) == "" {
		return fmt.Errorf("rpc_advertise_host is required")
	}
	if cfg.RPCPort <= 0 || cfg.RPCPort > 65535 {
		return fmt.Errorf("rpc_port must be between 1 and 65535")
	}
	return nil
}

func validateRunnableConfig(cfg Config) error {
	if strings.TrimSpace(cfg.JoinToken) == "" {
		return fmt.Errorf("join_token is required before starting worker")
	}
	return nil
}

func workerArgs(cfg Config) []string {
	args := []string{
		"worker", "run",
		"--manager", cfg.ManagerURL,
		"--token", cfg.JoinToken,
		"--name", cfg.NodeName,
		"--cpu", strconv.Itoa(cfg.CPU),
		"--memory-gb", strconv.Itoa(cfg.MemoryGB),
		"--disk-gb", strconv.Itoa(cfg.DiskGB),
		"--vram-gb", strconv.Itoa(cfg.VRAMGB),
		"--job-slots", strconv.Itoa(cfg.JobSlots),
		"--gpu=" + strconv.FormatBool(cfg.GPUEnabled),
		"--cache-dir", cfg.WorkerCacheDir,
	}
	if cfg.Benchmark {
		args = append(args, "--benchmark")
	}
	return args
}

func llamaCPPRPCArgs(cfg Config) []string {
	args := []string{
		"--host", cfg.RPCHost,
		"--port", strconv.Itoa(cfg.RPCPort),
	}
	if cfg.RPCCache {
		args = append(args, "-c")
	}
	return args
}

func rpcBindEndpoint(cfg Config) string {
	return net.JoinHostPort(cfg.RPCHost, strconv.Itoa(cfg.RPCPort))
}

func rpcAdvertiseEndpoint(cfg Config) string {
	return net.JoinHostPort(cfg.RPCAdvertiseHost, strconv.Itoa(cfg.RPCPort))
}

func inferRPCAdvertiseHost(managerURL string, bindHost string) string {
	cacheKey := strings.TrimSpace(managerURL) + "|" + strings.TrimSpace(bindHost)
	if cached, ok := inferredRPCAdvertiseHosts.Load(cacheKey); ok {
		if value, ok := cached.(string); ok && value != "" {
			return value
		}
	}
	inferred := inferRPCAdvertiseHostUncached(managerURL, bindHost)
	inferredRPCAdvertiseHosts.Store(cacheKey, inferred)
	return inferred
}

func inferRPCAdvertiseHostUncached(managerURL string, bindHost string) string {
	bindHost = strings.TrimSpace(bindHost)
	if bindHost != "" && bindHost != "0.0.0.0" && bindHost != "::" {
		return bindHost
	}
	targetHost := "8.8.8.8"
	targetPort := "80"
	if parsed, err := url.Parse(strings.TrimSpace(managerURL)); err == nil && parsed.Host != "" {
		host := parsed.Hostname()
		port := parsed.Port()
		if host != "" && !isLoopbackHost(host) {
			targetHost = host
			if port != "" {
				targetPort = port
			} else if parsed.Scheme == "https" {
				targetPort = "443"
			} else {
				targetPort = "80"
			}
		}
	}
	conn, err := net.DialTimeout("udp", net.JoinHostPort(targetHost, targetPort), 500*time.Millisecond)
	if err == nil {
		defer conn.Close()
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok && addr.IP != nil && !addr.IP.IsUnspecified() {
			return addr.IP.String()
		}
	}
	if bindHost != "" && bindHost != "0.0.0.0" && bindHost != "::" {
		return bindHost
	}
	return "127.0.0.1"
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func resolveWorkerBinary(configured string) (string, error) {
	if configured != "" {
		return configured, nil
	}
	if value := os.Getenv("CMESH_WORKER_BIN"); value != "" {
		return value, nil
	}
	self, err := os.Executable()
	if err == nil && self != "" {
		return self, nil
	}
	return exec.LookPath("cmesh")
}

func defaultConfigPath() string {
	if value := os.Getenv("CMESH_WORKER_CONTROL_CONFIG"); value != "" {
		return value
	}
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return "./data/worker-control.json"
	}
	return filepath.Join(dir, "cmesh", "worker-control.json")
}

func defaultCacheDir() string {
	dir, err := os.UserCacheDir()
	if err != nil || dir == "" {
		return "./data/cache"
	}
	return filepath.Join(dir, "cmesh", "cache")
}

func defaultNodeName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "worker"
	}
	return host
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type tailBuffer struct {
	limit int
	buf   []byte
}

func newTailBuffer(limit int) *tailBuffer {
	return &tailBuffer{limit: limit}
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.limit {
		b.buf = b.buf[len(b.buf)-b.limit:]
	}
	return len(p), nil
}

func (b *tailBuffer) WriteString(value string) {
	_, _ = b.Write([]byte(value))
}

func (b *tailBuffer) String() string {
	return string(b.buf)
}
