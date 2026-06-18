package manager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cmesh/cmesh/internal/cdip"
	"github.com/cmesh/cmesh/internal/cluster"
	"github.com/cmesh/cmesh/internal/jobs"
	"github.com/cmesh/cmesh/internal/membership"
	"github.com/cmesh/cmesh/internal/models"
	"github.com/cmesh/cmesh/internal/protocol"
	"github.com/cmesh/cmesh/internal/resources"
	"github.com/cmesh/cmesh/internal/transport"
	"github.com/cmesh/cmesh/internal/version"
)

type Server struct {
	addr                    string
	state                   Store
	joinToken               string
	operatorToken           string
	publicURL               string
	backgroundCDIPAdvance   bool
	cdipAdvanceEvery        time.Duration
	backgroundRPCHealth     bool
	rpcHealthRefreshEvery   time.Duration
	rpcHealthTimeout        time.Duration
	cdipActivationTransport transport.ActivationTransport
	mux                     *http.ServeMux
	server                  *http.Server
	snapshotMu              sync.RWMutex
	snapshots               map[string]CapacitySnapshot
}

const rpcEndpointQuarantineConsecutiveFailures = 3

type ServerOptions struct {
	Addr                  string
	JoinToken             string
	OperatorToken         string
	PublicURL             string
	BackgroundCDIPAdvance bool
	CDIPAdvanceEvery      time.Duration
	BackgroundRPCHealth   bool
	RPCHealthEvery        time.Duration
	RPCHealthTimeout      time.Duration
}

func NewServer(addr string, state Store) *Server {
	return NewServerWithOptions(ServerOptions{Addr: addr}, state)
}

func NewServerWithOptions(options ServerOptions, state Store) *Server {
	mux := http.NewServeMux()
	advanceEvery := options.CDIPAdvanceEvery
	if advanceEvery <= 0 {
		advanceEvery = time.Second
	}
	rpcHealthEvery := options.RPCHealthEvery
	if rpcHealthEvery <= 0 {
		rpcHealthEvery = 10 * time.Second
	}
	rpcHealthTimeout := options.RPCHealthTimeout
	if rpcHealthTimeout <= 0 {
		rpcHealthTimeout = time.Second
	}
	s := &Server{
		addr:                    options.Addr,
		state:                   state,
		joinToken:               options.JoinToken,
		operatorToken:           options.OperatorToken,
		publicURL:               strings.TrimRight(options.PublicURL, "/"),
		backgroundCDIPAdvance:   options.BackgroundCDIPAdvance,
		cdipAdvanceEvery:        advanceEvery,
		backgroundRPCHealth:     options.BackgroundRPCHealth,
		rpcHealthRefreshEvery:   rpcHealthEvery,
		rpcHealthTimeout:        rpcHealthTimeout,
		cdipActivationTransport: transport.NewMemoryActivationTransport(8),
		mux:                     mux,
		snapshots:               make(map[string]CapacitySnapshot),
	}

	mux.HandleFunc("/", s.handleDashboard)
	mux.HandleFunc("/invite", s.handleInvite)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/v1/cluster", s.handleCluster)
	mux.HandleFunc("/v1/capacity/snapshots", s.handleCapacitySnapshots)
	mux.HandleFunc("/v1/capacity", s.handleCapacity)
	mux.HandleFunc("/v1/dashboard/status", s.handleDashboardStatus)
	mux.HandleFunc("/v1/runtime/stage-probes", s.handleRuntimeStageProbes)
	mux.HandleFunc("/v1/runtime/rpc-pool", s.handleRuntimeRPCPool)
	mux.HandleFunc("/v1/runtime/rpc-pool/refresh", s.handleRuntimeRPCPoolRefresh)
	mux.HandleFunc("/v1/runtime/rpc-pool/smoke", s.handleRuntimeRPCPoolSmoke)
	mux.HandleFunc("/v1/nodes", s.handleNodes)
	mux.HandleFunc("/v1/benchmarks", s.handleBenchmarks)
	mux.HandleFunc("/v1/cluster-benchmarks", s.handleClusterBenchmarks)
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/v1/models/", s.handleModel)
	mux.HandleFunc("/v1/conversations", s.handleConversations)
	mux.HandleFunc("/v1/conversations/", s.handleConversation)
	mux.HandleFunc("/v1/memories", s.handleMemories)
	mux.HandleFunc("/v1/memories/", s.handleMemory)
	mux.HandleFunc("/v1/jobs", s.handleJobs)
	mux.HandleFunc("/v1/jobs/", s.handleJob)
	mux.HandleFunc("/v1/cdip/activations/", s.handleCDIPActivation)
	mux.HandleFunc("/v1/cdip/jobs/", s.handleCDIPJob)
	mux.HandleFunc("/v1/cdip/stages/", s.handleCDIPStage)
	mux.HandleFunc("/v1/workers/", s.handleWorkerRoutes)
	mux.HandleFunc("/v1/workers/join", s.handleWorkerJoin)
	mux.HandleFunc("/v1/workers/heartbeat", s.handleWorkerHeartbeat)
	mux.HandleFunc("/v1/workers/leave", s.handleWorkerLeave)

	s.server = &http.Server{
		Addr:              options.Addr,
		Handler:           requestLogger(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestLogger(s.mux).ServeHTTP(w, r)
}

func (s *Server) Start(ctx context.Context) error {
	errCh := make(chan error, 1)
	if s.backgroundCDIPAdvance {
		go s.runCDIPAdvanceLoop(ctx)
	}
	if s.backgroundRPCHealth {
		go s.runRPCHealthRefreshLoop(ctx)
	}
	go func() {
		errCh <- s.server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.server.Shutdown(shutdownCtx)
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func (s *Server) runCDIPAdvanceLoop(ctx context.Context) {
	ticker := time.NewTicker(s.cdipAdvanceEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.advanceActiveCDIPJobs()
		}
	}
}

func (s *Server) runRPCHealthRefreshLoop(ctx context.Context) {
	ticker := time.NewTicker(s.rpcHealthRefreshEvery)
	defer ticker.Stop()
	s.refreshRuntimeRPCHealth(ctx, int(s.rpcHealthTimeout.Milliseconds()))
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refreshRuntimeRPCHealth(ctx, int(s.rpcHealthTimeout.Milliseconds()))
		}
	}
}

func (s *Server) advanceActiveCDIPJobs() {
	for _, job := range s.state.Jobs() {
		if job.Type != models.JobGenerateDistributed {
			continue
		}
		if job.Status == jobs.StatusSucceeded || job.Status == jobs.StatusFailed || job.Status == jobs.StatusCanceled {
			continue
		}
		_, _ = advanceCDIPDistributedJob(s.state, s.cdipActivationTransport, job.ID, 0, "")
	}
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if !s.requireOperatorAuth(w, r, true) {
		return
	}

	nodes := s.state.Nodes()
	allJobs := s.state.Jobs()
	clusterBenchmarks := clusterBenchmarkSummaries(allJobs, 5)
	modelCatalog := models.Catalog()
	modelsView := modelSummaries(modelCatalog, allJobs, nodes)
	rpcPoolSummary, rpcPoolWorkers, rpcPoolEndpoints := runtimeRPCPoolReport(nodes)
	rpcHealth := s.state.RPCHealth()
	rankedRPCEndpoints := rankRPCEndpoints(rpcPoolEndpoints, rpcHealth)
	data := struct {
		Summary            ClusterSummary
		OnlineNodes        []cluster.Node
		OfflineWorkerCount int
		Benchmarks         map[string]NodeBenchmarkSummary
		ClusterBenchmarks  []ClusterBenchmarkSummary
		Models             []ModelSummary
		RPCPool            RuntimeRPCPoolSummary
		RPCWorkers         []RuntimeRPCPoolWorker
		RPCEndpoints       []string
		RPCHealth          []RPCHealthRecord
		Readiness          ReadinessSummary
		Capacity           CapacitySummary
		NodesByID          map[string]cluster.Node
		WorkerActiveJobs   map[string]int
		MaxClusterGFLOPS   float64
		Jobs               []jobs.Job
		DistributedRuns    []DistributedRunSummary
		ChatJobs           []jobs.Job
		Conversations      []Conversation
		Memories           []Memory
		InviteURL          string
	}{
		Summary:            s.state.ClusterSummary(),
		OnlineNodes:        onlineWorkerNodes(nodes),
		OfflineWorkerCount: offlineWorkerCount(nodes),
		Benchmarks:         s.state.BenchmarkSummaryByNode(),
		ClusterBenchmarks:  clusterBenchmarks,
		Models:             modelsView,
		RPCPool:            rpcPoolSummary,
		RPCWorkers:         rpcPoolWorkers,
		RPCEndpoints:       rankedRPCEndpoints,
		RPCHealth:          rpcHealth,
		Readiness:          clusterReadiness(onlineWorkerNodes(nodes), modelsView, allJobs),
		Capacity:           clusterCapacity(s.state.ClusterSummary(), modelsView, onlineWorkerNodes(nodes)),
		NodesByID:          nodesByID(nodes),
		WorkerActiveJobs:   activeJobsByWorker(allJobs),
		MaxClusterGFLOPS:   maxClusterBenchmarkGFLOPS(clusterBenchmarks),
		Jobs:               recentJobs(allJobs, 12),
		DistributedRuns:    distributedRunSummaries(allJobs, 20),
		ChatJobs:           recentChatJobs(allJobs, 6),
		Conversations:      recentConversations(s.state, 12),
		Memories:           recentMemories(s.state, 12),
		InviteURL:          "/invite",
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := dashboardTemplate.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleInvite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireOperatorAuth(w, r, true) {
		return
	}
	if s.joinToken == "" {
		http.Error(w, "worker join token is not configured", http.StatusConflict)
		return
	}

	managerURL := s.publicURL
	if managerURL == "" {
		managerURL = localManagerURL(r)
	}
	data := InvitePageData{
		ManagerURL:          managerURL,
		JoinToken:           s.joinToken,
		DesktopInviteURL:    desktopInviteURL(managerURL, s.joinToken),
		DesktopInviteHref:   template.URL(desktopInviteURL(managerURL, s.joinToken)),
		DownloadURL:         releaseDownloadBase(version.Version) + "CMesh-Worker-Apple-Silicon.dmg",
		ReleaseDownloadBase: releaseDownloadBase(version.Version),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := inviteTemplate.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) requireOperatorAuth(w http.ResponseWriter, r *http.Request, html bool) bool {
	if s.operatorToken == "" {
		return true
	}
	if s.hasOperatorAuth(r) {
		if token := r.URL.Query().Get("token"); token == s.operatorToken {
			http.SetCookie(w, &http.Cookie{
				Name:     "cmesh_operator_token",
				Value:    token,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				Secure:   r.Header.Get("X-Forwarded-Proto") == "https" || r.TLS != nil,
				MaxAge:   12 * 60 * 60,
			})
		}
		return true
	}
	if html {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_ = operatorLoginTemplate.Execute(w, map[string]string{
			"Path": r.URL.Path,
		})
		return false
	}
	http.Error(w, "operator token required", http.StatusUnauthorized)
	return false
}

func (s *Server) hasOperatorAuth(r *http.Request) bool {
	if s.operatorToken == "" {
		return true
	}
	if r.URL.Query().Get("token") == s.operatorToken {
		return true
	}
	if r.Header.Get("X-CMesh-Operator-Token") == s.operatorToken {
		return true
	}
	if strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") == s.operatorToken {
		return true
	}
	cookie, err := r.Cookie("cmesh_operator_token")
	return err == nil && cookie.Value == s.operatorToken
}

func localManagerURL(r *http.Request) string {
	proto := r.Header.Get("X-Forwarded-Proto")
	if proto == "" {
		proto = "http"
	}
	host := r.Host
	if host == "" {
		host = "127.0.0.1"
	}
	return proto + "://" + host
}

type InvitePageData struct {
	ManagerURL          string
	JoinToken           string
	DesktopInviteURL    string
	DesktopInviteHref   template.URL
	DownloadURL         string
	ReleaseDownloadBase string
}

type clusterBenchmarkRequest struct {
	Size        int    `json:"size"`
	Iterations  int    `json:"iterations"`
	RequestedBy string `json:"requested_by"`
}

type ClusterBenchmarkSummary struct {
	ID          string     `json:"id"`
	RequestedBy string     `json:"requested_by"`
	Size        int        `json:"size"`
	Iterations  int        `json:"iterations"`
	Status      string     `json:"status"`
	Workers     int        `json:"workers"`
	Completed   int        `json:"completed"`
	Failed      int        `json:"failed"`
	Active      int        `json:"active"`
	TotalGFLOPS float64    `json:"total_gflops"`
	Jobs        []jobs.Job `json:"jobs"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func desktopInviteURL(managerURL string, joinToken string) string {
	values := url.Values{}
	values.Set("manager", managerURL)
	values.Set("token", joinToken)
	return "cmesh://join?" + values.Encode()
}

func releaseDownloadBase(appVersion string) string {
	if strings.HasPrefix(appVersion, "v") {
		return "https://github.com/NythralHome/cmesh/releases/download/" + appVersion + "/"
	}
	return "https://github.com/NythralHome/cmesh/releases/latest/download/"
}

var operatorLoginTemplate = template.Must(template.New("operator-login").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>CMesh Operator Login</title>
  <style>
    :root {
      color-scheme: light;
      --bg: #f6f7f9;
      --panel: #ffffff;
      --text: #17202a;
      --muted: #657282;
      --line: #d9dee5;
      --accent: #0f766e;
      --accent-2: #2563eb;
      --soft: #eef7f5;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      min-height: 100vh;
      display: grid;
      place-items: center;
      font-family: ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      background: var(--bg);
      color: var(--text);
    }
    form {
      width: min(420px, calc(100vw - 32px));
      background: var(--panel);
      border: 1px solid var(--line);
      border-radius: 8px;
      padding: 20px;
    }
    h1 { margin: 0 0 8px; font-size: 22px; letter-spacing: 0; }
    p { margin: 0 0 18px; color: var(--muted); font-size: 14px; }
    label { display: block; margin-bottom: 8px; color: var(--muted); font-size: 13px; font-weight: 600; }
    input {
      width: 100%;
      min-height: 40px;
      border: 1px solid var(--line);
      border-radius: 6px;
      padding: 0 10px;
      font: inherit;
    }
    button {
      margin-top: 14px;
      min-height: 38px;
      padding: 0 14px;
      border: 1px solid var(--accent);
      border-radius: 6px;
      background: var(--accent);
      color: #ffffff;
      font: inherit;
      font-weight: 700;
      cursor: pointer;
    }
  </style>
</head>
<body>
  <form method="get" action="{{.Path}}">
    <h1>CMesh Operator</h1>
    <p>This cluster dashboard is private.</p>
    <label for="token">Operator token</label>
    <input id="token" name="token" type="password" autocomplete="current-password" autofocus>
    <button type="submit">Open cluster</button>
  </form>
</body>
</html>`))

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
		"role":   "manager",
		"mode":   "single-node-bootstrap",
	})
}

func (s *Server) handleCluster(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorAuth(w, r, false) {
		return
	}
	writeJSON(w, http.StatusOK, s.state.ClusterSummary())
}

func (s *Server) handleCapacity(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorAuth(w, r, false) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	current := s.capacitySnapshot("")
	payload := map[string]any{
		"summary":  current.Summary,
		"capacity": current.Capacity,
	}
	if baselineID := strings.TrimSpace(r.URL.Query().Get("baseline")); baselineID != "" {
		if baseline, ok := s.capacitySnapshotByID(baselineID); ok {
			payload["baseline"] = baseline
			payload["delta"] = capacityDelta(baseline, current)
		} else {
			http.Error(w, "baseline snapshot not found", http.StatusNotFound)
			return
		}
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) handleCapacitySnapshots(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorAuth(w, r, false) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"snapshots": s.capacitySnapshots()})
	case http.MethodPost:
		var req struct {
			Label     string `json:"label"`
			CompareTo string `json:"compare_to"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&req)
		}
		snapshot := s.saveCapacitySnapshot(req.Label)
		payload := map[string]any{"snapshot": snapshot}
		if baselineID := strings.TrimSpace(req.CompareTo); baselineID != "" {
			if baseline, ok := s.capacitySnapshotByID(baselineID); ok {
				payload["baseline"] = baseline
				payload["delta"] = capacityDelta(baseline, snapshot)
			} else {
				http.Error(w, "compare_to snapshot not found", http.StatusNotFound)
				return
			}
		}
		writeJSON(w, http.StatusCreated, payload)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleDashboardStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorAuth(w, r, false) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	nodes := s.state.Nodes()
	jobsList := s.state.Jobs()
	modelsView := modelSummaries(models.Catalog(), jobsList, nodes)
	summary := s.state.ClusterSummary()
	readiness := clusterReadiness(onlineWorkerNodes(nodes), modelsView, jobsList)
	writeJSON(w, http.StatusOK, map[string]any{
		"readiness_status":   readiness.Status,
		"workers_online":     summary.WorkersOnline,
		"workers_total":      summary.WorkersTotal,
		"ready_models":       readiness.GeneratableModels,
		"active_jobs":        readiness.ActiveJobs,
		"recent_failures":    readiness.RecentFailures,
		"jobs_total":         len(jobsList),
		"runtime_ready":      readiness.RuntimeReadyWorkers,
		"installed_models":   readiness.InstalledModels,
		"benchmark_score":    summary.BenchmarkScore,
		"updated_at_unix_ms": time.Now().UTC().UnixMilli(),
	})
}

type RuntimeStageProbeSummary struct {
	Workers                    int `json:"workers"`
	RuntimeReadyWorkers        int `json:"runtime_ready_workers"`
	StageProbeWorkers          int `json:"stage_probe_workers"`
	ReadyStageRuntimeWorkers   int `json:"ready_stage_runtime_workers"`
	BlockedStageRuntimeWorkers int `json:"blocked_stage_runtime_workers"`
}

type RuntimeStageProbeWorker struct {
	NodeID       string                         `json:"node_id"`
	NodeName     string                         `json:"node_name"`
	Status       cluster.NodeStatus             `json:"status"`
	Runtime      string                         `json:"runtime"`
	RuntimeReady bool                           `json:"runtime_ready"`
	Probes       []cluster.StageRuntimeResource `json:"probes"`
}

func (s *Server) handleRuntimeStageProbes(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorAuth(w, r, false) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	summary, workers := runtimeStageProbeReport(s.state.Nodes())
	writeJSON(w, http.StatusOK, map[string]any{
		"summary": summary,
		"workers": workers,
	})
}

func runtimeStageProbeReport(nodes []cluster.Node) (RuntimeStageProbeSummary, []RuntimeStageProbeWorker) {
	summary := RuntimeStageProbeSummary{}
	workers := make([]RuntimeStageProbeWorker, 0)
	for _, node := range nodes {
		if node.Role != cluster.NodeRoleWorker {
			continue
		}
		summary.Workers++
		for _, runtimeStatus := range node.Resources.Runtimes {
			if runtimeStatus.Ready {
				summary.RuntimeReadyWorkers++
			}
			if len(runtimeStatus.StageRuntimes) == 0 {
				continue
			}
			stageReady := false
			for _, probe := range runtimeStatus.StageRuntimes {
				if probe.Ready {
					stageReady = true
					break
				}
			}
			summary.StageProbeWorkers++
			if stageReady {
				summary.ReadyStageRuntimeWorkers++
			} else {
				summary.BlockedStageRuntimeWorkers++
			}
			workers = append(workers, RuntimeStageProbeWorker{
				NodeID:       node.ID,
				NodeName:     nodeDisplayName(node),
				Status:       node.Status,
				Runtime:      runtimeStatus.Name,
				RuntimeReady: runtimeStatus.Ready,
				Probes:       append([]cluster.StageRuntimeResource(nil), runtimeStatus.StageRuntimes...),
			})
		}
	}
	sort.Slice(workers, func(i, j int) bool {
		if workers[i].NodeName != workers[j].NodeName {
			return workers[i].NodeName < workers[j].NodeName
		}
		return workers[i].Runtime < workers[j].Runtime
	})
	return summary, workers
}

type RuntimeRPCPoolSummary struct {
	Workers             int `json:"workers"`
	RuntimeReadyWorkers int `json:"runtime_ready_workers"`
	RPCReadyWorkers     int `json:"rpc_ready_workers"`
	Endpoints           int `json:"endpoints"`
}

type RuntimeRPCPoolWorker struct {
	NodeID       string                     `json:"node_id"`
	NodeName     string                     `json:"node_name"`
	Status       cluster.NodeStatus         `json:"status"`
	Runtime      string                     `json:"runtime"`
	RuntimeReady bool                       `json:"runtime_ready"`
	RPC          cluster.RPCRuntimeResource `json:"rpc"`
	Capabilities []string                   `json:"capabilities,omitempty"`
}

type RuntimeRPCSmokeResult struct {
	Endpoint  string `json:"endpoint"`
	Ready     bool   `json:"ready"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

type RuntimeRPCSmokeReport struct {
	Checked     int                     `json:"checked"`
	Ready       int                     `json:"ready"`
	Failed      int                     `json:"failed"`
	TimeoutMS   int                     `json:"timeout_ms"`
	DurationMS  int64                   `json:"duration_ms"`
	Results     []RuntimeRPCSmokeResult `json:"results"`
	RunnableNow bool                    `json:"runnable_now"`
}

type RuntimeRPCPoolRefreshReport struct {
	Report         RuntimeRPCSmokeReport  `json:"report"`
	Summary        RuntimeRPCPoolSummary  `json:"summary"`
	Workers        []RuntimeRPCPoolWorker `json:"workers"`
	Endpoints      []string               `json:"endpoints"`
	Health         []RPCHealthRecord      `json:"health"`
	LlamaCLIRPCArg string                 `json:"llama_cli_rpc_arg"`
}

func (s *Server) handleRuntimeRPCPool(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorAuth(w, r, false) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	summary, workers, endpoints := runtimeRPCPoolReport(s.state.Nodes())
	health := s.state.RPCHealth()
	rankedEndpoints := usableRPCEndpoints(endpoints, health, false)
	writeJSON(w, http.StatusOK, map[string]any{
		"summary":           summary,
		"workers":           workers,
		"endpoints":         rankedEndpoints,
		"health":            health,
		"llama_cli_rpc_arg": strings.Join(rankedEndpoints, ","),
	})
}

func (s *Server) handleRuntimeRPCPoolRefresh(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorAuth(w, r, false) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	report := s.refreshRuntimeRPCHealth(r.Context(), intQuery(r, "timeout_ms", 1000))
	summary, workers, endpoints := runtimeRPCPoolReport(s.state.Nodes())
	health := s.state.RPCHealth()
	rankedEndpoints := usableRPCEndpoints(endpoints, health, false)
	status := http.StatusOK
	if report.Checked == 0 {
		status = http.StatusConflict
	}
	writeJSON(w, status, RuntimeRPCPoolRefreshReport{
		Report:         report,
		Summary:        summary,
		Workers:        workers,
		Endpoints:      rankedEndpoints,
		Health:         health,
		LlamaCLIRPCArg: strings.Join(rankedEndpoints, ","),
	})
}

func (s *Server) handleRuntimeRPCPoolSmoke(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorAuth(w, r, false) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	report := s.refreshRuntimeRPCHealth(r.Context(), intQuery(r, "timeout_ms", 1000))
	status := http.StatusOK
	if report.Checked == 0 {
		status = http.StatusConflict
	}
	writeJSON(w, status, report)
}

func (s *Server) refreshRuntimeRPCHealth(ctx context.Context, timeoutMS int) RuntimeRPCSmokeReport {
	if timeoutMS < 100 {
		timeoutMS = 100
	}
	if timeoutMS > 5000 {
		timeoutMS = 5000
	}
	_, workers, endpoints := runtimeRPCPoolReport(s.state.Nodes())
	report := smokeRuntimeRPCEndpoints(ctx, usableRPCEndpoints(endpoints, s.state.RPCHealth(), true), time.Duration(timeoutMS)*time.Millisecond)
	s.recordRPCHealthReport(workers, report)
	report.TimeoutMS = timeoutMS
	return report
}

func (s *Server) recordRPCHealthReport(workers []RuntimeRPCPoolWorker, report RuntimeRPCSmokeReport) {
	workersByEndpoint := rpcWorkersByEndpoint(workers)
	now := time.Now().UTC()
	for _, result := range report.Results {
		worker := workersByEndpoint[result.Endpoint]
		_ = s.state.PutRPCHealth(RPCHealthUpdate{
			Endpoint:  result.Endpoint,
			NodeID:    worker.NodeID,
			NodeName:  worker.NodeName,
			Ready:     result.Ready,
			LatencyMS: result.LatencyMS,
			Error:     result.Error,
			CheckedAt: now,
		})
	}
}

func smokeRuntimeRPCEndpoints(ctx context.Context, endpoints []string, timeout time.Duration) RuntimeRPCSmokeReport {
	started := time.Now()
	report := RuntimeRPCSmokeReport{
		Checked: len(endpoints),
		Results: make([]RuntimeRPCSmokeResult, 0, len(endpoints)),
	}
	for _, endpoint := range endpoints {
		result := smokeRuntimeRPCEndpoint(ctx, endpoint, timeout)
		if result.Ready {
			report.Ready++
		} else {
			report.Failed++
		}
		report.Results = append(report.Results, result)
	}
	report.DurationMS = time.Since(started).Milliseconds()
	report.RunnableNow = report.Checked > 0 && report.Failed == 0
	return report
}

func smokeRuntimeRPCEndpoint(ctx context.Context, endpoint string, timeout time.Duration) RuntimeRPCSmokeResult {
	endpoint = strings.TrimSpace(endpoint)
	result := RuntimeRPCSmokeResult{Endpoint: endpoint}
	if endpoint == "" {
		result.Error = "empty endpoint"
		return result
	}
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", endpoint)
	result.LatencyMS = time.Since(started).Milliseconds()
	if err != nil {
		result.Error = err.Error()
		return result
	}
	_ = conn.Close()
	result.Ready = true
	return result
}

func runtimeRPCPoolReport(nodes []cluster.Node) (RuntimeRPCPoolSummary, []RuntimeRPCPoolWorker, []string) {
	summary := RuntimeRPCPoolSummary{}
	workers := make([]RuntimeRPCPoolWorker, 0)
	endpoints := make([]string, 0)
	seenEndpoint := map[string]bool{}
	for _, node := range nodes {
		if node.Role != cluster.NodeRoleWorker || node.Status != cluster.NodeStatusOnline {
			continue
		}
		summary.Workers++
		for _, runtimeStatus := range node.Resources.Runtimes {
			if runtimeStatus.Ready {
				summary.RuntimeReadyWorkers++
			}
			if !runtimeStatus.Ready {
				continue
			}
			for _, rpc := range runtimeStatus.RPCRuntimes {
				endpoint := strings.TrimSpace(rpc.Endpoint)
				if rpc.Ready && endpoint != "" {
					summary.RPCReadyWorkers++
					if !seenEndpoint[endpoint] {
						seenEndpoint[endpoint] = true
						endpoints = append(endpoints, endpoint)
					}
				}
				workers = append(workers, RuntimeRPCPoolWorker{
					NodeID:       node.ID,
					NodeName:     nodeDisplayName(node),
					Status:       node.Status,
					Runtime:      runtimeStatus.Name,
					RuntimeReady: runtimeStatus.Ready,
					RPC:          rpc,
					Capabilities: append([]string(nil), runtimeStatus.Capabilities...),
				})
			}
		}
	}
	sort.Strings(endpoints)
	sort.Slice(workers, func(i, j int) bool {
		if workers[i].NodeName != workers[j].NodeName {
			return workers[i].NodeName < workers[j].NodeName
		}
		return workers[i].RPC.Endpoint < workers[j].RPC.Endpoint
	})
	summary.Endpoints = len(endpoints)
	return summary, workers, endpoints
}

func rankRPCEndpoints(endpoints []string, health []RPCHealthRecord) []string {
	out := cleanEndpointList(endpoints)
	if len(out) <= 1 {
		return out
	}
	healthByEndpoint := rpcHealthByEndpoint(health)
	sort.SliceStable(out, func(i, j int) bool {
		left, leftKnown := healthByEndpoint[out[i]]
		right, rightKnown := healthByEndpoint[out[j]]
		leftRank := rpcHealthRank(left, leftKnown)
		rightRank := rpcHealthRank(right, rightKnown)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if leftKnown && rightKnown {
			if left.ConsecutiveFailures != right.ConsecutiveFailures {
				return left.ConsecutiveFailures < right.ConsecutiveFailures
			}
			if left.Failures != right.Failures {
				return left.Failures < right.Failures
			}
			if left.LastLatencyMS != right.LastLatencyMS {
				if left.LastLatencyMS == 0 {
					return false
				}
				if right.LastLatencyMS == 0 {
					return true
				}
				return left.LastLatencyMS < right.LastLatencyMS
			}
		}
		return out[i] < out[j]
	})
	return out
}

func usableRPCEndpoints(endpoints []string, health []RPCHealthRecord, includeQuarantined bool) []string {
	ranked := rankRPCEndpoints(endpoints, health)
	if includeQuarantined {
		return ranked
	}
	healthByEndpoint := rpcHealthByEndpoint(health)
	out := make([]string, 0, len(ranked))
	for _, endpoint := range ranked {
		record, ok := healthByEndpoint[endpoint]
		if ok && rpcEndpointQuarantined(record) {
			continue
		}
		out = append(out, endpoint)
	}
	return out
}

func rpcHealthByEndpoint(health []RPCHealthRecord) map[string]RPCHealthRecord {
	out := make(map[string]RPCHealthRecord, len(health))
	for _, record := range health {
		endpoint := strings.TrimSpace(record.Endpoint)
		if endpoint != "" {
			out[endpoint] = record
		}
	}
	return out
}

func quarantinedRPCEndpoints(endpoints []string, health []RPCHealthRecord) []string {
	endpointSet := map[string]bool{}
	for _, endpoint := range cleanEndpointList(endpoints) {
		endpointSet[endpoint] = true
	}
	out := make([]string, 0)
	for _, record := range health {
		if endpointSet[record.Endpoint] && rpcEndpointQuarantined(record) {
			out = append(out, record.Endpoint)
		}
	}
	sort.Strings(out)
	return out
}

func rpcEndpointQuarantined(record RPCHealthRecord) bool {
	return !record.Ready && record.ConsecutiveFailures >= rpcEndpointQuarantineConsecutiveFailures
}

func rpcHealthRank(record RPCHealthRecord, known bool) int {
	if !known {
		return 1
	}
	if record.Ready {
		return 0
	}
	return 2
}

func cleanEndpointList(endpoints []string) []string {
	out := make([]string, 0, len(endpoints))
	seen := map[string]bool{}
	for _, endpoint := range endpoints {
		endpoint = strings.TrimSpace(endpoint)
		if endpoint == "" || seen[endpoint] {
			continue
		}
		seen[endpoint] = true
		out = append(out, endpoint)
	}
	return out
}

func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorAuth(w, r, false) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"nodes": s.state.Nodes(),
	})
}

func (s *Server) handleBenchmarks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !s.requireOperatorAuth(w, r, false) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"benchmarks": s.state.Benchmarks(),
			"by_node":    s.state.BenchmarkSummaryByNode(),
		})
	case http.MethodPost:
		var result resources.BenchmarkResult
		if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if result.NodeID == "" {
			http.Error(w, "node_id is required", http.StatusBadRequest)
			return
		}
		if result.Kind == "" {
			http.Error(w, "kind is required", http.StatusBadRequest)
			return
		}
		if !s.state.PutBenchmark(result) {
			http.Error(w, "unknown node", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusCreated, result)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleClusterBenchmarks(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorAuth(w, r, false) {
		return
	}

	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"cluster_benchmarks": clusterBenchmarkSummaries(s.state.Jobs(), 12),
		})
	case http.MethodPost:
		var req clusterBenchmarkRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Size == 0 {
			req.Size = 512
		}
		if req.Iterations == 0 {
			req.Iterations = 6
		}
		if req.Size < 16 || req.Size > 2048 {
			http.Error(w, "size must be between 16 and 2048", http.StatusBadRequest)
			return
		}
		if req.Iterations < 1 || req.Iterations > 100 {
			http.Error(w, "iterations must be between 1 and 100", http.StatusBadRequest)
			return
		}

		nodes := onlineWorkerNodes(s.state.Nodes())
		if len(nodes) == 0 {
			http.Error(w, "no online workers available", http.StatusConflict)
			return
		}

		runID := newClusterBenchmarkID()
		requestedBy := clusterBenchmarkRequestedBy(runID, req.RequestedBy)
		input, err := json.Marshal(map[string]int{
			"size":       req.Size,
			"iterations": req.Iterations,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		created := make([]jobs.Job, 0, len(nodes))
		for _, node := range nodes {
			job, err := s.state.CreateJob(jobs.CreateRequest{
				Type:         "compute.matrix_multiply",
				Input:        string(input),
				RequestedBy:  requestedBy,
				AssignedTo:   node.ID,
				Requirements: matrixJobRequirements(req.Size),
			})
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			created = append(created, job)
		}

		writeJSON(w, http.StatusCreated, buildClusterBenchmarkSummary(runID, requestedBy, created))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorAuth(w, r, false) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"jobs": s.state.Jobs()})
	case http.MethodPost:
		var req jobs.CreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Type == "" {
			http.Error(w, "type is required", http.StatusBadRequest)
			return
		}
		job, err := s.state.CreateJob(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, job)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorAuth(w, r, false) {
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	filters := modelCatalogFiltersFromRequest(r)
	summaries := modelSummaries(models.Catalog(), s.state.Jobs(), s.state.Nodes())
	filtered := filterAndSortModelSummaries(summaries, filters)
	writeJSON(w, http.StatusOK, map[string]any{
		"models":  filtered,
		"total":   len(summaries),
		"count":   len(filtered),
		"filters": filters,
	})
}

func modelCatalogFiltersFromRequest(r *http.Request) ModelCatalogFilters {
	query := r.URL.Query()
	return ModelCatalogFilters{
		Query:       firstNonEmptyString(query.Get("q"), query.Get("query")),
		Status:      query.Get("status"),
		Family:      query.Get("family"),
		CapableOnly: boolQuery(query.Get("capable")),
		Sort:        query.Get("sort"),
	}
}

func boolQuery(raw string) bool {
	raw = strings.TrimSpace(strings.ToLower(raw))
	return raw == "1" || raw == "true" || raw == "yes"
}

func (s *Server) handleModel(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorAuth(w, r, false) {
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/models/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 1 || len(parts) > 2 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	model, ok := models.Find(parts[0])
	if !ok {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 1 {
		s.handleModelDetail(w, r, model)
		return
	}
	action := parts[1]
	switch action {
	case "distributed-plan":
		s.handleModelDistributedPlan(w, r, model)
	case "distributed-rpc-plan":
		s.handleModelDistributedRPCPlan(w, r, model)
	case "distributed-rpc-readiness":
		s.handleModelDistributedRPCReadiness(w, r, model)
	case "distributed-rpc-generate":
		s.handleModelDistributedRPCGenerate(w, r, model)
	case "distributed-generate":
		s.handleModelDistributedGenerate(w, r, model)
	case "placement":
		s.handleModelPlacement(w, r, model)
	case "install":
		s.handleModelInstall(w, r, model)
	case "delete":
		s.handleModelDelete(w, r, model)
	case "repair":
		s.handleModelRepair(w, r, model)
	case "generate":
		s.handleModelGenerate(w, r, model)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleModelDistributedGenerate(w http.ResponseWriter, r *http.Request, model models.Model) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Prompt         string `json:"prompt"`
		ConversationID string `json:"conversation_id"`
		SystemPrompt   string `json:"system_prompt"`
		MaxTokens      int    `json:"max_tokens"`
		Temperature    string `json:"temperature"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		http.Error(w, "prompt is required", http.StatusBadRequest)
		return
	}
	plan := distributedModelPlan(model, s.state.Nodes())
	if !plan.Feasible {
		reason := "distributed model execution is not ready"
		if len(plan.Blockers) > 0 {
			reason = strings.Join(plan.Blockers, " | ")
		}
		writeJSON(w, http.StatusConflict, distributedGenerateConflictResponse{
			Error:  "distributed model generate is not executable",
			Reason: reason,
			Plan:   plan,
		})
		return
	}
	conversationID := strings.TrimSpace(req.ConversationID)
	if conversationID == "" {
		conversationID = newConversationID()
	}
	if activeJobID := activeGenerateJobForConversation(s.state.Jobs(), conversationID); activeJobID != "" {
		http.Error(w, "conversation already has an active generate job: "+activeJobID, http.StatusConflict)
		return
	}
	systemPrompt := strings.TrimSpace(req.SystemPrompt)
	if systemPrompt == "" {
		systemPrompt = modelDefaultSystemPrompt(model)
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = models.QualityPresetFor(model).MaxTokens
	}
	temperature := strings.TrimSpace(req.Temperature)
	if temperature == "" {
		temperature = models.QualityPresetFor(model).Temperature
	}
	conversation := appendConversationMessage(s.state, conversationID, model.ID, "distributed", systemPrompt, models.ChatMessage{
		Role:    "user",
		Content: req.Prompt,
	})
	effectiveSystemPrompt := systemPromptWithMemory(systemPrompt, model.ID, s.state)
	budgetedMessages := budgetConversationMessages(model, effectiveSystemPrompt, conversation.Messages, maxTokens)
	input, err := json.Marshal(models.DistributedGenerateInput{
		ModelID:        model.ID,
		Prompt:         req.Prompt,
		Messages:       budgetedMessages,
		SystemPrompt:   effectiveSystemPrompt,
		ConversationID: conversation.ID,
		MaxTokens:      maxTokens,
		Temperature:    temperature,
		Mode:           plan.Mode,
		Stages:         distributedStageInputs(plan.Stages),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	parent, err := s.state.CreateJob(jobs.CreateRequest{
		Type:        models.JobGenerateDistributed,
		Input:       string(input),
		RequestedBy: "dashboard-chat",
		Requirements: jobs.Requirements{
			CPUCores:    len(plan.Stages),
			MemoryBytes: model.MemoryBytes,
			DiskBytes:   model.DiskBytes,
			VRAMBytes:   model.VRAMBytes,
		},
		MaxAttempts:  1,
		NoAutoAssign: true,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	stageRequests, err := distributedStageJobRequests(parent, models.DistributedGenerateInput{
		ModelID:        model.ID,
		Prompt:         req.Prompt,
		Messages:       budgetedMessages,
		SystemPrompt:   effectiveSystemPrompt,
		ConversationID: conversation.ID,
		MaxTokens:      maxTokens,
		Temperature:    temperature,
		Mode:           plan.Mode,
		Stages:         distributedStageInputs(plan.Stages),
		Shards:         cdipShardManifest(model, plan).Shards,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stageJobs, err := s.state.CreateJobsBatch(stageRequests)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusAccepted, distributedGenerateResponse{
		Job:           parent,
		StageJobs:     stageJobs,
		Plan:          plan,
		ExecutableNow: plan.ExecutableNow,
	})
}

func (s *Server) handleModelDistributedPlan(w http.ResponseWriter, r *http.Request, model models.Model) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	plan := distributedModelPlan(model, s.state.Nodes())
	writeJSON(w, http.StatusOK, map[string]any{
		"plan":                plan,
		"cdip_proposal":       cdipPlanProposal(plan),
		"cdip_shard_manifest": cdipShardManifest(model, plan),
	})
}

func (s *Server) handleModelPlacement(w http.ResponseWriter, r *http.Request, model models.Model) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	summaries := modelSummaries([]models.Model{model}, s.state.Jobs(), s.state.Nodes())
	if len(summaries) == 0 {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"placement": modelPlacementPlan(summaries[0]),
	})
}

func (s *Server) handleModelDetail(w http.ResponseWriter, r *http.Request, model models.Model) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	summaries := modelSummaries([]models.Model{model}, s.state.Jobs(), s.state.Nodes())
	if len(summaries) == 0 {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"model": summaries[0],
	})
}

func (s *Server) handleModelInstall(w http.ResponseWriter, r *http.Request, model models.Model) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		NodeID string `json:"node_id"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	summaries := modelSummaries([]models.Model{model}, s.state.Jobs(), s.state.Nodes())
	if len(summaries) == 0 {
		http.Error(w, "model is not available", http.StatusNotFound)
		return
	}
	summary := summaries[0]
	if ok, reason := modelInstallEligibility(summary, req.NodeID); !ok {
		writeJSON(w, http.StatusConflict, modelInstallConflictResponse{
			Error:     "no eligible worker for model install",
			Reason:    reason,
			Placement: modelPlacementPlan(summary),
		})
		return
	}
	input, err := json.Marshal(models.InstallInput{ModelID: model.ID})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	job, err := s.state.CreateJob(jobs.CreateRequest{
		Type:        models.JobInstall,
		Input:       string(input),
		RequestedBy: "dashboard-models",
		AssignedTo:  req.NodeID,
		Requirements: jobs.Requirements{
			CPUCores:    1,
			MemoryBytes: model.MemoryBytes,
			DiskBytes:   model.DiskBytes,
			VRAMBytes:   model.VRAMBytes,
		},
		MaxAttempts: 1,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, job)
}

func (s *Server) handleModelDelete(w http.ResponseWriter, r *http.Request, model models.Model) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		NodeID string `json:"node_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.NodeID == "" {
		http.Error(w, "node_id is required", http.StatusBadRequest)
		return
	}
	summaries := modelSummaries([]models.Model{model}, s.state.Jobs(), s.state.Nodes())
	if len(summaries) == 0 || !modelInstalledOn(summaries[0], req.NodeID) {
		http.Error(w, "model is not installed on the selected worker", http.StatusConflict)
		return
	}
	if activeJobID := activeModelJobForNode(s.state.Jobs(), model.ID, req.NodeID); activeJobID != "" {
		http.Error(w, "model has an active job on the selected worker: "+activeJobID, http.StatusConflict)
		return
	}
	input, err := json.Marshal(models.DeleteInput{ModelID: model.ID})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	job, err := s.state.CreateJob(jobs.CreateRequest{
		Type:        models.JobDelete,
		Input:       string(input),
		RequestedBy: "dashboard-models",
		AssignedTo:  req.NodeID,
		MaxAttempts: 1,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, job)
}

func (s *Server) handleModelRepair(w http.ResponseWriter, r *http.Request, model models.Model) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		NodeID string `json:"node_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.NodeID == "" {
		http.Error(w, "node_id is required", http.StatusBadRequest)
		return
	}
	summaries := modelSummaries([]models.Model{model}, s.state.Jobs(), s.state.Nodes())
	if len(summaries) == 0 || !modelInstalledOn(summaries[0], req.NodeID) {
		http.Error(w, "model is not installed on the selected worker", http.StatusConflict)
		return
	}
	if activeJobID := activeModelJobForNode(s.state.Jobs(), model.ID, req.NodeID); activeJobID != "" {
		http.Error(w, "model has an active job on the selected worker: "+activeJobID, http.StatusConflict)
		return
	}
	input, err := json.Marshal(models.RepairInput{ModelID: model.ID})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	job, err := s.state.CreateJob(jobs.CreateRequest{
		Type:        models.JobRepair,
		Input:       string(input),
		RequestedBy: "dashboard-models",
		AssignedTo:  req.NodeID,
		Requirements: jobs.Requirements{
			CPUCores:    1,
			MemoryBytes: model.MemoryBytes,
			VRAMBytes:   model.VRAMBytes,
		},
		MaxAttempts: 1,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, job)
}

func (s *Server) handleModelGenerate(w http.ResponseWriter, r *http.Request, model models.Model) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		NodeID         string `json:"node_id"`
		Prompt         string `json:"prompt"`
		ConversationID string `json:"conversation_id"`
		SystemPrompt   string `json:"system_prompt"`
		MaxTokens      int    `json:"max_tokens"`
		Temperature    string `json:"temperature"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		http.Error(w, "prompt is required", http.StatusBadRequest)
		return
	}
	if req.NodeID == "" {
		http.Error(w, "node_id is required", http.StatusBadRequest)
		return
	}
	summaries := modelSummaries([]models.Model{model}, s.state.Jobs(), s.state.Nodes())
	if len(summaries) == 0 || !modelInstalledOn(summaries[0], req.NodeID) {
		http.Error(w, "model is not installed on the selected worker", http.StatusConflict)
		return
	}
	conversationID := strings.TrimSpace(req.ConversationID)
	if conversationID == "" {
		conversationID = newConversationID()
	}
	if activeJobID := activeGenerateJobForConversation(s.state.Jobs(), conversationID); activeJobID != "" {
		http.Error(w, "conversation already has an active generate job: "+activeJobID, http.StatusConflict)
		return
	}
	if !modelGeneratableOn(summaries[0], req.NodeID) {
		http.Error(w, modelGenerateBlockedReason(summaries[0], req.NodeID), http.StatusConflict)
		return
	}
	systemPrompt := strings.TrimSpace(req.SystemPrompt)
	if systemPrompt == "" {
		systemPrompt = modelDefaultSystemPrompt(model)
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = models.QualityPresetFor(model).MaxTokens
	}
	temperature := strings.TrimSpace(req.Temperature)
	if temperature == "" {
		temperature = models.QualityPresetFor(model).Temperature
	}
	conversation := appendConversationMessage(s.state, conversationID, model.ID, req.NodeID, systemPrompt, models.ChatMessage{
		Role:    "user",
		Content: req.Prompt,
	})
	effectiveSystemPrompt := systemPromptWithMemory(systemPrompt, model.ID, s.state)
	budgetedMessages := budgetConversationMessages(model, effectiveSystemPrompt, conversation.Messages, maxTokens)
	input, err := json.Marshal(models.GenerateInput{
		ModelID:        model.ID,
		Prompt:         req.Prompt,
		Messages:       budgetedMessages,
		SystemPrompt:   effectiveSystemPrompt,
		ConversationID: conversation.ID,
		MaxTokens:      maxTokens,
		Temperature:    temperature,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	job, err := s.state.CreateJob(jobs.CreateRequest{
		Type:        models.JobGenerate,
		Input:       string(input),
		RequestedBy: "dashboard-chat",
		AssignedTo:  req.NodeID,
		Requirements: jobs.Requirements{
			CPUCores:    1,
			MemoryBytes: model.MemoryBytes,
			DiskBytes:   model.DiskBytes,
			VRAMBytes:   model.VRAMBytes,
		},
		MaxAttempts: 1,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, job)
}

type ModelDistributedRPCReadiness struct {
	Ready        bool                    `json:"ready"`
	ModelID      string                  `json:"model_id"`
	NodeID       string                  `json:"node_id,omitempty"`
	Blockers     []string                `json:"blockers,omitempty"`
	RPCEndpoints []string                `json:"rpc_endpoints,omitempty"`
	RPCPool      RuntimeRPCPoolSummary   `json:"rpc_pool"`
	Plan         ModelDistributedRPCPlan `json:"plan"`
}

type ModelDistributedRPCBackend struct {
	NodeID       string `json:"node_id"`
	NodeName     string `json:"node_name"`
	Runtime      string `json:"runtime"`
	Endpoint     string `json:"endpoint"`
	HealthStatus string `json:"health_status,omitempty"`
	LatencyMS    int64  `json:"latency_ms,omitempty"`
	Error        string `json:"error,omitempty"`
}

type ModelDistributedRPCPlan struct {
	ModelID             string                       `json:"model_id"`
	Mode                string                       `json:"mode"`
	CoordinatorNodeID   string                       `json:"coordinator_node_id,omitempty"`
	CoordinatorNodeName string                       `json:"coordinator_node_name,omitempty"`
	ExecutableNow       bool                         `json:"executable_now"`
	Blockers            []string                     `json:"blockers,omitempty"`
	Warnings            []string                     `json:"warnings,omitempty"`
	RPCEndpoints        []string                     `json:"rpc_endpoints,omitempty"`
	Backends            []ModelDistributedRPCBackend `json:"backends,omitempty"`
	RPCPool             RuntimeRPCPoolSummary        `json:"rpc_pool"`
	HealthChecked       bool                         `json:"health_checked"`
}

func (s *Server) handleModelDistributedRPCPlan(w http.ResponseWriter, r *http.Request, model models.Model) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	nodeID := strings.TrimSpace(r.URL.Query().Get("node_id"))
	checkHealth := boolQuery(firstNonEmptyString(r.URL.Query().Get("check"), r.URL.Query().Get("health")))
	writeJSON(w, http.StatusOK, s.modelDistributedRPCPlan(r.Context(), model, nodeID, checkHealth, time.Second))
}

func (s *Server) handleModelDistributedRPCReadiness(w http.ResponseWriter, r *http.Request, model models.Model) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	nodeID := strings.TrimSpace(r.URL.Query().Get("node_id"))
	readiness := s.modelDistributedRPCReadiness(model, nodeID)
	writeJSON(w, http.StatusOK, readiness)
}

func (s *Server) modelDistributedRPCReadiness(model models.Model, nodeID string) ModelDistributedRPCReadiness {
	plan := s.modelDistributedRPCPlan(context.Background(), model, nodeID, false, 0)
	readiness := ModelDistributedRPCReadiness{
		Ready:        plan.ExecutableNow,
		ModelID:      plan.ModelID,
		NodeID:       plan.CoordinatorNodeID,
		Blockers:     append([]string(nil), plan.Blockers...),
		RPCEndpoints: append([]string(nil), plan.RPCEndpoints...),
		RPCPool:      plan.RPCPool,
		Plan:         plan,
	}
	return readiness
}

func (s *Server) modelDistributedRPCPlan(ctx context.Context, model models.Model, nodeID string, checkHealth bool, healthTimeout time.Duration) ModelDistributedRPCPlan {
	nodes := s.state.Nodes()
	summary, workers, endpoints := runtimeRPCPoolReport(nodes)
	health := s.state.RPCHealth()
	plan := ModelDistributedRPCPlan{
		ModelID:      model.ID,
		Mode:         "llama.cpp-rpc",
		RPCPool:      summary,
		RPCEndpoints: usableRPCEndpoints(endpoints, health, false),
	}
	if quarantined := quarantinedRPCEndpoints(endpoints, health); len(quarantined) > 0 && !checkHealth {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf("%d llama.cpp rpc endpoint(s) are quarantined after repeated health failures", len(quarantined)))
	}
	healthByEndpoint := map[string]RuntimeRPCSmokeResult{}
	recordByEndpoint := rpcHealthByEndpoint(health)
	if checkHealth && len(endpoints) > 0 {
		if healthTimeout <= 0 {
			healthTimeout = time.Second
		}
		plan.HealthChecked = true
		report := smokeRuntimeRPCEndpoints(ctx, usableRPCEndpoints(endpoints, health, true), healthTimeout)
		s.recordRPCHealthReport(workers, report)
		readyEndpoints := make([]string, 0, len(endpoints))
		for _, result := range report.Results {
			healthByEndpoint[result.Endpoint] = result
			if result.Ready {
				readyEndpoints = append(readyEndpoints, result.Endpoint)
			}
		}
		plan.RPCEndpoints = usableRPCEndpoints(readyEndpoints, s.state.RPCHealth(), false)
		if report.Ready == 0 {
			plan.Blockers = append(plan.Blockers, "no reachable llama.cpp rpc endpoints passed health check")
		}
		if report.Failed > 0 {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("%d llama.cpp rpc endpoint(s) failed health check and will be excluded", report.Failed))
		}
	}
	for _, worker := range workers {
		endpoint := strings.TrimSpace(worker.RPC.Endpoint)
		if !worker.RuntimeReady || !worker.RPC.Ready || endpoint == "" {
			continue
		}
		backend := ModelDistributedRPCBackend{
			NodeID:       worker.NodeID,
			NodeName:     worker.NodeName,
			Runtime:      worker.Runtime,
			Endpoint:     endpoint,
			HealthStatus: "unchecked",
		}
		if !plan.HealthChecked {
			if record, ok := recordByEndpoint[endpoint]; ok && rpcEndpointQuarantined(record) {
				backend.HealthStatus = "quarantined"
				backend.Error = fmt.Sprintf("endpoint has %d consecutive health failures", record.ConsecutiveFailures)
			}
		}
		if plan.HealthChecked {
			backend.HealthStatus = "failed"
			if result, ok := healthByEndpoint[endpoint]; ok {
				backend.LatencyMS = result.LatencyMS
				backend.Error = result.Error
				if result.Ready {
					backend.HealthStatus = "ready"
				}
			} else {
				backend.Error = "endpoint was not checked"
			}
		}
		plan.Backends = append(plan.Backends, backend)
	}
	if len(plan.RPCEndpoints) == 0 && !plan.HealthChecked {
		plan.Blockers = append(plan.Blockers, "no active llama.cpp rpc endpoints are available")
	}
	summaries := modelSummaries([]models.Model{model}, s.state.Jobs(), s.state.Nodes())
	if len(summaries) == 0 {
		plan.Blockers = append(plan.Blockers, "model is not available")
		return plan
	}
	modelSummary := summaries[0]
	plan.CoordinatorNodeID = strings.TrimSpace(nodeID)
	if plan.CoordinatorNodeID == "" {
		for _, install := range modelSummary.Installed {
			if install.GenerateReady {
				plan.CoordinatorNodeID = install.NodeID
				break
			}
		}
	}
	if plan.CoordinatorNodeID != "" {
		for _, node := range nodes {
			if node.ID == plan.CoordinatorNodeID {
				plan.CoordinatorNodeName = nodeDisplayName(node)
				break
			}
		}
	}
	if plan.CoordinatorNodeID == "" {
		plan.Blockers = append(plan.Blockers, "select a worker with this model installed")
	} else if !modelInstalledOn(modelSummary, plan.CoordinatorNodeID) {
		plan.Blockers = append(plan.Blockers, "model is not installed on the selected worker")
	} else if !modelGeneratableOn(modelSummary, plan.CoordinatorNodeID) {
		if reason := modelGenerateBlockedReason(modelSummary, plan.CoordinatorNodeID); reason != "" {
			plan.Blockers = append(plan.Blockers, reason)
		} else {
			plan.Blockers = append(plan.Blockers, "selected worker cannot generate this model")
		}
	}
	plan.ExecutableNow = len(plan.Blockers) == 0
	return plan
}

func distributedRPCExecutionPlanForJob(plan ModelDistributedRPCPlan) protocol.DistributedRPCExecutionPlan {
	backends := make([]protocol.DistributedRPCBackend, 0, len(plan.Backends))
	for _, backend := range plan.Backends {
		if !rpcPlanContainsEndpoint(plan.RPCEndpoints, backend.Endpoint) {
			continue
		}
		backends = append(backends, protocol.DistributedRPCBackend{
			NodeID:       backend.NodeID,
			NodeName:     backend.NodeName,
			Runtime:      backend.Runtime,
			Endpoint:     backend.Endpoint,
			HealthStatus: backend.HealthStatus,
			LatencyMS:    backend.LatencyMS,
			Error:        backend.Error,
		})
	}
	return protocol.DistributedRPCExecutionPlan{
		ID:                  newJobID(),
		Protocol:            protocol.DistributedRPCProtocol,
		ProtocolVersion:     protocol.DistributedRPCProtocolVersion,
		PlanSchemaVersion:   protocol.DistributedRPCPlanSchemaVersion,
		Mode:                plan.Mode,
		ModelID:             plan.ModelID,
		CoordinatorNodeID:   plan.CoordinatorNodeID,
		CoordinatorNodeName: plan.CoordinatorNodeName,
		RPCEndpoints:        append([]string(nil), plan.RPCEndpoints...),
		Backends:            backends,
		HealthChecked:       plan.HealthChecked,
		PlannedAt:           time.Now().UTC().Format(time.RFC3339),
	}
}

func rpcPlanContainsEndpoint(endpoints []string, endpoint string) bool {
	for _, candidate := range endpoints {
		if candidate == endpoint {
			return true
		}
	}
	return false
}

func rpcWorkersByEndpoint(workers []RuntimeRPCPoolWorker) map[string]RuntimeRPCPoolWorker {
	out := make(map[string]RuntimeRPCPoolWorker, len(workers))
	for _, worker := range workers {
		endpoint := strings.TrimSpace(worker.RPC.Endpoint)
		if endpoint == "" {
			continue
		}
		if _, exists := out[endpoint]; !exists {
			out[endpoint] = worker
		}
	}
	return out
}

func (s *Server) handleModelDistributedRPCGenerate(w http.ResponseWriter, r *http.Request, model models.Model) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		NodeID         string `json:"node_id"`
		Prompt         string `json:"prompt"`
		ConversationID string `json:"conversation_id"`
		SystemPrompt   string `json:"system_prompt"`
		MaxTokens      int    `json:"max_tokens"`
		Temperature    string `json:"temperature"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		http.Error(w, "prompt is required", http.StatusBadRequest)
		return
	}
	plan := s.modelDistributedRPCPlan(r.Context(), model, req.NodeID, true, time.Second)
	if !plan.ExecutableNow {
		reason := "distributed RPC model generate is not executable"
		if len(plan.Blockers) > 0 {
			reason = strings.Join(plan.Blockers, " | ")
		}
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":  "distributed RPC model generate is not executable",
			"reason": reason,
			"plan":   plan,
		})
		return
	}
	conversationID := strings.TrimSpace(req.ConversationID)
	if conversationID == "" {
		conversationID = newConversationID()
	}
	if activeJobID := activeGenerateJobForConversation(s.state.Jobs(), conversationID); activeJobID != "" {
		http.Error(w, "conversation already has an active generate job: "+activeJobID, http.StatusConflict)
		return
	}
	systemPrompt := strings.TrimSpace(req.SystemPrompt)
	if systemPrompt == "" {
		systemPrompt = modelDefaultSystemPrompt(model)
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = models.QualityPresetFor(model).MaxTokens
	}
	temperature := strings.TrimSpace(req.Temperature)
	if temperature == "" {
		temperature = models.QualityPresetFor(model).Temperature
	}
	conversation := appendConversationMessage(s.state, conversationID, model.ID, plan.CoordinatorNodeID, systemPrompt, models.ChatMessage{
		Role:    "user",
		Content: req.Prompt,
	})
	effectiveSystemPrompt := systemPromptWithMemory(systemPrompt, model.ID, s.state)
	budgetedMessages := budgetConversationMessages(model, effectiveSystemPrompt, conversation.Messages, maxTokens)
	executionPlan := distributedRPCExecutionPlanForJob(plan)
	if err := protocol.ValidateDistributedRPCExecutionPlan(executionPlan, model.ID, plan.CoordinatorNodeID); err != nil {
		http.Error(w, "invalid distributed rpc execution plan: "+err.Error(), http.StatusInternalServerError)
		return
	}
	input, err := json.Marshal(models.DistributedRPCGenerateInput{
		ModelID:        model.ID,
		Prompt:         req.Prompt,
		Messages:       budgetedMessages,
		SystemPrompt:   effectiveSystemPrompt,
		ConversationID: conversation.ID,
		MaxTokens:      maxTokens,
		Temperature:    temperature,
		RPCEndpoints:   plan.RPCEndpoints,
		ExecutionPlan:  executionPlan,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	job, err := s.state.CreateJob(jobs.CreateRequest{
		Type:        models.JobGenerateDistributedRPC,
		Input:       string(input),
		RequestedBy: "dashboard-chat-rpc",
		AssignedTo:  plan.CoordinatorNodeID,
		Requirements: jobs.Requirements{
			CPUCores:    1,
			MemoryBytes: model.MemoryBytes,
			DiskBytes:   model.DiskBytes,
			VRAMBytes:   model.VRAMBytes,
		},
		MaxAttempts: 1,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, job)
}

func (s *Server) handleConversations(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorAuth(w, r, false) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"conversations": recentConversations(s.state, 100),
	})
}

func (s *Server) handleConversation(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorAuth(w, r, false) {
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/conversations/"), "/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	conversations, ok := s.state.(conversationStore)
	if !ok {
		http.Error(w, "conversation persistence is not available", http.StatusNotImplemented)
		return
	}
	if r.Method == http.MethodDelete {
		if !conversations.DeleteConversation(id) {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
		return
	}
	conversation, ok := conversations.Conversation(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, conversation)
}

func (s *Server) handleMemories(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorAuth(w, r, false) {
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	modelID := strings.TrimSpace(r.URL.Query().Get("model_id"))
	if r.Method == http.MethodPost {
		memory, err := s.decodeMemoryRequest(r, "")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		saved, err := s.saveMemory(memory)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, saved)
		return
	}
	if r.Method == http.MethodDelete {
		if modelID == "" {
			http.Error(w, "model_id is required", http.StatusBadRequest)
			return
		}
		memories, ok := s.state.(memoryStore)
		if !ok {
			http.Error(w, "memory persistence is not available", http.StatusNotImplemented)
			return
		}
		deleted := memories.DeleteMemoriesByModel(modelID)
		writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"memories": memoriesForModel(s.state, modelID),
	})
}

func (s *Server) handleMemory(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorAuth(w, r, false) {
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/memories/"), "/")
	if id == "preview" {
		s.handleMemoryPreview(w, r)
		return
	}
	if id == "" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodDelete && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	memories, ok := s.state.(memoryStore)
	if !ok {
		http.Error(w, "memory persistence is not available", http.StatusNotImplemented)
		return
	}
	if r.Method == http.MethodPost {
		memory, err := s.decodeMemoryRequest(r, id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		saved, err := memories.UpsertMemory(memory)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, saved)
		return
	}
	if !memories.DeleteMemory(id) {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) decodeMemoryRequest(r *http.Request, id string) (Memory, error) {
	var req struct {
		ModelID        string `json:"model_id"`
		Key            string `json:"key"`
		Value          string `json:"value"`
		Source         string `json:"source"`
		ConversationID string `json:"conversation_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return Memory{}, err
	}
	return Memory{
		ID:             strings.TrimSpace(id),
		ModelID:        req.ModelID,
		Key:            req.Key,
		Value:          req.Value,
		Source:         req.Source,
		ConversationID: req.ConversationID,
	}, nil
}

func (s *Server) saveMemory(memory Memory) (Memory, error) {
	memories, ok := s.state.(memoryStore)
	if !ok {
		return Memory{}, errors.New("memory persistence is not available")
	}
	return memories.UpsertMemory(memory)
}

func (s *Server) handleMemoryPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	modelID := strings.TrimSpace(r.URL.Query().Get("model_id"))
	model, ok := models.Find(modelID)
	if !ok {
		http.Error(w, "unknown model", http.StatusNotFound)
		return
	}
	systemPrompt := strings.TrimSpace(r.URL.Query().Get("system_prompt"))
	conversationID := strings.TrimSpace(r.URL.Query().Get("conversation_id"))
	draftPrompt := strings.TrimSpace(r.URL.Query().Get("prompt"))
	maxTokens := intQuery(r, "max_tokens", models.QualityPresetFor(model).MaxTokens)
	if systemPrompt == "" {
		systemPrompt = modelDefaultSystemPrompt(model)
	}
	messages := []models.ChatMessage{}
	if conversations, ok := s.state.(conversationStore); ok && conversationID != "" {
		if conversation, found := conversations.Conversation(conversationID); found {
			messages = append(messages, conversation.Messages...)
			if systemPrompt == "" && conversation.SystemPrompt != "" {
				systemPrompt = conversation.SystemPrompt
			}
		}
	}
	if draftPrompt != "" {
		messages = append(messages, models.ChatMessage{Role: "user", Content: draftPrompt})
	}
	memories := memoriesForModel(s.state, model.ID)
	effectiveSystemPrompt := systemPromptWithMemory(systemPrompt, model.ID, s.state)
	preview := promptContextPreview(model, effectiveSystemPrompt, messages, maxTokens)
	writeJSON(w, http.StatusOK, map[string]any{
		"model_id":                model.ID,
		"memories":                memories,
		"memory_context":          memoryContext(model.ID, memories),
		"effective_system_prompt": effectiveSystemPrompt,
		"context":                 preview,
	})
}

func intQuery(r *http.Request, key string, fallback int) int {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

func (s *Server) handleJob(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/jobs/")
	if path == "" {
		http.NotFound(w, r)
		return
	}

	parts := strings.Split(path, "/")
	jobID := parts[0]
	if len(parts) == 2 && parts[1] == "complete" {
		s.handleJobComplete(w, r, jobID)
		return
	}
	if len(parts) == 2 && parts[1] == "progress" {
		s.handleJobProgress(w, r, jobID)
		return
	}
	if len(parts) == 2 && parts[1] == "cancel" {
		s.handleJobCancel(w, r, jobID)
		return
	}
	if len(parts) != 1 || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	if !s.requireOperatorAuth(w, r, false) {
		return
	}

	job, ok := s.state.Job(jobID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleJobCancel(w http.ResponseWriter, r *http.Request, jobID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireOperatorAuth(w, r, false) {
		return
	}

	job, ok := s.state.CancelJob(jobID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleJobComplete(w http.ResponseWriter, r *http.Request, jobID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req jobs.CompleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.NodeID == "" {
		http.Error(w, "node_id is required", http.StatusBadRequest)
		return
	}

	if job, handled, ok := s.handleCDIPStageComplete(req, jobID); handled && ok {
		writeJSON(w, http.StatusOK, job)
		return
	} else if handled {
		http.Error(w, "invalid CDIP stage transition", http.StatusConflict)
		return
	}

	job, ok := s.state.CompleteJob(jobID, req)
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

type cdipStageWorkerResult struct {
	Kind string `json:"kind"`
}

func (s *Server) handleCDIPStageComplete(req jobs.CompleteRequest, jobID string) (jobs.Job, bool, bool) {
	job, ok := s.state.Job(jobID)
	if !ok || job.Type != models.JobGenerateStage || job.AssignedTo != req.NodeID {
		return jobs.Job{}, false, false
	}
	if strings.TrimSpace(req.Error) != "" {
		updated, ok := s.state.UpdateCDIPStageState(jobID, cdip.StageFailed, req.Error)
		return updated, true, ok
	}
	var result cdipStageWorkerResult
	if err := json.Unmarshal([]byte(req.Result), &result); err != nil || result.Kind != "cdip.stage_ready" {
		return jobs.Job{}, false, false
	}
	updated, ok := s.state.UpdateCDIPStageState(jobID, cdip.StageReady, "worker reported cdip.stage_ready")
	return updated, true, ok
}

func (s *Server) handleJobProgress(w http.ResponseWriter, r *http.Request, jobID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req jobs.ProgressRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.NodeID == "" {
		http.Error(w, "node_id is required", http.StatusBadRequest)
		return
	}
	job, ok := s.state.UpdateJobProgress(jobID, req)
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleCDIPStage(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorAuth(w, r, false) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/cdip/stages/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		http.NotFound(w, r)
		return
	}
	next, ok := cdipStageAction(parts[1])
	if !ok {
		http.NotFound(w, r)
		return
	}
	var req struct {
		Detail string `json:"detail"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	job, ok := s.state.UpdateCDIPStageState(parts[0], next, req.Detail)
	if !ok {
		http.Error(w, "invalid CDIP stage transition", http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"job":        job,
		"cdip_state": job.CDIPState,
	})
}

func (s *Server) handleCDIPJob(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorAuth(w, r, false) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/cdip/jobs/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	switch parts[1] {
	case "advance":
		var req struct {
			Step   uint64 `json:"step"`
			Output string `json:"output"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&req)
		}
		result, err := advanceCDIPDistributedJob(s.state, s.cdipActivationTransport, parts[0], req.Step, req.Output)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusAccepted, result)
	case "prepare":
		result, err := prepareCDIPDistributedJob(s.state, parts[0])
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusAccepted, result)
	case "prefill":
		result, err := startCDIPPrefill(s.state, parts[0])
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusAccepted, result)
	case "decode":
		var req struct {
			Step uint64 `json:"step"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&req)
		}
		result, err := startCDIPDecode(s.state, s.cdipActivationTransport, parts[0], req.Step)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusAccepted, result)
	case "complete":
		var req struct {
			Output string `json:"output"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&req)
		}
		result, err := completeCDIPDistributedJob(s.state, parts[0], req.Output)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusAccepted, result)
	case "mock-run":
		result, err := runCDIPMockCoordinator(s.state, parts[0])
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusAccepted, result)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleCDIPActivation(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/cdip/activations/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] != "frames" {
		http.NotFound(w, r)
		return
	}
	parentJobID := parts[0]
	stageJobID := parts[1]
	stageJob, ok := s.validateCDIPActivationStream(parentJobID, stageJobID)
	if !ok {
		http.Error(w, "CDIP activation stream not found", http.StatusNotFound)
		return
	}
	if !s.requireCDIPActivationAuth(w, r, stageJob) {
		return
	}
	stream := transport.StreamID{ParentJobID: parentJobID, StageJobID: stageJobID}

	switch r.Method {
	case http.MethodPost:
		var frame transport.ActivationFrame
		if err := json.NewDecoder(r.Body).Decode(&frame); err != nil {
			http.Error(w, "invalid activation frame", http.StatusBadRequest)
			return
		}
		if frame.Header.ParentJobID != parentJobID || frame.Header.StageJobID != stageJobID {
			http.Error(w, "activation frame stream does not match URL", http.StatusBadRequest)
			return
		}
		if err := frame.Validate(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writer, err := s.cdipActivationTransport.OpenWriter(r.Context(), stream, stageJob.AssignedTo)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if err := writer.Send(r.Context(), frame); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"stream": map[string]string{
				"parent_job_id": parentJobID,
				"stage_job_id":  stageJobID,
			},
			"sequence": frame.Header.Sequence,
			"bytes":    len(frame.Payload),
		})
	case http.MethodGet:
		timeout := time.Duration(intQuery(r, "timeout_ms", 250)) * time.Millisecond
		if timeout <= 0 {
			timeout = 250 * time.Millisecond
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		reader, err := s.cdipActivationTransport.OpenReader(ctx, stream)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		frame, err := reader.Receive(ctx)
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusOK, frame)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) validateCDIPActivationStream(parentJobID string, stageJobID string) (jobs.Job, bool) {
	parent, ok := s.state.Job(parentJobID)
	if !ok || parent.Type != models.JobGenerateDistributed {
		return jobs.Job{}, false
	}
	stageJob, ok := s.state.Job(stageJobID)
	if !ok || stageJob.Type != models.JobGenerateStage || stageJob.CDIPParentJobID != parentJobID {
		return jobs.Job{}, false
	}
	return stageJob, true
}

func (s *Server) requireCDIPActivationAuth(w http.ResponseWriter, r *http.Request, stageJob jobs.Job) bool {
	if s.hasOperatorAuth(r) {
		return true
	}
	nodeID := strings.TrimSpace(r.Header.Get("X-CMesh-Node-ID"))
	if nodeID == "" {
		nodeID = strings.TrimSpace(r.URL.Query().Get("node_id"))
	}
	if nodeID == "" {
		http.Error(w, "operator token or worker node id required", http.StatusUnauthorized)
		return false
	}
	for _, allowed := range cdipActivationAllowedNodes(stageJob) {
		if nodeID == allowed {
			return true
		}
	}
	http.Error(w, "worker is not allowed to access this activation stream", http.StatusForbidden)
	return false
}

func cdipActivationAllowedNodes(stageJob jobs.Job) []string {
	allowed := []string{}
	if strings.TrimSpace(stageJob.AssignedTo) != "" {
		allowed = append(allowed, stageJob.AssignedTo)
	}
	var input models.DistributedStageJobInput
	if err := json.Unmarshal([]byte(stageJob.Input), &input); err == nil {
		if strings.TrimSpace(input.DownstreamNodeID) != "" {
			allowed = append(allowed, input.DownstreamNodeID)
		}
		if strings.TrimSpace(input.UpstreamNodeID) != "" {
			allowed = append(allowed, input.UpstreamNodeID)
		}
	}
	return allowed
}

func cdipStageAction(action string) (cdip.StageState, bool) {
	switch action {
	case "prepare":
		return cdip.StagePreparing, true
	case "ready":
		return cdip.StageReady, true
	case "prefill":
		return cdip.StagePrefill, true
	case "decode":
		return cdip.StageDecode, true
	case "complete":
		return cdip.StageCompleted, true
	case "abort":
		return cdip.StageAborted, true
	case "fail":
		return cdip.StageFailed, true
	default:
		return "", false
	}
}

func (s *Server) handleWorkerRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/workers/")
	parts := strings.Split(path, "/")
	if len(parts) == 3 && parts[1] == "jobs" && parts[2] == "next" {
		s.handleWorkerNextJob(w, r, parts[0])
		return
	}
	if len(parts) == 2 && parts[1] == "model-cleanup" {
		s.handleWorkerModelCleanup(w, r, parts[0])
		return
	}
	http.NotFound(w, r)
}

func (s *Server) handleWorkerModelCleanup(w http.ResponseWriter, r *http.Request, nodeID string) {
	if !s.requireOperatorAuth(w, r, false) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		http.Error(w, "node_id is required", http.StatusBadRequest)
		return
	}
	node, ok := findNodeByID(s.state.Nodes(), nodeID)
	if !ok || node.Role != cluster.NodeRoleWorker {
		http.Error(w, "worker not found", http.StatusNotFound)
		return
	}
	if node.Status != cluster.NodeStatusOnline {
		http.Error(w, "worker is not online", http.StatusConflict)
		return
	}
	input, err := json.Marshal(models.CleanupInput{Scope: "cache"})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	job, err := s.state.CreateJob(jobs.CreateRequest{
		Type:        models.JobCleanup,
		Input:       string(input),
		RequestedBy: "dashboard-workers",
		AssignedTo:  nodeID,
		Requirements: jobs.Requirements{
			CPUCores: 1,
		},
		MaxAttempts: 1,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, job)
}

func findNodeByID(nodes []cluster.Node, nodeID string) (cluster.Node, bool) {
	for _, node := range nodes {
		if node.ID == nodeID {
			return node, true
		}
	}
	return cluster.Node{}, false
}

func (s *Server) handleWorkerNextJob(w http.ResponseWriter, r *http.Request, nodeID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	job, ok := s.state.NextJobForWorker(nodeID)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"job": nil})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"job": job})
}

func (s *Server) handleWorkerJoin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req membership.JoinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.NodeName == "" {
		http.Error(w, "node_name is required", http.StatusBadRequest)
		return
	}
	if s.joinToken != "" && req.JoinToken != s.joinToken {
		http.Error(w, "invalid join token", http.StatusUnauthorized)
		return
	}

	resp := s.state.RegisterWorker(req)
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleWorkerHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var hb membership.Heartbeat
	if err := json.NewDecoder(r.Body).Decode(&hb); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if hb.NodeID == "" {
		http.Error(w, "node_id is required", http.StatusBadRequest)
		return
	}

	if !s.state.Heartbeat(hb) {
		http.Error(w, "unknown node", http.StatusNotFound)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleWorkerLeave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req membership.LeaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.NodeID == "" {
		http.Error(w, "node_id is required", http.StatusBadRequest)
		return
	}

	if !s.state.MarkWorkerOffline(req.NodeID) {
		http.Error(w, "unknown node", http.StatusNotFound)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "offline"})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		fmt.Fprintf(w, `{"error":"%s"}`, err)
	}
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}

func onlineWorkerNodes(nodes []cluster.Node) []cluster.Node {
	out := make([]cluster.Node, 0, len(nodes))
	for _, node := range nodes {
		if node.Role == cluster.NodeRoleWorker && node.Status == cluster.NodeStatusOnline {
			out = append(out, node)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out
}

func offlineWorkerCount(nodes []cluster.Node) int {
	var count int
	for _, node := range nodes {
		if node.Role == cluster.NodeRoleWorker && node.Status != cluster.NodeStatusOnline {
			count++
		}
	}
	return count
}

func nodesByID(nodes []cluster.Node) map[string]cluster.Node {
	out := make(map[string]cluster.Node, len(nodes))
	for _, node := range nodes {
		out[node.ID] = node
	}
	return out
}

func recentJobs(in []jobs.Job, limit int) []jobs.Job {
	out := append([]jobs.Job(nil), in...)
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func recentChatJobs(in []jobs.Job, limit int) []jobs.Job {
	out := make([]jobs.Job, 0, len(in))
	for _, job := range in {
		if job.Type != models.JobGenerate && job.Type != models.JobGenerateDistributedRPC {
			continue
		}
		if job.RequestedBy != "dashboard-chat" && job.RequestedBy != "dashboard-chat-rpc" {
			continue
		}
		if job.Status != jobs.StatusSucceeded || strings.TrimSpace(job.Result) == "" {
			continue
		}
		out = append(out, job)
	}
	return recentJobs(out, limit)
}

type DistributedRunSummary struct {
	JobID               string
	Status              jobs.Status
	ModelID             string
	PlanID              string
	Mode                string
	CoordinatorNodeID   string
	CoordinatorNodeName string
	Protocol            string
	ProtocolVersion     int
	SchemaVersion       int
	Runtime             string
	RuntimeVersion      string
	WorkerRuntime       string
	Endpoints           []string
	EndpointCount       int
	DurationMS          int64
	Output              string
	Error               string
	CreatedAt           time.Time
	FinishedAt          time.Time
}

func distributedRunSummaries(in []jobs.Job, limit int) []DistributedRunSummary {
	out := make([]DistributedRunSummary, 0)
	for _, job := range in {
		if job.Type != models.JobGenerateDistributedRPC {
			continue
		}
		summary := DistributedRunSummary{
			JobID:      job.ID,
			Status:     job.Status,
			Error:      firstNonEmptyString(job.Error, job.LastFailure),
			CreatedAt:  job.CreatedAt,
			FinishedAt: job.FinishedAt,
		}
		var input models.DistributedRPCGenerateInput
		if err := json.Unmarshal([]byte(job.Input), &input); err == nil {
			summary.ModelID = input.ModelID
			summary.PlanID = input.ExecutionPlan.ID
			summary.Mode = input.ExecutionPlan.Mode
			summary.CoordinatorNodeID = input.ExecutionPlan.CoordinatorNodeID
			summary.CoordinatorNodeName = input.ExecutionPlan.CoordinatorNodeName
			summary.Protocol = input.ExecutionPlan.Protocol
			summary.ProtocolVersion = input.ExecutionPlan.ProtocolVersion
			summary.SchemaVersion = input.ExecutionPlan.PlanSchemaVersion
			summary.Endpoints = append([]string(nil), input.ExecutionPlan.RPCEndpoints...)
			summary.EndpointCount = len(input.ExecutionPlan.RPCEndpoints)
		}
		if result, ok := distributedRPCExecutionResult(job); ok {
			summary.PlanID = firstNonEmptyString(result.PlanID, summary.PlanID)
			summary.Protocol = firstNonEmptyString(result.Protocol, summary.Protocol)
			if result.ProtocolVersion != 0 {
				summary.ProtocolVersion = result.ProtocolVersion
			}
			if result.PlanSchemaVersion != 0 {
				summary.SchemaVersion = result.PlanSchemaVersion
			}
			summary.Runtime = result.Runtime
			summary.RuntimeVersion = result.RuntimeVersion
			summary.WorkerRuntime = result.WorkerRuntime
			summary.Endpoints = append([]string(nil), result.RPCEndpoints...)
			summary.EndpointCount = result.RPCEndpointCount
			if summary.EndpointCount == 0 {
				summary.EndpointCount = len(result.RPCEndpoints)
			}
			summary.DurationMS = result.DurationMS
			summary.Output = result.Output
		}
		out = append(out, summary)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func activeGenerateJobForConversation(in []jobs.Job, conversationID string) string {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return ""
	}
	for _, job := range in {
		if job.Type != models.JobGenerate && job.Type != models.JobGenerateDistributedRPC && job.Type != models.JobGenerateDistributed {
			continue
		}
		if job.Status != jobs.StatusQueued && job.Status != jobs.StatusScheduled && job.Status != jobs.StatusRunning {
			continue
		}
		if jobConversationID(job) == conversationID {
			return job.ID
		}
	}
	return ""
}

func jobConversationID(job jobs.Job) string {
	switch job.Type {
	case models.JobGenerate:
		var input models.GenerateInput
		if err := json.Unmarshal([]byte(job.Input), &input); err != nil {
			return ""
		}
		return input.ConversationID
	case models.JobGenerateDistributedRPC:
		var input models.DistributedRPCGenerateInput
		if err := json.Unmarshal([]byte(job.Input), &input); err != nil {
			return ""
		}
		return input.ConversationID
	case models.JobGenerateDistributed:
		var input models.DistributedGenerateInput
		if err := json.Unmarshal([]byte(job.Input), &input); err != nil {
			return ""
		}
		return input.ConversationID
	default:
		return ""
	}
}

func activeModelJobForNode(in []jobs.Job, modelID string, nodeID string) string {
	modelID = strings.TrimSpace(modelID)
	nodeID = strings.TrimSpace(nodeID)
	if modelID == "" || nodeID == "" {
		return ""
	}
	for _, job := range in {
		if job.AssignedTo != nodeID {
			continue
		}
		if job.Status != jobs.StatusQueued && job.Status != jobs.StatusScheduled && job.Status != jobs.StatusRunning {
			continue
		}
		activeModelID, ok := jobModelID(job)
		if ok && activeModelID == modelID {
			return job.ID
		}
	}
	return ""
}

func maxClusterBenchmarkGFLOPS(in []ClusterBenchmarkSummary) float64 {
	var maxValue float64
	for _, summary := range in {
		if summary.TotalGFLOPS > maxValue {
			maxValue = summary.TotalGFLOPS
		}
	}
	return maxValue
}

func clusterBenchmarkSummaries(in []jobs.Job, limit int) []ClusterBenchmarkSummary {
	grouped := make(map[string][]jobs.Job)
	for _, job := range in {
		runID, ok := clusterBenchmarkRunID(job.RequestedBy)
		if !ok {
			continue
		}
		grouped[runID] = append(grouped[runID], job)
	}

	out := make([]ClusterBenchmarkSummary, 0, len(grouped))
	for runID, groupedJobs := range grouped {
		out = append(out, buildClusterBenchmarkSummary(runID, groupedJobs[0].RequestedBy, groupedJobs))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func buildClusterBenchmarkSummary(runID string, requestedBy string, in []jobs.Job) ClusterBenchmarkSummary {
	summary := ClusterBenchmarkSummary{
		ID:          runID,
		RequestedBy: requestedBy,
		Workers:     len(in),
		Jobs:        recentJobs(in, 0),
	}
	for i, job := range in {
		if i == 0 || job.CreatedAt.Before(summary.CreatedAt) {
			summary.CreatedAt = job.CreatedAt
		}
		if job.UpdatedAt.After(summary.UpdatedAt) {
			summary.UpdatedAt = job.UpdatedAt
		}
		if summary.Size == 0 || summary.Iterations == 0 {
			size, iterations := computeJobInput(job.Input)
			summary.Size = size
			summary.Iterations = iterations
		}
		switch job.Status {
		case jobs.StatusSucceeded:
			summary.Completed++
			summary.TotalGFLOPS += computeJobGFLOPS(job)
		case jobs.StatusFailed, jobs.StatusCanceled:
			summary.Failed++
		case jobs.StatusQueued, jobs.StatusScheduled, jobs.StatusRunning:
			summary.Active++
		}
	}
	switch {
	case summary.Active > 0:
		summary.Status = "running"
	case summary.Failed > 0 && summary.Completed > 0:
		summary.Status = "partial_failed"
	case summary.Failed > 0:
		summary.Status = "failed"
	case summary.Completed == summary.Workers && summary.Workers > 0:
		summary.Status = "succeeded"
	default:
		summary.Status = "queued"
	}
	return summary
}

func clusterBenchmarkRequestedBy(runID string, label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return "cluster-benchmark:" + runID
	}
	label = strings.ReplaceAll(label, ":", "-")
	return "cluster-benchmark:" + runID + ":" + label
}

func clusterBenchmarkRunID(requestedBy string) (string, bool) {
	parts := strings.Split(requestedBy, ":")
	if len(parts) < 2 || parts[0] != "cluster-benchmark" || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

func computeJobInput(input string) (int, int) {
	var payload struct {
		Size       int `json:"size"`
		Iterations int `json:"iterations"`
	}
	if err := json.Unmarshal([]byte(input), &payload); err != nil {
		return 0, 0
	}
	return payload.Size, payload.Iterations
}

func computeJobGFLOPS(job jobs.Job) float64 {
	if job.Result == "" {
		return 0
	}
	var payload struct {
		GFLOPS float64 `json:"gflops"`
	}
	if err := json.Unmarshal([]byte(job.Result), &payload); err != nil {
		return 0
	}
	return payload.GFLOPS
}

func hasActiveJobs(in []jobs.Job) bool {
	for _, job := range in {
		switch job.Status {
		case jobs.StatusQueued, jobs.StatusScheduled, jobs.StatusRunning:
			return true
		}
	}
	return false
}

type ReadinessSummary struct {
	Status              string
	WorkersOnline       int
	RuntimeReadyWorkers int
	InstalledModels     int
	GeneratableModels   int
	ActiveJobs          int
	RecentFailures      int
	Checks              []ReadinessCheck
}

type ReadinessCheck struct {
	Name   string
	Status string
	Detail string
	Action string
	Tab    string
}

type CapacitySummary struct {
	WorkersOnline              int              `json:"workers_online"`
	AllowedCPUCores            int              `json:"allowed_cpu_cores"`
	AllowedMemoryBytes         uint64           `json:"allowed_memory_bytes"`
	AllowedStorageBytes        uint64           `json:"allowed_storage_bytes"`
	FreeStorageBytes           uint64           `json:"free_storage_bytes"`
	AllowedVRAMBytes           uint64           `json:"allowed_vram_bytes"`
	CatalogModels              int              `json:"catalog_models"`
	SingleWorkerRunnableModels int              `json:"single_worker_runnable_models"`
	ShardedEstimateModels      int              `json:"sharded_estimate_models"`
	BlockedModels              int              `json:"blocked_models"`
	LargestSingleWorkerModel   CapacityModel    `json:"largest_single_worker_model"`
	LargestShardedModel        CapacityModel    `json:"largest_sharded_model"`
	Workers                    []CapacityWorker `json:"workers"`
	SingleWorkerRunnable       []CapacityModel  `json:"single_worker_runnable"`
	ShardedEstimate            []CapacityModel  `json:"sharded_estimate"`
	Blocked                    []CapacityModel  `json:"blocked"`
	UnlockTargets              []CapacityTarget `json:"unlock_targets"`
}

type CapacityModel struct {
	ID               string `json:"id,omitempty"`
	Name             string `json:"name,omitempty"`
	Parameters       string `json:"parameters,omitempty"`
	Quant            string `json:"quant,omitempty"`
	RequiredMemory   uint64 `json:"required_memory_bytes,omitempty"`
	RequiredDisk     uint64 `json:"required_disk_bytes,omitempty"`
	PlacementMode    string `json:"placement_mode,omitempty"`
	PlacementHint    string `json:"placement_hint,omitempty"`
	CandidateWorkers int    `json:"candidate_workers,omitempty"`
	ShardWorkers     int    `json:"shard_workers,omitempty"`
}

type CapacityWorker struct {
	NodeID               string        `json:"node_id"`
	Name                 string        `json:"name"`
	AllowedCPUCores      int           `json:"allowed_cpu_cores"`
	AllowedMemoryBytes   uint64        `json:"allowed_memory_bytes"`
	AllowedStorageBytes  uint64        `json:"allowed_storage_bytes"`
	FreeStorageBytes     uint64        `json:"free_storage_bytes"`
	AllowedVRAMBytes     uint64        `json:"allowed_vram_bytes"`
	JobSlots             int           `json:"job_slots"`
	RuntimeReady         bool          `json:"runtime_ready"`
	InstalledModels      int           `json:"installed_models"`
	RunnableModels       int           `json:"runnable_models"`
	LargestRunnableModel CapacityModel `json:"largest_runnable_model"`
	MemorySharePercent   float64       `json:"memory_share_percent"`
	StorageSharePercent  float64       `json:"storage_share_percent"`
	FreeStorageSharePct  float64       `json:"free_storage_share_percent"`
	VRAMSharePercent     float64       `json:"vram_share_percent"`
}

type CapacityTarget struct {
	Model                CapacityModel `json:"model"`
	MemoryShortBytes     uint64        `json:"memory_short_bytes,omitempty"`
	DiskShortBytes       uint64        `json:"disk_short_bytes,omitempty"`
	AggregateMemoryBytes uint64        `json:"aggregate_memory_bytes,omitempty"`
	AggregateDiskBytes   uint64        `json:"aggregate_disk_bytes,omitempty"`
	Blockers             []string      `json:"blockers,omitempty"`
}

type CapacitySnapshot struct {
	ID        string          `json:"id"`
	Label     string          `json:"label,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	Summary   ClusterSummary  `json:"summary"`
	Capacity  CapacitySummary `json:"capacity"`
}

type CapacityDelta struct {
	BaselineID                    string          `json:"baseline_id"`
	CurrentID                     string          `json:"current_id,omitempty"`
	WorkersOnlineDelta            int             `json:"workers_online_delta"`
	AllowedCPUCoresDelta          int             `json:"allowed_cpu_cores_delta"`
	AllowedMemoryBytesDelta       int64           `json:"allowed_memory_bytes_delta"`
	AllowedStorageBytesDelta      int64           `json:"allowed_storage_bytes_delta"`
	FreeStorageBytesDelta         int64           `json:"free_storage_bytes_delta"`
	AllowedVRAMBytesDelta         int64           `json:"allowed_vram_bytes_delta"`
	SingleWorkerRunnableDelta     int             `json:"single_worker_runnable_delta"`
	ShardedEstimateDelta          int             `json:"sharded_estimate_delta"`
	BlockedModelsDelta            int             `json:"blocked_models_delta"`
	NewSingleWorkerRunnableModels []CapacityModel `json:"new_single_worker_runnable_models,omitempty"`
	NewShardedEstimateModels      []CapacityModel `json:"new_sharded_estimate_models,omitempty"`
}

func clusterCapacity(summary ClusterSummary, modelsView []ModelSummary, onlineNodes []cluster.Node) CapacitySummary {
	capacity := CapacitySummary{
		WorkersOnline:        summary.WorkersOnline,
		AllowedCPUCores:      summary.Resources.CPU.CoresAllowed,
		AllowedMemoryBytes:   summary.Resources.Memory.AllowedBytes,
		AllowedStorageBytes:  summary.Resources.Storage.AllowedBytes,
		FreeStorageBytes:     summary.Resources.Storage.FreeBytes,
		AllowedVRAMBytes:     summary.VRAMAllowedBytes,
		CatalogModels:        len(modelsView),
		SingleWorkerRunnable: make([]CapacityModel, 0),
		ShardedEstimate:      make([]CapacityModel, 0),
		Blocked:              make([]CapacityModel, 0),
	}
	for _, modelSummary := range modelsView {
		plan := modelPlacementPlan(modelSummary)
		modelCapacity := capacityModelFromPlacement(modelSummary, plan)
		switch {
		case plan.RunnableNow:
			capacity.SingleWorkerRunnableModels++
			capacity.SingleWorkerRunnable = append(capacity.SingleWorkerRunnable, modelCapacity)
			if modelCapacity.RequiredMemory > capacity.LargestSingleWorkerModel.RequiredMemory {
				capacity.LargestSingleWorkerModel = modelCapacity
			}
		case plan.Feasible:
			capacity.ShardedEstimateModels++
			capacity.ShardedEstimate = append(capacity.ShardedEstimate, modelCapacity)
			if modelCapacity.RequiredMemory > capacity.LargestShardedModel.RequiredMemory {
				capacity.LargestShardedModel = modelCapacity
			}
		default:
			capacity.BlockedModels++
			capacity.Blocked = append(capacity.Blocked, modelCapacity)
			capacity.UnlockTargets = append(capacity.UnlockTargets, capacityTargetFromPlacement(modelCapacity, plan))
		}
	}
	sort.Slice(capacity.UnlockTargets, func(i, j int) bool {
		left := capacity.UnlockTargets[i]
		right := capacity.UnlockTargets[j]
		leftTotal := left.MemoryShortBytes + left.DiskShortBytes
		rightTotal := right.MemoryShortBytes + right.DiskShortBytes
		if leftTotal != rightTotal {
			return leftTotal < rightTotal
		}
		return left.Model.RequiredMemory < right.Model.RequiredMemory
	})
	if len(capacity.UnlockTargets) > 5 {
		capacity.UnlockTargets = capacity.UnlockTargets[:5]
	}
	capacity.Workers = capacityWorkers(summary, modelsView, onlineNodes)
	return capacity
}

func capacityTargetFromPlacement(model CapacityModel, plan ModelPlacementPlan) CapacityTarget {
	return CapacityTarget{
		Model:                model,
		MemoryShortBytes:     shortfall(plan.RequiredMemoryBytes, plan.AggregateMemoryBytes),
		DiskShortBytes:       shortfall(plan.RequiredDiskBytes, plan.AggregateDiskBytes),
		AggregateMemoryBytes: plan.AggregateMemoryBytes,
		AggregateDiskBytes:   plan.AggregateDiskBytes,
		Blockers:             append([]string(nil), plan.Blockers...),
	}
}

func shortfall(required uint64, available uint64) uint64 {
	if required <= available {
		return 0
	}
	return required - available
}

func capacityModelFromPlacement(summary ModelSummary, plan ModelPlacementPlan) CapacityModel {
	return CapacityModel{
		ID:               summary.Model.ID,
		Name:             summary.Model.Name,
		Parameters:       summary.Model.Parameters,
		Quant:            summary.Model.Quant,
		RequiredMemory:   summary.Model.MemoryBytes,
		RequiredDisk:     summary.Model.DiskBytes,
		PlacementMode:    plan.Mode,
		PlacementHint:    modelPlacementHint(plan),
		CandidateWorkers: len(plan.SingleNodeCandidates),
		ShardWorkers:     len(plan.Shards),
	}
}

func (s *Server) capacitySnapshot(label string) CapacitySnapshot {
	nodes := s.state.Nodes()
	jobsList := s.state.Jobs()
	summary := s.state.ClusterSummary()
	modelsView := modelSummaries(models.Catalog(), jobsList, nodes)
	return CapacitySnapshot{
		ID:        newCapacitySnapshotID(),
		Label:     strings.TrimSpace(label),
		CreatedAt: time.Now().UTC(),
		Summary:   summary,
		Capacity:  clusterCapacity(summary, modelsView, onlineWorkerNodes(nodes)),
	}
}

func (s *Server) saveCapacitySnapshot(label string) CapacitySnapshot {
	snapshot := s.capacitySnapshot(label)
	s.snapshotMu.Lock()
	s.snapshots[snapshot.ID] = snapshot
	s.snapshotMu.Unlock()
	return snapshot
}

func (s *Server) capacitySnapshots() []CapacitySnapshot {
	s.snapshotMu.RLock()
	defer s.snapshotMu.RUnlock()
	out := make([]CapacitySnapshot, 0, len(s.snapshots))
	for _, snapshot := range s.snapshots {
		out = append(out, snapshot)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

func (s *Server) capacitySnapshotByID(id string) (CapacitySnapshot, bool) {
	s.snapshotMu.RLock()
	defer s.snapshotMu.RUnlock()
	snapshot, ok := s.snapshots[id]
	return snapshot, ok
}

func capacityDelta(baseline CapacitySnapshot, current CapacitySnapshot) CapacityDelta {
	return CapacityDelta{
		BaselineID:                    baseline.ID,
		CurrentID:                     current.ID,
		WorkersOnlineDelta:            current.Capacity.WorkersOnline - baseline.Capacity.WorkersOnline,
		AllowedCPUCoresDelta:          current.Capacity.AllowedCPUCores - baseline.Capacity.AllowedCPUCores,
		AllowedMemoryBytesDelta:       uint64Delta(current.Capacity.AllowedMemoryBytes, baseline.Capacity.AllowedMemoryBytes),
		AllowedStorageBytesDelta:      uint64Delta(current.Capacity.AllowedStorageBytes, baseline.Capacity.AllowedStorageBytes),
		FreeStorageBytesDelta:         uint64Delta(current.Capacity.FreeStorageBytes, baseline.Capacity.FreeStorageBytes),
		AllowedVRAMBytesDelta:         uint64Delta(current.Capacity.AllowedVRAMBytes, baseline.Capacity.AllowedVRAMBytes),
		SingleWorkerRunnableDelta:     current.Capacity.SingleWorkerRunnableModels - baseline.Capacity.SingleWorkerRunnableModels,
		ShardedEstimateDelta:          current.Capacity.ShardedEstimateModels - baseline.Capacity.ShardedEstimateModels,
		BlockedModelsDelta:            current.Capacity.BlockedModels - baseline.Capacity.BlockedModels,
		NewSingleWorkerRunnableModels: newCapacityModels(baseline.Capacity.SingleWorkerRunnable, current.Capacity.SingleWorkerRunnable),
		NewShardedEstimateModels:      newCapacityModels(baseline.Capacity.ShardedEstimate, current.Capacity.ShardedEstimate),
	}
}

func newCapacityModels(baseline []CapacityModel, current []CapacityModel) []CapacityModel {
	seen := make(map[string]bool, len(baseline))
	for _, model := range baseline {
		seen[model.ID] = true
	}
	out := make([]CapacityModel, 0)
	for _, model := range current {
		if model.ID == "" || seen[model.ID] {
			continue
		}
		out = append(out, model)
	}
	return out
}

func uint64Delta(current uint64, baseline uint64) int64 {
	if current >= baseline {
		return int64(current - baseline)
	}
	return -int64(baseline - current)
}

func newCapacitySnapshotID() string {
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "cap-unknown"
	}
	return "cap-" + hex.EncodeToString(buf[:])
}

func capacityWorkers(summary ClusterSummary, modelsView []ModelSummary, onlineNodes []cluster.Node) []CapacityWorker {
	out := make([]CapacityWorker, 0, len(onlineNodes))
	for _, node := range onlineNodes {
		worker := CapacityWorker{
			NodeID:              node.ID,
			Name:                nodeDisplayName(node),
			AllowedCPUCores:     node.Resources.CPU.CoresAllowed,
			AllowedMemoryBytes:  node.Resources.Memory.AllowedBytes,
			AllowedStorageBytes: node.Resources.Storage.AllowedBytes,
			FreeStorageBytes:    node.Resources.Storage.FreeBytes,
			JobSlots:            node.Resources.JobSlots,
			RuntimeReady:        nodeAnyRuntimeReady(node),
			InstalledModels:     len(node.Resources.Models),
			MemorySharePercent:  percentOf(node.Resources.Memory.AllowedBytes, summary.Resources.Memory.AllowedBytes),
			StorageSharePercent: percentOf(node.Resources.Storage.AllowedBytes, summary.Resources.Storage.AllowedBytes),
			FreeStorageSharePct: percentOf(node.Resources.Storage.FreeBytes, summary.Resources.Storage.FreeBytes),
		}
		for _, gpu := range node.Resources.GPU {
			worker.AllowedVRAMBytes += gpu.AllowedVRAMBytes
		}
		worker.VRAMSharePercent = percentOf(worker.AllowedVRAMBytes, summary.VRAMAllowedBytes)
		for _, modelSummary := range modelsView {
			capability, ok := modelCapabilityForNode(modelSummary, node.ID)
			if !ok || !capability.Capable {
				continue
			}
			worker.RunnableModels++
			modelCapacity := capacityModelFromPlacement(modelSummary, modelPlacementPlan(modelSummary))
			if modelCapacity.RequiredMemory > worker.LargestRunnableModel.RequiredMemory {
				worker.LargestRunnableModel = modelCapacity
			}
		}
		out = append(out, worker)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AllowedMemoryBytes != out[j].AllowedMemoryBytes {
			return out[i].AllowedMemoryBytes > out[j].AllowedMemoryBytes
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func modelCapabilityForNode(summary ModelSummary, nodeID string) (ModelCapability, bool) {
	for _, capability := range summary.Capabilities {
		if capability.NodeID == nodeID {
			return capability, true
		}
	}
	return ModelCapability{}, false
}

func nodeAnyRuntimeReady(node cluster.Node) bool {
	for _, runtime := range node.Resources.Runtimes {
		if runtime.Ready {
			return true
		}
	}
	return false
}

func percentOf(value uint64, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(value) * 100 / float64(total)
}

func clusterReadiness(onlineNodes []cluster.Node, modelsView []ModelSummary, jobsList []jobs.Job) ReadinessSummary {
	readiness := ReadinessSummary{
		WorkersOnline:       len(onlineNodes),
		RuntimeReadyWorkers: runtimeReadyWorkerCount(onlineNodes),
		InstalledModels:     installedModelCount(modelsView),
		GeneratableModels:   generatableModelCount(modelsView),
		ActiveJobs:          activeJobCount(jobsList),
		RecentFailures:      recentFailureCount(jobsList),
		Status:              "ready",
	}
	readiness.Checks = []ReadinessCheck{
		readinessWorkersCheck(readiness.WorkersOnline),
		readinessRuntimeCheck(readiness.WorkersOnline, readiness.RuntimeReadyWorkers),
		readinessModelsCheck(readiness.InstalledModels, readiness.GeneratableModels),
		readinessJobsCheck(readiness.ActiveJobs),
		readinessFailuresCheck(readiness.RecentFailures),
	}
	for _, check := range readiness.Checks {
		if check.Status == "blocked" {
			readiness.Status = "blocked"
			return readiness
		}
		if check.Status == "warn" && readiness.Status == "ready" {
			readiness.Status = "warn"
		}
	}
	return readiness
}

func readinessWorkersCheck(workersOnline int) ReadinessCheck {
	if workersOnline == 0 {
		return ReadinessCheck{
			Name:   "Workers",
			Status: "blocked",
			Detail: "No workers are online.",
			Action: "Invite and start at least one worker.",
			Tab:    "workers",
		}
	}
	return ReadinessCheck{
		Name:   "Workers",
		Status: "ready",
		Detail: fmt.Sprintf("%d online worker(s).", workersOnline),
		Action: "Worker plane is available.",
		Tab:    "workers",
	}
}

func readinessRuntimeCheck(workersOnline int, runtimeReadyWorkers int) ReadinessCheck {
	if workersOnline == 0 {
		return ReadinessCheck{Name: "Runtime", Status: "blocked", Detail: "No worker runtime can be checked.", Action: "Connect a worker first.", Tab: "workers"}
	}
	if runtimeReadyWorkers == 0 {
		return ReadinessCheck{Name: "Runtime", Status: "blocked", Detail: "No online worker reports a ready AI runtime.", Action: "Open worker app and repair llama.cpp runtime.", Tab: "workers"}
	}
	if runtimeReadyWorkers < workersOnline {
		return ReadinessCheck{Name: "Runtime", Status: "warn", Detail: fmt.Sprintf("%d of %d worker(s) have ready runtime.", runtimeReadyWorkers, workersOnline), Action: "Repair runtime on workers that report missing llama.cpp.", Tab: "workers"}
	}
	return ReadinessCheck{Name: "Runtime", Status: "ready", Detail: fmt.Sprintf("%d worker(s) runtime-ready.", runtimeReadyWorkers), Action: "Runtime plane is available.", Tab: "workers"}
}

func readinessModelsCheck(installedModels int, generatableModels int) ReadinessCheck {
	if installedModels == 0 {
		return ReadinessCheck{Name: "Models", Status: "blocked", Detail: "No model is installed.", Action: "Install a small catalog model first.", Tab: "models"}
	}
	if generatableModels == 0 {
		return ReadinessCheck{Name: "Models", Status: "blocked", Detail: fmt.Sprintf("%d model(s) installed but none are runtime-ready.", installedModels), Action: "Repair runtime or reinstall the model on a ready worker.", Tab: "models"}
	}
	if generatableModels < installedModels {
		return ReadinessCheck{Name: "Models", Status: "warn", Detail: fmt.Sprintf("%d installed, %d ready for chat.", installedModels, generatableModels), Action: "Some installed models are not generatable yet.", Tab: "models"}
	}
	return ReadinessCheck{Name: "Models", Status: "ready", Detail: fmt.Sprintf("%d model(s) ready for chat.", generatableModels), Action: "Model chat can run locally.", Tab: "chat"}
}

func readinessJobsCheck(activeJobs int) ReadinessCheck {
	if activeJobs == 0 {
		return ReadinessCheck{Name: "Jobs", Status: "ready", Detail: "No active jobs.", Action: "Scheduler is idle.", Tab: "jobs"}
	}
	return ReadinessCheck{Name: "Jobs", Status: "warn", Detail: fmt.Sprintf("%d queued/scheduled/running job(s).", activeJobs), Action: "Wait for active jobs before release smoke testing.", Tab: "scheduler"}
}

func readinessFailuresCheck(recentFailures int) ReadinessCheck {
	if recentFailures == 0 {
		return ReadinessCheck{Name: "Failures", Status: "ready", Detail: "No recent terminal failures.", Action: "Recent job history is clean.", Tab: "model-activity"}
	}
	return ReadinessCheck{Name: "Failures", Status: "warn", Detail: fmt.Sprintf("%d recent failed/canceled job(s).", recentFailures), Action: "Review Model Activity and Jobs before release.", Tab: "model-activity"}
}

func runtimeReadyWorkerCount(nodes []cluster.Node) int {
	total := 0
	for _, node := range nodes {
		for _, runtime := range node.Resources.Runtimes {
			if runtime.Ready {
				total++
				break
			}
		}
	}
	return total
}

func activeJobCount(in []jobs.Job) int {
	total := 0
	for _, job := range in {
		switch job.Status {
		case jobs.StatusQueued, jobs.StatusScheduled, jobs.StatusRunning:
			total++
		}
	}
	return total
}

func recentFailureCount(in []jobs.Job) int {
	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	total := 0
	for _, job := range in {
		if job.UpdatedAt.Before(cutoff) {
			continue
		}
		if job.Status == jobs.StatusFailed || job.Status == jobs.StatusCanceled {
			total++
		}
	}
	return total
}

func schedulerJobs(in []jobs.Job) []jobs.Job {
	out := make([]jobs.Job, 0)
	for _, job := range in {
		switch job.Status {
		case jobs.StatusQueued, jobs.StatusScheduled, jobs.StatusRunning:
			out = append(out, job)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out
}

func isModelJob(job jobs.Job) bool {
	return job.Type == models.JobInstall || job.Type == models.JobDelete || job.Type == models.JobGenerate || job.Type == models.JobGenerateDistributedRPC || job.Type == models.JobGenerateDistributed || job.Type == models.JobGenerateStage || job.Type == models.JobRepair || job.Type == models.JobCleanup
}

func modelJobCount(in []jobs.Job) int {
	count := 0
	for _, job := range in {
		if isModelJob(job) {
			count++
		}
	}
	return count
}

func installedModelCount(in []ModelSummary) int {
	count := 0
	for _, summary := range in {
		if len(summary.InstalledOn) > 0 {
			count++
		}
	}
	return count
}

func installedModelInstanceCount(in []ModelSummary) int {
	count := 0
	for _, summary := range in {
		count += len(summary.Installed)
	}
	return count
}

func generatableModelCount(in []ModelSummary) int {
	count := 0
	for _, summary := range in {
		if len(summary.GeneratableOn) > 0 && summary.Status != "deleting" {
			count++
		}
	}
	return count
}

func modelFailureHint(summary ModelSummary) string {
	if strings.TrimSpace(summary.LastError) != "" && !strings.Contains(summary.LastError, "unsupported job type") {
		return "Last model job failed: " + summary.LastError
	}
	if summary.CapableNodes > 0 || len(summary.Capabilities) == 0 {
		return ""
	}
	reasons := make([]string, 0, len(summary.Capabilities))
	for _, capability := range summary.Capabilities {
		if capability.Capable || len(capability.Reasons) == 0 {
			continue
		}
		reasons = append(reasons, capability.Name+": "+strings.Join(capability.Reasons, "; "))
		if len(reasons) == 2 {
			break
		}
	}
	if len(reasons) == 0 {
		return ""
	}
	return "No capable worker yet. " + strings.Join(reasons, " | ")
}

func modelPlacementClass(plan ModelPlacementPlan) string {
	switch {
	case plan.RunnableNow:
		return "model-placement-card is-ready"
	case plan.Feasible:
		return "model-placement-card is-estimate"
	default:
		return "model-placement-card is-blocked"
	}
}

func modelPlacementLabel(plan ModelPlacementPlan) string {
	switch plan.Mode {
	case "single_worker":
		return "Single-worker ready"
	case "sharded_estimate":
		return "Sharded estimate"
	default:
		return "Blocked"
	}
}

func modelPlacementHint(plan ModelPlacementPlan) string {
	switch {
	case plan.RunnableNow:
		if len(plan.SingleNodeCandidates) == 1 {
			return "Can run on 1 online worker."
		}
		return fmt.Sprintf("Can run on %d online workers.", len(plan.SingleNodeCandidates))
	case plan.Feasible:
		return fmt.Sprintf("Aggregate resources fit across %d workers, but distributed model execution is not implemented yet.", len(plan.Shards))
	case len(plan.Blockers) > 0:
		return strings.Join(plan.Blockers, " | ")
	default:
		return "No viable placement found."
	}
}

type conversationStore interface {
	Conversation(id string) (Conversation, bool)
	Conversations() []Conversation
	AppendConversationMessage(id string, modelID string, nodeID string, systemPrompt string, message models.ChatMessage) Conversation
	DeleteConversation(id string) bool
}

type memoryStore interface {
	Memories(modelID string) []Memory
	UpsertMemory(memory Memory) (Memory, error)
	DeleteMemory(id string) bool
	DeleteMemoriesByModel(modelID string) int
}

type modelInstallConflictResponse struct {
	Error     string             `json:"error"`
	Reason    string             `json:"reason"`
	Placement ModelPlacementPlan `json:"placement"`
}

type distributedGenerateConflictResponse struct {
	Error  string               `json:"error"`
	Reason string               `json:"reason"`
	Plan   DistributedModelPlan `json:"plan"`
}

type distributedGenerateResponse struct {
	Job           jobs.Job             `json:"job"`
	StageJobs     []jobs.Job           `json:"stage_jobs"`
	Plan          DistributedModelPlan `json:"plan"`
	ExecutableNow bool                 `json:"executable_now"`
}

func distributedStageInputs(stages []DistributedPlanStage) []models.DistributedStageInput {
	out := make([]models.DistributedStageInput, 0, len(stages))
	for _, stage := range stages {
		out = append(out, models.DistributedStageInput{
			Index:      stage.Index,
			NodeID:     stage.NodeID,
			NodeName:   stage.NodeName,
			LayerStart: stage.LayerStart,
			LayerEnd:   stage.LayerEnd,
			Layers:     stage.Layers,
		})
	}
	return out
}

func appendConversationMessage(store Store, id string, modelID string, nodeID string, systemPrompt string, message models.ChatMessage) Conversation {
	if conversations, ok := store.(conversationStore); ok {
		return conversations.AppendConversationMessage(id, modelID, nodeID, systemPrompt, message)
	}
	return Conversation{
		ID:           id,
		ModelID:      modelID,
		NodeID:       nodeID,
		SystemPrompt: systemPrompt,
		Messages:     []models.ChatMessage{normalizeChatMessage(message)},
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
}

func systemPromptWithMemory(systemPrompt string, modelID string, store Store) string {
	memories := memoriesForModel(store, modelID)
	context := memoryContext(modelID, memories)
	if context == "" {
		return systemPrompt
	}
	base := strings.TrimSpace(systemPrompt)
	if base == "" {
		return context
	}
	return base + "\n\n" + context
}

func modelDefaultSystemPrompt(model models.Model) string {
	return models.QualityPresetFor(model).SystemPrompt
}

func recentConversations(store Store, limit int) []Conversation {
	conversations, ok := store.(conversationStore)
	if !ok {
		return nil
	}
	out := conversations.Conversations()
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func memoriesForModel(store Store, modelID string) []Memory {
	memories, ok := store.(memoryStore)
	if !ok {
		return nil
	}
	out := memories.Memories(modelID)
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out
}

func recentMemories(store Store, limit int) []Memory {
	out := memoriesForModel(store, "")
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func memoryLabel(memory Memory) string {
	if memory.Key == "" {
		return "-"
	}
	return memory.Key
}

func memorySubtitle(memory Memory) string {
	parts := make([]string, 0, 3)
	if memory.ModelID != "" {
		parts = append(parts, memory.ModelID)
	}
	if !memory.UpdatedAt.IsZero() {
		parts = append(parts, memory.UpdatedAt.Format("15:04"))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " · ")
}

func runtimeSummary(runtimes []cluster.RuntimeResource) string {
	if len(runtimes) == 0 {
		return "not reported"
	}
	parts := make([]string, 0, len(runtimes))
	for _, runtime := range runtimes {
		status := "missing"
		if runtime.Ready {
			status = "ready"
		}
		detail := runtime.Name + " " + status
		if runtime.Version != "" {
			detail += " " + runtime.Version
		}
		if runtime.Source != "" {
			detail += " (" + runtime.Source + ")"
		}
		if runtime.Error != "" && !runtime.Ready {
			detail += ": " + runtime.Error
		}
		parts = append(parts, detail)
	}
	return strings.Join(parts, "; ")
}

func heartbeatAge(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	age := time.Since(t)
	if age < 0 {
		age = 0
	}
	if age < time.Minute {
		return fmt.Sprintf("%.0fs ago", age.Seconds())
	}
	if age < time.Hour {
		return fmt.Sprintf("%.0fm ago", age.Minutes())
	}
	return fmt.Sprintf("%.1fh ago", age.Hours())
}

func workerModelBytes(models []cluster.ModelResource) uint64 {
	var total uint64
	for _, model := range models {
		total += model.Bytes
	}
	return total
}

func conversationTitle(conversation Conversation) string {
	for _, message := range conversation.Messages {
		if message.Role == "user" && strings.TrimSpace(message.Content) != "" {
			title := strings.TrimSpace(message.Content)
			if len([]rune(title)) > 56 {
				runes := []rune(title)
				title = string(runes[:56]) + "..."
			}
			return title
		}
	}
	return "New conversation"
}

func conversationSubtitle(conversation Conversation) string {
	parts := make([]string, 0, 3)
	if conversation.ModelID != "" {
		parts = append(parts, conversation.ModelID)
	}
	if conversation.NodeID != "" {
		parts = append(parts, conversation.NodeID)
	}
	if !conversation.UpdatedAt.IsZero() {
		parts = append(parts, conversation.UpdatedAt.Format("15:04"))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " · ")
}

func activeJobsByWorker(in []jobs.Job) map[string]int {
	out := make(map[string]int)
	for _, job := range in {
		if job.AssignedTo == "" {
			continue
		}
		if job.Status == jobs.StatusScheduled || job.Status == jobs.StatusRunning {
			out[job.AssignedTo]++
		}
	}
	return out
}

func jobDuration(job jobs.Job) string {
	if job.StartedAt.IsZero() {
		return "-"
	}
	end := job.FinishedAt
	if end.IsZero() {
		end = job.UpdatedAt
	}
	if end.IsZero() || end.Before(job.StartedAt) {
		return "-"
	}
	return formatDuration(end.Sub(job.StartedAt))
}

func jobTimeline(job jobs.Job) string {
	parts := []string{"created " + formatClock(job.CreatedAt)}
	if !job.StartedAt.IsZero() {
		parts = append(parts, "started "+formatClock(job.StartedAt))
	}
	if !job.FinishedAt.IsZero() {
		parts = append(parts, "finished "+formatClock(job.FinishedAt))
	}
	return strings.Join(parts, " / ")
}

func jobDetail(job jobs.Job) string {
	if job.Error != "" {
		return job.Error
	}
	if job.Result == "" {
		if job.AssignedTo == "" {
			if job.LastFailure != "" {
				return job.LastFailure
			}
			return "Waiting for a capable worker."
		}
		return "Waiting for worker result."
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(job.Result), &result); err != nil {
		return job.Result
	}
	if trace, ok := distributedRPCExecutionResult(job); ok {
		return distributedRPCExecutionResultText(trace)
	}
	if output, ok := result["output"].(string); ok && strings.TrimSpace(output) != "" {
		return output
	}
	if progress := jobProgress(job); progress != "" {
		return progress
	}
	if job.Type == models.JobRepair {
		parts := []string{"Model repaired"}
		if reinstalled, ok := result["reinstalled"].(bool); ok && reinstalled {
			parts = append(parts, "reinstalled")
		}
		if manifestRepaired, ok := result["manifest_repaired"].(bool); ok && manifestRepaired {
			parts = append(parts, "manifest repaired")
		}
		if tempCleaned, ok := result["temp_cleaned"].(bool); ok && tempCleaned {
			parts = append(parts, "partial download removed")
		}
		if bytesValue := numberResult(result, "bytes"); bytesValue > 0 {
			parts = append(parts, fmt.Sprintf("%.1f GB", bytesValue/1024/1024/1024))
		}
		return strings.Join(parts, "; ")
	}
	if job.Type == models.JobCleanup {
		parts := []string{"Model cache cleaned"}
		if partial := int(numberResult(result, "partial_files_removed")); partial > 0 {
			parts = append(parts, fmt.Sprintf("removed %d partial file(s)", partial))
		}
		if orphans := int(numberResult(result, "orphan_dirs_removed")); orphans > 0 {
			parts = append(parts, fmt.Sprintf("removed %d orphan dir(s)", orphans))
		}
		if manifests := int(numberResult(result, "stale_manifests_removed")); manifests > 0 {
			parts = append(parts, fmt.Sprintf("removed %d stale manifest(s)", manifests))
		}
		if bytesValue := numberResult(result, "total_bytes_removed"); bytesValue > 0 {
			parts = append(parts, fmt.Sprintf("freed %.1f GB", bytesValue/1024/1024/1024))
		}
		return strings.Join(parts, "; ")
	}
	if job.Type == models.JobDelete {
		parts := []string{"Model files removed"}
		if freed := numberResult(result, "freed_bytes"); freed > 0 {
			parts = append(parts, fmt.Sprintf("freed %.1f GB", freed/1024/1024/1024))
		}
		if memories := int(numberResult(result, "deleted_memories")); memories > 0 {
			parts = append(parts, fmt.Sprintf("cleared %d memory item(s)", memories))
		}
		if conversations := int(numberResult(result, "deleted_conversations")); conversations > 0 {
			parts = append(parts, fmt.Sprintf("cleared %d conversation(s)", conversations))
		}
		return strings.Join(parts, "; ")
	}
	if runtimeValue, ok := result["worker_runtime"].(string); ok && strings.TrimSpace(runtimeValue) != "" {
		return "Completed on " + runtimeValue
	}
	return "Completed."
}

func distributedRPCExecutionResult(job jobs.Job) (protocol.DistributedRPCExecutionResult, bool) {
	if job.Type != models.JobGenerateDistributedRPC || strings.TrimSpace(job.Result) == "" {
		return protocol.DistributedRPCExecutionResult{}, false
	}
	var payload struct {
		ExecutionResult protocol.DistributedRPCExecutionResult `json:"execution_result"`
	}
	if err := json.Unmarshal([]byte(job.Result), &payload); err != nil {
		return protocol.DistributedRPCExecutionResult{}, false
	}
	if strings.TrimSpace(payload.ExecutionResult.Protocol) == "" {
		return protocol.DistributedRPCExecutionResult{}, false
	}
	return payload.ExecutionResult, true
}

func distributedRPCExecutionResultText(result protocol.DistributedRPCExecutionResult) string {
	parts := []string{"Distributed RPC"}
	if result.DurationMS > 0 {
		parts = append(parts, fmt.Sprintf("%d ms", result.DurationMS))
	}
	if result.RPCEndpointCount > 0 {
		parts = append(parts, fmt.Sprintf("%d endpoint(s)", result.RPCEndpointCount))
	} else if len(result.RPCEndpoints) > 0 {
		parts = append(parts, fmt.Sprintf("%d endpoint(s)", len(result.RPCEndpoints)))
	}
	if strings.TrimSpace(result.Runtime) != "" {
		parts = append(parts, result.Runtime)
	}
	if strings.TrimSpace(result.PlanID) != "" {
		parts = append(parts, "plan "+shortValueID(result.PlanID))
	}
	return strings.Join(parts, " · ")
}

func shortValueID(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}

func jobProgress(job jobs.Job) string {
	if strings.TrimSpace(job.Result) == "" {
		return ""
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(job.Result), &result); err != nil {
		return ""
	}
	if kind, _ := result["kind"].(string); kind != "job.progress" {
		return ""
	}
	label, _ := result["progress_label"].(string)
	if strings.TrimSpace(label) == "" {
		label = "Running"
	}
	written := numberResult(result, "progress_bytes")
	total := numberResult(result, "total_bytes")
	percent := numberResult(result, "progress_percent")
	if total > 0 {
		return fmt.Sprintf("%s %.1f%% · %.1f / %.1f GB", label, percent, written/1024/1024/1024, total/1024/1024/1024)
	}
	if written > 0 {
		return fmt.Sprintf("%s · %.1f GB", label, written/1024/1024/1024)
	}
	return label
}

func numberResult(result map[string]any, key string) float64 {
	switch value := result[key].(type) {
	case float64:
		return value
	case float32:
		return float64(value)
	case int:
		return float64(value)
	case int64:
		return float64(value)
	case uint64:
		return float64(value)
	case json.Number:
		parsed, _ := value.Float64()
		return parsed
	default:
		return 0
	}
}

func jobWorkload(job jobs.Job) string {
	if modelID, ok := jobModelID(job); ok {
		switch job.Type {
		case models.JobInstall:
			return "install " + modelID
		case models.JobDelete:
			return "delete " + modelID
		case models.JobRepair:
			return "repair " + modelID
		case models.JobGenerate:
			var input models.GenerateInput
			if err := json.Unmarshal([]byte(job.Input), &input); err == nil && strings.TrimSpace(input.Prompt) != "" {
				return "generate " + modelID + ": " + input.Prompt
			}
			return "generate " + modelID
		case models.JobGenerateDistributedRPC:
			var input models.DistributedRPCGenerateInput
			if err := json.Unmarshal([]byte(job.Input), &input); err == nil && strings.TrimSpace(input.Prompt) != "" {
				return "distributed rpc generate " + modelID + ": " + input.Prompt
			}
			return "distributed rpc generate " + modelID
		case models.JobGenerateDistributed:
			return "distributed generate " + modelID
		case models.JobGenerateStage:
			var input models.DistributedStageJobInput
			if err := json.Unmarshal([]byte(job.Input), &input); err == nil {
				return fmt.Sprintf("stage %d %s layers %d-%d", input.Stage.Index, modelID, input.Stage.LayerStart, input.Stage.LayerEnd)
			}
			return "distributed stage " + modelID
		}
	}
	if job.Type == models.JobCleanup {
		return "cleanup model cache"
	}
	size, iterations := computeJobInput(job.Input)
	if size > 0 && iterations > 0 {
		return fmt.Sprintf("%dx%d x %d", size, size, iterations)
	}
	if strings.TrimSpace(job.Input) == "" {
		return "-"
	}
	return job.Input
}

func matrixJobRequirements(size int) jobs.Requirements {
	requirements := jobs.Requirements{CPUCores: 1}
	if size <= 0 {
		return requirements
	}
	matrixBytes := uint64(size) * uint64(size) * 8
	requirements.MemoryBytes = matrixBytes * 3
	return requirements
}

func jobRequirements(job jobs.Job) string {
	req := job.Requirements
	parts := make([]string, 0, 5)
	if req.CPUCores > 0 {
		parts = append(parts, fmt.Sprintf("%d CPU", req.CPUCores))
	}
	if req.MemoryBytes > 0 {
		parts = append(parts, fmt.Sprintf("%.1f GB RAM", float64(req.MemoryBytes)/1024/1024/1024))
	}
	if req.DiskBytes > 0 {
		parts = append(parts, fmt.Sprintf("%.1f GB disk", float64(req.DiskBytes)/1024/1024/1024))
	}
	if req.GPURequired {
		parts = append(parts, "GPU")
	}
	if req.VRAMBytes > 0 {
		parts = append(parts, fmt.Sprintf("%.1f GB VRAM", float64(req.VRAMBytes)/1024/1024/1024))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ")
}

func formatClock(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.Format("15:04:05 MST")
}

func formatDuration(value time.Duration) string {
	if value < 0 {
		return "-"
	}
	if value < time.Second {
		return fmt.Sprintf("%d ms", value.Milliseconds())
	}
	if value < time.Minute {
		return fmt.Sprintf("%.1f s", value.Seconds())
	}
	return value.Round(time.Second).String()
}

var dashboardTemplate = template.Must(template.New("dashboard").Funcs(template.FuncMap{
	"gb": func(bytes uint64) float64 {
		return float64(bytes) / 1024 / 1024 / 1024
	},
	"mb": func(bytes uint64) float64 {
		return float64(bytes) / 1024 / 1024
	},
	"shortID": func(value string) string {
		return shortValueID(value)
	},
	"jobOutput": func(job jobs.Job) string {
		if job.Error != "" {
			return job.Error
		}
		if job.Result != "" {
			return job.Result
		}
		return "-"
	},
	"jobWorkerLabel": func(nodes map[string]cluster.Node, job jobs.Job) string {
		if job.AssignedTo == "" {
			return "Queued"
		}
		if node, ok := nodes[job.AssignedTo]; ok && strings.TrimSpace(node.Name) != "" {
			return node.Name
		}
		if len(job.AssignedTo) <= 12 {
			return job.AssignedTo
		}
		return job.AssignedTo[:12]
	},
	"jobDetail": jobDetail,
	"jobProgress": func(job jobs.Job) string {
		if progress := jobProgress(job); progress != "" {
			return progress
		}
		return "-"
	},
	"clip": func(value string, limit int) string {
		value = strings.TrimSpace(value)
		if limit <= 0 || len(value) <= limit {
			return value
		}
		return value[:limit] + "..."
	},
	"jobPillClass": func(status jobs.Status) string {
		switch status {
		case jobs.StatusSucceeded:
			return "pill"
		case jobs.StatusFailed, jobs.StatusCanceled:
			return "pill pill-failed"
		case jobs.StatusRunning, jobs.StatusScheduled:
			return "pill pill-job"
		default:
			return "pill pill-muted"
		}
	},
	"benchmarkPillClass": func(status string) string {
		switch status {
		case "succeeded":
			return "pill"
		case "failed", "partial_failed":
			return "pill pill-failed"
		case "running", "queued":
			return "pill pill-job"
		default:
			return "pill pill-muted"
		}
	},
	"nodeLabel": func(nodes map[string]cluster.Node, nodeID string) string {
		if node, ok := nodes[nodeID]; ok && strings.TrimSpace(node.Name) != "" {
			return node.Name
		}
		if len(nodeID) <= 12 {
			return nodeID
		}
		return nodeID[:12]
	},
	"barPercent": func(value float64, maxValue float64) int {
		if value <= 0 || maxValue <= 0 {
			return 2
		}
		percent := int((value / maxValue) * 100)
		if percent < 2 {
			return 2
		}
		if percent > 100 {
			return 100
		}
		return percent
	},
	"hasActiveJobs":          hasActiveJobs,
	"schedulerJobs":          schedulerJobs,
	"isModelJob":             isModelJob,
	"modelJobCount":          modelJobCount,
	"installedModelCount":    installedModelCount,
	"installedInstanceCount": installedModelInstanceCount,
	"generatableCount":       generatableModelCount,
	"modelFailureHint":       modelFailureHint,
	"modelPlacement":         modelPlacementPlan,
	"modelPlacementClass":    modelPlacementClass,
	"modelPlacementLabel":    modelPlacementLabel,
	"modelPlacementHint":     modelPlacementHint,
	"formatClock":            formatClock,
	"workerSlots":            workerJobSlots,
	"jobCanCancel":           jobCanBeCanceled,
	"jobDuration":            jobDuration,
	"jobTimeline":            jobTimeline,
	"jobWorkload":            jobWorkload,
	"jobRequirements":        jobRequirements,
	"conversationTitle":      conversationTitle,
	"conversationSubtitle":   conversationSubtitle,
	"memoryLabel":            memoryLabel,
	"memorySubtitle":         memorySubtitle,
	"runtimeSummary":         runtimeSummary,
	"heartbeatAge":           heartbeatAge,
	"workerModelBytes":       workerModelBytes,
	"modelReadyNodeOptions": func(nodes map[string]cluster.Node, summary ModelSummary) []cluster.Node {
		out := make([]cluster.Node, 0, len(summary.GeneratableOn))
		for _, nodeID := range summary.GeneratableOn {
			if node, ok := nodes[nodeID]; ok {
				out = append(out, node)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return out
	},
	"modelStatusClass": func(status string) string {
		switch status {
		case "installed":
			return "pill"
		case "installing", "deleting", "repairing":
			return "pill pill-job"
		default:
			return "pill pill-muted"
		}
	},
	"modelCanInstall": func(summary ModelSummary) bool {
		return summary.Status != "installing" && summary.Status != "deleting" && summary.Status != "repairing" && summary.CapableNodes > 0
	},
	"modelCanGenerate": func(summary ModelSummary) bool {
		return len(summary.GeneratableOn) > 0 && summary.Status != "deleting"
	},
	"modelPreset": func(model models.Model) models.QualityPreset {
		return models.QualityPresetFor(model)
	},
	"jobMetric": func(job jobs.Job, key string) string {
		if job.Result == "" {
			return "-"
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(job.Result), &result); err != nil {
			return "-"
		}
		value, ok := result[key]
		if !ok {
			return "-"
		}
		switch typed := value.(type) {
		case float64:
			if key == "gflops" {
				return fmt.Sprintf("%.2f", typed)
			}
			return fmt.Sprintf("%.0f", typed)
		case string:
			return typed
		default:
			return fmt.Sprintf("%v", typed)
		}
	},
	"distributedRPCTrace": func(job jobs.Job) string {
		result, ok := distributedRPCExecutionResult(job)
		if !ok {
			return ""
		}
		return distributedRPCExecutionResultText(result)
	},
	"distributedRPCEndpoints": func(job jobs.Job) string {
		result, ok := distributedRPCExecutionResult(job)
		if !ok || len(result.RPCEndpoints) == 0 {
			return ""
		}
		return strings.Join(result.RPCEndpoints, ", ")
	},
	"joinStrings": func(values []string, sep string) string {
		return strings.Join(values, sep)
	},
}).Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>CMesh Dashboard</title>
  <style>
    :root {
      color-scheme: light;
      --bg: #f6f7f9;
      --panel: #ffffff;
      --text: #17202a;
      --muted: #657282;
      --line: #d9dee5;
      --accent: #0f766e;
      --accent-2: #2563eb;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      font-family: ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      background: var(--bg);
      color: var(--text);
    }
    header {
      padding: 28px 32px 18px;
      border-bottom: 1px solid var(--line);
      background: var(--panel);
      display: flex;
      align-items: end;
      justify-content: space-between;
      gap: 18px;
    }
    h1 {
      margin: 0 0 6px;
      font-size: 28px;
      letter-spacing: 0;
    }
    .sub {
      margin: 0;
      color: var(--muted);
      font-size: 14px;
    }
    main {
      padding: 24px 32px 40px;
      width: 100%;
      max-width: 1920px;
      margin: 0 auto;
    }
    .dashboard-layout {
      display: grid;
      grid-template-columns: 248px minmax(0, 1fr);
      gap: 20px;
      align-items: start;
    }
    .dashboard-sidebar {
      position: sticky;
      top: 16px;
      display: grid;
      gap: 12px;
      min-width: 0;
      overflow: visible;
    }
    .sidebar-card {
      padding: 12px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: rgba(255, 255, 255, 0.94);
      backdrop-filter: blur(14px);
    }
    .sidebar-title {
      display: grid;
      gap: 2px;
      margin-bottom: 10px;
    }
    .sidebar-title strong {
      font-size: 14px;
    }
    .dashboard-content {
      min-width: 0;
    }
    .grid {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
      gap: 12px;
      margin-bottom: 24px;
    }
    .onboarding {
      margin-bottom: 20px;
      background: var(--panel);
      border: 1px solid var(--line);
      border-radius: 8px;
      overflow: hidden;
    }
    .onboarding-body {
      display: grid;
      grid-template-columns: minmax(0, 1.15fr) minmax(320px, .85fr);
      gap: 18px;
      padding: 16px;
    }
    .step-list {
      display: grid;
      gap: 10px;
      margin: 0;
      padding: 0;
      list-style: none;
    }
    .step {
      display: grid;
      grid-template-columns: 30px 1fr auto;
      gap: 10px;
      align-items: center;
      min-height: 42px;
      padding: 10px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: #fbfcfd;
    }
    .step-index {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      width: 26px;
      height: 26px;
      border-radius: 999px;
      background: #eef2f7;
      color: var(--muted);
      font-size: 12px;
      font-weight: 800;
    }
    .step strong {
      display: block;
      font-size: 14px;
    }
    .step span {
      display: block;
      color: var(--muted);
      font-size: 12px;
      margin-top: 2px;
    }
    .step.done .step-index {
      background: #ecfdf5;
      color: var(--accent);
    }
    .first-test-panel {
      display: grid;
      gap: 12px;
      align-content: start;
      padding: 14px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: #fbfcfd;
    }
    .first-test-panel h3 {
      margin: 0;
      font-size: 14px;
    }
    .first-test-grid {
      display: grid;
      grid-template-columns: repeat(2, minmax(0, 1fr));
      gap: 8px;
    }
    .first-test-stat {
      padding: 10px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: var(--panel);
    }
    .first-test-stat span {
      display: block;
      color: var(--muted);
      font-size: 11px;
      text-transform: uppercase;
      margin-bottom: 5px;
    }
    .first-test-stat strong {
      font-size: 18px;
    }
    .first-test-form {
      display: grid;
      grid-template-columns: 1fr 1fr;
      gap: 8px;
    }
    .first-test-form .wide {
      grid-column: 1 / -1;
    }
    .readiness-hero {
      display: grid;
      grid-template-columns: minmax(240px, .85fr) minmax(0, 1.15fr);
      gap: 14px;
      padding: 16px;
      border-bottom: 1px solid var(--line);
      background: #fbfcfd;
    }
    .readiness-status {
      display: grid;
      align-content: start;
      gap: 12px;
      padding: 16px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: var(--panel);
    }
    .readiness-status strong {
      font-size: 28px;
      text-transform: capitalize;
    }
    .readiness-grid {
      display: grid;
      grid-template-columns: repeat(3, minmax(0, 1fr));
      gap: 8px;
    }
    .readiness-checks {
      display: grid;
      gap: 10px;
    }
    .readiness-check {
      display: grid;
      grid-template-columns: 120px 1fr auto;
      gap: 12px;
      align-items: center;
      padding: 12px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: var(--panel);
    }
    .readiness-check strong {
      font-size: 14px;
    }
    .readiness-check p {
      margin: 0;
    }
    .readiness-actions {
      display: flex;
      gap: 8px;
      flex-wrap: wrap;
      padding: 16px;
      border-top: 1px solid var(--line);
      background: #fbfcfd;
    }
    .capacity-snapshot-panel {
      display: grid;
      gap: 12px;
      padding: 16px;
      border-top: 1px solid var(--line);
      background: #fbfcfd;
    }
    .capacity-snapshot-toolbar {
      display: flex;
      gap: 8px;
      flex-wrap: wrap;
      align-items: center;
    }
    .capacity-snapshot-toolbar input {
      min-width: min(320px, 100%);
    }
    .capacity-snapshot-list {
      display: grid;
      gap: 8px;
    }
    .capacity-snapshot-row {
      display: grid;
      grid-template-columns: minmax(160px, 1fr) minmax(120px, auto) auto;
      gap: 10px;
      align-items: center;
      padding: 10px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: #fff;
    }
    .capacity-delta-grid {
      display: grid;
      grid-template-columns: repeat(4, minmax(0, 1fr));
      gap: 10px;
    }
    .model-run-guide {
      display: grid;
      grid-template-columns: repeat(3, minmax(0, 1fr));
      gap: 12px;
      padding: 16px;
      border-bottom: 1px solid var(--line);
      background: #fbfcfd;
    }
    .model-run-step {
      display: grid;
      gap: 10px;
      align-content: start;
      min-height: 150px;
      padding: 14px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: var(--panel);
    }
    .model-run-step h3 {
      margin: 0;
      font-size: 16px;
    }
    .model-run-step .step-index {
      width: 30px;
      height: 30px;
      margin-bottom: 2px;
    }
    .model-run-step.done {
      border-color: #b7e4cf;
      background: #f0fdf7;
    }
    .model-run-step.current {
      border-color: #91c5b9;
      box-shadow: inset 0 0 0 1px #91c5b9;
    }
    .metric {
      background: var(--panel);
      border: 1px solid var(--line);
      border-radius: 8px;
      padding: 16px;
    }
    .metric span {
      display: block;
      color: var(--muted);
      font-size: 12px;
      text-transform: uppercase;
      letter-spacing: .04em;
      margin-bottom: 8px;
    }
    .metric strong {
      font-size: 24px;
      letter-spacing: 0;
    }
    section {
      background: var(--panel);
      border: 1px solid var(--line);
      border-radius: 8px;
      overflow: hidden;
    }
    .section-head {
      padding: 14px 16px;
      border-bottom: 1px solid var(--line);
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 16px;
    }
    h2 {
      margin: 0;
      font-size: 16px;
      letter-spacing: 0;
    }
    table {
      width: 100%;
      border-collapse: collapse;
      font-size: 14px;
    }
    th, td {
      padding: 12px 16px;
      border-bottom: 1px solid var(--line);
      text-align: left;
      vertical-align: top;
    }
    th {
      color: var(--muted);
      font-weight: 600;
      font-size: 12px;
      text-transform: uppercase;
      letter-spacing: .04em;
    }
    .empty {
      padding: 28px 16px;
      color: var(--muted);
    }
    .pill {
      display: inline-block;
      padding: 3px 8px;
      border-radius: 999px;
      background: #ecfdf5;
      color: var(--accent);
      font-weight: 600;
      font-size: 12px;
    }
    .pill-job {
      background: #eff6ff;
      color: var(--accent-2);
    }
    .pill-failed {
      background: #fef2f2;
      color: #b91c1c;
    }
    .pill-muted {
      background: #f3f4f6;
      color: #4b5563;
    }
    .table-wrap {
      overflow-x: auto;
    }
    .mono-output {
      max-width: 420px;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
    }
    .actions {
      display: flex;
      gap: 10px;
      flex-wrap: wrap;
    }
    .button {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      gap: 8px;
      min-height: 36px;
      padding: 0 12px;
      border: 1px solid var(--line);
      border-radius: 6px;
      background: var(--panel);
      color: var(--text);
      text-decoration: none;
      font-size: 14px;
      font-weight: 600;
    }
    .icon {
      width: 16px;
      height: 16px;
      flex: 0 0 auto;
      stroke: currentColor;
      stroke-width: 2;
      stroke-linecap: round;
      stroke-linejoin: round;
      fill: none;
    }
    .console-tabs {
      display: grid;
      gap: 12px;
      align-content: start;
      overflow: visible;
    }
    .nav-group {
      display: grid;
      gap: 6px;
    }
    .nav-label {
      padding: 6px 10px 2px;
      color: var(--muted);
      font-size: 11px;
      font-weight: 900;
      letter-spacing: 0.08em;
      text-transform: uppercase;
    }
    .tab-button {
      display: grid;
      grid-template-columns: 18px minmax(0, 1fr) auto;
      align-items: center;
      justify-content: start;
      gap: 8px;
      min-height: 38px;
      width: 100%;
      padding: 0 10px;
      border: 1px solid transparent;
      border-radius: 6px;
      background: transparent;
      color: var(--muted);
      font: inherit;
      font-weight: 700;
      cursor: pointer;
      text-align: left;
      line-height: 1.15;
    }
    .tab-button span {
      min-width: 0;
    }
    .nav-badge {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      min-width: 22px;
      height: 20px;
      padding: 0 6px;
      border-radius: 999px;
      background: #edf2f7;
      color: var(--muted);
      font-size: 11px;
      font-weight: 900;
      line-height: 1;
    }
    .nav-badge[hidden] {
      display: none;
    }
    .nav-badge.ready {
      background: #dcfce7;
      color: #166534;
    }
    .nav-badge.warn {
      background: #fef3c7;
      color: #92400e;
    }
    .nav-badge.blocked,
    .nav-badge.failed {
      background: #fee2e2;
      color: #991b1b;
    }
    .tab-button.active {
      border-color: var(--line);
      background: var(--accent);
      color: #fff;
    }
    .tab-button.active .nav-badge {
      background: rgba(255,255,255,.22);
      color: #fff;
    }
    .tab-panel {
      display: grid;
      gap: 20px;
    }
    .tab-panel[hidden] {
      display: none;
    }
    .job-runner,
    .cluster-runner {
      display: grid;
      grid-template-columns: repeat(6, minmax(0, 1fr));
      gap: 12px;
      padding: 16px;
      border-bottom: 1px solid var(--line);
      background: #fbfcfd;
    }
    .field {
      display: grid;
      gap: 6px;
    }
    label {
      color: var(--muted);
      font-size: 12px;
      font-weight: 700;
      text-transform: uppercase;
    }
    input,
    select,
    textarea {
      min-height: 36px;
      width: 100%;
      padding: 0 10px;
      border: 1px solid var(--line);
      border-radius: 6px;
      background: var(--panel);
      color: var(--text);
      font: inherit;
    }
    button.button {
      cursor: pointer;
    }
    button.button:disabled {
      cursor: not-allowed;
      opacity: 0.52;
    }
    .primary {
      background: var(--accent);
      border-color: var(--accent);
      color: #ffffff;
    }
    .danger {
      background: #fff1f2;
      border-color: #fecdd3;
      color: #be123c;
    }
    .job-cancel-form {
      margin-top: 10px;
    }
    .runner-status {
      padding: 0 16px 14px;
      color: var(--muted);
      font-size: 13px;
    }
    .benchmark-summary {
      display: grid;
      grid-template-columns: repeat(4, minmax(80px, 1fr));
      gap: 8px;
      min-width: 320px;
    }
    .benchmark-summary span {
      display: block;
      color: var(--muted);
      font-size: 11px;
      text-transform: uppercase;
    }
    .benchmark-summary strong {
      font-size: 14px;
    }
    .growth-list {
      display: grid;
      gap: 10px;
      padding: 16px;
      border-bottom: 1px solid var(--line);
    }
    .growth-row {
      display: grid;
      grid-template-columns: 150px 1fr 92px;
      gap: 12px;
      align-items: center;
      font-size: 13px;
    }
    .growth-meta {
      color: var(--muted);
    }
    .growth-track {
      height: 10px;
      border-radius: 999px;
      background: #eef2f7;
      overflow: hidden;
    }
    .growth-fill {
      height: 100%;
      border-radius: inherit;
      background: linear-gradient(90deg, var(--accent), var(--accent-2));
    }
    .worker-breakdown {
      display: grid;
      gap: 8px;
      min-width: 360px;
    }
    .worker-result {
      display: grid;
      grid-template-columns: minmax(120px, 1fr) 84px 80px 92px 110px;
      gap: 8px;
      align-items: center;
      padding: 8px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: #fbfcfd;
    }
    .worker-result span {
      color: var(--muted);
      font-size: 12px;
    }
    .models-shell {
      display: grid;
      grid-template-columns: minmax(360px, .78fr) minmax(0, 1.22fr);
      gap: 16px;
      padding: 16px;
      background: #fbfcfd;
    }
    .chat-shell {
      min-height: calc(100vh - 230px);
      display: grid;
      grid-template-columns: 1fr;
      gap: 16px;
      padding: 18px;
      background: #fbfcfd;
    }
    .conversation-sidebar {
      display: grid;
      grid-template-rows: auto 1fr;
      gap: 12px;
      min-height: 0;
    }
    .conversation-sidebar-head {
      display: grid;
      gap: 10px;
      padding: 12px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: var(--panel);
    }
    .conversation-list {
      display: grid;
      align-content: start;
      gap: 8px;
      min-height: 0;
      overflow: auto;
    }
    .conversation-shell,
    .memory-shell,
    .debug-shell {
      display: grid;
      grid-template-columns: minmax(280px, 420px) minmax(0, 1fr);
      gap: 16px;
      padding: 18px;
      background: #fbfcfd;
    }
    .memory-panel {
      display: grid;
      gap: 8px;
      padding: 12px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: var(--panel);
    }
    .memory-panel h3 {
      margin: 0;
      font-size: 15px;
    }
    .memory-panel-head {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 8px;
    }
    .memory-panel-head .button {
      padding: 6px 8px;
      font-size: 12px;
    }
    .memory-list {
      display: grid;
      gap: 8px;
    }
    .memory-item {
      display: grid;
      grid-template-columns: 1fr auto auto;
      gap: 8px;
      align-items: start;
      padding: 8px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: #fbfcfd;
    }
    .memory-main {
      display: grid;
      gap: 4px;
      min-width: 0;
    }
    .memory-value {
      font-weight: 800;
      word-break: break-word;
    }
    .memory-editor {
      display: grid;
      grid-template-columns: 1fr;
      gap: 8px;
      padding: 10px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: #fbfcfd;
    }
    .memory-editor textarea {
      min-height: 68px;
    }
    .memory-preview {
      display: grid;
      gap: 8px;
      padding: 10px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: #fbfcfd;
    }
    .memory-preview pre {
      max-height: 520px;
      margin: 0;
      overflow: auto;
      white-space: pre-wrap;
      word-break: break-word;
      font-size: 12px;
      line-height: 1.35;
    }
    .context-metrics {
      display: grid;
      grid-template-columns: repeat(4, minmax(0, 1fr));
      gap: 8px;
    }
    .context-metric {
      display: grid;
      gap: 3px;
      padding: 8px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: var(--panel);
    }
    .context-metric span {
      color: var(--muted);
      font-size: 11px;
      font-weight: 800;
      text-transform: uppercase;
    }
    .context-metric strong {
      font-size: 18px;
    }
    .context-message-list {
      display: grid;
      gap: 8px;
      max-height: 360px;
      overflow: auto;
    }
    .context-message {
      display: grid;
      gap: 4px;
      padding: 8px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: var(--panel);
    }
    .context-message strong {
      font-size: 12px;
      text-transform: uppercase;
      color: var(--accent);
    }
    .context-message p {
      margin: 0;
      white-space: pre-wrap;
      word-break: break-word;
    }
    .empty-action {
      display: grid;
      place-items: center;
      min-height: 360px;
      padding: 32px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: var(--panel);
      text-align: center;
    }
    .empty-action h3 {
      margin: 0 0 8px;
      font-size: 22px;
    }
    .empty-action p {
      max-width: 520px;
      margin: 0 auto 18px;
      color: var(--muted);
      line-height: 1.45;
    }
    .conversation-item {
      display: grid;
      gap: 4px;
      width: 100%;
      padding: 10px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: var(--panel);
      color: var(--text);
      text-align: left;
      cursor: pointer;
    }
    .conversation-row {
      display: grid;
      grid-template-columns: 1fr auto;
      gap: 8px;
      align-items: stretch;
    }
    .conversation-item.active {
      border-color: var(--accent);
      background: #ecfdf5;
    }
    .conversation-title {
      font-weight: 800;
      line-height: 1.25;
    }
    .conversation-meta {
      color: var(--muted);
      font-size: 12px;
      line-height: 1.35;
      word-break: break-word;
    }
    .chat-main {
      min-width: 0;
      display: grid;
      grid-template-rows: auto 1fr auto;
      gap: 16px;
    }
    .chat-topbar {
      display: grid;
      grid-template-columns: minmax(0, 1fr) minmax(320px, .85fr);
      gap: 12px;
      padding: 12px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: var(--panel);
    }
    .chat-selectors {
      display: grid;
      grid-template-columns: minmax(220px, 1fr) minmax(180px, .7fr) minmax(140px, .45fr);
      gap: 10px;
      min-width: 0;
    }
    .inline-toggle {
      align-self: end;
      min-height: 36px;
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 8px;
      padding: 0 10px;
      border: 1px solid var(--line);
      border-radius: 6px;
      background: var(--panel);
      color: var(--text);
      text-transform: none;
      font-size: 13px;
    }
    .inline-toggle input {
      width: auto;
      min-height: 0;
      margin: 0;
    }
    .chat-settings {
      display: grid;
      grid-template-columns: minmax(0, 1fr) 110px 110px;
      gap: 10px;
      grid-column: 1 / -1;
    }
    .chat-settings textarea {
      min-height: 72px;
      resize: vertical;
    }
    .chat-thread {
      display: grid;
      align-content: end;
      gap: 12px;
      padding: 20px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: var(--panel);
      overflow: auto;
    }
    .chat-empty {
      align-self: center;
      justify-self: center;
      max-width: 640px;
      text-align: center;
      color: var(--muted);
    }
    .chat-empty h3 {
      margin: 0 0 8px;
      color: var(--text);
      font-size: 30px;
    }
    .chat-message {
      max-width: min(820px, 88%);
      padding: 12px 14px;
      border: 1px solid var(--line);
      border-radius: 8px;
      white-space: pre-wrap;
      line-height: 1.45;
    }
    .chat-message.user {
      justify-self: end;
      background: var(--accent);
      border-color: var(--accent);
      color: #ffffff;
    }
    .chat-message.assistant {
      justify-self: start;
      background: #f8fafc;
    }
    .chat-message.system {
      justify-self: center;
      max-width: min(720px, 100%);
      background: #fff7ed;
      border-color: #fed7aa;
      color: #9a3412;
      font-weight: 600;
    }
    .chat-composer {
      display: grid;
      gap: 10px;
      padding: 12px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: var(--panel);
    }
    .chat-composer textarea {
      min-height: 92px;
      border-radius: 8px;
      resize: vertical;
    }
    .chat-composer-actions {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 12px;
    }
    .model-workspace {
      display: grid;
      gap: 16px;
      align-content: start;
    }
    .model-block {
      display: grid;
      gap: 12px;
      padding: 14px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: var(--panel);
    }
    .model-block-head {
      display: flex;
      align-items: start;
      justify-content: space-between;
      gap: 12px;
    }
    .model-block-title {
      display: flex;
      gap: 10px;
      align-items: center;
    }
    .model-block h3 {
      margin: 0;
      font-size: 17px;
    }
    .model-catalog {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
      gap: 12px;
      padding: 16px;
      background: #fbfcfd;
    }
    .catalog-toolbar {
      display: grid;
      grid-template-columns: minmax(220px, 1fr) minmax(140px, auto) minmax(140px, auto) minmax(160px, auto) auto auto auto;
      gap: 10px;
      align-items: end;
      padding: 14px 16px;
      border-bottom: 1px solid var(--line);
      background: var(--panel);
    }
    .catalog-toolbar .field {
      margin: 0;
    }
    .catalog-toggle {
      display: inline-flex;
      align-items: center;
      gap: 8px;
      min-height: 40px;
      color: var(--muted);
      font-size: 13px;
      font-weight: 800;
      white-space: nowrap;
    }
    .catalog-clear {
      align-self: end;
      min-height: 40px;
      padding-inline: 14px;
    }
    .catalog-empty {
      display: none;
      margin: 16px;
      padding: 24px;
      border: 1px dashed var(--line);
      border-radius: 8px;
      background: #fff;
      text-align: center;
      color: var(--muted);
    }
    .catalog-empty.is-visible {
      display: block;
    }
    .catalog-empty strong {
      display: block;
      margin-bottom: 6px;
      color: var(--ink);
      font-size: 18px;
    }
    .catalog-toggle input {
      width: 16px;
      min-height: 16px;
    }
    .catalog-count {
      align-self: center;
      color: var(--muted);
      font-size: 13px;
      font-weight: 800;
      white-space: nowrap;
    }
    .installed-models {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
      gap: 12px;
      padding: 16px;
      border-bottom: 1px solid var(--line);
      background: #fbfcfd;
    }
    .installed-model {
      display: grid;
      gap: 8px;
      padding: 14px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: var(--panel);
    }
    .installed-model h3 {
      margin: 0;
      font-size: 16px;
    }
    .model-card {
      display: grid;
      gap: 12px;
      padding: 14px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: var(--panel);
    }
    .model-title {
      display: flex;
      justify-content: space-between;
      gap: 10px;
      align-items: start;
    }
    .model-title h3 {
      margin: 0 0 4px;
      font-size: 16px;
    }
    .model-specs {
      display: grid;
      grid-template-columns: repeat(3, minmax(0, 1fr));
      gap: 8px;
    }
    .model-specs div {
      padding: 8px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: #fbfcfd;
    }
    .model-specs span {
      display: block;
      color: var(--muted);
      font-size: 10px;
      text-transform: uppercase;
      margin-bottom: 4px;
    }
    .model-specs strong {
      font-size: 13px;
    }
    .storage-detail {
      display: grid;
      gap: 4px;
      padding: 10px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: #fbfcfd;
      color: var(--muted);
      font-size: 12px;
      line-height: 1.35;
    }
    .storage-detail strong {
      color: var(--text);
      font-weight: 800;
    }
    .model-placement-card {
      display: grid;
      gap: 8px;
      padding: 10px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: #f8faf9;
      color: var(--muted);
      font-size: 12px;
      line-height: 1.35;
    }
    .model-placement-card strong {
      color: var(--text);
      font-size: 13px;
    }
    .model-placement-card.is-ready {
      border-color: #a9dfc7;
      background: #f3fbf7;
    }
    .model-placement-card.is-estimate {
      border-color: #eac56f;
      background: #fff9e8;
    }
    .model-placement-card.is-blocked {
      border-color: #f4b4b4;
      background: #fff7f7;
    }
    .model-placement-card code {
      white-space: normal;
    }
    .model-actions {
      display: flex;
      gap: 8px;
      flex-wrap: wrap;
    }
    body.modal-open {
      overflow: hidden;
    }
    .model-detail-panel {
      position: fixed;
      inset: 0;
      z-index: 80;
      display: grid;
      place-items: center;
      padding: 24px;
      background: rgba(15, 23, 42, 0.36);
      backdrop-filter: blur(4px);
    }
    .model-detail-panel[hidden] {
      display: none;
    }
    .model-detail-dialog {
      width: min(980px, calc(100vw - 32px));
      max-height: min(820px, calc(100vh - 32px));
      overflow: auto;
      padding: 18px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: #fff;
      box-shadow: 0 24px 70px rgba(15, 23, 42, 0.24);
    }
    .model-detail-head {
      display: flex;
      justify-content: space-between;
      gap: 12px;
      align-items: flex-start;
      margin-bottom: 14px;
    }
    .model-detail-head h3 {
      margin: 0 0 4px;
      font-size: 20px;
    }
    .model-detail-grid {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
      gap: 10px;
      margin-bottom: 14px;
    }
    .model-detail-grid div {
      border: 1px solid var(--line);
      border-radius: 8px;
      padding: 10px;
      background: #f8faf9;
    }
    .model-detail-grid span {
      display: block;
      color: var(--muted);
      font-size: 12px;
      font-weight: 800;
      text-transform: uppercase;
    }
    .model-detail-grid strong {
      display: block;
      margin-top: 4px;
      font-size: 16px;
    }
    .model-detail-list {
      display: grid;
      gap: 8px;
    }
    .model-detail-row {
      display: grid;
      grid-template-columns: minmax(160px, 1fr) minmax(80px, auto) minmax(220px, 2fr) auto;
      gap: 10px;
      align-items: center;
      border: 1px solid var(--line);
      border-radius: 8px;
      padding: 10px;
      background: #fbfcfd;
    }
    .model-detail-actions {
      display: flex;
      gap: 8px;
      flex-wrap: wrap;
      margin-bottom: 14px;
    }
    .model-detail-placement {
      display: grid;
      gap: 10px;
      margin-bottom: 14px;
      padding: 12px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: #f8faf9;
    }
    .model-detail-placement h4 {
      margin: 0;
      font-size: 15px;
    }
    .model-detail-placement .placement-summary {
      display: flex;
      gap: 8px;
      flex-wrap: wrap;
      align-items: center;
    }
    .model-detail-placement .placement-list {
      display: grid;
      gap: 8px;
    }
    .placement-shard {
      display: grid;
      grid-template-columns: minmax(160px, 1fr) minmax(120px, auto) minmax(120px, auto);
      gap: 10px;
      padding: 10px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: #fff;
    }
    .placement-warning {
      color: #8a5a00;
      font-weight: 700;
    }
    .placement-blocker {
      color: #b42318;
      font-weight: 700;
    }
    @media (max-width: 720px) {
      .model-detail-panel {
        padding: 12px;
      }
      .model-detail-head {
        align-items: stretch;
        flex-direction: column;
      }
      .model-detail-row {
        grid-template-columns: 1fr;
      }
      .placement-shard {
        grid-template-columns: 1fr;
      }
    }
    .model-operation {
      display: grid;
      gap: 6px;
      padding: 10px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: #f8fafc;
    }
    .model-operation strong {
      font-size: 13px;
    }
    .model-operation code {
      color: var(--muted);
      white-space: normal;
      word-break: break-word;
    }
    .capability-list {
      display: grid;
      gap: 6px;
      padding: 10px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: #fbfcfd;
    }
    .capability-row {
      display: grid;
      grid-template-columns: minmax(120px, .7fr) minmax(0, 1fr) auto;
      gap: 10px;
      align-items: start;
      font-size: 12px;
    }
    .capability-row strong {
      font-size: 12px;
    }
    .capability-row span {
      color: var(--muted);
      text-align: right;
    }
    .capability-row .button {
      padding: 6px 8px;
      font-size: 12px;
    }
    .hint {
      padding: 10px;
      border: 1px solid #fed7aa;
      border-radius: 8px;
      background: #fff7ed;
      color: #9a3412;
      font-size: 13px;
      font-weight: 600;
    }
    .chat-panel {
      display: grid;
      gap: 12px;
      align-content: start;
    }
    .chat-panel h3 {
      margin: 0;
      font-size: 18px;
    }
    textarea {
      min-height: 112px;
      width: 100%;
      padding: 10px;
      border: 1px solid var(--line);
      border-radius: 8px;
      resize: vertical;
      font: inherit;
    }
    .result-grid {
      display: grid;
      grid-template-columns: repeat(3, minmax(88px, 1fr));
      gap: 6px;
      min-width: 260px;
    }
    .result-grid span {
      display: block;
      color: var(--muted);
      font-size: 11px;
      text-transform: uppercase;
    }
    .result-grid strong {
      font-size: 13px;
    }
    .timeline {
      color: var(--muted);
      font-size: 12px;
      line-height: 1.35;
      max-width: 260px;
    }
    .job-main {
      display: grid;
      gap: 4px;
    }
    .job-main strong {
      font-size: 13px;
    }
    code {
      font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
      font-size: 13px;
    }
    @media (max-width: 640px) {
      header { display: block; }
      header .actions { margin-top: 14px; }
      header, main { padding-left: 18px; padding-right: 18px; }
      .dashboard-layout { grid-template-columns: 1fr; gap: 14px; }
      .dashboard-sidebar { position: static; max-height: none; }
      .console-tabs {
        grid-auto-flow: column;
        grid-auto-columns: max-content;
        grid-template-columns: none;
        max-height: none;
        overflow-x: auto;
        overflow-y: hidden;
      }
      .nav-group {
        grid-auto-flow: column;
        grid-auto-columns: max-content;
        grid-template-columns: none;
        align-items: center;
      }
      .nav-label {
        padding: 0 6px 0 0;
        white-space: nowrap;
      }
      .tab-button { width: auto; min-width: 132px; }
      table { display: block; overflow-x: auto; }
      .onboarding-body { grid-template-columns: 1fr; }
      .readiness-hero { grid-template-columns: 1fr; }
      .readiness-grid { grid-template-columns: 1fr; }
      .readiness-check { grid-template-columns: 1fr; }
      .model-run-guide { grid-template-columns: 1fr; }
      .step { grid-template-columns: 30px 1fr; }
      .step .pill, .step .pill-job, .step .pill-muted { grid-column: 2; width: fit-content; }
      .first-test-form { grid-template-columns: 1fr; }
      .first-test-form .wide { grid-column: auto; }
      .job-runner, .cluster-runner { grid-template-columns: 1fr; }
      .models-shell { grid-template-columns: 1fr; }
      .catalog-toolbar { grid-template-columns: 1fr; }
      .chat-shell { grid-template-columns: 1fr; }
      .conversation-shell, .memory-shell, .debug-shell { grid-template-columns: 1fr; }
      .chat-topbar { grid-template-columns: 1fr; }
      .chat-selectors { grid-template-columns: 1fr; }
      .chat-settings { grid-template-columns: 1fr; }
      .chat-message { max-width: 100%; }
      .model-specs { grid-template-columns: 1fr; }
      .growth-row { grid-template-columns: 1fr; }
      .worker-result { grid-template-columns: 1fr 1fr; }
    }
  </style>
</head>
<body data-active-jobs="{{hasActiveJobs .Jobs}}">
  <svg aria-hidden="true" width="0" height="0" style="position:absolute">
    <symbol id="icon-plus" viewBox="0 0 24 24"><path d="M12 5v14M5 12h14"/></symbol>
    <symbol id="icon-workers" viewBox="0 0 24 24"><path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M22 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/></symbol>
    <symbol id="icon-cpu" viewBox="0 0 24 24"><rect x="4" y="4" width="16" height="16" rx="2"/><rect x="9" y="9" width="6" height="6"/><path d="M9 1v3M15 1v3M9 20v3M15 20v3M20 9h3M20 14h3M1 9h3M1 14h3"/></symbol>
    <symbol id="icon-brain" viewBox="0 0 24 24"><path d="M12 5a3 3 0 0 0-5.83 1A3.5 3.5 0 0 0 5 12.8V18a3 3 0 0 0 3 3h4V5Z"/><path d="M12 5a3 3 0 0 1 5.83 1A3.5 3.5 0 0 1 19 12.8V18a3 3 0 0 1-3 3h-4V5Z"/><path d="M8 13h4M12 9h4M12 17h4"/></symbol>
    <symbol id="icon-terminal" viewBox="0 0 24 24"><path d="m7 8 4 4-4 4"/><path d="M13 16h4"/><rect x="3" y="4" width="18" height="16" rx="2"/></symbol>
    <symbol id="icon-chart" viewBox="0 0 24 24"><path d="M3 3v18h18"/><path d="m19 9-5 5-4-4-3 3"/></symbol>
    <symbol id="icon-play" viewBox="0 0 24 24"><path d="m8 5 11 7-11 7Z"/></symbol>
    <symbol id="icon-download" viewBox="0 0 24 24"><path d="M12 3v12"/><path d="m7 10 5 5 5-5"/><path d="M5 21h14"/></symbol>
    <symbol id="icon-refresh" viewBox="0 0 24 24"><path d="M21 12a9 9 0 0 1-15.5 6.2"/><path d="M3 12A9 9 0 0 1 18.5 5.8"/><path d="M18 2v4h4"/><path d="M6 22v-4H2"/></symbol>
    <symbol id="icon-trash" viewBox="0 0 24 24"><path d="M3 6h18"/><path d="M8 6V4h8v2"/><path d="M19 6l-1 15H6L5 6"/><path d="M10 11v6M14 11v6"/></symbol>
    <symbol id="icon-send" viewBox="0 0 24 24"><path d="m22 2-7 20-4-9-9-4Z"/><path d="M22 2 11 13"/></symbol>
    <symbol id="icon-edit" viewBox="0 0 24 24"><path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z"/></symbol>
    <symbol id="icon-x" viewBox="0 0 24 24"><path d="M18 6 6 18M6 6l12 12"/></symbol>
  </svg>
  <header>
    <div>
      <h1>CMesh</h1>
      <p class="sub">Decentralized-ready AI compute cluster manager</p>
    </div>
    <div class="actions">
      <a class="button" href="{{.InviteURL}}"><svg class="icon"><use href="#icon-plus"></use></svg>Invite worker</a>
    </div>
  </header>
  <main>
    <div class="dashboard-layout">
      <aside class="dashboard-sidebar" aria-label="Dashboard navigation">
        <div class="sidebar-card">
          <div class="sidebar-title">
            <strong>Cluster Console</strong>
            <span class="sub" id="sidebar-summary-text">{{.Readiness.Status}} · {{len .OnlineNodes}} worker(s) online</span>
          </div>
          <nav class="console-tabs" aria-label="Dashboard sections">
            <div class="nav-group" aria-label="Cluster">
              <div class="nav-label">Cluster</div>
              <button class="tab-button active" type="button" data-tab-target="overview"><svg class="icon"><use href="#icon-workers"></use></svg><span>Overview</span></button>
              <button class="tab-button" type="button" data-tab-target="readiness"><svg class="icon"><use href="#icon-chart"></use></svg><span>Readiness</span><strong class="nav-badge {{.Readiness.Status}}" data-nav-badge="readiness">{{.Readiness.Status}}</strong></button>
              <button class="tab-button" type="button" data-tab-target="workers"><svg class="icon"><use href="#icon-workers"></use></svg><span>Workers</span><strong class="nav-badge" data-nav-badge="workers">{{.Summary.WorkersOnline}}/{{.Summary.WorkersTotal}}</strong></button>
              <button class="tab-button" type="button" data-tab-target="rpc-pool"><svg class="icon"><use href="#icon-chart"></use></svg><span>RPC Pool</span><strong class="nav-badge {{if gt .RPCPool.Endpoints 0}}ready{{else}}blocked{{end}}">{{.RPCPool.Endpoints}}</strong></button>
              <button class="tab-button" type="button" data-tab-target="distributed-runs"><svg class="icon"><use href="#icon-terminal"></use></svg><span>Distributed Runs</span><strong class="nav-badge">{{len .DistributedRuns}}</strong></button>
              <button class="tab-button" type="button" data-tab-target="benchmarks"><svg class="icon"><use href="#icon-chart"></use></svg><span>Benchmarks</span></button>
            </div>
            <div class="nav-group" aria-label="AI workspace">
              <div class="nav-label">AI Workspace</div>
              <button class="tab-button" type="button" data-tab-target="chat"><svg class="icon"><use href="#icon-send"></use></svg><span>Chat</span><strong class="nav-badge {{if gt .Readiness.GeneratableModels 0}}ready{{else}}blocked{{end}}" data-nav-badge="ready-models">{{.Readiness.GeneratableModels}}</strong></button>
              <button class="tab-button" type="button" data-tab-target="memory"><svg class="icon"><use href="#icon-brain"></use></svg><span>Memory</span></button>
              <button class="tab-button" type="button" data-tab-target="conversations"><svg class="icon"><use href="#icon-terminal"></use></svg><span>Conversations</span></button>
              <button class="tab-button" type="button" data-tab-target="prompt-debug"><svg class="icon"><use href="#icon-terminal"></use></svg><span>Prompt Debug</span></button>
            </div>
            <div class="nav-group" aria-label="Models">
              <div class="nav-label">Models</div>
              <button class="tab-button" type="button" data-tab-target="model-inventory"><svg class="icon"><use href="#icon-download"></use></svg><span>Model Inventory</span></button>
              <button class="tab-button" type="button" data-tab-target="installed-models"><svg class="icon"><use href="#icon-download"></use></svg><span>Installed Models</span></button>
              <button class="tab-button" type="button" data-tab-target="models"><svg class="icon"><use href="#icon-brain"></use></svg><span>Model Catalog</span></button>
              <button class="tab-button" type="button" data-tab-target="model-activity"><svg class="icon"><use href="#icon-terminal"></use></svg><span>Model Activity</span><strong class="nav-badge failed" data-nav-badge="recent-failures" {{if eq .Readiness.RecentFailures 0}}hidden{{end}}>{{.Readiness.RecentFailures}}</strong></button>
            </div>
            <div class="nav-group" aria-label="Operations">
              <div class="nav-label">Operations</div>
              <button class="tab-button" type="button" data-tab-target="scheduler"><svg class="icon"><use href="#icon-chart"></use></svg><span>Scheduler</span><strong class="nav-badge warn" data-nav-badge="active-jobs" {{if eq .Readiness.ActiveJobs 0}}hidden{{end}}>{{.Readiness.ActiveJobs}}</strong></button>
              <button class="tab-button" type="button" data-tab-target="jobs"><svg class="icon"><use href="#icon-terminal"></use></svg><span>Jobs</span><strong class="nav-badge" data-nav-badge="jobs-total">{{len .Jobs}}</strong></button>
            </div>
          </nav>
        </div>
      </aside>
      <div class="dashboard-content">
    <div class="tab-panel" id="tab-overview">
    <section class="onboarding" aria-label="Cluster console">
      <div class="section-head">
        <h2>Cluster Console</h2>
        <code>{{if .OnlineNodes}}ready for compute{{else}}connect a worker{{end}}</code>
      </div>
      <div class="onboarding-body">
        <div class="first-test-panel">
          <h3>{{if .OnlineNodes}}Cluster is ready{{else}}Waiting for workers{{end}}</h3>
          <p class="sub">Invite machines, watch their usable capacity, then run compute jobs against the scheduler. This is the last cluster validation surface before adding model inference jobs.</p>
          <div class="actions">
            <a class="button primary" href="{{.InviteURL}}"><svg class="icon"><use href="#icon-plus"></use></svg>Invite worker</a>
            <button class="button" type="button" data-tab-shortcut="jobs"><svg class="icon"><use href="#icon-terminal"></use></svg>Open jobs</button>
          </div>
          <div class="first-test-grid">
            <div class="first-test-stat"><span>Workers</span><strong>{{.Summary.WorkersOnline}} / {{.Summary.WorkersTotal}}</strong></div>
            <div class="first-test-stat"><span>CPU cores</span><strong>{{.Summary.Resources.CPU.CoresAllowed}}</strong></div>
            <div class="first-test-stat"><span>Memory</span><strong>{{printf "%.1f" (gb .Summary.Resources.Memory.AllowedBytes)}} GB</strong></div>
            <div class="first-test-stat"><span>Jobs</span><strong>{{len .Jobs}}</strong></div>
          </div>
        </div>
        <div class="first-test-panel">
          <h3>Run cluster compute test</h3>
          <div class="first-test-grid">
            <div class="first-test-stat"><span>Score</span><strong>{{printf "%.0f" .Summary.BenchmarkScore}}</strong></div>
            {{if .ClusterBenchmarks}}{{with index .ClusterBenchmarks 0}}
            <div class="first-test-stat"><span>Last run</span><strong>{{.Status}}</strong></div>
            <div class="first-test-stat"><span>Total GFLOPS</span><strong>{{printf "%.2f" .TotalGFLOPS}}</strong></div>
            {{end}}{{else}}
            <div class="first-test-stat"><span>Last run</span><strong>-</strong></div>
            <div class="first-test-stat"><span>Total GFLOPS</span><strong>-</strong></div>
            {{end}}
          </div>
          <form class="first-test-form" id="first-test-form">
            <div class="field">
              <label for="first-test-size">Matrix size</label>
              <input id="first-test-size" name="size" type="number" min="16" max="2048" step="16" value="512">
            </div>
            <div class="field">
              <label for="first-test-iterations">Iterations</label>
              <input id="first-test-iterations" name="iterations" type="number" min="1" max="100" step="1" value="6">
            </div>
            <button class="button primary wide" type="submit" {{if not .OnlineNodes}}disabled{{end}}><svg class="icon"><use href="#icon-play"></use></svg>Run cluster test</button>
          </form>
          <div class="runner-status" id="first-test-status">{{if .OnlineNodes}}Ready to run one task on each online worker.{{else}}Connect a worker first, then this button becomes available.{{end}}</div>
        </div>
      </div>
    </section>
    <div class="grid">
      <div class="metric"><span>Workers online</span><strong>{{.Summary.WorkersOnline}} / {{.Summary.WorkersTotal}}</strong></div>
      <div class="metric"><span>Allowed CPU cores</span><strong>{{.Summary.Resources.CPU.CoresAllowed}}</strong></div>
      <div class="metric"><span>Allowed memory</span><strong>{{printf "%.1f" (gb .Summary.Resources.Memory.AllowedBytes)}} GB</strong></div>
      <div class="metric"><span>GPUs</span><strong>{{.Summary.GPUs}}</strong></div>
      <div class="metric"><span>Allowed VRAM</span><strong>{{printf "%.1f" (gb .Summary.VRAMAllowedBytes)}} GB</strong></div>
      <div class="metric"><span>Allowed storage</span><strong>{{printf "%.1f" (gb .Summary.Resources.Storage.AllowedBytes)}} GB</strong></div>
      <div class="metric"><span>Benchmark score</span><strong>{{printf "%.0f" .Summary.BenchmarkScore}}</strong></div>
    </div>
    </div>
    <div class="tab-panel" id="tab-readiness" hidden>
    <section id="readiness">
      <div class="section-head">
        <h2>Cluster Readiness</h2>
        <code>{{.Readiness.Status}}</code>
      </div>
      <div class="readiness-hero">
        <div class="readiness-status">
          <span class="{{if eq .Readiness.Status "ready"}}pill{{else if eq .Readiness.Status "warn"}}pill pill-job{{else}}pill pill-failed{{end}}">{{.Readiness.Status}}</span>
          <strong>{{if eq .Readiness.Status "ready"}}Ready for alpha test{{else if eq .Readiness.Status "warn"}}Ready with warnings{{else}}Blocked{{end}}</strong>
          <p class="sub">This screen checks whether the cluster is ready for a real model smoke test without jumping between Workers, Models, Jobs, and Activity.</p>
          <div class="readiness-grid">
            <div class="first-test-stat"><span>Workers</span><strong>{{.Readiness.WorkersOnline}}</strong></div>
            <div class="first-test-stat"><span>Runtime ready</span><strong>{{.Readiness.RuntimeReadyWorkers}}</strong></div>
            <div class="first-test-stat"><span>Ready models</span><strong>{{.Readiness.GeneratableModels}}</strong></div>
            <div class="first-test-stat"><span>Installed models</span><strong>{{.Readiness.InstalledModels}}</strong></div>
            <div class="first-test-stat"><span>Active jobs</span><strong>{{.Readiness.ActiveJobs}}</strong></div>
            <div class="first-test-stat"><span>Recent failures</span><strong>{{.Readiness.RecentFailures}}</strong></div>
          </div>
        </div>
        <div class="readiness-checks">
          {{range .Readiness.Checks}}
          <div class="readiness-check">
            <strong>{{.Name}}</strong>
            <div>
              <p>{{.Detail}}</p>
              <p class="sub">{{.Action}}</p>
            </div>
            <button class="button" type="button" data-tab-shortcut="{{.Tab}}">{{.Status}}</button>
          </div>
          {{end}}
        </div>
      </div>
      <div class="readiness-hero">
        <div class="readiness-status">
          <span class="pill pill-muted">capacity</span>
          <strong>Cluster model capacity</strong>
          <p class="sub">This shows the usable model capacity reported by online workers. Sharded estimates prove aggregate capacity, but distributed model execution is not implemented yet.</p>
          <div class="readiness-grid">
            <div class="first-test-stat"><span>Allowed CPU</span><strong>{{.Capacity.AllowedCPUCores}}</strong></div>
            <div class="first-test-stat"><span>Allowed RAM</span><strong>{{printf "%.1f" (gb .Capacity.AllowedMemoryBytes)}} GB</strong></div>
            <div class="first-test-stat"><span>Allowed disk</span><strong>{{printf "%.1f" (gb .Capacity.AllowedStorageBytes)}} GB</strong></div>
            <div class="first-test-stat"><span>Free disk</span><strong>{{printf "%.1f" (gb .Capacity.FreeStorageBytes)}} GB</strong></div>
            <div class="first-test-stat"><span>Allowed VRAM</span><strong>{{printf "%.1f" (gb .Capacity.AllowedVRAMBytes)}} GB</strong></div>
            <div class="first-test-stat"><span>Catalog models</span><strong>{{.Capacity.CatalogModels}}</strong></div>
          </div>
        </div>
        <div class="readiness-checks">
          <div class="readiness-check">
            <strong>Single worker</strong>
            <div>
              <p>{{.Capacity.SingleWorkerRunnableModels}} catalog model(s) can run on one online worker now.</p>
              {{if .Capacity.LargestSingleWorkerModel.ID}}
              <p class="sub">Largest: {{.Capacity.LargestSingleWorkerModel.Name}} · {{printf "%.1f" (gb .Capacity.LargestSingleWorkerModel.RequiredMemory)}} GB RAM · {{printf "%.1f" (gb .Capacity.LargestSingleWorkerModel.RequiredDisk)}} GB disk.</p>
              {{else}}
              <p class="sub">No model has a single-worker placement yet.</p>
              {{end}}
            </div>
            <button class="button" type="button" data-tab-shortcut="models">Models</button>
          </div>
          <div class="readiness-check">
            <strong>Sharded estimate</strong>
            <div>
              <p>{{.Capacity.ShardedEstimateModels}} catalog model(s) fit only as aggregate multi-worker estimates.</p>
              {{if .Capacity.LargestShardedModel.ID}}
              <p class="sub">Largest estimate: {{.Capacity.LargestShardedModel.Name}} · {{.Capacity.LargestShardedModel.ShardWorkers}} worker shards · {{printf "%.1f" (gb .Capacity.LargestShardedModel.RequiredMemory)}} GB RAM.</p>
              {{else}}
              <p class="sub">No aggregate-only model placement is currently feasible.</p>
              {{end}}
            </div>
            <button class="button" type="button" data-tab-shortcut="models">Catalog</button>
          </div>
          <div class="readiness-check">
            <strong>Blocked</strong>
            <div>
              <p>{{.Capacity.BlockedModels}} catalog model(s) do not fit current online resources.</p>
              <p class="sub">Add workers, increase allowed RAM/disk, or free storage on existing workers.</p>
            </div>
            <button class="button" type="button" data-tab-shortcut="workers">Workers</button>
          </div>
          <div class="readiness-check">
            <strong>Next unlock</strong>
            <div>
              {{if .Capacity.UnlockTargets}}{{with index .Capacity.UnlockTargets 0}}
              <p>{{.Model.Name}}</p>
              <p class="sub">Short by {{printf "%.1f" (gb .MemoryShortBytes)}} GB RAM · {{printf "%.1f" (gb .DiskShortBytes)}} GB disk.</p>
              {{end}}{{else}}
              <p>No blocked model targets.</p>
              <p class="sub">Current resources cover the catalog or no catalog target exists.</p>
              {{end}}
            </div>
            <button class="button" type="button" data-tab-shortcut="models">Catalog</button>
          </div>
        </div>
      </div>
      <div class="readiness-hero">
        <div class="readiness-status">
          <span class="pill pill-muted">growth</span>
          <strong>Worker capacity contributors</strong>
          <p class="sub">Each online worker contribution is measured from the resource limits it reports to this manager.</p>
        </div>
        <div class="readiness-checks">
          {{if .Capacity.Workers}}
          {{range .Capacity.Workers}}
          <div class="readiness-check">
            <strong>{{.Name}}</strong>
            <div>
              <p>{{printf "%.1f" (gb .AllowedMemoryBytes)}} GB RAM · {{printf "%.1f" (gb .AllowedStorageBytes)}} GB disk · {{.AllowedCPUCores}} CPU core(s){{if gt .AllowedVRAMBytes 0}} · {{printf "%.1f" (gb .AllowedVRAMBytes)}} GB VRAM{{end}}</p>
              <p class="sub">{{.RunnableModels}} runnable catalog model(s) on this worker{{if .LargestRunnableModel.ID}} · largest {{.LargestRunnableModel.Name}}{{end}} · RAM share {{printf "%.0f" .MemorySharePercent}}% · disk share {{printf "%.0f" .StorageSharePercent}}%</p>
            </div>
            <button class="button" type="button" data-tab-shortcut="workers">{{if .RuntimeReady}}runtime ready{{else}}runtime missing{{end}}</button>
          </div>
          {{end}}
          {{else}}
          <div class="readiness-check">
            <strong>No contributors</strong>
            <div>
              <p>No online workers are reporting capacity yet.</p>
              <p class="sub">Invite a worker to start measuring cluster growth.</p>
            </div>
            <button class="button" type="button" data-tab-shortcut="workers">Workers</button>
          </div>
          {{end}}
        </div>
      </div>
      <div class="capacity-snapshot-panel">
        <div class="section-head">
          <div>
            <h3>Capacity snapshots</h3>
            <p class="sub">Save a baseline, connect more workers, then compare current cluster capacity against that baseline.</p>
          </div>
          <code id="capacity-snapshot-count">0 snapshots</code>
        </div>
        <div class="capacity-snapshot-toolbar">
          <input id="capacity-snapshot-label" type="text" placeholder="Baseline label">
          <button class="button primary" type="button" id="capacity-save-snapshot"><svg class="icon"><use href="#icon-plus"></use></svg>Save baseline</button>
          <button class="button" type="button" id="capacity-refresh-snapshots"><svg class="icon"><use href="#icon-refresh"></use></svg>Refresh</button>
        </div>
        <div class="runner-status" id="capacity-snapshot-status">No baseline selected.</div>
        <div class="capacity-delta-grid" id="capacity-delta-grid" hidden></div>
        <div class="capacity-snapshot-list" id="capacity-snapshot-list"></div>
      </div>
      <div class="readiness-actions">
        <button class="button" type="button" data-tab-shortcut="workers"><svg class="icon"><use href="#icon-workers"></use></svg>Workers</button>
        <button class="button" type="button" data-tab-shortcut="models"><svg class="icon"><use href="#icon-brain"></use></svg>Models</button>
        <button class="button" type="button" data-tab-shortcut="model-activity"><svg class="icon"><use href="#icon-terminal"></use></svg>Model Activity</button>
        <button class="button {{if eq .Readiness.Status "ready"}}primary{{end}}" type="button" data-tab-shortcut="chat" {{if ne .Readiness.Status "ready"}}disabled{{end}}><svg class="icon"><use href="#icon-send"></use></svg>Open Chat</button>
      </div>
    </section>
    </div>
    <div class="tab-panel" id="tab-workers" hidden>
    <section>
      <div class="section-head">
        <h2>Worker Inventory</h2>
        <code>{{.OfflineWorkerCount}} offline hidden</code>
      </div>
      {{if .OnlineNodes}}
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Status</th>
              <th>CPU</th>
              <th>Memory</th>
              <th>Storage</th>
              <th>Installed models</th>
              <th>Runtime</th>
              <th>GPU</th>
              <th>Job slots</th>
              <th>Benchmark</th>
              <th>Last seen</th>
            </tr>
          </thead>
          <tbody>
          {{range .OnlineNodes}}
            <tr>
              <td><code>{{.Name}}</code><br><span class="sub">{{.ID}}</span></td>
              <td><span class="pill">{{.Status}}</span><br><span class="sub">heartbeat {{heartbeatAge .UpdatedAt}}</span></td>
              <td>{{.Resources.CPU.CoresAllowed}} / {{.Resources.CPU.CoresTotal}} cores</td>
              <td>{{printf "%.1f" (gb .Resources.Memory.AllowedBytes)}} / {{printf "%.1f" (gb .Resources.Memory.TotalBytes)}} GB</td>
              <td>
                {{printf "%.1f" (gb .Resources.Storage.AllowedBytes)}} GB allowed<br>
                <span class="sub">{{printf "%.1f" (gb .Resources.Storage.FreeBytes)}} GB free</span><br>
                <span class="sub">CMesh {{printf "%.1f" (gb .Resources.Storage.UsedByCacheBytes)}} GB</span><br>
                <span class="sub">models {{printf "%.1f" (gb .Resources.Storage.UsedByModelsBytes)}} GB · runtimes {{printf "%.1f" (gb .Resources.Storage.UsedByRuntimesBytes)}} GB</span>
                {{if or (gt .Resources.Storage.PartialModelFiles 0) (gt .Resources.Storage.OrphanModelDirs 0)}}<br><span class="sub">cleanup candidates: {{.Resources.Storage.PartialModelFiles}} partial / {{.Resources.Storage.OrphanModelDirs}} orphan · {{printf "%.1f" (gb .Resources.Storage.PartialModelBytes)}} GB partial · {{printf "%.1f" (gb .Resources.Storage.OrphanModelBytes)}} GB orphan</span><br><button class="button model-cleanup" type="button" data-node-id="{{.ID}}"><svg class="icon"><use href="#icon-refresh"></use></svg><span>Cleanup cache</span></button>{{end}}
              </td>
              <td>
                {{range .Resources.Models}}
                  <div><strong>{{.Name}}</strong><br><span class="sub">{{printf "%.0f" (mb .Bytes)}} MB</span></div>
                {{else}}
                  <span class="sub">No installed models reported</span>
                {{end}}
                {{if .Resources.Models}}<div class="sub">total {{printf "%.0f" (mb (workerModelBytes .Resources.Models))}} MB</div>{{end}}
              </td>
              <td><span class="sub">{{runtimeSummary .Resources.Runtimes}}</span></td>
              <td>{{range .Resources.GPU}}<div>{{.Name}}</div>{{else}}0{{end}}</td>
              <td>{{index $.WorkerActiveJobs .ID}} / {{workerSlots .}} active</td>
              <td>{{with index $.Benchmarks .ID}}{{printf "%.0f" .TotalScore}}{{else}}Not run{{end}}</td>
              <td>{{heartbeatAge .UpdatedAt}}<br><span class="sub">{{.UpdatedAt.Format "15:04:05 MST"}}</span></td>
            </tr>
          {{end}}
          </tbody>
        </table>
      </div>
      {{else}}
      <div class="empty">No workers are online right now.</div>
      {{end}}
    </section>
    </div>
    <div class="tab-panel" id="tab-rpc-pool" hidden>
    <section>
      <div class="section-head">
        <h2>Distributed RPC Pool</h2>
        <code>{{.RPCPool.Endpoints}} active endpoint{{if ne .RPCPool.Endpoints 1}}s{{end}}</code>
      </div>
      <div class="stat-grid">
        <div class="stat"><span>Online workers</span><strong>{{.RPCPool.Workers}}</strong></div>
        <div class="stat"><span>Runtime ready</span><strong>{{.RPCPool.RuntimeReadyWorkers}}</strong></div>
        <div class="stat"><span>RPC ready</span><strong>{{.RPCPool.RPCReadyWorkers}}</strong></div>
        <div class="stat"><span>Endpoints</span><strong>{{.RPCPool.Endpoints}}</strong></div>
      </div>
      {{if .RPCEndpoints}}
      <div class="runner-status">
        <strong>llama-cli RPC argument</strong><br>
        <code>--rpc {{range $index, $endpoint := .RPCEndpoints}}{{if $index}},{{end}}{{$endpoint}}{{end}}</code>
      </div>
      <div class="actions">
        <button class="button" type="button" id="rpc-health-refresh"><svg class="icon"><use href="#icon-refresh"></use></svg><span>Refresh RPC health</span></button>
        <button class="button primary" type="button" id="rpc-smoke-run"><svg class="icon"><use href="#icon-play"></use></svg><span>Run RPC smoke test</span></button>
      </div>
      <div class="runner-status" id="rpc-smoke-result">Smoke test checks TCP reachability for active RPC endpoints before distributed inference.</div>
      {{else}}
      <div class="empty">No RPC backend is active yet. Open Worker Desktop on a runtime-ready machine and start RPC backend from the AI runtime tab.</div>
      {{end}}
      <form class="cluster-runner" id="rpc-prompt-smoke-form">
        <div class="field span-2">
          <label for="rpc-smoke-model">Model</label>
          <select id="rpc-smoke-model" name="model_id">
            {{if eq (generatableCount .Models) 0}}<option value="">Install a model first</option>{{end}}
            {{range .Models}}{{if modelCanGenerate .}}{{$preset := modelPreset .Model}}<option value="{{.Model.ID}}" data-max-tokens="{{$preset.MaxTokens}}" data-temperature="{{$preset.Temperature}}">{{.Model.Name}}</option>{{end}}{{end}}
          </select>
        </div>
        <div class="field span-2">
          <label for="rpc-smoke-node">Coordinator worker</label>
          <select id="rpc-smoke-node" name="node_id">
            {{if eq (generatableCount .Models) 0}}<option value="">No installed model worker</option>{{end}}
            {{range .Models}}{{$rpcModelID := .Model.ID}}{{range modelReadyNodeOptions $.NodesByID .}}<option value="{{.ID}}" data-model-id="{{$rpcModelID}}">{{.Name}}</option>{{end}}{{end}}
          </select>
        </div>
        <div class="field">
          <label for="rpc-smoke-max-tokens">Max tokens</label>
          <input id="rpc-smoke-max-tokens" name="max_tokens" type="number" min="16" max="2048" step="16" value="128">
        </div>
        <div class="field">
          <label>&nbsp;</label>
          <button class="button primary" type="submit" {{if or (eq .RPCPool.Endpoints 0) (eq (generatableCount .Models) 0)}}disabled{{end}}><svg class="icon"><use href="#icon-send"></use></svg>Run distributed prompt</button>
        </div>
        <div class="field span-6">
          <label for="rpc-smoke-prompt">Prompt</label>
          <textarea id="rpc-smoke-prompt" name="prompt" placeholder="Ask a short test question through the RPC pool">Reply with one sentence: what runtime path handled this request?</textarea>
        </div>
      </form>
      <div class="runner-status" id="rpc-execution-plan">Select a model and coordinator to inspect the distributed RPC execution plan.</div>
      <div class="runner-status" id="rpc-prompt-smoke-status">{{if eq .RPCPool.Endpoints 0}}Start at least one RPC backend before running a distributed prompt.{{else if eq (generatableCount .Models) 0}}Install a model before running a distributed prompt.{{else}}Ready to run a distributed prompt smoke test.{{end}}</div>
      {{if .RPCWorkers}}
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Worker</th>
              <th>RPC status</th>
              <th>Endpoint</th>
              <th>Runtime</th>
              <th>Protocol</th>
              <th>Blockers</th>
              <th>Capabilities</th>
            </tr>
          </thead>
          <tbody>
          {{range .RPCWorkers}}
            <tr>
              <td><code>{{.NodeName}}</code><br><span class="sub">{{.NodeID}}</span></td>
              <td><span class="{{if .RPC.Ready}}pill{{else}}pill pill-failed{{end}}">{{if .RPC.Ready}}ready{{else}}blocked{{end}}</span></td>
              <td><code>{{if .RPC.Endpoint}}{{.RPC.Endpoint}}{{else}}-{{end}}</code></td>
              <td>{{.Runtime}}<br><span class="sub">{{if .RuntimeReady}}runtime ready{{else}}runtime missing{{end}}</span></td>
              <td>{{if .RPC.Protocol}}{{.RPC.Protocol}}{{else}}-{{end}}</td>
              <td>
                {{if .RPC.Blockers}}
                  {{range .RPC.Blockers}}<div class="sub">{{.}}</div>{{end}}
                {{else if .RPC.Ready}}
                  <span class="sub">Ready for distributed RPC inference.</span>
                {{else}}
                  <span class="sub">RPC backend is not ready.</span>
                {{end}}
              </td>
              <td>
                {{range .Capabilities}}<span class="pill pill-muted">{{.}}</span> {{else}}<span class="sub">-</span>{{end}}
              </td>
            </tr>
          {{end}}
          </tbody>
        </table>
      </div>
      {{else}}
      <div class="empty">No online runtime-ready workers report llama.cpp RPC support yet.</div>
      {{end}}
      {{if .RPCHealth}}
      <div class="section-head">
        <h3>RPC Health History</h3>
        <code>{{len .RPCHealth}} endpoint record{{if ne (len .RPCHealth) 1}}s{{end}}</code>
      </div>
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Endpoint</th>
              <th>Status</th>
              <th>Worker</th>
              <th>Score</th>
              <th>Latency</th>
              <th>Last error</th>
              <th>Updated</th>
            </tr>
          </thead>
          <tbody>
          {{range .RPCHealth}}
            <tr>
              <td><code>{{.Endpoint}}</code></td>
              <td><span class="{{if .Ready}}pill{{else}}pill pill-failed{{end}}">{{if .Ready}}ready{{else}}failed{{end}}</span></td>
              <td>{{if .NodeName}}<code>{{.NodeName}}</code>{{else}}<span class="sub">unknown</span>{{end}}{{if .NodeID}}<br><span class="sub">{{shortID .NodeID}}</span>{{end}}</td>
              <td>{{.Successes}} ok / {{.Failures}} failed{{if .ConsecutiveFailures}}<br><span class="sub">{{.ConsecutiveFailures}} consecutive failure{{if ne .ConsecutiveFailures 1}}s{{end}}</span>{{end}}</td>
              <td>{{if .LastLatencyMS}}{{.LastLatencyMS}} ms{{else}}-{{end}}</td>
              <td>{{if .LastError}}<span class="sub">{{clip .LastError 120}}</span>{{else}}<span class="sub">-</span>{{end}}</td>
              <td>{{formatClock .UpdatedAt}}</td>
            </tr>
          {{end}}
          </tbody>
        </table>
      </div>
      {{else}}
      <div class="empty">No RPC health history yet. Run a plan check, smoke test, or distributed prompt.</div>
      {{end}}
    </section>
    </div>
    <div class="tab-panel" id="tab-distributed-runs" hidden>
    <section>
      <div class="section-head">
        <h2>Distributed Runs</h2>
        <code>{{len .DistributedRuns}} protocol run{{if ne (len .DistributedRuns) 1}}s{{end}}</code>
      </div>
      {{if .DistributedRuns}}
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Run</th>
              <th>Status</th>
              <th>Model</th>
              <th>Coordinator</th>
              <th>Protocol</th>
              <th>Runtime</th>
              <th>Endpoints</th>
              <th>Result</th>
            </tr>
          </thead>
          <tbody>
          {{range .DistributedRuns}}
            <tr>
              <td><code>{{shortID .JobID}}</code>{{if .PlanID}}<br><span class="sub">plan {{shortID .PlanID}}</span>{{end}}<br><span class="sub">{{formatClock .CreatedAt}}</span></td>
              <td><span class="{{jobPillClass .Status}}">{{.Status}}</span>{{if .FinishedAt.IsZero}}{{else}}<br><span class="sub">finished {{formatClock .FinishedAt}}</span>{{end}}</td>
              <td><code>{{if .ModelID}}{{.ModelID}}{{else}}-{{end}}</code></td>
              <td>{{if .CoordinatorNodeName}}<code>{{.CoordinatorNodeName}}</code>{{else if .CoordinatorNodeID}}<code>{{shortID .CoordinatorNodeID}}</code>{{else}}<span class="sub">-</span>{{end}}{{if and .CoordinatorNodeName .CoordinatorNodeID}}<br><span class="sub">{{shortID .CoordinatorNodeID}}</span>{{end}}</td>
              <td><code>{{if .Protocol}}{{.Protocol}}{{else}}-{{end}}</code><br><span class="sub">v{{.ProtocolVersion}} schema {{.SchemaVersion}}</span>{{if .Mode}}<br><span class="sub">{{.Mode}}</span>{{end}}</td>
              <td>{{if .Runtime}}<code>{{.Runtime}}</code>{{else}}<span class="sub">pending</span>{{end}}{{if .RuntimeVersion}}<br><span class="sub">{{.RuntimeVersion}}</span>{{end}}{{if .WorkerRuntime}}<br><span class="sub">{{.WorkerRuntime}}</span>{{end}}</td>
              <td>{{if .EndpointCount}}{{.EndpointCount}} endpoint{{if ne .EndpointCount 1}}s{{end}}{{else}}-{{end}}{{if .Endpoints}}<br><span class="sub">{{clip (joinStrings .Endpoints ", ") 140}}</span>{{end}}</td>
              <td>{{if .DurationMS}}{{.DurationMS}} ms{{else}}-{{end}}{{if .Output}}<br><span class="sub">{{clip .Output 140}}</span>{{end}}{{if .Error}}<br><span class="sub">{{clip .Error 140}}</span>{{end}}</td>
            </tr>
          {{end}}
          </tbody>
        </table>
      </div>
      {{else}}
      <div class="empty">No distributed RPC runs yet. Start an RPC backend, install a model, then run a distributed prompt from RPC Pool.</div>
      {{end}}
    </section>
    </div>
    <div class="tab-panel" id="tab-model-inventory" hidden>
    <section id="model-inventory">
      <div class="section-head">
        <h2>Model Inventory</h2>
        <code>{{installedInstanceCount .Models}} worker installs</code>
      </div>
      {{if gt (installedInstanceCount .Models) 0}}
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Worker</th>
              <th>Model</th>
              <th>Model state</th>
              <th>Runtime</th>
              <th>Generate</th>
              <th>Storage</th>
              <th>Path</th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
          {{range .Models}}
          {{$modelID := .Model.ID}}
          {{$modelName := .Model.Name}}
          {{range .Installed}}
            <tr data-model-id="{{$modelID}}" data-model-surface="inventory">
              <td><code>{{.NodeName}}</code><br><span class="sub">{{shortID .NodeID}}</span></td>
              <td>
                <strong>{{$modelName}}</strong><br>
                <span class="sub">{{if .Family}}{{.Family}}{{else}}-{{end}} · {{printf "%.0f" (mb .Bytes)}} MB · installed {{formatClock .InstalledAt}}</span>
              </td>
              <td>
                {{if .ModelReady}}<span class="pill">model ready</span>{{else}}<span class="pill pill-failed">model blocked</span>{{end}}
                {{if .ModelError}}<br><span class="sub">{{.ModelError}}</span>{{end}}
                {{if .RepairReason}}<br><span class="sub">repair: {{.RepairReason}}</span>{{end}}
                {{if .ActiveJobID}}<br><span class="sub">active {{.ActiveJobID}}</span>{{end}}
              </td>
              <td>
                {{if .RuntimeReady}}<span class="pill">runtime ready</span>{{else}}<span class="pill pill-failed">runtime blocked</span>{{end}}<br>
                <span class="sub">{{.Runtime}}{{if .RuntimeStatus.Version}} · {{.RuntimeStatus.Version}}{{end}}{{if .RuntimeStatus.Source}} · {{.RuntimeStatus.Source}}{{end}}</span>
                {{if and (not .RuntimeReady) .RuntimeStatus.Error}}<br><span class="sub">{{.RuntimeStatus.Error}}</span>{{end}}
              </td>
              <td>
                {{if .GenerateReady}}<span class="pill">ready</span>{{else}}<span class="pill pill-failed">blocked</span>{{end}}
                {{if .GenerateBlocked}}<br><span class="sub">{{.GenerateBlocked}}</span>{{end}}
              </td>
              <td>
                <strong>{{printf "%.1f" (gb .UsedByModelsBytes)}} GB</strong> models<br>
                <span class="sub">{{printf "%.1f" (gb .UsedByCacheBytes)}} GB CMesh cache</span><br>
                <span class="sub">{{printf "%.1f" (gb .AllowedStorageBytes)}} GB allowed · {{printf "%.1f" (gb .FreeStorageBytes)}} GB free</span>
              </td>
              <td><code>{{.Path}}</code></td>
              <td>
                <div class="model-actions">
                  <button class="button model-repair" type="button" data-model-id="{{$modelID}}" data-node-id="{{.NodeID}}" data-repairable="{{.Repairable}}" {{if or .ActiveJobID (not .Repairable)}}disabled{{end}}><svg class="icon"><use href="#icon-refresh"></use></svg><span>Repair</span></button>
                  <button class="button danger model-delete" type="button" data-model-id="{{$modelID}}" data-node-id="{{.NodeID}}" {{if .ActiveJobID}}disabled{{end}}><svg class="icon"><use href="#icon-trash"></use></svg><span>Delete</span></button>
                </div>
              </td>
            </tr>
          {{end}}
          {{end}}
          </tbody>
        </table>
      </div>
      {{else}}
      <div class="empty-action">
        <div>
          <h3>No model inventory yet</h3>
          <p>Install a catalog model on an online worker to populate worker-level inventory.</p>
          <button class="button primary" type="button" data-tab-shortcut="models"><svg class="icon"><use href="#icon-brain"></use></svg>Open Model Catalog</button>
        </div>
      </div>
      {{end}}
    </section>
    </div>
    <div class="tab-panel" id="tab-chat" hidden>
    <section id="chat">
      <div class="section-head">
        <h2>Model Chat</h2>
        <code>{{generatableCount .Models}} ready models</code>
      </div>
      <div class="chat-shell">
        <form class="chat-main" id="model-chat-form">
          <div class="chat-topbar">
            <div>
              <h3>Ask the cluster</h3>
              <p class="sub">Choose a ready local model, tune the system prompt, and keep context across messages.</p>
            </div>
            <div class="chat-selectors">
              <div class="field">
                <label for="chat-model">Model</label>
                <select id="chat-model" name="model_id">
                  {{if eq (generatableCount .Models) 0}}<option value="">Install a model first</option>{{end}}
                  {{range .Models}}{{if modelCanGenerate .}}{{$preset := modelPreset .Model}}<option value="{{.Model.ID}}" data-temperature="{{$preset.Temperature}}" data-max-tokens="{{$preset.MaxTokens}}" data-system-prompt="{{$preset.SystemPrompt}}">{{.Model.Name}}</option>{{end}}{{end}}
                </select>
              </div>
              <div class="field">
                <label for="chat-node">Worker</label>
                <select id="chat-node" name="node_id">
                  {{if eq (generatableCount .Models) 0}}<option value="">No installed model worker</option>{{end}}
                  {{range .Models}}{{$chatModelID := .Model.ID}}{{range modelReadyNodeOptions $.NodesByID .}}<option value="{{.ID}}" data-model-id="{{$chatModelID}}">{{.Name}}</option>{{end}}{{end}}
                </select>
              </div>
              <label class="inline-toggle" title="Use active llama.cpp RPC endpoints from this cluster for distributed inference.">
                <input id="chat-use-rpc" name="use_rpc" type="checkbox" {{if eq .RPCPool.Endpoints 0}}disabled{{end}}>
                <span>RPC pool</span>
                <code>{{.RPCPool.Endpoints}} endpoint{{if ne .RPCPool.Endpoints 1}}s{{end}}</code>
              </label>
              <div class="chat-settings">
                <div class="field">
                  <label for="chat-system-prompt">System prompt</label>
                  <textarea id="chat-system-prompt" name="system_prompt" placeholder="Default model adapter prompt"></textarea>
                </div>
                <div class="field">
                  <label for="chat-temperature">Temperature</label>
                  <input id="chat-temperature" name="temperature" type="number" min="0" max="2" step="0.1" value="0.7">
                </div>
                <div class="field">
                  <label for="chat-max-tokens">Max tokens</label>
                  <input id="chat-max-tokens" name="max_tokens" type="number" min="16" max="2048" step="16" value="512">
                </div>
              </div>
            </div>
          </div>
          <div class="chat-thread" id="chat-thread">
            {{if eq (generatableCount .Models) 0}}
            <div class="chat-empty">
              <h3>No model is ready yet</h3>
              <p>Install a model from the Models tab before chatting.</p>
              <button class="button primary" type="button" data-tab-shortcut="models"><svg class="icon"><use href="#icon-brain"></use></svg>Go to Models</button>
            </div>
            {{else}}
            <div class="chat-empty">
              <h3>What should this cluster answer?</h3>
              <p>Responses run on your selected worker, not an external API.</p>
            </div>
            {{end}}
          </div>
          <div class="chat-composer">
            <textarea id="chat-prompt" name="prompt" placeholder="Message the selected local model"></textarea>
            <div class="chat-composer-actions">
              <div class="runner-status" id="model-status">{{if eq (generatableCount .Models) 0}}Install a model first, then submit a prompt.{{else}}Ready.{{end}}</div>
              <button class="button primary" type="submit" {{if or (not .OnlineNodes) (eq (generatableCount .Models) 0)}}disabled{{end}}><svg class="icon"><use href="#icon-send"></use></svg>Generate</button>
            </div>
          </div>
        </form>
      </div>
    </section>
    </div>
    <div class="tab-panel" id="tab-memory" hidden>
    <section id="memory">
      <div class="section-head">
        <h2>Model Memory</h2>
        <code>model-scoped context</code>
      </div>
      <div class="memory-shell">
        <div class="memory-panel">
          <div class="memory-panel-head">
            <h3>Saved memory</h3>
            <button class="button danger" id="memory-clear-model" type="button"><svg class="icon"><use href="#icon-trash"></use></svg>Clear model</button>
          </div>
          <div class="field">
            <label for="memory-model-select">Model</label>
            <select id="memory-model-select">
              {{if eq (generatableCount .Models) 0}}<option value="">Install a model first</option>{{end}}
              {{range .Models}}{{if modelCanGenerate .}}<option value="{{.Model.ID}}">{{.Model.Name}}</option>{{end}}{{end}}
            </select>
          </div>
          <div class="memory-list" id="memory-list">
            <div class="sub">Select a model to load memory.</div>
          </div>
        </div>
        <form class="memory-panel" id="memory-editor">
          <input type="hidden" name="memory_id" value="">
          <div>
            <h3>Add or edit memory</h3>
            <p class="sub">Memory is injected into prompts for this model before conversation history.</p>
          </div>
          <div class="field">
            <label for="memory-key">Key</label>
            <input id="memory-key" name="key" type="text" placeholder="user.name">
          </div>
          <div class="field">
            <label for="memory-value">Value</label>
            <textarea id="memory-value" name="value" placeholder="Sergiy"></textarea>
          </div>
          <div class="actions">
            <button class="button primary" type="submit"><svg class="icon"><use href="#icon-plus"></use></svg><span>Save memory</span></button>
            <button class="button" id="memory-editor-reset" type="button"><svg class="icon"><use href="#icon-x"></use></svg>Reset</button>
          </div>
        </form>
      </div>
    </section>
    </div>
    <div class="tab-panel" id="tab-conversations" hidden>
    <section id="conversations">
      <div class="section-head">
        <h2>Conversations</h2>
        <code>{{len .Conversations}} saved</code>
      </div>
      <div class="conversation-shell">
        <aside class="conversation-sidebar">
          <div class="conversation-sidebar-head">
            <button class="button primary wide" id="new-chat-button" type="button"><svg class="icon"><use href="#icon-plus"></use></svg>New chat</button>
            <div class="sub">Conversation history is stored in this cluster manager.</div>
          </div>
          <div class="conversation-list" id="conversation-list">
            {{range .Conversations}}
            <div class="conversation-row" data-conversation-id="{{.ID}}">
              <button class="conversation-item" type="button" data-conversation-id="{{.ID}}">
                <span class="conversation-title">{{conversationTitle .}}</span>
                <span class="conversation-meta">{{conversationSubtitle .}}</span>
              </button>
              <button class="button danger conversation-delete" type="button" data-conversation-id="{{.ID}}"><svg class="icon"><use href="#icon-trash"></use></svg></button>
            </div>
            {{else}}
            <div class="empty">No saved conversations yet.</div>
            {{end}}
          </div>
        </aside>
        <div class="empty-action">
          <div>
            <h3>Open a conversation</h3>
            <p>Select a saved conversation to load it into the Chat screen, or start a clean one.</p>
            <button class="button" type="button" data-tab-shortcut="chat"><svg class="icon"><use href="#icon-send"></use></svg>Open Chat</button>
          </div>
        </div>
      </div>
    </section>
    </div>
    <div class="tab-panel" id="tab-prompt-debug" hidden>
    <section id="prompt-debug">
      <div class="section-head">
        <h2>Prompt Debug</h2>
        <code>effective context</code>
      </div>
      <div class="debug-shell">
        <div class="memory-panel">
          <h3>Preview input</h3>
          <div class="field">
            <label for="debug-model-select">Model</label>
            <select id="debug-model-select">
              {{if eq (generatableCount .Models) 0}}<option value="">Install a model first</option>{{end}}
              {{range .Models}}{{if modelCanGenerate .}}<option value="{{.Model.ID}}">{{.Model.Name}}</option>{{end}}{{end}}
            </select>
          </div>
          <p class="sub">This shows exactly what memory and system prompt context will be sent before the chat messages.</p>
        </div>
        <div class="memory-preview">
          <span class="conversation-meta">Context budget</span>
          <div class="context-metrics" id="context-metrics"></div>
          <span class="conversation-meta">Effective system context</span>
          <pre id="memory-preview-text">Select a model to preview the prompt context.</pre>
          <span class="conversation-meta">Included chat messages</span>
          <div class="context-message-list" id="context-message-list"></div>
        </div>
      </div>
    </section>
    </div>
    <div class="tab-panel" id="tab-installed-models" hidden>
    <section id="installed-models">
      <div class="section-head">
        <h2>Installed Models</h2>
        <code>{{installedInstanceCount .Models}} worker installs</code>
      </div>
      {{if gt (installedInstanceCount .Models) 0}}
      <div class="installed-models">
        {{range .Models}}
        {{$modelID := .Model.ID}}
        {{$modelName := .Model.Name}}
        {{range .Installed}}
        <article class="installed-model" data-model-id="{{$modelID}}" data-model-surface="installed">
          <div class="model-title">
            <div>
              <h3>{{$modelName}}</h3>
              <p class="sub"><code>{{.NodeName}}</code></p>
            </div>
            {{if .GenerateReady}}<span class="pill">generate ready</span>{{else}}<span class="pill pill-failed">generate blocked</span>{{end}}
          </div>
          <div class="model-specs">
            <div><span>Size</span><strong>{{printf "%.0f" (mb .Bytes)}} MB</strong></div>
            <div><span>Family</span><strong>{{if .Family}}{{.Family}}{{else}}-{{end}}</strong></div>
            <div><span>Runtime</span><strong>{{.Runtime}}</strong></div>
            <div><span>Worker</span><strong>{{shortID .NodeID}}</strong></div>
            <div><span>Installed</span><strong>{{formatClock .InstalledAt}}</strong></div>
          </div>
          {{if .ModelError}}
          <div class="hint">Model inventory warning on this worker: {{.ModelError}}</div>
          {{end}}
          {{if .RepairReason}}
          <p class="sub">Repair action: {{.RepairReason}}</p>
          {{end}}
          <div class="storage-detail">
            <div><strong>{{printf "%.1f" (gb .UsedByModelsBytes)}} GB</strong> used by worker models</div>
            <div><strong>{{printf "%.1f" (gb .UsedByCacheBytes)}} GB</strong> used by CMesh cache</div>
            <div><strong>{{printf "%.1f" (gb .AllowedStorageBytes)}} GB</strong> allowed · <strong>{{printf "%.1f" (gb .FreeStorageBytes)}} GB</strong> free on disk</div>
          </div>
          <p class="sub">Path <code>{{.Path}}</code></p>
          {{if .RuntimeReady}}
          <p class="sub">Runtime ready{{if .RuntimeStatus.Version}} · {{.RuntimeStatus.Version}}{{end}}{{if .RuntimeStatus.Source}} · {{.RuntimeStatus.Source}}{{end}}</p>
          {{else}}
          <div class="hint">Runtime is not ready on this worker: {{if .RuntimeStatus.Error}}{{.RuntimeStatus.Error}}{{else}}not reported{{end}}</div>
          <p class="sub">Open CMesh Worker on <code>{{.NodeName}}</code>, then use Runtime → Repair runtime.</p>
          {{end}}
          {{if .ActiveJobID}}
          <div class="hint">Model is busy on this worker: {{.ActiveJobID}}</div>
          {{end}}
          {{if .GenerateBlocked}}
          <div class="hint">Generate blocked: {{.GenerateBlocked}}</div>
          {{end}}
          <div class="model-actions">
            <button class="button model-repair" type="button" data-model-id="{{$modelID}}" data-node-id="{{.NodeID}}" data-repairable="{{.Repairable}}" {{if or .ActiveJobID (not .Repairable)}}disabled{{end}}><svg class="icon"><use href="#icon-refresh"></use></svg><span>Repair</span></button>
            <button class="button danger model-delete" type="button" data-model-id="{{$modelID}}" data-node-id="{{.NodeID}}" {{if .ActiveJobID}}disabled{{end}}><svg class="icon"><use href="#icon-trash"></use></svg><span>Delete from {{.NodeName}}</span></button>
          </div>
        </article>
        {{end}}
        {{end}}
      </div>
      {{else}}
      <div class="empty-action">
        <div>
          <h3>No installed models</h3>
          <p>Install a catalog model on a capable online worker before using Chat.</p>
          <button class="button primary" type="button" data-tab-shortcut="models"><svg class="icon"><use href="#icon-brain"></use></svg>Open Model Catalog</button>
        </div>
      </div>
      {{end}}
    </section>
    </div>
    <div class="tab-panel" id="tab-models" hidden>
    <section id="models">
      <div class="section-head">
        <h2>Model Catalog</h2>
        <code>{{len .Models}} catalog entries</code>
      </div>
      <div class="catalog-toolbar">
        <div class="field">
          <label for="model-catalog-search">Search</label>
          <input id="model-catalog-search" type="search" placeholder="Model, family, quant, runtime">
        </div>
        <div class="field">
          <label for="model-catalog-status">Status</label>
          <select id="model-catalog-status">
            <option value="">Any status</option>
            <option value="available">Available</option>
            <option value="installed">Installed</option>
            <option value="installing">Installing</option>
            <option value="repairing">Repairing</option>
            <option value="deleting">Deleting</option>
          </select>
        </div>
        <div class="field">
          <label for="model-catalog-family">Family</label>
          <select id="model-catalog-family">
            <option value="">Any family</option>
            {{range .Models}}<option value="{{.Model.Family}}">{{.Model.Family}}</option>{{end}}
          </select>
        </div>
        <div class="field">
          <label for="model-catalog-sort">Sort</label>
          <select id="model-catalog-sort">
            <option value="recommended">Recommended</option>
            <option value="name">Name</option>
            <option value="ram-asc">RAM low to high</option>
            <option value="ram-desc">RAM high to low</option>
            <option value="disk-asc">Disk low to high</option>
            <option value="capable-desc">Most capable</option>
          </select>
        </div>
        <label class="catalog-toggle" for="model-catalog-capable"><input id="model-catalog-capable" type="checkbox">Capable only</label>
        <div class="catalog-count" id="model-catalog-count">{{len .Models}} shown</div>
        <button class="button catalog-clear" type="button" id="model-catalog-clear">Clear</button>
      </div>
      <div class="catalog-empty" id="model-catalog-empty">
        <strong>No models match these filters</strong>
        Adjust search, status, family, or capability filters.
      </div>
      <div class="model-catalog">
          {{range .Models}}
          {{$catalogModelID := .Model.ID}}
          {{$catalogStatus := .Status}}
          {{$placement := modelPlacement .}}
          <article class="model-card" data-model-id="{{.Model.ID}}" data-model-surface="catalog" data-model-search="{{.Model.ID}} {{.Model.Name}} {{.Model.Family}} {{.Model.Parameters}} {{.Model.Quant}} {{.Model.Runtime}} {{.Model.Description}}" data-model-status-value="{{.Status}}" data-model-family="{{.Model.Family}}" data-model-capable="{{if gt .CapableNodes 0}}true{{else}}false{{end}}" data-model-name="{{.Model.Name}}" data-model-memory="{{.Model.MemoryBytes}}" data-model-disk="{{.Model.DiskBytes}}" data-model-capable-nodes="{{.CapableNodes}}">
            <div class="model-title">
              <div>
                <h3>{{.Model.Name}}</h3>
                <p class="sub">{{.Model.Description}}</p>
              </div>
              <span class="{{modelStatusClass .Status}}" data-model-status="{{.Model.ID}}">{{.Status}}</span>
            </div>
            <div class="model-specs">
              <div><span>Model</span><strong>{{.Model.Parameters}} / {{.Model.Quant}}</strong></div>
              <div><span>Required RAM</span><strong>{{printf "%.1f" (gb .Model.MemoryBytes)}} GB</strong></div>
              <div><span>Required disk</span><strong>{{printf "%.1f" (gb .Model.DiskBytes)}} GB</strong></div>
            </div>
            <div class="{{modelPlacementClass $placement}}" data-model-placement="{{.Model.ID}}" data-placement-mode="{{$placement.Mode}}">
              <strong>{{modelPlacementLabel $placement}}</strong>
              <span>{{modelPlacementHint $placement}}</span>
              <code>RAM {{printf "%.1f" (gb $placement.RequiredMemoryBytes)}} GB · disk {{printf "%.1f" (gb $placement.RequiredDiskBytes)}} GB</code>
            </div>
            <p class="sub">Runtime {{.Model.Runtime}} · context {{.Model.Context}} · {{.CapableNodes}} capable online workers</p>
            {{if modelFailureHint .}}<div class="hint">{{modelFailureHint .}}</div>{{end}}
            {{if .Capabilities}}
            <div class="capability-list">
              {{range .Capabilities}}
              <div class="capability-row">
                <strong>{{.Name}}</strong>
                {{if .Capable}}
                <span>ready · jobs {{.ActiveJobs}}/{{.JobSlots}} · {{printf "%.1f" (gb .AllowedMemoryBytes)}} GB RAM · {{printf "%.1f" (gb .AllowedStorageBytes)}} GB allowed disk · {{printf "%.1f" (gb .FreeStorageBytes)}} GB free{{if gt .AllowedVRAMBytes 0}} · {{printf "%.1f" (gb .AllowedVRAMBytes)}} GB VRAM{{end}}</span>
                <button class="button model-install" type="button" data-model-id="{{$catalogModelID}}" data-node-id="{{.NodeID}}" {{if or (or (eq $catalogStatus "installing") (eq $catalogStatus "deleting")) (eq $catalogStatus "repairing")}}disabled{{end}}><svg class="icon"><use href="#icon-download"></use></svg><span>Install here</span></button>
                {{else}}
                <span>{{range $index, $reason := .Reasons}}{{if $index}}; {{end}}{{$reason}}{{end}} · jobs {{.ActiveJobs}}/{{.JobSlots}} · has {{printf "%.1f" (gb .AllowedMemoryBytes)}} GB RAM / {{printf "%.1f" (gb .AllowedStorageBytes)}} GB allowed disk / {{printf "%.1f" (gb .FreeStorageBytes)}} GB free{{if gt .AllowedVRAMBytes 0}} / {{printf "%.1f" (gb .AllowedVRAMBytes)}} GB VRAM{{end}}</span>
                <span></span>
                {{end}}
              </div>
              {{end}}
            </div>
            {{else}}
            <p class="sub">No online workers are reporting resources for this model.</p>
            {{end}}
            {{if .LastError}}
            <p class="sub">Last error: {{.LastError}}</p>
            {{end}}
            <div class="model-actions">
              <button class="button model-detail" type="button" data-model-id="{{.Model.ID}}"><svg class="icon"><use href="#icon-terminal"></use></svg><span>Details</span></button>
              <button class="button primary model-install" type="button" data-model-id="{{.Model.ID}}" {{if not (modelCanInstall .)}}disabled{{end}}><svg class="icon"><use href="#icon-download"></use></svg><span>Install</span></button>
            </div>
          </article>
          {{end}}
      </div>
      <div class="model-detail-panel" id="model-detail-panel" hidden>
        <div class="model-detail-dialog" role="dialog" aria-modal="true" aria-labelledby="model-detail-title">
          <div class="model-detail-head">
            <div>
              <h3 id="model-detail-title">Model details</h3>
              <p class="sub" id="model-detail-subtitle"></p>
            </div>
            <button class="button" type="button" id="model-detail-close"><svg class="icon"><use href="#icon-x"></use></svg><span>Close</span></button>
          </div>
          <div class="model-detail-grid" id="model-detail-grid"></div>
          <div class="model-detail-placement" id="model-detail-placement"></div>
          <div class="model-detail-actions" id="model-detail-actions"></div>
          <div class="model-detail-list" id="model-detail-list"></div>
        </div>
      </div>
    </section>
    </div>
    <div class="tab-panel" id="tab-model-activity" hidden>
    <section>
      <div class="section-head">
        <h2>Model Activity</h2>
        <code>{{modelJobCount .Jobs}} model jobs</code>
      </div>
      {{if gt (modelJobCount .Jobs) 0}}
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Job</th>
              <th>Status</th>
              <th>Workload</th>
              <th>Requirements</th>
              <th>Worker</th>
              <th>Timeline</th>
              <th>Result</th>
            </tr>
          </thead>
          <tbody>
          {{range .Jobs}}{{if isModelJob .}}
            <tr>
              <td><code>{{shortID .ID}}</code><br><span class="sub">{{.Type}}</span></td>
              <td><span class="{{jobPillClass .Status}}">{{.Status}}</span></td>
              <td><code>{{clip (jobWorkload .) 90}}</code></td>
              <td><code>{{jobRequirements .}}</code></td>
              <td><code>{{jobWorkerLabel $.NodesByID .}}</code></td>
              <td><div class="timeline">{{jobTimeline .}}<br>attempt {{.Attempts}} / {{.MaxAttempts}}{{if .LastFailure}}<br>{{.LastFailure}}{{end}}</div></td>
              <td class="mono-output"><code>{{clip (jobDetail .) 160}}</code>{{if distributedRPCTrace .}}<br><span class="sub">{{distributedRPCTrace .}}</span>{{end}}{{if distributedRPCEndpoints .}}<br><span class="sub">{{clip (distributedRPCEndpoints .) 120}}</span>{{end}}</td>
            </tr>
          {{end}}{{end}}
          </tbody>
        </table>
      </div>
      {{else}}
      <div class="empty">No model activity yet.</div>
      {{end}}
    </section>
    </div>
    <div class="tab-panel" id="tab-benchmarks" hidden>
    <section>
      <div class="section-head">
        <h2>Benchmark History</h2>
        <code>{{len .ClusterBenchmarks}} recent runs</code>
      </div>
      {{if .ClusterBenchmarks}}
      <div class="growth-list">
        {{range .ClusterBenchmarks}}
        <div class="growth-row">
          <div><code>{{.ID}}</code><br><span class="growth-meta">{{.Workers}} workers</span></div>
          <div class="growth-track"><div class="growth-fill" style="width: {{barPercent .TotalGFLOPS $.MaxClusterGFLOPS}}%;"></div></div>
          <strong>{{printf "%.2f" .TotalGFLOPS}} GFLOPS</strong>
        </div>
        {{end}}
      </div>
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Run</th>
              <th>Status</th>
              <th>Progress</th>
              <th>Workload</th>
              <th>Updated</th>
              <th>Cluster result</th>
              <th>Worker breakdown</th>
            </tr>
          </thead>
          <tbody>
          {{range .ClusterBenchmarks}}
            <tr>
              <td><code>{{.ID}}</code></td>
              <td><span class="{{benchmarkPillClass .Status}}">{{.Status}}</span></td>
              <td>{{.Completed}} / {{.Workers}} done{{if .Failed}}, {{.Failed}} failed{{end}}{{if .Active}}, {{.Active}} active{{end}}</td>
              <td>{{.Size}} x {{.Size}}, {{.Iterations}} iterations</td>
              <td>{{.UpdatedAt.Format "15:04:05 MST"}}</td>
              <td>
                <div class="benchmark-summary">
                  <div><span>Total GFLOPS</span><strong>{{printf "%.2f" .TotalGFLOPS}}</strong></div>
                  <div><span>Workers</span><strong>{{.Workers}}</strong></div>
                  <div><span>Completed</span><strong>{{.Completed}}</strong></div>
                  <div><span>Failed</span><strong>{{.Failed}}</strong></div>
                </div>
              </td>
              <td>
                <div class="worker-breakdown">
                  {{range .Jobs}}
                  <div class="worker-result">
                    <code>{{nodeLabel $.NodesByID .AssignedTo}}</code>
                    <span>{{.Status}}</span>
                    <strong>{{jobMetric . "gflops"}}</strong>
                    <span>{{jobMetric . "duration_ms"}} ms</span>
                    <span>{{jobMetric . "worker_runtime"}}</span>
                  </div>
                  {{end}}
                </div>
              </td>
            </tr>
          {{end}}
          </tbody>
        </table>
      </div>
      {{else}}
      <div class="empty">No cluster benchmark runs yet.</div>
      {{end}}
    </section>
    </div>
    <div class="tab-panel" id="tab-scheduler" hidden>
    <section id="scheduler">
      <div class="section-head">
        <h2>Scheduler</h2>
        <code>{{len (schedulerJobs .Jobs)}} active decisions</code>
      </div>
      {{if schedulerJobs .Jobs}}
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Job</th>
              <th>Status</th>
              <th>Worker</th>
              <th>Requirements</th>
              <th>Attempts</th>
              <th>Decision</th>
            </tr>
          </thead>
          <tbody>
          {{range schedulerJobs .Jobs}}
            <tr>
              <td><code>{{shortID .ID}}</code><br><span class="sub">{{.Type}}</span></td>
              <td><span class="{{jobPillClass .Status}}">{{.Status}}</span></td>
              <td><code>{{jobWorkerLabel $.NodesByID .}}</code>{{if .AssignedTo}}<br><span class="sub">{{shortID .AssignedTo}}</span>{{end}}</td>
              <td><code>{{jobRequirements .}}</code></td>
              <td>{{.Attempts}} / {{.MaxAttempts}}</td>
              <td class="mono-output"><code>{{if .LastFailure}}{{.LastFailure}}{{else}}{{jobDetail .}}{{end}}</code></td>
            </tr>
          {{end}}
          </tbody>
        </table>
      </div>
      {{else}}
      <div class="empty-action">
        <div>
          <h3>No active scheduler decisions</h3>
          <p>Queued, scheduled, and running jobs will appear here with placement requirements and last failure reasons.</p>
        </div>
      </div>
      {{end}}
    </section>
    </div>
    <div class="tab-panel" id="tab-jobs" hidden>
    <section id="jobs">
      <div class="section-head">
        <h2>Compute Jobs</h2>
        <code>{{len .Jobs}} recent</code>
      </div>
      <form class="job-runner" id="compute-job-form">
        <input type="hidden" name="type" value="compute.matrix_multiply">
        <div class="field">
          <label for="job-size">Matrix size</label>
          <input id="job-size" name="size" type="number" min="16" max="2048" step="16" value="512">
        </div>
        <div class="field">
          <label for="job-iterations">Iterations</label>
          <input id="job-iterations" name="iterations" type="number" min="1" max="100" step="1" value="6">
        </div>
        <div class="field">
          <label for="job-requested-by">Requested by</label>
          <input id="job-requested-by" name="requested_by" value="dashboard">
        </div>
        <div class="field">
          <label for="job-cpu-cores">CPU cores</label>
          <input id="job-cpu-cores" name="cpu_cores" type="number" min="1" max="256" step="1" value="1">
        </div>
        <div class="field">
          <label for="job-memory-gb">RAM GB</label>
          <input id="job-memory-gb" name="memory_gb" type="number" min="0" max="2048" step="0.1" value="0">
        </div>
        <div class="field">
          <label for="job-gpu-required">GPU</label>
          <select id="job-gpu-required" name="gpu_required">
            <option value="false" selected>Not required</option>
            <option value="true">Required</option>
          </select>
        </div>
        <div class="field">
          <label>&nbsp;</label>
          <button class="button primary" type="submit" {{if not .OnlineNodes}}disabled{{end}}><svg class="icon"><use href="#icon-play"></use></svg>Run compute job</button>
        </div>
      </form>
      <div class="runner-status" id="compute-job-status">{{if .OnlineNodes}}Submit one compute job to the scheduler. Capacity, requirements, and job slots decide where it runs.{{else}}Connect at least one worker before submitting a compute job.{{end}}</div>
      {{if .Jobs}}
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Job</th>
              <th>Status</th>
              <th>Workload</th>
              <th>Requirements</th>
              <th>Worker</th>
              <th>Timeline</th>
              <th>Result</th>
              <th>Detail</th>
            </tr>
          </thead>
          <tbody>
          {{range .Jobs}}
            <tr>
              <td>
                <div class="job-main">
                  <code>{{shortID .ID}}</code>
                  <strong>{{.Type}}</strong>
                  <span class="sub">attempt {{.Attempts}} / {{.MaxAttempts}}</span>
                </div>
              </td>
              <td><span class="{{jobPillClass .Status}}">{{.Status}}</span></td>
              <td><code>{{clip (jobWorkload .) 64}}</code></td>
              <td><code>{{jobRequirements .}}</code></td>
              <td><code>{{jobWorkerLabel $.NodesByID .}}</code>{{if .AssignedTo}}<br><span class="sub">{{shortID .AssignedTo}}</span>{{end}}</td>
              <td><div class="timeline">{{jobTimeline .}}<br>duration {{jobDuration .}}</div></td>
              <td>
                <div class="result-grid">
                  <div><span>Runtime ms</span><strong>{{jobMetric . "duration_ms"}}</strong></div>
                  <div><span>GFLOPS</span><strong>{{jobMetric . "gflops"}}</strong></div>
                  <div><span>Runtime</span><strong>{{jobMetric . "worker_runtime"}}</strong></div>
                  <div><span>RPC endpoints</span><strong>{{jobMetric . "rpc_endpoint_count"}}</strong></div>
                  <div><span>Progress</span><strong>{{jobProgress .}}</strong></div>
                </div>
              </td>
              <td class="mono-output">
                <code>{{clip (jobDetail .) 180}}</code>
                {{if jobCanCancel .}}
                <form class="job-cancel-form" data-job-id="{{.ID}}">
                  <button class="button danger" type="submit"><svg class="icon"><use href="#icon-x"></use></svg>Cancel</button>
                </form>
                {{end}}
              </td>
            </tr>
            {{if .LastFailure}}
            <tr>
              <td></td>
              <td colspan="7"><span class="sub">Last failure: {{.LastFailure}}</span></td>
            </tr>
            {{end}}
          {{end}}
          </tbody>
        </table>
      </div>
      {{else}}
      <div class="empty">No jobs have been submitted yet.</div>
      {{end}}
    </section>
    </div>
      </div>
    </div>
  </main>
  <script>
    function setButtonText(button, text) {
      var label = button.querySelector("span");
      if (label) {
        label.innerText = text;
      } else {
        button.innerText = text;
      }
    }
    function activateTab(name, updateHash) {
      var target = String(name || "overview").split("?")[0] || "overview";
      var panel = document.getElementById("tab-" + target);
      if (!panel) target = "overview";
      document.querySelectorAll(".tab-panel").forEach(function(section) {
        section.hidden = section.id !== "tab-" + target;
      });
      document.querySelectorAll(".tab-button[data-tab-target]").forEach(function(button) {
        button.classList.toggle("active", button.dataset.tabTarget === target);
        if (button.dataset.tabTarget === target && typeof button.scrollIntoView === "function") {
          button.scrollIntoView({ block: "nearest", inline: "nearest" });
        }
      });
      if (updateHash) {
        var baseURL = window.location.pathname + window.location.search;
        history.replaceState(null, "", target === "overview" ? baseURL : baseURL + "#" + target);
        try {
          window.localStorage.setItem("cmesh.dashboard.activeTab", target);
        } catch (error) {}
      }
    }
    function currentActiveTab() {
      var active = document.querySelector(".tab-button.active");
      return active ? active.dataset.tabTarget : "";
    }
    document.querySelectorAll("[data-tab-target]").forEach(function(button) {
      button.addEventListener("click", function() {
        activateTab(button.dataset.tabTarget, true);
      });
    });
    document.querySelectorAll("[data-tab-shortcut]").forEach(function(button) {
      button.addEventListener("click", function() {
        activateTab(button.dataset.tabShortcut, true);
      });
    });
    var rpcSmokeButton = document.getElementById("rpc-smoke-run");
    var rpcHealthRefreshButton = document.getElementById("rpc-health-refresh");
    var rpcSmokeResult = document.getElementById("rpc-smoke-result");
    var rpcPromptSmokeForm = document.getElementById("rpc-prompt-smoke-form");
    var rpcPromptSmokeModel = document.getElementById("rpc-smoke-model");
    var rpcPromptSmokeNode = document.getElementById("rpc-smoke-node");
    var rpcExecutionPlan = document.getElementById("rpc-execution-plan");
    var rpcPromptSmokeStatus = document.getElementById("rpc-prompt-smoke-status");
    var rpcPromptSmokeSubmitting = false;
    function renderRPCSmokeReport(report) {
      if (!rpcSmokeResult) return;
      rpcSmokeResult.innerHTML = "";
      var summary = document.createElement("strong");
      summary.textContent = "Checked " + (report.checked || 0) + " endpoint(s): " + (report.ready || 0) + " ready, " + (report.failed || 0) + " failed.";
      rpcSmokeResult.appendChild(summary);
      var list = document.createElement("div");
      list.className = "result-grid";
      (report.results || []).forEach(function(result) {
        var item = document.createElement("div");
        var label = document.createElement("span");
        label.textContent = result.endpoint || "endpoint";
        var value = document.createElement("strong");
        value.textContent = result.ready ? "ready · " + (result.latency_ms || 0) + " ms" : "failed";
        item.appendChild(label);
        item.appendChild(value);
        if (result.error) {
          var detail = document.createElement("span");
          detail.className = "sub";
          detail.textContent = result.error;
          item.appendChild(detail);
        }
        list.appendChild(item);
      });
      rpcSmokeResult.appendChild(list);
      if (report.duration_ms !== undefined) {
        var duration = document.createElement("span");
        duration.className = "sub";
        duration.textContent = "Total smoke duration " + report.duration_ms + " ms.";
        rpcSmokeResult.appendChild(duration);
      }
    }
    function fetchRPCPoolHealthRefresh() {
      return fetch("/v1/runtime/rpc-pool/refresh?timeout_ms=1000", {
        method: "POST"
      }).then(function(response) {
        return response.json().then(function(payload) {
          if (!response.ok) {
            var error = new Error(payload.error || response.statusText);
            error.payload = payload;
            throw error;
          }
          return payload;
        });
      });
    }
    if (rpcHealthRefreshButton) {
      rpcHealthRefreshButton.addEventListener("click", function() {
        rpcHealthRefreshButton.disabled = true;
        setButtonText(rpcHealthRefreshButton, "Refreshing...");
        if (rpcSmokeResult) rpcSmokeResult.textContent = "Refreshing RPC endpoint health...";
        fetchRPCPoolHealthRefresh().then(function(payload) {
          renderRPCSmokeReport(payload.report || payload);
          if (rpcSmokeResult) {
            var note = document.createElement("span");
            note.className = "sub";
            note.textContent = "Health refreshed. Ranked endpoints now available through /v1/runtime/rpc-pool.";
            rpcSmokeResult.appendChild(note);
          }
        }).catch(function(error) {
          var report = error.payload && (error.payload.report || error.payload);
          if (report && report.results) {
            renderRPCSmokeReport(report);
          } else if (rpcSmokeResult) {
            rpcSmokeResult.textContent = "RPC health refresh failed: " + error.message;
          }
        }).finally(function() {
          rpcHealthRefreshButton.disabled = false;
          setButtonText(rpcHealthRefreshButton, "Refresh RPC health");
        });
      });
    }
    if (rpcSmokeButton) {
      rpcSmokeButton.addEventListener("click", function() {
        rpcSmokeButton.disabled = true;
        setButtonText(rpcSmokeButton, "Testing...");
        if (rpcSmokeResult) rpcSmokeResult.textContent = "Checking active RPC endpoints...";
        fetch("/v1/runtime/rpc-pool/smoke?timeout_ms=1000", {
          method: "POST"
        }).then(function(response) {
          return response.json().then(function(payload) {
            if (!response.ok) {
              var error = new Error(payload.error || response.statusText);
              error.payload = payload;
              throw error;
            }
            return payload;
          });
        }).then(function(payload) {
          renderRPCSmokeReport(payload);
        }).catch(function(error) {
          if (error.payload && error.payload.results) {
            renderRPCSmokeReport(error.payload);
          } else if (rpcSmokeResult) {
            rpcSmokeResult.textContent = "RPC smoke test failed: " + error.message;
          }
        }).finally(function() {
          rpcSmokeButton.disabled = false;
          setButtonText(rpcSmokeButton, "Run RPC smoke test");
        });
      });
    }
    function syncRPCPromptSmokeNodes() {
      if (!rpcPromptSmokeModel || !rpcPromptSmokeNode) return;
      var modelID = rpcPromptSmokeModel.value;
      var firstVisible = "";
      Array.prototype.forEach.call(rpcPromptSmokeNode.options, function(option) {
        var matches = !modelID || option.getAttribute("data-model-id") === modelID;
        option.hidden = !matches;
        option.disabled = !matches;
        if (matches && !firstVisible) firstVisible = option.value;
      });
      if (firstVisible) rpcPromptSmokeNode.value = firstVisible;
    }
    function renderRPCExecutionPlan(plan) {
      if (!rpcExecutionPlan) return;
      rpcExecutionPlan.innerHTML = "";
      var title = document.createElement("strong");
      title.textContent = plan.executable_now ? "Distributed RPC plan is executable" : "Distributed RPC plan is blocked";
      rpcExecutionPlan.appendChild(title);
      var details = document.createElement("div");
      details.className = "result-grid";
      var coordinator = document.createElement("div");
      coordinator.innerHTML = "<span>Coordinator</span><strong></strong>";
      coordinator.querySelector("strong").textContent = plan.coordinator_node_name || plan.coordinator_node_id || "not selected";
      details.appendChild(coordinator);
      var mode = document.createElement("div");
      mode.innerHTML = "<span>Mode</span><strong></strong>";
      mode.querySelector("strong").textContent = plan.mode || "llama.cpp-rpc";
      details.appendChild(mode);
      var endpoints = document.createElement("div");
      endpoints.innerHTML = "<span>RPC endpoints</span><strong></strong>";
      endpoints.querySelector("strong").textContent = String((plan.rpc_endpoints || []).length);
      details.appendChild(endpoints);
      rpcExecutionPlan.appendChild(details);
      if ((plan.backends || []).length > 0) {
        var backendList = document.createElement("div");
        backendList.className = "sub";
        backendList.textContent = "Backends: " + plan.backends.map(function(backend) {
          return (backend.node_name || backend.node_id || "worker") + " -> " + backend.endpoint;
        }).join("; ");
        rpcExecutionPlan.appendChild(backendList);
      }
      if ((plan.blockers || []).length > 0) {
        var blockers = document.createElement("div");
        blockers.className = "sub";
        blockers.textContent = "Blockers: " + plan.blockers.join("; ");
        rpcExecutionPlan.appendChild(blockers);
      }
      if ((plan.warnings || []).length > 0) {
        var warnings = document.createElement("div");
        warnings.className = "sub";
        warnings.textContent = "Warnings: " + plan.warnings.join("; ");
        rpcExecutionPlan.appendChild(warnings);
      }
    }
    function refreshRPCExecutionPlan() {
      if (!rpcExecutionPlan || !rpcPromptSmokeModel || !rpcPromptSmokeNode) return;
      var modelID = String(rpcPromptSmokeModel.value || "").trim();
      var nodeID = String(rpcPromptSmokeNode.value || "").trim();
      if (!modelID) {
        rpcExecutionPlan.textContent = "Install a model to inspect the distributed RPC execution plan.";
        return;
      }
      var params = new URLSearchParams();
      if (nodeID) params.set("node_id", nodeID);
      params.set("check", "1");
      rpcExecutionPlan.textContent = "Loading distributed RPC execution plan...";
      fetch("/v1/models/" + encodeURIComponent(modelID) + "/distributed-rpc-plan?" + params.toString()).then(function(response) {
        if (!response.ok) {
          return response.text().then(function(text) { throw new Error(text || response.statusText); });
        }
        return response.json();
      }).then(renderRPCExecutionPlan).catch(function(error) {
        rpcExecutionPlan.textContent = "Could not load distributed RPC plan: " + error.message;
      });
    }
    function setRPCPromptSmokeSubmitting(isSubmitting) {
      rpcPromptSmokeSubmitting = isSubmitting;
      if (!rpcPromptSmokeForm) return;
      var button = rpcPromptSmokeForm.querySelector('button[type="submit"]');
      if (button) {
        button.disabled = isSubmitting || !rpcPromptSmokeModel || !rpcPromptSmokeNode || !rpcPromptSmokeModel.value || !rpcPromptSmokeNode.value;
        setButtonText(button, isSubmitting ? "Running..." : "Run distributed prompt");
      }
      if (rpcPromptSmokeModel) rpcPromptSmokeModel.disabled = isSubmitting;
      if (rpcPromptSmokeNode) rpcPromptSmokeNode.disabled = isSubmitting;
    }
    function initialDashboardTab() {
      var fromHash = (window.location.hash || "").replace("#", "").split("?")[0];
      if (fromHash) return fromHash;
      try {
        return window.localStorage.getItem("cmesh.dashboard.activeTab") || "";
      } catch (error) {
        return "";
      }
    }
    window.addEventListener("hashchange", function() {
      var hashTarget = (window.location.hash || "").replace("#", "").split("?")[0];
      activateTab(hashTarget, false);
      applyModelCatalogFiltersFromHash();
    });
    activateTab(initialDashboardTab(), false);
    function navBadge(name) {
      return document.querySelector('[data-nav-badge="' + name + '"]');
    }
    function setNavBadge(name, value, tone, visible) {
      var badge = navBadge(name);
      if (!badge) return;
      badge.textContent = String(value);
      badge.hidden = visible === false;
      badge.className = "nav-badge" + (tone ? " " + tone : "");
    }
    function refreshSidebarStatus() {
      fetch("/v1/dashboard/status").then(function(response) {
        if (!response.ok) {
          return response.text().then(function(text) { throw new Error(text || response.statusText); });
        }
        return response.json();
      }).then(function(status) {
        var readiness = status.readiness_status || "blocked";
        var workersOnline = Number(status.workers_online || 0);
        var workersTotal = Number(status.workers_total || 0);
        var readyModels = Number(status.ready_models || 0);
        var activeJobs = Number(status.active_jobs || 0);
        var recentFailures = Number(status.recent_failures || 0);
        var jobsTotal = Number(status.jobs_total || 0);
        var sidebarSummary = document.getElementById("sidebar-summary-text");
        if (sidebarSummary) {
          sidebarSummary.textContent = readiness + " · " + workersOnline + " worker(s) online";
        }
        setNavBadge("readiness", readiness, readiness, true);
        setNavBadge("workers", workersOnline + "/" + workersTotal, "", true);
        setNavBadge("ready-models", readyModels, readyModels > 0 ? "ready" : "blocked", true);
        setNavBadge("recent-failures", recentFailures, "failed", recentFailures > 0);
        setNavBadge("active-jobs", activeJobs, "warn", activeJobs > 0);
        setNavBadge("jobs-total", jobsTotal, "", true);
        document.body.dataset.activeJobs = activeJobs > 0 ? "true" : "false";
      }).catch(function() {
        setNavBadge("readiness", "offline", "failed", true);
      });
    }
    refreshSidebarStatus();
    window.setInterval(refreshSidebarStatus, 5000);
    var capacitySnapshotLabel = document.getElementById("capacity-snapshot-label");
    var capacitySaveSnapshot = document.getElementById("capacity-save-snapshot");
    var capacityRefreshSnapshots = document.getElementById("capacity-refresh-snapshots");
    var capacitySnapshotStatus = document.getElementById("capacity-snapshot-status");
    var capacitySnapshotList = document.getElementById("capacity-snapshot-list");
    var capacitySnapshotCount = document.getElementById("capacity-snapshot-count");
    var capacityDeltaGrid = document.getElementById("capacity-delta-grid");
    function signedNumber(value) {
      var number = Number(value || 0);
      return (number > 0 ? "+" : "") + String(number);
    }
    function signedBytes(value) {
      var number = Number(value || 0);
      var prefix = number > 0 ? "+" : "";
      return prefix + formatBytesGB(Math.abs(number));
    }
    function setCapacitySnapshotStatus(text) {
      if (capacitySnapshotStatus) capacitySnapshotStatus.textContent = text;
    }
    function renderCapacityDelta(delta) {
      if (!capacityDeltaGrid) return;
      capacityDeltaGrid.innerHTML = "";
      if (!delta) {
        capacityDeltaGrid.hidden = true;
        return;
      }
      [
        ["Workers", signedNumber(delta.workers_online_delta)],
        ["CPU cores", signedNumber(delta.allowed_cpu_cores_delta)],
        ["RAM", signedBytes(delta.allowed_memory_bytes_delta)],
        ["Disk", signedBytes(delta.allowed_storage_bytes_delta)],
        ["Free disk", signedBytes(delta.free_storage_bytes_delta)],
        ["VRAM", signedBytes(delta.allowed_vram_bytes_delta)],
        ["Runnable models", signedNumber(delta.single_worker_runnable_delta)],
        ["Sharded estimates", signedNumber(delta.sharded_estimate_delta)]
      ].forEach(function(item) {
        var cell = document.createElement("div");
        cell.className = "first-test-stat";
        cell.innerHTML = "<span></span><strong></strong>";
        cell.querySelector("span").textContent = item[0];
        cell.querySelector("strong").textContent = item[1];
        capacityDeltaGrid.appendChild(cell);
      });
      capacityDeltaGrid.hidden = false;
    }
    function compareCapacitySnapshot(snapshotID) {
      if (!snapshotID) return;
      setCapacitySnapshotStatus("Comparing current capacity...");
      fetch("/v1/capacity?baseline=" + encodeURIComponent(snapshotID)).then(function(response) {
        if (!response.ok) {
          return response.text().then(function(text) { throw new Error(text || response.statusText); });
        }
        return response.json();
      }).then(function(payload) {
        renderCapacityDelta(payload.delta);
        var delta = payload.delta || {};
        var newModels = (delta.new_single_worker_runnable_models || []).map(function(model) { return model.name || model.id; }).slice(0, 3);
        var suffix = newModels.length ? " New runnable: " + newModels.join(", ") + "." : "";
        setCapacitySnapshotStatus("Compared to " + snapshotID + "." + suffix);
      }).catch(function(error) {
        renderCapacityDelta(null);
        setCapacitySnapshotStatus("Compare failed: " + error.message);
      });
    }
    function renderCapacitySnapshots(snapshots) {
      snapshots = snapshots || [];
      if (capacitySnapshotCount) capacitySnapshotCount.textContent = snapshots.length + " snapshots";
      if (!capacitySnapshotList) return;
      capacitySnapshotList.innerHTML = "";
      if (!snapshots.length) {
        var empty = document.createElement("div");
        empty.className = "hint";
        empty.textContent = "No capacity baselines saved yet.";
        capacitySnapshotList.appendChild(empty);
        return;
      }
      snapshots.forEach(function(snapshot) {
        var row = document.createElement("div");
        row.className = "capacity-snapshot-row";
        row.innerHTML = "<div><strong></strong><br><span class=\"sub\"></span></div><code></code><button class=\"button\" type=\"button\">Compare</button>";
        row.querySelector("strong").textContent = snapshot.label || snapshot.id;
        row.querySelector("span").textContent = snapshot.created_at || "";
        row.querySelector("code").textContent = (snapshot.capacity ? snapshot.capacity.workers_online || 0 : 0) + " workers";
        var button = row.querySelector("button");
        button.addEventListener("click", function() {
          compareCapacitySnapshot(snapshot.id);
        });
        capacitySnapshotList.appendChild(row);
      });
    }
    function loadCapacitySnapshots() {
      if (!capacitySnapshotList) return;
      fetch("/v1/capacity/snapshots").then(function(response) {
        if (!response.ok) {
          return response.text().then(function(text) { throw new Error(text || response.statusText); });
        }
        return response.json();
      }).then(function(payload) {
        renderCapacitySnapshots(payload.snapshots || []);
      }).catch(function(error) {
        setCapacitySnapshotStatus("Snapshot refresh failed: " + error.message);
      });
    }
    if (capacitySaveSnapshot) {
      capacitySaveSnapshot.addEventListener("click", function() {
        var label = capacitySnapshotLabel ? String(capacitySnapshotLabel.value || "").trim() : "";
        setCapacitySnapshotStatus("Saving capacity baseline...");
        capacitySaveSnapshot.disabled = true;
        fetch("/v1/capacity/snapshots", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ label: label })
        }).then(function(response) {
          if (!response.ok) {
            return response.text().then(function(text) { throw new Error(text || response.statusText); });
          }
          return response.json();
        }).then(function(payload) {
          if (capacitySnapshotLabel) capacitySnapshotLabel.value = "";
          setCapacitySnapshotStatus("Saved baseline " + (payload.snapshot ? payload.snapshot.id : "") + ".");
          loadCapacitySnapshots();
        }).catch(function(error) {
          setCapacitySnapshotStatus("Snapshot save failed: " + error.message);
        }).finally(function() {
          capacitySaveSnapshot.disabled = false;
        });
      });
    }
    if (capacityRefreshSnapshots) {
      capacityRefreshSnapshots.addEventListener("click", loadCapacitySnapshots);
    }
    loadCapacitySnapshots();
    var form = document.getElementById("compute-job-form");
    var status = document.getElementById("compute-job-status");
    if (form) {
      form.addEventListener("submit", function(event) {
        event.preventDefault();
        var size = parseInt(form.elements.size.value, 10);
        var iterations = parseInt(form.elements.iterations.value, 10);
        var requestedBy = String(form.elements.requested_by.value || "dashboard").trim();
        var cpuCores = parseInt(form.elements.cpu_cores.value, 10);
        var memoryGB = parseFloat(form.elements.memory_gb.value);
        var gpuRequired = String(form.elements.gpu_required.value) === "true";
        if (!Number.isFinite(size) || size < 16 || !Number.isFinite(iterations) || iterations < 1) {
          status.innerText = "Use a valid matrix size and iteration count.";
          return;
        }
        if (!Number.isFinite(cpuCores) || cpuCores < 1 || !Number.isFinite(memoryGB) || memoryGB < 0) {
          status.innerText = "Use valid resource requirements.";
          return;
        }
        status.innerText = "Submitting compute job...";
        fetch("/v1/jobs", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            type: "compute.matrix_multiply",
            input: JSON.stringify({ size: size, iterations: iterations }),
            requested_by: requestedBy,
            requirements: {
              cpu_cores: cpuCores,
              memory_bytes: Math.round(memoryGB * 1024 * 1024 * 1024),
              gpu_required: gpuRequired
            }
          })
        }).then(function(response) {
          if (!response.ok) {
            return response.text().then(function(text) { throw new Error(text || response.statusText); });
          }
          return response.json();
        }).then(function(job) {
          status.innerText = "Submitted " + job.id + " to " + (job.assigned_to || "the queue") + ". Refreshing results...";
          setTimeout(function() { window.location.reload(); }, 1200);
        }).catch(function(error) {
          status.innerText = "Job submit failed: " + error.message;
        });
      });
    }
    document.querySelectorAll(".job-cancel-form").forEach(function(cancelForm) {
      cancelForm.addEventListener("submit", function(event) {
        event.preventDefault();
        var jobID = cancelForm.dataset.jobId;
        if (!jobID) return;
        var button = cancelForm.querySelector("button");
        if (button) {
          button.disabled = true;
          setButtonText(button, "Canceling...");
        }
        fetch("/v1/jobs/" + encodeURIComponent(jobID) + "/cancel", {
          method: "POST"
        }).then(function(response) {
          if (!response.ok) {
            return response.text().then(function(text) { throw new Error(text || response.statusText); });
          }
          return response.json();
        }).then(function() {
          window.location.reload();
        }).catch(function(error) {
          if (button) {
            button.disabled = false;
            setButtonText(button, "Cancel");
          }
          alert("Cancel failed: " + error.message);
        });
      });
    });
    function startClusterBenchmark(sourceForm, statusElement, label) {
      var size = parseInt(sourceForm.elements.size.value, 10);
      var iterations = parseInt(sourceForm.elements.iterations.value, 10);
      if (!Number.isFinite(size) || size < 16 || !Number.isFinite(iterations) || iterations < 1) {
        statusElement.innerText = "Use a valid matrix size and iteration count.";
        return;
      }
      statusElement.innerText = "Starting cluster benchmark...";
      fetch("/v1/cluster-benchmarks", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          size: size,
          iterations: iterations,
          requested_by: label
        })
      }).then(function(response) {
        if (!response.ok) {
          return response.text().then(function(text) { throw new Error(text || response.statusText); });
        }
        return response.json();
      }).then(function(run) {
        statusElement.innerText = "Started " + run.id + " on " + run.workers + " workers. Refreshing results...";
        setTimeout(function() { window.location.reload(); }, 1200);
      }).catch(function(error) {
        statusElement.innerText = "Cluster benchmark failed: " + error.message;
      });
    }
    var firstTestForm = document.getElementById("first-test-form");
    var firstTestStatus = document.getElementById("first-test-status");
    if (firstTestForm) {
      firstTestForm.addEventListener("submit", function(event) {
        event.preventDefault();
        startClusterBenchmark(firstTestForm, firstTestStatus, "first-test");
      });
    }
    function cssIdent(value) {
      if (window.CSS && typeof window.CSS.escape === "function") return window.CSS.escape(value);
      return String(value || "").replace(/"/g, '\\"');
    }
    function modelCardsFor(modelID) {
      if (!modelID) return [];
      return Array.prototype.slice.call(document.querySelectorAll('[data-model-id="' + cssIdent(modelID) + '"]'));
    }
    var modelCatalogSearch = document.getElementById("model-catalog-search");
    var modelCatalogStatus = document.getElementById("model-catalog-status");
    var modelCatalogFamily = document.getElementById("model-catalog-family");
    var modelCatalogSort = document.getElementById("model-catalog-sort");
    var modelCatalogCapable = document.getElementById("model-catalog-capable");
    var modelCatalogCount = document.getElementById("model-catalog-count");
    var modelCatalogClear = document.getElementById("model-catalog-clear");
    var modelCatalogEmpty = document.getElementById("model-catalog-empty");
    var modelCatalogStorageKey = "cmesh.modelCatalog.filters";
    var applyingModelCatalogHash = false;
    function catalogHashParams() {
      var raw = String(window.location.hash || "");
      var marker = "#models?";
      if (raw.indexOf(marker) !== 0) return null;
      return new URLSearchParams(raw.slice(marker.length));
    }
    function setCatalogControlValues(values) {
      if (!values) return false;
      var changed = false;
      function setValue(control, value) {
        if (!control || value === null || typeof value === "undefined") return;
        if (control.value !== value) {
          control.value = value;
          changed = true;
        }
      }
      setValue(modelCatalogSearch, values.query || "");
      setValue(modelCatalogStatus, values.status || "");
      setValue(modelCatalogFamily, values.family || "");
      setValue(modelCatalogSort, values.sort || "recommended");
      if (modelCatalogCapable) {
        var capable = Boolean(values.capableOnly);
        if (modelCatalogCapable.checked !== capable) {
          modelCatalogCapable.checked = capable;
          changed = true;
        }
      }
      return changed;
    }
    function currentModelCatalogFilters() {
      return {
        query: modelCatalogSearch ? modelCatalogSearch.value : "",
        status: modelCatalogStatus ? modelCatalogStatus.value : "",
        family: modelCatalogFamily ? modelCatalogFamily.value : "",
        sort: modelCatalogSort ? modelCatalogSort.value : "recommended",
        capableOnly: Boolean(modelCatalogCapable && modelCatalogCapable.checked)
      };
    }
    function updateModelCatalogHash() {
      if (applyingModelCatalogHash || !window.history || currentActiveTab() !== "models") return;
      var filters = currentModelCatalogFilters();
      var params = new URLSearchParams();
      if (filters.query) params.set("q", filters.query);
      if (filters.status) params.set("status", filters.status);
      if (filters.family) params.set("family", filters.family);
      if (filters.sort && filters.sort !== "recommended") params.set("sort", filters.sort);
      if (filters.capableOnly) params.set("capable", "true");
      var nextHash = params.toString() ? "#models?" + params.toString() : "#models";
      if (window.location.hash !== nextHash) {
        window.history.replaceState(null, "", window.location.pathname + window.location.search + nextHash);
      }
    }
    function applyModelCatalogFiltersFromHash() {
      var params = catalogHashParams();
      if (!params) return false;
      applyingModelCatalogHash = true;
      setCatalogControlValues({
        query: params.get("q") || params.get("query") || "",
        status: params.get("status") || "",
        family: params.get("family") || "",
        sort: params.get("sort") || "recommended",
        capableOnly: params.get("capable") === "true" || params.get("capable") === "1"
      });
      applyingModelCatalogHash = false;
      saveModelCatalogFilters();
      applyModelCatalogFilters();
      return true;
    }
    function saveModelCatalogFilters() {
      try {
        if (!window.localStorage) return;
        window.localStorage.setItem(modelCatalogStorageKey, JSON.stringify(currentModelCatalogFilters()));
      } catch (error) {}
    }
    function restoreModelCatalogFilters() {
      try {
        if (!window.localStorage) return;
        var raw = window.localStorage.getItem(modelCatalogStorageKey);
        if (!raw) return;
        var saved = JSON.parse(raw);
        setCatalogControlValues(saved);
      } catch (error) {}
    }
    function clearModelCatalogFilters() {
      if (modelCatalogSearch) modelCatalogSearch.value = "";
      if (modelCatalogStatus) modelCatalogStatus.value = "";
      if (modelCatalogFamily) modelCatalogFamily.value = "";
      if (modelCatalogSort) modelCatalogSort.value = "recommended";
      if (modelCatalogCapable) modelCatalogCapable.checked = false;
      saveModelCatalogFilters();
      updateModelCatalogHash();
      applyModelCatalogFilters();
    }
    function dedupeModelFamilyOptions() {
      if (!modelCatalogFamily) return;
      var seen = {};
      Array.prototype.slice.call(modelCatalogFamily.options).forEach(function(option) {
        var value = String(option.value || "").trim();
        if (!value) return;
        var key = value.toLowerCase();
        if (seen[key]) {
          option.remove();
        } else {
          seen[key] = true;
        }
      });
    }
    function applyModelCatalogFilters() {
      var query = modelCatalogSearch ? String(modelCatalogSearch.value || "").trim().toLowerCase() : "";
      var status = modelCatalogStatus ? String(modelCatalogStatus.value || "").trim() : "";
      var family = modelCatalogFamily ? String(modelCatalogFamily.value || "").trim().toLowerCase() : "";
      var sortMode = modelCatalogSort ? String(modelCatalogSort.value || "recommended") : "recommended";
      var capableOnly = Boolean(modelCatalogCapable && modelCatalogCapable.checked);
      var total = 0;
      var visible = 0;
      var catalog = document.querySelector(".model-catalog");
      var cards = Array.prototype.slice.call(document.querySelectorAll('.model-card[data-model-surface="catalog"]'));
      cards.sort(function(left, right) {
        var leftCapableNodes = Number(left.dataset.modelCapableNodes || "0");
        var rightCapableNodes = Number(right.dataset.modelCapableNodes || "0");
        var leftMemory = Number(left.dataset.modelMemory || "0");
        var rightMemory = Number(right.dataset.modelMemory || "0");
        var leftDisk = Number(left.dataset.modelDisk || "0");
        var rightDisk = Number(right.dataset.modelDisk || "0");
        var leftName = String(left.dataset.modelName || left.dataset.modelId || "");
        var rightName = String(right.dataset.modelName || right.dataset.modelId || "");
        if (sortMode === "name") return leftName.localeCompare(rightName);
        if (sortMode === "ram-asc") return leftMemory - rightMemory || leftName.localeCompare(rightName);
        if (sortMode === "ram-desc") return rightMemory - leftMemory || leftName.localeCompare(rightName);
        if (sortMode === "disk-asc") return leftDisk - rightDisk || leftName.localeCompare(rightName);
        if (sortMode === "capable-desc") return rightCapableNodes - leftCapableNodes || leftMemory - rightMemory || leftName.localeCompare(rightName);
        return rightCapableNodes - leftCapableNodes || leftMemory - rightMemory || leftDisk - rightDisk || leftName.localeCompare(rightName);
      });
      if (catalog) {
        cards.forEach(function(card) {
          catalog.appendChild(card);
        });
      }
      cards.forEach(function(card) {
        total++;
        var text = String(card.dataset.modelSearch || "").toLowerCase();
        var cardStatus = String(card.dataset.modelStatusValue || "");
        var cardFamily = String(card.dataset.modelFamily || "").toLowerCase();
        var cardCapable = card.dataset.modelCapable === "true";
        var matches = (!query || text.indexOf(query) !== -1) &&
          (!status || cardStatus === status) &&
          (!family || cardFamily === family) &&
          (!capableOnly || cardCapable);
        card.hidden = !matches;
        if (matches) visible++;
      });
      if (modelCatalogCount) {
        modelCatalogCount.textContent = visible + " / " + total + " shown";
      }
      if (modelCatalogEmpty) {
        modelCatalogEmpty.classList.toggle("is-visible", total > 0 && visible === 0);
      }
      updateModelCatalogHash();
    }
    function setModelButtons(modelID, disabled) {
      modelCardsFor(modelID).forEach(function(card) {
        card.querySelectorAll(".model-install, .model-delete, .model-repair").forEach(function(button) {
          button.disabled = disabled;
        });
      });
    }
    function modelStatusClass(status) {
      if (status === "installed") return "pill";
      if (status === "installing" || status === "deleting" || status === "repairing") return "pill pill-job";
      return "pill pill-muted";
    }
    function updateModelStatus(summary) {
      if (!summary || !summary.model || !summary.model.id) return;
      var modelID = summary.model.id;
      document.querySelectorAll('[data-model-status="' + cssIdent(modelID) + '"]').forEach(function(statusElement) {
        statusElement.textContent = summary.status || "available";
        statusElement.className = modelStatusClass(summary.status || "available");
      });
      modelCardsFor(modelID).forEach(function(card) {
        if (card.getAttribute("data-model-surface") === "catalog") {
          card.dataset.modelStatusValue = summary.status || "available";
          card.dataset.modelCapable = summary.capable_nodes > 0 ? "true" : "false";
          card.dataset.modelCapableNodes = String(summary.capable_nodes || 0);
        }
        if (card.getAttribute("data-model-surface") === "installed") {
          card.hidden = !(summary.installed_on && summary.installed_on.length);
        }
        card.querySelectorAll(".model-install").forEach(function(install) {
          install.disabled = summary.status === "installing" || summary.status === "deleting" || summary.status === "repairing" || !(summary.capable_nodes > 0);
          setButtonText(install, install.dataset.nodeId ? "Install here" : "Install");
        });
        card.querySelectorAll(".model-delete").forEach(function(button) {
          button.disabled = !(summary.installed_on && summary.installed_on.length) || summary.status === "deleting" || Boolean(summary.active_job_id);
        });
        card.querySelectorAll(".model-repair").forEach(function(button) {
          button.disabled = button.dataset.repairable !== "true" || !(summary.installed_on && summary.installed_on.length) || summary.status === "repairing" || Boolean(summary.active_job_id);
        });
      });
      applyModelCatalogFilters();
    }
    function refreshModelSummary(modelID) {
      if (!modelID) return Promise.resolve(null);
      return fetch("/v1/models").then(function(response) {
        if (!response.ok) {
          return response.text().then(function(text) { throw new Error(text || response.statusText); });
        }
        return response.json();
      }).then(function(payload) {
        var models = payload.models || [];
        for (var i = 0; i < models.length; i++) {
          if (models[i].model && models[i].model.id === modelID) {
            updateModelStatus(models[i]);
            refreshModelPlacement(modelID);
            return models[i];
          }
        }
        return null;
      });
    }
    dedupeModelFamilyOptions();
    [modelCatalogSearch, modelCatalogStatus, modelCatalogFamily, modelCatalogSort, modelCatalogCapable].forEach(function(control) {
      if (!control) return;
      control.addEventListener("input", function() {
        saveModelCatalogFilters();
        applyModelCatalogFilters();
      });
      control.addEventListener("change", function() {
        saveModelCatalogFilters();
        applyModelCatalogFilters();
      });
    });
    if (modelCatalogClear) {
      modelCatalogClear.addEventListener("click", clearModelCatalogFilters);
    }
    if (!applyModelCatalogFiltersFromHash()) {
      restoreModelCatalogFilters();
    }
    applyModelCatalogFilters();
    var modelDetailPanel = document.getElementById("model-detail-panel");
    var modelDetailTitle = document.getElementById("model-detail-title");
    var modelDetailSubtitle = document.getElementById("model-detail-subtitle");
    var modelDetailGrid = document.getElementById("model-detail-grid");
    var modelDetailPlacement = document.getElementById("model-detail-placement");
    var modelDetailActions = document.getElementById("model-detail-actions");
    var modelDetailList = document.getElementById("model-detail-list");
    var modelDetailClose = document.getElementById("model-detail-close");
    function formatBytesGB(bytes) {
      var value = Number(bytes || 0);
      if (!value) return "0.0 GB";
      return (value / 1024 / 1024 / 1024).toFixed(1) + " GB";
    }
    function openModelDetailPanel() {
      if (!modelDetailPanel) return;
      modelDetailPanel.hidden = false;
      document.body.classList.add("modal-open");
    }
    function closeModelDetailPanel() {
      if (!modelDetailPanel) return;
      modelDetailPanel.hidden = true;
      document.body.classList.remove("modal-open");
    }
    function setModelDetailLoading(modelID) {
      if (!modelDetailPanel) return;
      openModelDetailPanel();
      if (modelDetailTitle) modelDetailTitle.textContent = "Loading model...";
      if (modelDetailSubtitle) modelDetailSubtitle.textContent = modelID;
      if (modelDetailGrid) modelDetailGrid.innerHTML = "";
      if (modelDetailPlacement) modelDetailPlacement.innerHTML = '<h4>Placement plan</h4><div class="hint">Loading placement planner.</div>';
      if (modelDetailActions) modelDetailActions.innerHTML = "";
      if (modelDetailList) modelDetailList.innerHTML = '<div class="hint">Loading model details from the manager API.</div>';
    }
    function renderModelPlacement(placement) {
      if (!modelDetailPlacement) return;
      modelDetailPlacement.innerHTML = "";
      var title = document.createElement("h4");
      title.textContent = "Placement plan";
      modelDetailPlacement.appendChild(title);
      if (!placement) {
        var missing = document.createElement("div");
        missing.className = "hint";
        missing.textContent = "Placement planner did not return a plan.";
        modelDetailPlacement.appendChild(missing);
        return;
      }
      var summary = document.createElement("div");
      summary.className = "placement-summary";
      var mode = String(placement.mode || "blocked").replace(/_/g, " ");
      var status = placement.runnable_now ? "runnable now" : (placement.feasible ? "feasible estimate" : "blocked");
      summary.innerHTML = "<span class=\"pill\"></span><span class=\"pill\"></span><code></code>";
      summary.querySelectorAll("span")[0].textContent = mode;
      summary.querySelectorAll("span")[1].textContent = status;
      summary.querySelector("code").textContent = "required RAM " + formatBytesGB(placement.required_memory_bytes) + " · disk " + formatBytesGB(placement.required_disk_bytes);
      modelDetailPlacement.appendChild(summary);

      var list = document.createElement("div");
      list.className = "placement-list";
      (placement.single_node_candidates || []).forEach(function(candidate) {
        var row = document.createElement("div");
        row.className = "placement-shard";
        row.innerHTML = "<strong></strong><span></span><code></code>";
        row.querySelector("strong").textContent = candidate.name || candidate.node_id || "worker";
        row.querySelector("span").textContent = "single worker";
        row.querySelector("code").textContent = "RAM " + formatBytesGB(candidate.allowed_memory_bytes) + " · disk " + formatBytesGB(candidate.allowed_storage_bytes);
        list.appendChild(row);
      });
      (placement.shards || []).forEach(function(shard) {
        var row = document.createElement("div");
        row.className = "placement-shard";
        row.innerHTML = "<strong></strong><span></span><code></code>";
        row.querySelector("strong").textContent = shard.node_name || shard.node_id || "worker";
        row.querySelector("span").textContent = "planned shard";
        row.querySelector("code").textContent = "RAM " + formatBytesGB(shard.memory_bytes) + " · disk " + formatBytesGB(shard.disk_bytes);
        list.appendChild(row);
      });
      (placement.blockers || []).forEach(function(blocker) {
        var item = document.createElement("div");
        item.className = "placement-blocker";
        item.textContent = blocker;
        list.appendChild(item);
      });
      (placement.warnings || []).forEach(function(warning) {
        var item = document.createElement("div");
        item.className = "placement-warning";
        item.textContent = warning;
        list.appendChild(item);
      });
      if (!list.children.length) {
        var empty = document.createElement("div");
        empty.className = "hint";
        empty.textContent = "No viable placement candidates yet.";
        list.appendChild(empty);
      }
      modelDetailPlacement.appendChild(list);
    }
    function modelInstallConflictText(conflict) {
      if (!conflict || typeof conflict !== "object") return "";
      var parts = [];
      if (conflict.error) parts.push(conflict.error);
      if (conflict.reason) parts.push(conflict.reason);
      if (conflict.placement && conflict.placement.blockers && conflict.placement.blockers.length) {
        parts.push(conflict.placement.blockers.join("; "));
      }
      if (conflict.placement && conflict.placement.mode === "sharded_estimate") {
        parts.push("multi-worker placement is only an estimate; distributed execution is not implemented yet");
      }
      return parts.join(": ");
    }
    function placementLabel(plan) {
      if (!plan) return "Blocked";
      if (plan.mode === "single_worker") return "Single-worker ready";
      if (plan.mode === "sharded_estimate") return "Sharded estimate";
      return "Blocked";
    }
    function placementClass(plan) {
      if (plan && plan.runnable_now) return "model-placement-card is-ready";
      if (plan && plan.feasible) return "model-placement-card is-estimate";
      return "model-placement-card is-blocked";
    }
    function placementHint(plan) {
      if (!plan) return "No viable placement found.";
      if (plan.runnable_now) {
        var candidates = (plan.single_node_candidates || []).length;
        return candidates === 1 ? "Can run on 1 online worker." : "Can run on " + candidates + " online workers.";
      }
      if (plan.feasible) {
        return "Aggregate resources fit across " + ((plan.shards || []).length) + " workers, but distributed model execution is not implemented yet.";
      }
      if (plan.blockers && plan.blockers.length) return plan.blockers.join(" | ");
      return "No viable placement found.";
    }
    function updateModelPlacementCard(modelID, plan) {
      if (!modelID || !plan) return;
      document.querySelectorAll('[data-model-placement="' + cssIdent(modelID) + '"]').forEach(function(card) {
        card.className = placementClass(plan);
        card.dataset.placementMode = plan.mode || "blocked";
        var title = card.querySelector("strong");
        var body = card.querySelector("span");
        var specs = card.querySelector("code");
        if (title) title.textContent = placementLabel(plan);
        if (body) body.textContent = placementHint(plan);
        if (specs) specs.textContent = "RAM " + formatBytesGB(plan.required_memory_bytes) + " · disk " + formatBytesGB(plan.required_disk_bytes);
      });
    }
    function refreshModelPlacement(modelID) {
      if (!modelID) return Promise.resolve(null);
      return fetch("/v1/models/" + encodeURIComponent(modelID) + "/placement").then(function(response) {
        if (!response.ok) return null;
        return response.json();
      }).then(function(payload) {
        if (payload && payload.placement) {
          updateModelPlacementCard(modelID, payload.placement);
          if (modelDetailPanel && !modelDetailPanel.hidden) {
            renderModelPlacement(payload.placement);
          }
          return payload.placement;
        }
        return null;
      }).catch(function() {
        return null;
      });
    }
    function renderModelDetail(summary, placement) {
      if (!summary || !summary.model || !modelDetailPanel) return;
      var model = summary.model;
      openModelDetailPanel();
      if (modelDetailTitle) modelDetailTitle.textContent = model.name || model.id;
      if (modelDetailSubtitle) {
        modelDetailSubtitle.textContent = model.id + " · " + (model.runtime || "runtime") + " · " + (summary.status || "available");
      }
      if (modelDetailGrid) {
        modelDetailGrid.innerHTML = "";
        [
          ["Parameters", (model.parameters || "-") + " / " + (model.quant || "-")],
          ["Required RAM", formatBytesGB(model.memory_bytes)],
          ["Required disk", formatBytesGB(model.disk_bytes)],
          ["Context", String(model.context || "-")],
          ["Capable workers", String(summary.capable_nodes || 0)],
          ["Installed workers", String((summary.installed_on || []).length)]
        ].forEach(function(item) {
          var cell = document.createElement("div");
          cell.innerHTML = "<span></span><strong></strong>";
          cell.querySelector("span").textContent = item[0];
          cell.querySelector("strong").textContent = item[1];
          modelDetailGrid.appendChild(cell);
        });
      }
      renderModelPlacement(placement);
      if (modelDetailActions) {
        modelDetailActions.innerHTML = "";
        var bestCapability = (summary.capabilities || []).find(function(capability) {
          return capability.capable && !capability.installed;
        });
        var installAny = document.createElement("button");
        installAny.className = "button primary model-detail-install";
        installAny.type = "button";
        installAny.dataset.modelId = model.id;
        installAny.innerHTML = '<svg class="icon"><use href="#icon-download"></use></svg><span>Install on best worker</span>';
        installAny.disabled = !bestCapability || summary.status === "installing" || summary.status === "deleting" || summary.status === "repairing";
        modelDetailActions.appendChild(installAny);
        if (bestCapability) {
          var installHere = document.createElement("button");
          installHere.className = "button model-detail-install";
          installHere.type = "button";
          installHere.dataset.modelId = model.id;
          installHere.dataset.nodeId = bestCapability.node_id || "";
          installHere.innerHTML = '<svg class="icon"><use href="#icon-download"></use></svg><span></span>';
          installHere.querySelector("span").textContent = "Install on " + (bestCapability.name || "worker");
          installHere.disabled = installAny.disabled;
          modelDetailActions.appendChild(installHere);
        }
      }
      if (modelDetailList) {
        modelDetailList.innerHTML = "";
        var capabilities = summary.capabilities || [];
        if (!capabilities.length) {
          modelDetailList.innerHTML = '<div class="hint">No online workers are reporting capability for this model.</div>';
        } else {
          capabilities.forEach(function(capability) {
            var row = document.createElement("div");
            row.className = "model-detail-row";
            var reasons = (capability.reasons || []).join("; ");
            row.innerHTML = "<strong></strong><span></span><code></code><div></div>";
            row.querySelector("strong").textContent = capability.name || capability.node_id || "worker";
            row.querySelector("span").textContent = capability.capable ? "capable" : "blocked";
            row.querySelector("code").textContent = capability.capable
              ? "jobs " + (capability.active_jobs || 0) + "/" + (capability.job_slots || 0) + " · RAM " + formatBytesGB(capability.allowed_memory_bytes) + " · disk " + formatBytesGB(capability.allowed_storage_bytes)
              : (reasons || "requirements not satisfied");
            var action = row.querySelector("div");
            if (capability.capable && !capability.installed) {
              var install = document.createElement("button");
              install.className = "button model-detail-install";
              install.type = "button";
              install.dataset.modelId = model.id;
              install.dataset.nodeId = capability.node_id || "";
              install.innerHTML = '<svg class="icon"><use href="#icon-download"></use></svg><span>Install</span>';
              install.disabled = summary.status === "installing" || summary.status === "deleting" || summary.status === "repairing";
              action.appendChild(install);
            } else if (capability.installed) {
              action.innerHTML = '<span class="pill">installed</span>';
            }
            modelDetailList.appendChild(row);
          });
        }
      }
    }
    function loadModelDetail(modelID) {
      if (!modelID) return;
      setModelDetailLoading(modelID);
      Promise.all([
        fetch("/v1/models/" + encodeURIComponent(modelID)).then(function(response) {
          if (!response.ok) {
            return response.text().then(function(text) { throw new Error(text || response.statusText); });
          }
          return response.json();
        }),
        fetch("/v1/models/" + encodeURIComponent(modelID) + "/placement").then(function(response) {
          if (!response.ok) return { placement: null };
          return response.json();
        }).catch(function() {
          return { placement: null };
        })
      ]).then(function(payloads) {
        renderModelDetail(payloads[0].model, payloads[1].placement);
      }).catch(function(error) {
        if (modelDetailTitle) modelDetailTitle.textContent = "Model detail failed";
        if (modelDetailSubtitle) modelDetailSubtitle.textContent = modelID;
        if (modelDetailPlacement) modelDetailPlacement.innerHTML = "";
        if (modelDetailList) modelDetailList.innerHTML = '<div class="hint"></div>';
        var hint = modelDetailList ? modelDetailList.querySelector(".hint") : null;
        if (hint) hint.textContent = error.message;
      });
    }
    document.querySelectorAll(".model-detail").forEach(function(button) {
      button.addEventListener("click", function() {
        loadModelDetail(button.dataset.modelId);
      });
    });
    if (modelDetailClose) {
      modelDetailClose.addEventListener("click", function() {
        closeModelDetailPanel();
      });
    }
    if (modelDetailPanel) {
      modelDetailPanel.addEventListener("click", function(event) {
        if (event.target === modelDetailPanel) closeModelDetailPanel();
      });
    }
    document.addEventListener("keydown", function(event) {
      if (event.key === "Escape" && modelDetailPanel && !modelDetailPanel.hidden) {
        closeModelDetailPanel();
      }
    });
    function modelOperationText(job) {
      if (!job) return "Waiting for job...";
      if (job.error) return job.error;
      if (job.result) {
        try {
          var parsed = JSON.parse(job.result);
          if (parsed.freed_bytes) return "Freed " + Math.round(parsed.freed_bytes / 1024 / 1024) + " MB.";
          if (parsed.bytes) return "Stored " + Math.round(parsed.bytes / 1024 / 1024) + " MB.";
          if (parsed.worker_runtime) return "Completed on " + parsed.worker_runtime + ".";
        } catch (error) {
          return job.result;
        }
      }
      if (job.last_failure) return job.last_failure;
      if (job.assigned_to) return "Assigned to " + job.assigned_to + ".";
      return "Waiting for an eligible worker.";
    }
    function renderModelOperation(modelID, job, fallbackText) {
      var status = job ? String(job.status || "queued") : "queued";
      var jobID = job ? String(job.id || "") : "";
      var text = fallbackText || modelOperationText(job);
      modelCardsFor(modelID).forEach(function(card) {
        var operation = card.querySelector(".model-operation");
        if (!operation) {
          operation = document.createElement("div");
          operation.className = "model-operation";
          card.appendChild(operation);
        }
        operation.innerHTML = "";
        var title = document.createElement("strong");
        title.textContent = "Operation " + status;
        operation.appendChild(title);
        if (jobID) {
          var code = document.createElement("code");
          code.textContent = jobID;
          operation.appendChild(code);
        }
        var body = document.createElement("span");
        body.className = "sub";
        body.textContent = text;
        operation.appendChild(body);
        if (jobID && status !== "succeeded" && status !== "failed" && status !== "canceled") {
          var cancelButton = document.createElement("button");
          cancelButton.className = "button danger model-operation-cancel";
          cancelButton.type = "button";
          cancelButton.dataset.jobId = jobID;
          cancelButton.dataset.modelId = modelID;
          cancelButton.innerHTML = '<svg class="icon"><use href="#icon-x"></use></svg><span>Cancel</span>';
          operation.appendChild(cancelButton);
        }
      });
    }
    document.addEventListener("click", function(event) {
      var button = event.target.closest ? event.target.closest(".model-operation-cancel") : null;
      if (!button) return;
      var jobID = button.dataset.jobId;
      var modelID = button.dataset.modelId;
      if (!jobID || !modelID) return;
      button.disabled = true;
      setButtonText(button, "Canceling...");
      fetch("/v1/jobs/" + encodeURIComponent(jobID) + "/cancel", {
        method: "POST"
      }).then(function(response) {
        if (!response.ok) {
          return response.text().then(function(text) { throw new Error(text || response.statusText); });
        }
        return response.json();
      }).then(function(job) {
        renderModelOperation(modelID, job, "Canceled by operator.");
        refreshModelSummary(modelID);
      }).catch(function(error) {
        button.disabled = false;
        setButtonText(button, "Cancel");
        renderModelOperation(modelID, null, "Cancel failed: " + error.message);
      });
    });
    function pollModelLifecycleJob(modelID, jobID, attempt) {
      if (!modelID || !jobID) return;
      fetch("/v1/jobs/" + encodeURIComponent(jobID)).then(function(response) {
        if (!response.ok) {
          return response.text().then(function(text) { throw new Error(text || response.statusText); });
        }
        return response.json();
      }).then(function(job) {
        renderModelOperation(modelID, job);
        if (job.status === "succeeded" || job.status === "failed" || job.status === "canceled") {
          refreshModelSummary(modelID).catch(function(error) {
            renderModelOperation(modelID, job, modelOperationText(job) + " Refresh failed: " + error.message);
          });
          if (job.status === "succeeded") {
            setTimeout(function() { window.location.reload(); }, 900);
          }
          return;
        }
        if (attempt < 240) {
          setTimeout(function() { pollModelLifecycleJob(modelID, jobID, attempt + 1); }, 1500);
        }
      }).catch(function(error) {
        renderModelOperation(modelID, null, "Could not read job: " + error.message);
      });
    }
    function submitModelInstall(modelID, nodeID, button) {
      if (!modelID) return;
      var originalText = button ? button.textContent.trim() || "Install" : "Install";
      setModelButtons(modelID, true);
      if (modelDetailActions) {
        modelDetailActions.querySelectorAll("button").forEach(function(actionButton) {
          actionButton.disabled = true;
        });
      }
      if (button) setButtonText(button, "Installing...");
      fetch("/v1/models/" + encodeURIComponent(modelID) + "/install", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(nodeID ? { node_id: nodeID } : {})
      }).then(function(response) {
        if (!response.ok) {
          return response.text().then(function(text) {
            var conflict = null;
            try {
              conflict = JSON.parse(text);
            } catch (error) {}
            if (conflict && conflict.placement) {
              updateModelPlacementCard(modelID, conflict.placement);
              if (modelDetailPanel && !modelDetailPanel.hidden) {
                renderModelPlacement(conflict.placement);
              }
              throw new Error(modelInstallConflictText(conflict) || response.statusText);
            }
            throw new Error(text || response.statusText);
          });
        }
        return response.json();
      }).then(function(job) {
        var statusElement = document.getElementById("model-status");
        if (statusElement) statusElement.innerText = "Install job " + job.id + " submitted.";
        renderModelOperation(modelID, job, "Install submitted.");
        refreshModelSummary(modelID).then(function(summary) {
          if (modelDetailPanel && !modelDetailPanel.hidden && summary && summary.model && summary.model.id === modelID) {
            renderModelDetail(summary);
          }
        });
        pollModelLifecycleJob(modelID, job.id, 0);
      }).catch(function(error) {
        refreshModelSummary(modelID).catch(function() { setModelButtons(modelID, false); });
        if (button) setButtonText(button, originalText);
        renderModelOperation(modelID, null, "Install failed: " + error.message);
      });
    }
    document.querySelectorAll(".model-install").forEach(function(button) {
      button.addEventListener("click", function() {
        submitModelInstall(button.dataset.modelId, button.dataset.nodeId || "", button);
      });
    });
    document.addEventListener("click", function(event) {
      var button = event.target.closest ? event.target.closest(".model-detail-install") : null;
      if (!button) return;
      submitModelInstall(button.dataset.modelId, button.dataset.nodeId || "", button);
    });
    document.querySelectorAll(".model-delete").forEach(function(button) {
      button.addEventListener("click", function() {
        var modelID = button.dataset.modelId;
        var nodeID = button.dataset.nodeId;
        if (!modelID || !nodeID) return;
        var originalText = button.textContent.trim() || "Delete";
        setModelButtons(modelID, true);
        setButtonText(button, "Deleting...");
        fetch("/v1/models/" + encodeURIComponent(modelID) + "/delete", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ node_id: nodeID })
        }).then(function(response) {
          if (!response.ok) {
            return response.text().then(function(text) { throw new Error(text || response.statusText); });
          }
          return response.json();
        }).then(function(job) {
          var statusElement = document.getElementById("model-status");
          if (statusElement) statusElement.innerText = "Delete job " + job.id + " submitted.";
          renderModelOperation(modelID, job, "Delete submitted.");
          refreshModelSummary(modelID);
          pollModelLifecycleJob(modelID, job.id, 0);
        }).catch(function(error) {
          refreshModelSummary(modelID).catch(function() { setModelButtons(modelID, false); });
          setButtonText(button, originalText);
          renderModelOperation(modelID, null, "Delete failed: " + error.message);
        });
      });
    });
    document.querySelectorAll(".model-repair").forEach(function(button) {
      button.addEventListener("click", function() {
        var modelID = button.dataset.modelId;
        var nodeID = button.dataset.nodeId;
        if (!modelID || !nodeID) return;
        var originalText = button.textContent.trim() || "Repair";
        setModelButtons(modelID, true);
        setButtonText(button, "Repairing...");
        fetch("/v1/models/" + encodeURIComponent(modelID) + "/repair", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ node_id: nodeID })
        }).then(function(response) {
          if (!response.ok) {
            return response.text().then(function(text) { throw new Error(text || response.statusText); });
          }
          return response.json();
        }).then(function(job) {
          var statusElement = document.getElementById("model-status");
          if (statusElement) statusElement.innerText = "Repair job " + job.id + " submitted.";
          renderModelOperation(modelID, job, "Repair submitted.");
          refreshModelSummary(modelID);
          pollModelLifecycleJob(modelID, job.id, 0);
        }).catch(function(error) {
          refreshModelSummary(modelID).catch(function() { setModelButtons(modelID, false); });
          setButtonText(button, originalText);
          renderModelOperation(modelID, null, "Repair failed: " + error.message);
        });
      });
    });
    document.querySelectorAll(".model-cleanup").forEach(function(button) {
      button.addEventListener("click", function() {
        var nodeID = button.dataset.nodeId;
        if (!nodeID) return;
        var originalText = button.textContent.trim() || "Cleanup cache";
        button.disabled = true;
        setButtonText(button, "Cleaning...");
        fetch("/v1/workers/" + encodeURIComponent(nodeID) + "/model-cleanup", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: "{}"
        }).then(function(response) {
          if (!response.ok) {
            return response.text().then(function(text) { throw new Error(text || response.statusText); });
          }
          return response.json();
        }).then(function(job) {
          setButtonText(button, "Cleanup submitted");
          button.title = "Job " + job.id + " submitted";
        }).catch(function(error) {
          button.disabled = false;
          setButtonText(button, originalText);
          button.title = error.message;
          window.alert("Cleanup failed: " + error.message);
        });
      });
    });
    var chatForm = document.getElementById("model-chat-form");
    var chatModel = document.getElementById("chat-model");
    var chatNode = document.getElementById("chat-node");
    var chatThread = document.getElementById("chat-thread");
    var modelStatus = document.getElementById("model-status");
    var newChatButton = document.getElementById("new-chat-button");
    var conversationList = document.getElementById("conversation-list");
    var memoryList = document.getElementById("memory-list");
    var memoryEditor = document.getElementById("memory-editor");
    var memoryEditorReset = document.getElementById("memory-editor-reset");
    var memoryClearModel = document.getElementById("memory-clear-model");
    var memoryPreviewText = document.getElementById("memory-preview-text");
    var contextMetrics = document.getElementById("context-metrics");
    var contextMessageList = document.getElementById("context-message-list");
    var memoryModelSelect = document.getElementById("memory-model-select");
    var debugModelSelect = document.getElementById("debug-model-select");
    var chatSystemPrompt = document.getElementById("chat-system-prompt");
    var chatTemperature = document.getElementById("chat-temperature");
    var chatMaxTokens = document.getElementById("chat-max-tokens");
    var chatPrompt = document.getElementById("chat-prompt");
    var chatUseRPC = document.getElementById("chat-use-rpc");
    var chatSubmitButton = chatForm ? chatForm.querySelector('button[type="submit"]') : null;
    var chatConversationKey = "cmesh.chat.conversation";
    var chatConversationID = "";
    var chatSubmitting = false;
    var chatSystemPromptDirty = false;
    try {
      chatConversationID = window.localStorage.getItem(chatConversationKey) || "";
    } catch (error) {}
    function setChatSubmitting(isSubmitting) {
      chatSubmitting = isSubmitting;
      if (chatSubmitButton) {
        chatSubmitButton.disabled = isSubmitting || !chatModel || !chatNode || !chatModel.value || !chatNode.value;
        setButtonText(chatSubmitButton, isSubmitting ? "Generating..." : (chatUseRPC && chatUseRPC.checked ? "Generate RPC" : "Generate"));
      }
      if (chatPrompt) chatPrompt.disabled = isSubmitting;
      if (chatModel) chatModel.disabled = isSubmitting;
      if (chatNode) chatNode.disabled = isSubmitting;
    }
    function syncChatNodes() {
      if (!chatModel || !chatNode) return;
      var modelID = chatModel.value;
      if (!modelID) {
        Array.prototype.forEach.call(chatNode.options, function(option) {
          option.hidden = false;
          option.disabled = false;
        });
        return;
      }
      var firstVisible = "";
      Array.prototype.forEach.call(chatNode.options, function(option) {
        var matches = option.getAttribute("data-model-id") === modelID;
        option.hidden = !matches;
        option.disabled = !matches;
        if (matches && !firstVisible) firstVisible = option.value;
      });
      if (firstVisible) chatNode.value = firstVisible;
    }
    function clearChatEmpty() {
      if (!chatThread) return;
      chatThread.querySelectorAll(".chat-empty").forEach(function(element) {
        element.remove();
      });
    }
    function appendChatMessage(kind, text) {
      if (!chatThread) return;
      clearChatEmpty();
      var message = document.createElement("div");
      message.className = "chat-message " + kind;
      message.textContent = text || "";
      chatThread.appendChild(message);
      chatThread.scrollTop = chatThread.scrollHeight;
    }
    function currentChatModelID() {
      return chatModel ? String(chatModel.value || "").trim() : "";
    }
    function currentModelPreset() {
      if (!chatModel || !chatModel.selectedOptions || chatModel.selectedOptions.length === 0) return null;
      var option = chatModel.selectedOptions[0];
      return {
        temperature: option.dataset.temperature || "0.6",
        maxTokens: option.dataset.maxTokens || "512",
        systemPrompt: option.dataset.systemPrompt || ""
      };
    }
    function applyChatModelPreset(forceSystemPrompt) {
      var preset = currentModelPreset();
      if (!preset) return;
      if (chatTemperature) chatTemperature.value = preset.temperature || "0.6";
      if (chatMaxTokens) chatMaxTokens.value = preset.maxTokens || "512";
      if (chatSystemPrompt && (forceSystemPrompt || !chatSystemPromptDirty || !chatSystemPrompt.value)) {
        chatSystemPrompt.value = preset.systemPrompt || "";
        chatSystemPromptDirty = false;
      }
      updateMemoryPreview();
    }
    function selectedMemoryModelID() {
      if (memoryModelSelect && memoryModelSelect.value) return String(memoryModelSelect.value || "").trim();
      return currentChatModelID();
    }
    function selectedDebugModelID() {
      if (debugModelSelect && debugModelSelect.value) return String(debugModelSelect.value || "").trim();
      return currentChatModelID();
    }
    function syncAuxModelSelects(modelID) {
      if (!modelID) return;
      if (memoryModelSelect) memoryModelSelect.value = modelID;
      if (debugModelSelect) debugModelSelect.value = modelID;
    }
    function renderMemoryList(memories) {
      if (!memoryList) return;
      memoryList.innerHTML = "";
      if (!memories || memories.length === 0) {
        var empty = document.createElement("div");
        empty.className = "sub";
        empty.textContent = selectedMemoryModelID() ? "No memory stored for this model yet." : "Select a model to load memory.";
        memoryList.appendChild(empty);
        return;
      }
      memories.forEach(function(memory) {
        var item = document.createElement("div");
        item.className = "memory-item";
        item.dataset.memoryId = memory.id || "";
        var main = document.createElement("div");
        main.className = "memory-main";
        var label = document.createElement("span");
        label.className = "conversation-meta";
        label.textContent = memory.key || "-";
        var value = document.createElement("span");
        value.className = "memory-value";
        value.textContent = memory.value || "";
        var meta = document.createElement("span");
        meta.className = "conversation-meta";
        meta.textContent = (memory.source ? "source: " + memory.source : memory.model_id || "") + (memory.updated_at ? " · " + new Date(memory.updated_at).toLocaleTimeString() : "");
        main.appendChild(label);
        main.appendChild(value);
        main.appendChild(meta);
        var editButton = document.createElement("button");
        editButton.className = "button memory-edit";
        editButton.type = "button";
        editButton.dataset.memoryId = memory.id || "";
        editButton.dataset.memoryKey = memory.key || "";
        editButton.dataset.memoryValue = memory.value || "";
        editButton.innerHTML = '<svg class="icon"><use href="#icon-edit"></use></svg>';
        var button = document.createElement("button");
        button.className = "button danger memory-delete";
        button.type = "button";
        button.dataset.memoryId = memory.id || "";
        button.innerHTML = '<svg class="icon"><use href="#icon-trash"></use></svg>';
        item.appendChild(main);
        item.appendChild(editButton);
        item.appendChild(button);
        memoryList.appendChild(item);
      });
    }
    function loadModelMemory() {
      var modelID = selectedMemoryModelID();
      if (!memoryList || !modelID) {
        renderMemoryList([]);
        if (memoryPreviewText) memoryPreviewText.textContent = "Select a model to preview the prompt context.";
        return;
      }
      fetch("/v1/memories?model_id=" + encodeURIComponent(modelID)).then(function(response) {
        if (!response.ok) {
          return response.text().then(function(text) { throw new Error(text || response.statusText); });
        }
        return response.json();
      }).then(function(payload) {
        renderMemoryList(payload.memories || []);
      }).catch(function(error) {
        if (memoryList) memoryList.innerHTML = '<div class="sub">Memory load failed: ' + error.message + '</div>';
      });
      updateMemoryPreview();
    }
    function updateMemoryPreview() {
      if (!memoryPreviewText) return;
      var modelID = selectedDebugModelID();
      if (!modelID) {
        memoryPreviewText.textContent = "Select a model to preview the prompt context.";
        renderContextMetrics(null);
        renderContextMessages([]);
        return;
      }
      var params = new URLSearchParams();
      params.set("model_id", modelID);
      if (chatSystemPrompt && chatSystemPrompt.value) params.set("system_prompt", chatSystemPrompt.value);
      if (chatConversationID) params.set("conversation_id", chatConversationID);
      if (chatPrompt && chatPrompt.value) params.set("prompt", chatPrompt.value);
      if (chatMaxTokens && chatMaxTokens.value) params.set("max_tokens", chatMaxTokens.value);
      fetch("/v1/memories/preview?" + params.toString()).then(function(response) {
        if (!response.ok) {
          return response.text().then(function(text) { throw new Error(text || response.statusText); });
        }
        return response.json();
      }).then(function(payload) {
        memoryPreviewText.textContent = payload.effective_system_prompt || "No effective prompt context.";
        renderContextMetrics(payload.context || null);
        renderContextMessages(payload.context && payload.context.included_messages ? payload.context.included_messages : []);
      }).catch(function(error) {
        memoryPreviewText.textContent = "Preview failed: " + error.message;
        renderContextMetrics(null);
        renderContextMessages([]);
      });
    }
    function renderContextMetrics(context) {
      if (!contextMetrics) return;
      contextMetrics.innerHTML = "";
      if (!context) {
        contextMetrics.innerHTML = '<div class="sub">No context budget loaded.</div>';
        return;
      }
      [
        ["Context", context.context_tokens || 0],
        ["Response reserve", context.output_reserve_tokens || 0],
        ["History budget", context.history_budget_tokens || 0],
        ["Dropped", context.dropped_messages || 0]
      ].forEach(function(item) {
        var card = document.createElement("div");
        card.className = "context-metric";
        var label = document.createElement("span");
        label.textContent = item[0];
        var value = document.createElement("strong");
        value.textContent = String(item[1]);
        card.appendChild(label);
        card.appendChild(value);
        contextMetrics.appendChild(card);
      });
    }
    function renderContextMessages(messages) {
      if (!contextMessageList) return;
      contextMessageList.innerHTML = "";
      if (!messages || messages.length === 0) {
        var empty = document.createElement("div");
        empty.className = "empty";
        empty.textContent = "No chat messages included yet.";
        contextMessageList.appendChild(empty);
        return;
      }
      messages.forEach(function(message) {
        var item = document.createElement("div");
        item.className = "context-message";
        var role = document.createElement("strong");
        role.textContent = message.role || "user";
        var content = document.createElement("p");
        content.textContent = message.content || "";
        item.appendChild(role);
        item.appendChild(content);
        contextMessageList.appendChild(item);
      });
    }
    function setActiveConversation(id) {
      chatConversationID = id || "";
      try {
        if (chatConversationID) {
          window.localStorage.setItem(chatConversationKey, chatConversationID);
        } else {
          window.localStorage.removeItem(chatConversationKey);
        }
      } catch (error) {}
      if (!conversationList) return;
      conversationList.querySelectorAll(".conversation-item").forEach(function(button) {
        button.classList.toggle("active", button.dataset.conversationId === chatConversationID);
      });
    }
    function resetChatThread() {
      if (!chatThread) return;
      chatThread.innerHTML = "";
      var empty = document.createElement("div");
      empty.className = "chat-empty";
      empty.innerHTML = "<h3>What should this cluster answer?</h3><p>Responses run on your selected worker, not an external API.</p>";
      chatThread.appendChild(empty);
    }
    function renderConversation(conversation) {
      if (!chatThread || !conversation) return;
      chatThread.innerHTML = "";
      (conversation.messages || []).forEach(function(message) {
        appendChatMessage(message.role === "assistant" ? "assistant" : "user", message.content || "");
      });
      if (chatSystemPrompt) chatSystemPrompt.value = conversation.system_prompt || "";
      chatSystemPromptDirty = Boolean(chatSystemPrompt && chatSystemPrompt.value);
      if (chatModel && conversation.model_id) chatModel.value = conversation.model_id;
      syncChatNodes();
      if (chatNode && conversation.node_id) chatNode.value = conversation.node_id;
      loadModelMemory();
      updateMemoryPreview();
      if (modelStatus) modelStatus.innerText = "Loaded conversation " + conversation.id + ".";
    }
    function conversationTitleText(conversation) {
      var messages = conversation && conversation.messages ? conversation.messages : [];
      for (var i = 0; i < messages.length; i++) {
        if (messages[i].role === "user" && messages[i].content) {
          var title = String(messages[i].content).trim();
          return title.length > 56 ? title.slice(0, 56) + "..." : title;
        }
      }
      return "New conversation";
    }
    function conversationSubtitleText(conversation) {
      var parts = [];
      if (conversation && conversation.model_id) parts.push(conversation.model_id);
      if (conversation && conversation.node_id) parts.push(conversation.node_id);
      if (conversation && conversation.updated_at) parts.push(new Date(conversation.updated_at).toLocaleTimeString());
      return parts.length ? parts.join(" · ") : "-";
    }
    function renderConversationList(conversations) {
      if (!conversationList) return;
      conversationList.innerHTML = "";
      if (!conversations || conversations.length === 0) {
        var empty = document.createElement("div");
        empty.className = "empty";
        empty.textContent = "No saved conversations yet.";
        conversationList.appendChild(empty);
        return;
      }
      conversations.forEach(function(conversation) {
        var row = document.createElement("div");
        row.className = "conversation-row";
        row.dataset.conversationId = conversation.id || "";
        var button = document.createElement("button");
        button.className = "conversation-item";
        if (conversation.id === chatConversationID) button.classList.add("active");
        button.type = "button";
        button.dataset.conversationId = conversation.id || "";
        var title = document.createElement("span");
        title.className = "conversation-title";
        title.textContent = conversationTitleText(conversation);
        var meta = document.createElement("span");
        meta.className = "conversation-meta";
        meta.textContent = conversationSubtitleText(conversation);
        button.appendChild(title);
        button.appendChild(meta);
        var deleteButton = document.createElement("button");
        deleteButton.className = "button danger conversation-delete";
        deleteButton.type = "button";
        deleteButton.dataset.conversationId = conversation.id || "";
        deleteButton.innerHTML = '<svg class="icon"><use href="#icon-trash"></use></svg>';
        row.appendChild(button);
        row.appendChild(deleteButton);
        conversationList.appendChild(row);
      });
    }
    function refreshConversationList() {
      if (!conversationList) return;
      fetch("/v1/conversations").then(function(response) {
        if (!response.ok) {
          return response.text().then(function(text) { throw new Error(text || response.statusText); });
        }
        return response.json();
      }).then(function(payload) {
        renderConversationList(payload.conversations || []);
      }).catch(function(error) {
        if (modelStatus) modelStatus.innerText = "Conversation refresh failed: " + error.message;
      });
    }
    function loadConversation(id, fallbackAssistantText) {
      if (!id) return;
      return fetch("/v1/conversations/" + encodeURIComponent(id)).then(function(response) {
        if (!response.ok) {
          return response.text().then(function(text) { throw new Error(text || response.statusText); });
        }
        return response.json();
      }).then(function(conversation) {
        setActiveConversation(conversation.id);
        renderConversation(conversation);
        activateTab("chat", true);
      }).catch(function(error) {
        if (modelStatus) modelStatus.innerText = "Could not load conversation: " + error.message;
        if (fallbackAssistantText) appendChatMessage("assistant", fallbackAssistantText);
      });
    }
    function modelJobText(job) {
      if (job.error) return job.error;
      if (!job.result) return "Waiting for worker result...";
      try {
        var parsed = JSON.parse(job.result);
        if (parsed.output) return String(parsed.output).trim();
        if (parsed.worker_runtime) return "Completed on " + parsed.worker_runtime;
      } catch (error) {
        return job.result;
      }
      return "Completed.";
    }
    function pollModelJob(jobID, attempt) {
      if (!jobID) return;
      fetch("/v1/jobs/" + encodeURIComponent(jobID)).then(function(response) {
        if (!response.ok) {
          return response.text().then(function(text) { throw new Error(text || response.statusText); });
        }
        return response.json();
      }).then(function(job) {
        if (job.status === "succeeded") {
          if (modelStatus) modelStatus.innerText = "Completed " + job.id + ".";
          if (chatConversationID) {
            loadConversation(chatConversationID, modelJobText(job));
          } else {
            appendChatMessage("assistant", modelJobText(job));
          }
          refreshConversationList();
          loadModelMemory();
          setChatSubmitting(false);
          return;
        }
        if (job.status === "failed" || job.status === "canceled") {
          if (modelStatus) modelStatus.innerText = "Model job " + job.status + ".";
          appendChatMessage("system", modelJobText(job));
          setChatSubmitting(false);
          return;
        }
        if (modelStatus) modelStatus.innerText = "Running " + job.id + " (" + job.status + ")...";
        if (attempt < 80) {
          setTimeout(function() { pollModelJob(jobID, attempt + 1); }, 1500);
        } else if (modelStatus) {
          modelStatus.innerText = "Job is still running. Check Jobs for details.";
        }
      }).catch(function(error) {
        if (modelStatus) modelStatus.innerText = "Could not read model job: " + error.message;
        setChatSubmitting(false);
      });
    }
    function checkDistributedRPCReadiness(modelID, nodeID, statusElement) {
      var targetStatus = statusElement || modelStatus;
      if (targetStatus) targetStatus.innerText = "Checking distributed RPC readiness...";
      var params = new URLSearchParams();
      if (nodeID) params.set("node_id", nodeID);
      return fetch("/v1/models/" + encodeURIComponent(modelID) + "/distributed-rpc-readiness?" + params.toString()).then(function(response) {
        if (!response.ok) {
          return response.text().then(function(text) { throw new Error(text || response.statusText); });
        }
        return response.json();
      }).then(function(payload) {
        if (!payload.ready) {
          throw new Error((payload.blockers || []).join("; ") || "distributed RPC is not ready");
        }
        return payload;
      });
    }
    function smokeRPCPoolForChat(modelID, nodeID) {
      return checkDistributedRPCReadiness(modelID, nodeID, modelStatus).then(function() {
        if (modelStatus) modelStatus.innerText = "Checking RPC pool network reachability...";
        return fetch("/v1/runtime/rpc-pool/refresh?timeout_ms=1000", {
          method: "POST"
        });
      }).then(function(response) {
        return response.json().then(function(payload) {
          var report = payload.report || payload;
          if (!response.ok || !report.runnable_now) {
            var details = (report.results || []).map(function(result) {
              if (result.ready) return (result.endpoint || "endpoint") + " ready";
              return (result.endpoint || "endpoint") + " failed" + (result.error ? ": " + result.error : "");
            }).join("; ");
            throw new Error(details || "RPC pool is not reachable.");
          }
          return report;
        });
      });
    }
    function smokeRPCPoolForPromptSmoke(modelID, nodeID) {
      return checkDistributedRPCReadiness(modelID, nodeID, rpcPromptSmokeStatus).then(function() {
        if (rpcPromptSmokeStatus) rpcPromptSmokeStatus.innerText = "Checking RPC pool network reachability...";
        return fetch("/v1/runtime/rpc-pool/refresh?timeout_ms=1000", {
          method: "POST"
        });
      }).then(function(response) {
        return response.json().then(function(payload) {
          var report = payload.report || payload;
          if (!response.ok || !report.runnable_now) {
            var details = (report.results || []).map(function(result) {
              if (result.ready) return (result.endpoint || "endpoint") + " ready";
              return (result.endpoint || "endpoint") + " failed" + (result.error ? ": " + result.error : "");
            }).join("; ");
            throw new Error(details || "RPC pool is not reachable.");
          }
          return report;
        });
      });
    }
    function pollRPCPromptSmokeJob(jobID, attempt) {
      if (!jobID) return;
      fetch("/v1/jobs/" + encodeURIComponent(jobID)).then(function(response) {
        if (!response.ok) {
          return response.text().then(function(text) { throw new Error(text || response.statusText); });
        }
        return response.json();
      }).then(function(job) {
        if (job.status === "succeeded") {
          if (rpcPromptSmokeStatus) rpcPromptSmokeStatus.innerText = "Distributed prompt completed: " + modelJobText(job);
          setRPCPromptSmokeSubmitting(false);
          return;
        }
        if (job.status === "failed" || job.status === "canceled") {
          if (rpcPromptSmokeStatus) rpcPromptSmokeStatus.innerText = "Distributed prompt " + job.status + ": " + modelJobText(job);
          setRPCPromptSmokeSubmitting(false);
          return;
        }
        if (rpcPromptSmokeStatus) rpcPromptSmokeStatus.innerText = "Running distributed prompt " + job.id + " (" + job.status + ")...";
        if (attempt < 80) {
          setTimeout(function() { pollRPCPromptSmokeJob(jobID, attempt + 1); }, 1500);
        } else {
          if (rpcPromptSmokeStatus) rpcPromptSmokeStatus.innerText = "Distributed prompt is still running. Check Jobs for details.";
          setRPCPromptSmokeSubmitting(false);
        }
      }).catch(function(error) {
        if (rpcPromptSmokeStatus) rpcPromptSmokeStatus.innerText = "Could not read distributed prompt job: " + error.message;
        setRPCPromptSmokeSubmitting(false);
      });
    }
    function submitChatGenerate(modelID, nodeID, prompt, useRPC, maxTokens) {
      var generatePath = useRPC ? "/distributed-rpc-generate" : "/generate";
      if (modelStatus) modelStatus.innerText = useRPC ? "Submitting distributed RPC model job..." : "Submitting model job...";
      return fetch("/v1/models/" + encodeURIComponent(modelID) + generatePath, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          node_id: nodeID,
          conversation_id: chatConversationID,
          system_prompt: chatSystemPrompt ? chatSystemPrompt.value : "",
          prompt: prompt,
          max_tokens: maxTokens,
          temperature: chatTemperature && chatTemperature.value ? chatTemperature.value : "0.7"
        })
      });
    }
    if (rpcPromptSmokeModel) {
      rpcPromptSmokeModel.addEventListener("change", function() {
        syncRPCPromptSmokeNodes();
        refreshRPCExecutionPlan();
      });
      syncRPCPromptSmokeNodes();
      refreshRPCExecutionPlan();
    }
    if (rpcPromptSmokeNode) {
      rpcPromptSmokeNode.addEventListener("change", refreshRPCExecutionPlan);
    }
    if (rpcPromptSmokeForm) {
      rpcPromptSmokeForm.addEventListener("submit", function(event) {
        event.preventDefault();
        if (rpcPromptSmokeSubmitting) {
          if (rpcPromptSmokeStatus) rpcPromptSmokeStatus.innerText = "A distributed prompt smoke test is already running.";
          return;
        }
        var modelID = rpcPromptSmokeModel ? String(rpcPromptSmokeModel.value || "").trim() : "";
        var nodeID = rpcPromptSmokeNode ? String(rpcPromptSmokeNode.value || "").trim() : "";
        var promptField = rpcPromptSmokeForm.elements.prompt;
        var prompt = String(promptField && promptField.value ? promptField.value : "").trim();
        var maxTokensField = rpcPromptSmokeForm.elements.max_tokens;
        var maxTokens = parseInt(maxTokensField && maxTokensField.value ? maxTokensField.value : "128", 10);
        if (!Number.isFinite(maxTokens) || maxTokens < 16) maxTokens = 128;
        if (maxTokens > 2048) maxTokens = 2048;
        if (!modelID || !nodeID) {
          if (rpcPromptSmokeStatus) rpcPromptSmokeStatus.innerText = "Install a model and select a coordinator worker first.";
          return;
        }
        if (!prompt) {
          if (rpcPromptSmokeStatus) rpcPromptSmokeStatus.innerText = "Prompt is required.";
          return;
        }
        setRPCPromptSmokeSubmitting(true);
        smokeRPCPoolForPromptSmoke(modelID, nodeID).then(function() {
          if (rpcPromptSmokeStatus) rpcPromptSmokeStatus.innerText = "Submitting distributed prompt job...";
          return fetch("/v1/models/" + encodeURIComponent(modelID) + "/distributed-rpc-generate", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
              node_id: nodeID,
              prompt: prompt,
              max_tokens: maxTokens
            })
          });
        }).then(function(response) {
          if (!response.ok) {
            return response.text().then(function(text) { throw new Error(text || response.statusText); });
          }
          return response.json();
        }).then(function(job) {
          if (rpcPromptSmokeStatus) rpcPromptSmokeStatus.innerText = "Distributed prompt job " + job.id + " submitted.";
          pollRPCPromptSmokeJob(job.id, 0);
        }).catch(function(error) {
          setRPCPromptSmokeSubmitting(false);
          if (rpcPromptSmokeStatus) rpcPromptSmokeStatus.innerText = "Distributed prompt failed: " + error.message;
        });
      });
    }
    if (chatModel) {
      chatModel.addEventListener("change", function() {
        syncChatNodes();
        syncAuxModelSelects(currentChatModelID());
        applyChatModelPreset(true);
        loadModelMemory();
        updateMemoryPreview();
      });
      syncChatNodes();
      syncAuxModelSelects(currentChatModelID());
      applyChatModelPreset(false);
      loadModelMemory();
    }
    if (memoryModelSelect) {
      memoryModelSelect.addEventListener("change", function() {
        loadModelMemory();
      });
    }
    if (debugModelSelect) {
      debugModelSelect.addEventListener("change", function() {
        updateMemoryPreview();
      });
    }
    if (conversationList) {
      conversationList.addEventListener("click", function(event) {
        var openButton = event.target.closest ? event.target.closest(".conversation-item") : null;
        if (openButton && conversationList.contains(openButton)) {
          loadConversation(openButton.dataset.conversationId || "");
          return;
        }
        var button = event.target.closest ? event.target.closest(".conversation-delete") : null;
        if (button && conversationList.contains(button)) {
          var id = button.dataset.conversationId || "";
          if (!id) return;
          button.disabled = true;
          fetch("/v1/conversations/" + encodeURIComponent(id), {
            method: "DELETE"
          }).then(function(response) {
            if (!response.ok) {
              return response.text().then(function(text) { throw new Error(text || response.statusText); });
            }
            return response.json();
          }).then(function() {
            if (chatConversationID === id) {
              setActiveConversation("");
              resetChatThread();
            }
            var row = button.closest ? button.closest(".conversation-row") : null;
            if (row) row.remove();
            if (modelStatus) modelStatus.innerText = "Conversation deleted.";
          }).catch(function(error) {
            button.disabled = false;
            if (modelStatus) modelStatus.innerText = "Conversation delete failed: " + error.message;
          });
        }
      });
    }
    if (newChatButton) {
      newChatButton.addEventListener("click", function() {
        setActiveConversation("");
        resetChatThread();
        if (chatSystemPrompt) chatSystemPrompt.value = "";
        chatSystemPromptDirty = false;
        applyChatModelPreset(true);
        loadModelMemory();
        updateMemoryPreview();
        if (modelStatus) modelStatus.innerText = "New chat ready.";
        activateTab("chat", true);
      });
    }
    if (chatSystemPrompt) {
      chatSystemPrompt.addEventListener("input", function() {
        chatSystemPromptDirty = true;
        updateMemoryPreview();
      });
    }
    if (chatPrompt) {
      chatPrompt.addEventListener("input", updateMemoryPreview);
    }
    if (chatMaxTokens) {
      chatMaxTokens.addEventListener("change", updateMemoryPreview);
      chatMaxTokens.addEventListener("input", updateMemoryPreview);
    }
    if (chatUseRPC) {
      chatUseRPC.addEventListener("change", function() {
        if (chatSubmitButton && !chatSubmitting) setButtonText(chatSubmitButton, chatUseRPC.checked ? "Generate RPC" : "Generate");
        if (modelStatus) {
          modelStatus.innerText = chatUseRPC.checked ? "Ready. RPC pool will be used for the next prompt." : "Ready.";
        }
      });
    }
    if (memoryList) {
      memoryList.addEventListener("click", function(event) {
        var editButton = event.target.closest ? event.target.closest(".memory-edit") : null;
        if (editButton && memoryEditor) {
          memoryEditor.querySelector('[name="memory_id"]').value = editButton.dataset.memoryId || "";
          memoryEditor.querySelector('[name="key"]').value = editButton.dataset.memoryKey || "";
          memoryEditor.querySelector('[name="value"]').value = editButton.dataset.memoryValue || "";
          if (modelStatus) modelStatus.innerText = "Editing memory " + (editButton.dataset.memoryKey || editButton.dataset.memoryId || "") + ".";
          return;
        }
        var button = event.target.closest ? event.target.closest(".memory-delete") : null;
        if (!button) return;
        var memoryID = button.dataset.memoryId;
        if (!memoryID) return;
        button.disabled = true;
        fetch("/v1/memories/" + encodeURIComponent(memoryID), {
          method: "DELETE"
        }).then(function(response) {
          if (!response.ok) {
            return response.text().then(function(text) { throw new Error(text || response.statusText); });
          }
          return response.json();
        }).then(function() {
          if (modelStatus) modelStatus.innerText = "Memory deleted.";
          loadModelMemory();
          updateMemoryPreview();
        }).catch(function(error) {
          button.disabled = false;
          if (modelStatus) modelStatus.innerText = "Memory delete failed: " + error.message;
        });
      });
    }
    if (memoryEditor) {
      memoryEditor.addEventListener("submit", function(event) {
        event.preventDefault();
        var modelID = selectedMemoryModelID();
        var memoryID = String(memoryEditor.querySelector('[name="memory_id"]').value || "").trim();
        var key = String(memoryEditor.querySelector('[name="key"]').value || "").trim();
        var value = String(memoryEditor.querySelector('[name="value"]').value || "").trim();
        if (!modelID) {
          if (modelStatus) modelStatus.innerText = "Select a model before saving memory.";
          return;
        }
        if (!key || !value) {
          if (modelStatus) modelStatus.innerText = "Memory key and value are required.";
          return;
        }
        var button = memoryEditor.querySelector("button[type=submit]");
        if (button) {
          button.disabled = true;
          setButtonText(button, "Saving...");
        }
        fetch(memoryID ? "/v1/memories/" + encodeURIComponent(memoryID) : "/v1/memories", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            model_id: modelID,
            key: key,
            value: value,
            source: "manual"
          })
        }).then(function(response) {
          if (!response.ok) {
            return response.text().then(function(text) { throw new Error(text || response.statusText); });
          }
          return response.json();
        }).then(function() {
          memoryEditor.reset();
          if (modelStatus) modelStatus.innerText = "Memory saved.";
          loadModelMemory();
          updateMemoryPreview();
        }).catch(function(error) {
          if (modelStatus) modelStatus.innerText = "Memory save failed: " + error.message;
        }).finally(function() {
          if (button) {
            button.disabled = false;
            setButtonText(button, "Save memory");
          }
        });
      });
    }
    if (memoryEditorReset && memoryEditor) {
      memoryEditorReset.addEventListener("click", function() {
        memoryEditor.reset();
        if (modelStatus) modelStatus.innerText = "Memory editor reset.";
      });
    }
    if (memoryClearModel) {
      memoryClearModel.addEventListener("click", function() {
        var modelID = selectedMemoryModelID();
        if (!modelID) {
          if (modelStatus) modelStatus.innerText = "Select a model before clearing memory.";
          return;
        }
        memoryClearModel.disabled = true;
        fetch("/v1/memories?model_id=" + encodeURIComponent(modelID), {
          method: "DELETE"
        }).then(function(response) {
          if (!response.ok) {
            return response.text().then(function(text) { throw new Error(text || response.statusText); });
          }
          return response.json();
        }).then(function(payload) {
          if (modelStatus) modelStatus.innerText = "Cleared " + (payload.deleted || 0) + " memory item(s).";
          loadModelMemory();
          updateMemoryPreview();
        }).catch(function(error) {
          if (modelStatus) modelStatus.innerText = "Clear memory failed: " + error.message;
        }).finally(function() {
          memoryClearModel.disabled = false;
        });
      });
    }
    if (chatConversationID) {
      setActiveConversation(chatConversationID);
      loadConversation(chatConversationID);
    }
    if (chatForm) {
      chatForm.addEventListener("submit", function(event) {
        event.preventDefault();
        if (!chatModel || !chatNode) return;
        if (chatSubmitting) {
          if (modelStatus) modelStatus.innerText = "A generate job is already running for this chat.";
          return;
        }
        var modelID = chatModel.value;
        var nodeID = chatNode.value;
        var prompt = String(chatForm.elements.prompt.value || "").trim();
        if (!modelID || !nodeID) {
          modelStatus.innerText = "Install a model before generating.";
          return;
        }
        if (!prompt) {
          modelStatus.innerText = "Prompt is required.";
          return;
        }
        setChatSubmitting(true);
        var useRPC = Boolean(chatUseRPC && chatUseRPC.checked && !chatUseRPC.disabled);
        var maxTokens = parseInt(chatMaxTokens && chatMaxTokens.value ? chatMaxTokens.value : "512", 10);
        if (!Number.isFinite(maxTokens) || maxTokens < 16) maxTokens = 512;
        if (maxTokens > 2048) maxTokens = 2048;
        (useRPC ? smokeRPCPoolForChat(modelID, nodeID) : Promise.resolve(null)).then(function() {
          appendChatMessage("user", prompt);
          chatForm.elements.prompt.value = "";
          return submitChatGenerate(modelID, nodeID, prompt, useRPC, maxTokens);
        }).then(function(response) {
          if (!response.ok) {
            return response.text().then(function(text) { throw new Error(text || response.statusText); });
          }
          return response.json();
        }).then(function(job) {
          try {
            var input = JSON.parse(job.input || "{}");
            if (input.conversation_id) {
              setActiveConversation(input.conversation_id);
            }
          } catch (error) {}
          loadModelMemory();
          modelStatus.innerText = (useRPC ? "Distributed RPC job " : "Generate job ") + job.id + " submitted.";
          pollModelJob(job.id, 0);
        }).catch(function(error) {
          setChatSubmitting(false);
          modelStatus.innerText = "Generate failed: " + error.message;
          appendChatMessage("system", error.message);
        });
      });
    }
  </script>
</body>
</html>`))

var inviteTemplate = template.Must(template.New("invite").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Invite Worker - CMesh</title>
  <style>
    :root {
      color-scheme: light;
      --bg: #f6f7f9;
      --panel: #ffffff;
      --text: #17202a;
      --muted: #657282;
      --line: #d9dee5;
      --accent: #0f766e;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      font-family: ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      background: var(--bg);
      color: var(--text);
    }
    header {
      padding: 28px 32px 18px;
      border-bottom: 1px solid var(--line);
      background: var(--panel);
      display: flex;
      align-items: end;
      justify-content: space-between;
      gap: 18px;
    }
    main {
      padding: 24px 32px 40px;
      width: 100%;
      max-width: 1280px;
      margin: 0 auto;
    }
    h1 { margin: 0 0 6px; font-size: 28px; letter-spacing: 0; }
    h2 { margin: 0; font-size: 16px; letter-spacing: 0; }
    .sub { margin: 0; color: var(--muted); font-size: 14px; }
    section {
      margin-top: 16px;
      background: var(--panel);
      border: 1px solid var(--line);
      border-radius: 8px;
      overflow: hidden;
    }
    .hero {
      display: grid;
      grid-template-columns: minmax(0, 1.1fr) minmax(340px, .9fr);
      gap: 16px;
      align-items: stretch;
      margin-top: 0;
    }
    .invite-card {
      background: var(--panel);
      border: 1px solid var(--line);
      border-radius: 8px;
      overflow: hidden;
    }
    .hero-copy {
      padding: 20px;
      display: grid;
      align-content: start;
      gap: 14px;
      background: linear-gradient(135deg, #ffffff 0%, var(--soft) 100%);
    }
    .hero-copy h2 {
      font-size: 22px;
    }
    .steps {
      display: grid;
      grid-template-columns: repeat(3, minmax(0, 1fr));
      gap: 10px;
    }
    .step-card {
      min-height: 92px;
      padding: 12px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: rgba(255,255,255,.72);
    }
    .step-card strong {
      display: block;
      font-size: 13px;
      margin-bottom: 5px;
    }
    .step-card span {
      color: var(--muted);
      font-size: 12px;
      line-height: 1.4;
    }
    .desktop-card {
      display: grid;
      align-content: start;
    }
    .desktop-primary {
      display: grid;
      gap: 14px;
      padding: 16px;
    }
    .desktop-primary h3 {
      margin: 0;
      font-size: 18px;
    }
    .desktop-primary p {
      margin: 0;
      color: var(--muted);
      font-size: 14px;
      line-height: 1.5;
    }
    .desktop-primary .hint {
      margin: 0;
    }
    .manual-invite {
      margin: 0 16px 16px;
      border: 1px solid var(--line);
      border-radius: 8px;
      overflow: hidden;
      background: #fbfcfd;
    }
    .manual-invite summary {
      padding: 12px 14px;
      color: var(--muted);
      font-size: 13px;
      font-weight: 700;
      cursor: pointer;
    }
    .manual-invite .manual-body {
      display: grid;
      gap: 10px;
      padding: 0 14px 14px;
    }
    .manual-invite pre {
      border-radius: 6px;
    }
    .section-head {
      padding: 14px 16px;
      border-bottom: 1px solid var(--line);
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 12px;
    }
    pre {
      margin: 0;
      padding: 16px;
      overflow-x: auto;
      background: #101820;
      color: #f7fbff;
      font-size: 13px;
      line-height: 1.5;
    }
    code {
      font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
    }
    button, a.button {
      min-height: 34px;
      padding: 0 12px;
      border: 1px solid var(--line);
      border-radius: 6px;
      background: var(--panel);
      color: var(--text);
      font-size: 14px;
      font-weight: 600;
      text-decoration: none;
      cursor: pointer;
    }
    .toolbar {
      display: flex;
      gap: 10px;
      flex-wrap: wrap;
    }
    .actions {
      display: flex;
      gap: 10px;
      flex-wrap: wrap;
      margin-top: 12px;
    }
    .primary {
      background: var(--accent);
      border-color: var(--accent);
      color: #ffffff;
    }
    .secondary {
      background: #eff6ff;
      border-color: #bfdbfe;
      color: var(--accent-2);
    }
    .hint {
      margin: 10px 16px 0;
      color: var(--muted);
      font-size: 13px;
    }
    .advanced-grid {
      display: grid;
      grid-template-columns: repeat(2, minmax(0, 1fr));
      gap: 16px;
      margin-top: 16px;
    }
    .full-span {
      grid-column: 1 / -1;
    }
    @media (max-width: 640px) {
      header { display: block; }
      header .toolbar { margin-top: 14px; }
      header, main { padding-left: 18px; padding-right: 18px; }
      .hero, .advanced-grid, .steps { grid-template-columns: 1fr; }
      .full-span { grid-column: auto; }
    }
  </style>
</head>
<body>
  <header>
    <div>
      <h1>Invite Worker</h1>
      <p class="sub">Connect a desktop or server machine to this private CMesh cluster.</p>
    </div>
    <div class="toolbar">
      <a class="button" href="/">Dashboard</a>
    </div>
  </header>
  <main>
    <div class="hero">
      <div class="invite-card hero-copy">
        <h2>Recommended flow</h2>
        <p class="sub">Install the worker app on the machine that will share resources. The invite link pre-fills this manager URL and one-time join token.</p>
        <div class="steps">
          <div class="step-card"><strong>1. Install</strong><span>Download the worker installer and move the app to Applications.</span></div>
          <div class="step-card"><strong>2. First launch</strong><span>On macOS, open it once from Applications with Control-click, then Open.</span></div>
          <div class="step-card"><strong>3. Open invite</strong><span>The installed app receives the manager URL and join token automatically.</span></div>
          <div class="step-card"><strong>4. Start worker</strong><span>Choose resource limits, save settings, then connect to the cluster.</span></div>
        </div>
      </div>

      <section class="desktop-card">
        <div class="section-head">
          <h2>Install worker app</h2>
          <code>recommended</code>
        </div>
        <div class="desktop-primary">
          <h3>Use the installer first</h3>
          <p>Install CMesh Worker on this machine. On macOS, launch it once from Applications with Control-click, then Open, before using the invite button.</p>
          <div class="actions">
            <a class="button primary" id="worker-download" href="{{.DownloadURL}}">Download Worker App</a>
            <a class="button secondary" href="{{.DesktopInviteHref}}">Open invite in app</a>
            <a class="button" href="https://github.com/NythralHome/cmesh/releases/latest">Other platforms</a>
          </div>
          <p class="hint" id="worker-download-hint">Direct downloads use the latest CMesh release. macOS requires the first launch from Finder before browser invite links can open the app.</p>
        </div>
        <details class="manual-invite">
          <summary>Manual invite link</summary>
          <div class="manual-body">
            <pre><code id="desktop-invite">{{.DesktopInviteURL}}</code></pre>
            <button type="button" data-copy="desktop-invite">Copy manual invite link</button>
          </div>
        </details>
      </section>
    </div>

    <div class="advanced-grid">
    <section>
      <div class="section-head">
        <h2>macOS / Linux</h2>
        <button type="button" data-copy="mac-linux">Copy</button>
      </div>
      <pre><code id="mac-linux">curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | \
  CMESH_MANAGER_URL="{{.ManagerURL}}" \
  CMESH_JOIN_TOKEN="{{.JoinToken}}" \
  CMESH_CPU=4 \
  CMESH_MEMORY_GB=8 \
  CMESH_DISK_GB=50 \
  sh</code></pre>
    </section>

    <section>
      <div class="section-head">
        <h2>Linux service</h2>
        <button type="button" data-copy="linux-service">Copy</button>
      </div>
      <pre><code id="linux-service">curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | \
  sudo env CMESH_MANAGER_URL="{{.ManagerURL}}" \
  CMESH_JOIN_TOKEN="{{.JoinToken}}" \
  CMESH_INSTALL_SERVICE=true \
  CMESH_CPU=4 \
  CMESH_MEMORY_GB=8 \
  CMESH_DISK_GB=50 \
  sh</code></pre>
    </section>

    <section class="full-span">
      <div class="section-head">
        <h2>Worker control</h2>
        <button type="button" data-copy="worker-control">Copy</button>
      </div>
      <pre><code id="worker-control">curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | sh -s -- status
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | sudo sh -s -- stop
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | sudo sh -s -- start
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | sudo sh -s -- uninstall</code></pre>
    </section>

    <section class="full-span">
      <div class="section-head">
        <h2>Windows PowerShell</h2>
        <button type="button" data-copy="windows">Copy</button>
      </div>
      <pre><code id="windows">$env:CMESH_MANAGER_URL="{{.ManagerURL}}"
$env:CMESH_JOIN_TOKEN="{{.JoinToken}}"
$env:CMESH_CPU="4"
$env:CMESH_MEMORY_GB="8"
$env:CMESH_DISK_GB="50"
iwr https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.ps1 -UseB | iex</code></pre>
    </section>
    </div>
  </main>
  <script>
    document.querySelectorAll("[data-copy]").forEach(function(button) {
      button.addEventListener("click", function() {
        var target = document.getElementById(button.getAttribute("data-copy"));
        navigator.clipboard.writeText(target.innerText).then(function() {
          button.innerText = "Copied";
          setTimeout(function() { button.innerText = "Copy"; }, 1200);
        });
      });
    });

    (function() {
      var releaseBase = "{{.ReleaseDownloadBase}}";
      var options = {
        macApple: {
          label: "Download for Apple Silicon",
          asset: "CMesh-Worker-Apple-Silicon.dmg",
          hint: "After installing, open CMesh Worker once from Applications with Control-click, then Open. Then use the invite button."
        },
        macIntel: {
          label: "Download for Intel Mac",
          asset: "CMesh-Worker-Intel-Mac.dmg",
          hint: "After installing, open CMesh Worker once from Applications with Control-click, then Open. Then use the invite button."
        },
        windows: {
          label: "Download for Windows",
          asset: "CMesh-Worker-windows-amd64.zip",
          hint: "Best for 64-bit Windows PCs."
        },
        linux: {
          label: "Download for Linux",
          asset: "CMesh-Worker-linux-amd64.tar.gz",
          hint: "Best for 64-bit Linux desktops."
        }
      };

      function chooseDownload() {
        var ua = navigator.userAgent || "";
        var platform = navigator.platform || "";
        var text = (ua + " " + platform).toLowerCase();
        if (text.indexOf("win") >= 0) return options.windows;
        if (text.indexOf("linux") >= 0) return options.linux;
        if (text.indexOf("mac") >= 0) return options.macApple;
        return null;
      }

      function applyChoice(choice) {
        var link = document.getElementById("worker-download");
        var hint = document.getElementById("worker-download-hint");
        if (!link || !choice) return;
        link.href = releaseBase + choice.asset;
        link.innerText = choice.label;
        if (hint) hint.innerText = choice.hint;
      }

      var choice = chooseDownload();
      if (navigator.userAgentData && navigator.userAgentData.getHighEntropyValues) {
        navigator.userAgentData.getHighEntropyValues(["architecture", "platform"]).then(function(values) {
          var platform = String(values.platform || "").toLowerCase();
          var arch = String(values.architecture || "").toLowerCase();
          if (platform.indexOf("mac") >= 0 && (arch.indexOf("x86") >= 0 || arch.indexOf("amd64") >= 0)) {
            applyChoice(options.macIntel);
            return;
          }
          if (platform.indexOf("mac") >= 0 && (arch.indexOf("arm") >= 0 || arch.indexOf("aarch64") >= 0)) {
            applyChoice(options.macApple);
            return;
          }
          applyChoice(choice);
        }).catch(function() {
          applyChoice(choice);
        });
        return;
      }
      applyChoice(choice);
    })();
  </script>
</body>
</html>`))
