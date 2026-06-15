package workercontrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cmesh/cmesh/internal/workerstatus"
)

type Config struct {
	ManagerURL     string `json:"manager_url"`
	JoinToken      string `json:"join_token"`
	NodeName       string `json:"node_name"`
	CPU            int    `json:"cpu"`
	MemoryGB       int    `json:"memory_gb"`
	DiskGB         int    `json:"disk_gb"`
	GPUEnabled     bool   `json:"gpu_enabled"`
	VRAMGB         int    `json:"vram_gb"`
	Benchmark      bool   `json:"benchmark"`
	WorkerBinary   string `json:"worker_binary"`
	WorkerCacheDir string `json:"worker_cache_dir"`
}

type Status struct {
	Running    bool                    `json:"running"`
	PID        int                     `json:"pid,omitempty"`
	StartedAt  *time.Time              `json:"started_at,omitempty"`
	ExitCode   *int                    `json:"exit_code,omitempty"`
	LastError  string                  `json:"last_error,omitempty"`
	LogTail    string                  `json:"log_tail"`
	JobStatus  *workerstatus.JobStatus `json:"job_status,omitempty"`
	Config     Config                  `json:"config"`
	ConfigPath string                  `json:"config_path"`
}

type Server struct {
	addr       string
	configPath string
	token      string

	mu        sync.Mutex
	config    Config
	cmd       *exec.Cmd
	startedAt *time.Time
	exitCode  *int
	lastError string
	logTail   *tailBuffer
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
	status := Status{
		Running:    s.cmd != nil && s.cmd.Process != nil,
		StartedAt:  s.startedAt,
		ExitCode:   s.exitCode,
		LastError:  s.lastError,
		LogTail:    s.logTail.String(),
		Config:     s.config,
		ConfigPath: s.configPath,
	}
	if jobStatus, ok := workerstatus.Read(s.config.WorkerCacheDir); ok {
		status.JobStatus = &jobStatus
	}
	if status.Running {
		status.PID = s.cmd.Process.Pid
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
		Benchmark:      true,
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
		"--gpu=" + strconv.FormatBool(cfg.GPUEnabled),
		"--cache-dir", cfg.WorkerCacheDir,
	}
	if cfg.Benchmark {
		args = append(args, "--benchmark")
	}
	return args
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
