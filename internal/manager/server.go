package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/cmesh/cmesh/internal/cluster"
	"github.com/cmesh/cmesh/internal/jobs"
	"github.com/cmesh/cmesh/internal/membership"
	"github.com/cmesh/cmesh/internal/models"
	"github.com/cmesh/cmesh/internal/resources"
	"github.com/cmesh/cmesh/internal/version"
)

type Server struct {
	addr          string
	state         Store
	joinToken     string
	operatorToken string
	publicURL     string
	mux           *http.ServeMux
	server        *http.Server
}

type ServerOptions struct {
	Addr          string
	JoinToken     string
	OperatorToken string
	PublicURL     string
}

func NewServer(addr string, state Store) *Server {
	return NewServerWithOptions(ServerOptions{Addr: addr}, state)
}

func NewServerWithOptions(options ServerOptions, state Store) *Server {
	mux := http.NewServeMux()
	s := &Server{
		addr:          options.Addr,
		state:         state,
		joinToken:     options.JoinToken,
		operatorToken: options.OperatorToken,
		publicURL:     strings.TrimRight(options.PublicURL, "/"),
		mux:           mux,
	}

	mux.HandleFunc("/", s.handleDashboard)
	mux.HandleFunc("/invite", s.handleInvite)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/v1/cluster", s.handleCluster)
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
	data := struct {
		Summary            ClusterSummary
		OnlineNodes        []cluster.Node
		OfflineWorkerCount int
		Benchmarks         map[string]NodeBenchmarkSummary
		ClusterBenchmarks  []ClusterBenchmarkSummary
		Models             []ModelSummary
		NodesByID          map[string]cluster.Node
		WorkerActiveJobs   map[string]int
		MaxClusterGFLOPS   float64
		Jobs               []jobs.Job
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
		NodesByID:          nodesByID(nodes),
		WorkerActiveJobs:   activeJobsByWorker(allJobs),
		MaxClusterGFLOPS:   maxClusterBenchmarkGFLOPS(clusterBenchmarks),
		Jobs:               recentJobs(allJobs, 12),
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
	writeJSON(w, http.StatusOK, map[string]any{
		"models": modelSummaries(models.Catalog(), s.state.Jobs(), s.state.Nodes()),
	})
}

func (s *Server) handleModel(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorAuth(w, r, false) {
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/models/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	model, ok := models.Find(parts[0])
	if !ok {
		http.NotFound(w, r)
		return
	}
	action := parts[1]
	switch action {
	case "install":
		s.handleModelInstall(w, r, model)
	case "delete":
		s.handleModelDelete(w, r, model)
	case "generate":
		s.handleModelGenerate(w, r, model)
	default:
		http.NotFound(w, r)
	}
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
	if ok, reason := modelInstallEligibility(summaries[0], req.NodeID); !ok {
		http.Error(w, "no eligible worker for model install: "+reason, http.StatusConflict)
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
	systemPrompt := strings.TrimSpace(req.SystemPrompt)
	if systemPrompt == "" {
		systemPrompt = modelDefaultSystemPrompt(model)
	}
	conversation := appendConversationMessage(s.state, conversationID, model.ID, req.NodeID, systemPrompt, models.ChatMessage{
		Role:    "user",
		Content: req.Prompt,
	})
	effectiveSystemPrompt := systemPromptWithMemory(systemPrompt, model.ID, s.state)
	input, err := json.Marshal(models.GenerateInput{
		ModelID:        model.ID,
		Prompt:         req.Prompt,
		Messages:       conversation.Messages,
		SystemPrompt:   effectiveSystemPrompt,
		ConversationID: conversation.ID,
		MaxTokens:      req.MaxTokens,
		Temperature:    req.Temperature,
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
	if systemPrompt == "" {
		systemPrompt = modelDefaultSystemPrompt(model)
	}
	memories := memoriesForModel(s.state, model.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"model_id":                model.ID,
		"memories":                memories,
		"memory_context":          memoryContext(model.ID, memories),
		"effective_system_prompt": systemPromptWithMemory(systemPrompt, model.ID, s.state),
	})
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

	job, ok := s.state.CompleteJob(jobID, req)
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleWorkerRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/workers/")
	parts := strings.Split(path, "/")
	if len(parts) == 3 && parts[1] == "jobs" && parts[2] == "next" {
		s.handleWorkerNextJob(w, r, parts[0])
		return
	}
	http.NotFound(w, r)
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
		if job.Type != models.JobGenerate {
			continue
		}
		if job.RequestedBy != "dashboard-chat" {
			continue
		}
		if job.Status != jobs.StatusSucceeded || strings.TrimSpace(job.Result) == "" {
			continue
		}
		out = append(out, job)
	}
	return recentJobs(out, limit)
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
	return job.Type == models.JobInstall || job.Type == models.JobDelete || job.Type == models.JobGenerate
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

func generatableModelCount(in []ModelSummary) int {
	count := 0
	for _, summary := range in {
		if len(summary.InstalledOn) > 0 && summary.Status != "deleting" {
			count++
		}
	}
	return count
}

func modelFailureHint(summary ModelSummary) string {
	if strings.Contains(summary.LastError, "unsupported job type") {
		return ""
	}
	return ""
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
	base := "You are CMesh's local AI assistant. Continue the conversation using the provided history. Answer the latest user message directly. If the user shared personal details earlier in this conversation, remember and use them. Do not print role names, chat template tokens, or hidden reasoning."
	if strings.Contains(strings.ToLower(model.ID), "deepseek") {
		return base + " Return only the final answer unless the user explicitly asks for reasoning."
	}
	switch strings.ToLower(model.Family) {
	case "qwen":
		return base + " Prefer concise, natural answers."
	case "gemma":
		return base + " Keep answers clear and conversational."
	case "mistral":
		return base + " Be practical and concise."
	case "phi":
		return base + " Keep answers short unless more detail is needed."
	default:
		return base
	}
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
	if output, ok := result["output"].(string); ok && strings.TrimSpace(output) != "" {
		return output
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
		case models.JobGenerate:
			var input models.GenerateInput
			if err := json.Unmarshal([]byte(job.Input), &input); err == nil && strings.TrimSpace(input.Prompt) != "" {
				return "generate " + modelID + ": " + input.Prompt
			}
			return "generate " + modelID
		}
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
		if len(value) <= 12 {
			return value
		}
		return value[:12]
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
	"hasActiveJobs":        hasActiveJobs,
	"schedulerJobs":        schedulerJobs,
	"isModelJob":           isModelJob,
	"modelJobCount":        modelJobCount,
	"installedModelCount":  installedModelCount,
	"generatableCount":     generatableModelCount,
	"modelFailureHint":     modelFailureHint,
	"workerSlots":          workerJobSlots,
	"jobCanCancel":         jobCanBeCanceled,
	"jobDuration":          jobDuration,
	"jobTimeline":          jobTimeline,
	"jobWorkload":          jobWorkload,
	"jobRequirements":      jobRequirements,
	"conversationTitle":    conversationTitle,
	"conversationSubtitle": conversationSubtitle,
	"memoryLabel":          memoryLabel,
	"memorySubtitle":       memorySubtitle,
	"runtimeSummary":       runtimeSummary,
	"heartbeatAge":         heartbeatAge,
	"workerModelBytes":     workerModelBytes,
	"modelInstalledOn":     modelInstalledOn,
	"modelNodeOptions": func(nodes map[string]cluster.Node, summary ModelSummary) []cluster.Node {
		out := make([]cluster.Node, 0, len(summary.InstalledOn))
		for _, nodeID := range summary.InstalledOn {
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
		case "installing", "deleting":
			return "pill pill-job"
		default:
			return "pill pill-muted"
		}
	},
	"modelCanInstall": func(summary ModelSummary) bool {
		return summary.Status == "available" && summary.CapableNodes > 0
	},
	"modelCanGenerate": func(summary ModelSummary) bool {
		return len(summary.InstalledOn) > 0 && summary.Status != "deleting"
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
      max-width: 1680px;
      margin: 0 auto;
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
      position: sticky;
      top: 0;
      z-index: 5;
      display: flex;
      gap: 8px;
      margin-bottom: 16px;
      padding: 8px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: rgba(255, 255, 255, 0.92);
      backdrop-filter: blur(14px);
    }
    .tab-button {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      gap: 8px;
      min-height: 38px;
      padding: 0 14px;
      border: 1px solid transparent;
      border-radius: 6px;
      background: transparent;
      color: var(--muted);
      font: inherit;
      font-weight: 700;
      cursor: pointer;
    }
    .tab-button.active {
      border-color: var(--line);
      background: var(--accent);
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
      grid-template-columns: minmax(220px, 1fr) minmax(180px, .7fr);
      gap: 10px;
      min-width: 0;
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
    }
    .installed-models {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
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
    .model-actions {
      display: flex;
      gap: 8px;
      flex-wrap: wrap;
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
      display: flex;
      justify-content: space-between;
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
      table { display: block; overflow-x: auto; }
      .onboarding-body { grid-template-columns: 1fr; }
      .model-run-guide { grid-template-columns: 1fr; }
      .step { grid-template-columns: 30px 1fr; }
      .step .pill, .step .pill-job, .step .pill-muted { grid-column: 2; width: fit-content; }
      .first-test-form { grid-template-columns: 1fr; }
      .first-test-form .wide { grid-column: auto; }
      .job-runner, .cluster-runner { grid-template-columns: 1fr; }
      .console-tabs { overflow-x: auto; }
      .models-shell { grid-template-columns: 1fr; }
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
    <nav class="console-tabs" aria-label="Dashboard sections">
      <button class="tab-button active" type="button" data-tab-target="overview"><svg class="icon"><use href="#icon-workers"></use></svg>Overview</button>
      <button class="tab-button" type="button" data-tab-target="workers"><svg class="icon"><use href="#icon-workers"></use></svg>Workers</button>
      <button class="tab-button" type="button" data-tab-target="chat"><svg class="icon"><use href="#icon-send"></use></svg>Chat</button>
      <button class="tab-button" type="button" data-tab-target="memory"><svg class="icon"><use href="#icon-brain"></use></svg>Memory</button>
      <button class="tab-button" type="button" data-tab-target="conversations"><svg class="icon"><use href="#icon-terminal"></use></svg>Conversations</button>
      <button class="tab-button" type="button" data-tab-target="models"><svg class="icon"><use href="#icon-brain"></use></svg>Models</button>
      <button class="tab-button" type="button" data-tab-target="prompt-debug"><svg class="icon"><use href="#icon-terminal"></use></svg>Prompt Debug</button>
      <button class="tab-button" type="button" data-tab-target="model-activity"><svg class="icon"><use href="#icon-terminal"></use></svg>Model Activity</button>
      <button class="tab-button" type="button" data-tab-target="scheduler"><svg class="icon"><use href="#icon-chart"></use></svg>Scheduler</button>
      <button class="tab-button" type="button" data-tab-target="jobs"><svg class="icon"><use href="#icon-terminal"></use></svg>Jobs</button>
      <button class="tab-button" type="button" data-tab-target="benchmarks"><svg class="icon"><use href="#icon-chart"></use></svg>Benchmarks</button>
    </nav>
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
              <td>{{printf "%.1f" (gb .Resources.Storage.AllowedBytes)}} GB allowed<br><span class="sub">{{printf "%.1f" (gb .Resources.Storage.FreeBytes)}} GB free</span></td>
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
                  {{range .Models}}{{if modelCanGenerate .}}<option value="{{.Model.ID}}">{{.Model.Name}}</option>{{end}}{{end}}
                </select>
              </div>
              <div class="field">
                <label for="chat-node">Worker</label>
                <select id="chat-node" name="node_id">
                  {{if eq (generatableCount .Models) 0}}<option value="">No installed model worker</option>{{end}}
                  {{range .Models}}{{$chatModelID := .Model.ID}}{{range modelNodeOptions $.NodesByID .}}<option value="{{.ID}}" data-model-id="{{$chatModelID}}">{{.Name}}</option>{{end}}{{end}}
                </select>
              </div>
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
          <span class="conversation-meta">Effective system context</span>
          <pre id="memory-preview-text">Select a model to preview the prompt context.</pre>
        </div>
      </div>
    </section>
    </div>
    <div class="tab-panel" id="tab-models" hidden>
    <section id="models">
      <div class="section-head">
        <h2>Model Catalog</h2>
        <code>{{installedModelCount .Models}} installed / {{len .Models}} catalog entries</code>
      </div>
      <div class="model-run-guide" aria-label="First model run">
        <div class="model-run-step {{if .OnlineNodes}}done{{else}}current{{end}}">
          <span class="step-index">{{if .OnlineNodes}}✓{{else}}1{{end}}</span>
          <h3>Connect a worker</h3>
          <p class="sub">{{if .OnlineNodes}}{{len .OnlineNodes}} online worker(s) can receive model jobs.{{else}}Install and start CMesh Worker before installing models.{{end}}</p>
          {{if not .OnlineNodes}}<a class="button primary" href="{{.InviteURL}}"><svg class="icon"><use href="#icon-plus"></use></svg>Invite worker</a>{{else}}<button class="button" type="button" data-tab-shortcut="workers"><svg class="icon"><use href="#icon-workers"></use></svg>View workers</button>{{end}}
        </div>
        <div class="model-run-step {{if gt (installedModelCount .Models) 0}}done{{else if .OnlineNodes}}current{{end}}">
          <span class="step-index">{{if gt (installedModelCount .Models) 0}}✓{{else}}2{{end}}</span>
          <h3>Install a model</h3>
          <p class="sub">{{if gt (installedModelCount .Models) 0}}{{installedModelCount .Models}} model(s) are installed on worker storage.{{else if .OnlineNodes}}Pick the smallest capable catalog model first, then move up.{{else}}A worker must be online before install buttons can run.{{end}}</p>
          <button class="button" type="button" onclick="document.querySelector('.model-catalog')?.scrollIntoView({behavior:'smooth', block:'start'});"><svg class="icon"><use href="#icon-download"></use></svg>Open catalog</button>
        </div>
        <div class="model-run-step {{if gt (generatableCount .Models) 0}}done{{else if gt (installedModelCount .Models) 0}}current{{end}}">
          <span class="step-index">{{if gt (generatableCount .Models) 0}}✓{{else}}3{{end}}</span>
          <h3>Chat locally</h3>
          <p class="sub">{{if gt (generatableCount .Models) 0}}{{generatableCount .Models}} ready model(s) can answer prompts through this manager.{{else}}The model must be installed and llama.cpp must be ready on that worker.{{end}}</p>
          <button class="button {{if gt (generatableCount .Models) 0}}primary{{end}}" type="button" data-tab-shortcut="chat" {{if eq (generatableCount .Models) 0}}disabled{{end}}><svg class="icon"><use href="#icon-send"></use></svg>Open Chat</button>
        </div>
      </div>
      <div class="installed-models">
        {{if gt (installedModelCount .Models) 0}}
        {{range .Models}}
        {{if .InstalledOn}}
        <article class="installed-model" data-model-id="{{.Model.ID}}">
          <div class="model-title">
            <div>
              <h3>{{.Model.Name}}</h3>
              <p class="sub">{{.Model.Parameters}} / {{.Model.Quant}} · {{printf "%.1f" (gb .Model.DiskBytes)}} GB catalog disk</p>
            </div>
            <span class="{{modelStatusClass .Status}}">{{.Status}}</span>
          </div>
          <p class="sub">Installed on {{range $index, $nodeID := .InstalledOn}}{{if $index}}, {{end}}<code>{{nodeLabel $.NodesByID $nodeID}}</code>{{end}}</p>
          <div class="model-actions">
            {{$modelID := .Model.ID}}
            {{range modelNodeOptions $.NodesByID .}}
            <button class="button danger model-delete" type="button" data-model-id="{{$modelID}}" data-node-id="{{.ID}}"><svg class="icon"><use href="#icon-trash"></use></svg><span>Delete from {{.Name}}</span></button>
            {{end}}
          </div>
        </article>
        {{end}}
        {{end}}
        {{else}}
        <div class="empty-action">
          <div>
            <h3>No installed models</h3>
            <p>Install one catalog model on a capable online worker before using Chat.</p>
          </div>
        </div>
        {{end}}
      </div>
      <div class="model-catalog">
          {{range .Models}}
          <article class="model-card" data-model-id="{{.Model.ID}}">
            <div class="model-title">
              <div>
                <h3>{{.Model.Name}}</h3>
                <p class="sub">{{.Model.Description}}</p>
              </div>
              <span class="{{modelStatusClass .Status}}">{{.Status}}</span>
            </div>
            <div class="model-specs">
              <div><span>Model</span><strong>{{.Model.Parameters}} / {{.Model.Quant}}</strong></div>
              <div><span>Required RAM</span><strong>{{printf "%.1f" (gb .Model.MemoryBytes)}} GB</strong></div>
              <div><span>Required disk</span><strong>{{printf "%.1f" (gb .Model.DiskBytes)}} GB</strong></div>
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
                {{else}}
                <span>{{range $index, $reason := .Reasons}}{{if $index}}; {{end}}{{$reason}}{{end}} · jobs {{.ActiveJobs}}/{{.JobSlots}} · has {{printf "%.1f" (gb .AllowedMemoryBytes)}} GB RAM / {{printf "%.1f" (gb .AllowedStorageBytes)}} GB allowed disk / {{printf "%.1f" (gb .FreeStorageBytes)}} GB free{{if gt .AllowedVRAMBytes 0}} / {{printf "%.1f" (gb .AllowedVRAMBytes)}} GB VRAM{{end}}</span>
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
              <button class="button primary model-install" type="button" data-model-id="{{.Model.ID}}" {{if not (modelCanInstall .)}}disabled{{end}}><svg class="icon"><use href="#icon-download"></use></svg><span>Install</span></button>
            </div>
          </article>
          {{end}}
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
              <td class="mono-output"><code>{{clip (jobDetail .) 160}}</code></td>
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
      var target = name || "overview";
      var panel = document.getElementById("tab-" + target);
      if (!panel) target = "overview";
      document.querySelectorAll(".tab-panel").forEach(function(section) {
        section.hidden = section.id !== "tab-" + target;
      });
      document.querySelectorAll(".tab-button[data-tab-target]").forEach(function(button) {
        button.classList.toggle("active", button.dataset.tabTarget === target);
      });
      if (updateHash) {
        history.replaceState(null, "", target === "overview" ? window.location.pathname : "#" + target);
      }
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
    activateTab((window.location.hash || "").replace("#", ""), false);
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
    function modelCardFor(modelID) {
      if (!modelID) return null;
      return document.querySelector('[data-model-id="' + cssIdent(modelID) + '"]');
    }
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
      var card = modelCardFor(modelID);
      if (!card) return;
      var operation = card.querySelector(".model-operation");
      if (!operation) {
        operation = document.createElement("div");
        operation.className = "model-operation";
        card.appendChild(operation);
      }
      var status = job ? String(job.status || "queued") : "queued";
      var jobID = job ? String(job.id || "") : "";
      var text = fallbackText || modelOperationText(job);
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
        setTimeout(function() { window.location.reload(); }, 900);
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
          setTimeout(function() { window.location.reload(); }, 900);
          return;
        }
        if (attempt < 240) {
          setTimeout(function() { pollModelLifecycleJob(modelID, jobID, attempt + 1); }, 1500);
        }
      }).catch(function(error) {
        renderModelOperation(modelID, null, "Could not read job: " + error.message);
      });
    }
    document.querySelectorAll(".model-install").forEach(function(button) {
      button.addEventListener("click", function() {
        var modelID = button.dataset.modelId;
        if (!modelID) return;
        button.disabled = true;
        setButtonText(button, "Installing...");
        fetch("/v1/models/" + encodeURIComponent(modelID) + "/install", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: "{}"
        }).then(function(response) {
          if (!response.ok) {
            return response.text().then(function(text) { throw new Error(text || response.statusText); });
          }
          return response.json();
        }).then(function(job) {
          var statusElement = document.getElementById("model-status");
          if (statusElement) statusElement.innerText = "Install job " + job.id + " submitted.";
          renderModelOperation(modelID, job, "Install submitted.");
          pollModelLifecycleJob(modelID, job.id, 0);
        }).catch(function(error) {
          button.disabled = false;
          setButtonText(button, "Install");
          alert("Install failed: " + error.message);
        });
      });
    });
    document.querySelectorAll(".model-delete").forEach(function(button) {
      button.addEventListener("click", function() {
        var modelID = button.dataset.modelId;
        var nodeID = button.dataset.nodeId;
        if (!modelID || !nodeID) return;
        button.disabled = true;
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
          pollModelLifecycleJob(modelID, job.id, 0);
        }).catch(function(error) {
          button.disabled = false;
          setButtonText(button, "Delete");
          alert("Delete failed: " + error.message);
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
    var memoryModelSelect = document.getElementById("memory-model-select");
    var debugModelSelect = document.getElementById("debug-model-select");
    var chatSystemPrompt = document.getElementById("chat-system-prompt");
    var chatTemperature = document.getElementById("chat-temperature");
    var chatMaxTokens = document.getElementById("chat-max-tokens");
    var chatConversationKey = "cmesh.chat.conversation";
    var chatConversationID = "";
    try {
      chatConversationID = window.localStorage.getItem(chatConversationKey) || "";
    } catch (error) {}
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
        return;
      }
      var params = new URLSearchParams();
      params.set("model_id", modelID);
      if (chatSystemPrompt && chatSystemPrompt.value) params.set("system_prompt", chatSystemPrompt.value);
      fetch("/v1/memories/preview?" + params.toString()).then(function(response) {
        if (!response.ok) {
          return response.text().then(function(text) { throw new Error(text || response.statusText); });
        }
        return response.json();
      }).then(function(payload) {
        memoryPreviewText.textContent = payload.effective_system_prompt || "No effective prompt context.";
      }).catch(function(error) {
        memoryPreviewText.textContent = "Preview failed: " + error.message;
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
      if (chatModel && conversation.model_id) chatModel.value = conversation.model_id;
      syncChatNodes();
      if (chatNode && conversation.node_id) chatNode.value = conversation.node_id;
      loadModelMemory();
      if (modelStatus) modelStatus.innerText = "Loaded conversation " + conversation.id + ".";
    }
    function loadConversation(id) {
      if (!id) return;
      fetch("/v1/conversations/" + encodeURIComponent(id)).then(function(response) {
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
          appendChatMessage("assistant", modelJobText(job));
          return;
        }
        if (job.status === "failed" || job.status === "canceled") {
          if (modelStatus) modelStatus.innerText = "Model job " + job.status + ".";
          appendChatMessage("system", modelJobText(job));
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
      });
    }
    if (chatModel) {
      chatModel.addEventListener("change", function() {
        syncChatNodes();
        syncAuxModelSelects(currentChatModelID());
        loadModelMemory();
        updateMemoryPreview();
      });
      syncChatNodes();
      syncAuxModelSelects(currentChatModelID());
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
      conversationList.querySelectorAll(".conversation-item").forEach(function(button) {
        button.addEventListener("click", function() {
          loadConversation(button.dataset.conversationId || "");
        });
      });
      conversationList.querySelectorAll(".conversation-delete").forEach(function(button) {
        button.addEventListener("click", function() {
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
        });
      });
    }
    if (newChatButton) {
      newChatButton.addEventListener("click", function() {
        setActiveConversation("");
        resetChatThread();
        if (chatSystemPrompt) chatSystemPrompt.value = "";
        loadModelMemory();
        if (modelStatus) modelStatus.innerText = "New chat ready.";
        activateTab("chat", true);
      });
    }
    if (chatSystemPrompt) {
      chatSystemPrompt.addEventListener("input", updateMemoryPreview);
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
        appendChatMessage("user", prompt);
        chatForm.elements.prompt.value = "";
        modelStatus.innerText = "Submitting model job...";
        var maxTokens = parseInt(chatMaxTokens && chatMaxTokens.value ? chatMaxTokens.value : "512", 10);
        if (!Number.isFinite(maxTokens) || maxTokens < 16) maxTokens = 512;
        if (maxTokens > 2048) maxTokens = 2048;
        fetch("/v1/models/" + encodeURIComponent(modelID) + "/generate", {
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
          modelStatus.innerText = "Generate job " + job.id + " submitted.";
          pollModelJob(job.id, 0);
        }).catch(function(error) {
          modelStatus.innerText = "Generate failed: " + error.message;
          appendChatMessage("system", error.message);
        });
      });
    }
    if (document.body.dataset.activeJobs === "true" && window.location.hash !== "#chat") {
      setTimeout(function() { window.location.reload(); }, 5000);
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
