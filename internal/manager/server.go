package manager

import (
	"context"
	"encoding/json"
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

	data := struct {
		Summary            ClusterSummary
		OnlineNodes        []cluster.Node
		OfflineWorkerCount int
		Benchmarks         map[string]NodeBenchmarkSummary
		ClusterBenchmarks  []ClusterBenchmarkSummary
		Jobs               []jobs.Job
		InviteURL          string
	}{
		Summary:            s.state.ClusterSummary(),
		OnlineNodes:        onlineWorkerNodes(s.state.Nodes()),
		OfflineWorkerCount: offlineWorkerCount(s.state.Nodes()),
		Benchmarks:         s.state.BenchmarkSummaryByNode(),
		ClusterBenchmarks:  clusterBenchmarkSummaries(s.state.Jobs(), 5),
		Jobs:               recentJobs(s.state.Jobs(), 12),
		InviteURL:          "/invite",
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := dashboardTemplate.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleInvite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
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
		DownloadURL:         releaseDownloadBase(version.Version) + "CMesh-Worker-macos-arm64.zip",
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
				Type:        "compute.matrix_multiply",
				Input:       string(input),
				RequestedBy: requestedBy,
				AssignedTo:  node.ID,
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

var dashboardTemplate = template.Must(template.New("dashboard").Funcs(template.FuncMap{
	"gb": func(bytes uint64) float64 {
		return float64(bytes) / 1024 / 1024 / 1024
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
	"hasActiveJobs": hasActiveJobs,
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
  <meta http-equiv="refresh" content="5">
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
      max-width: 1180px;
    }
    .grid {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
      gap: 12px;
      margin-bottom: 24px;
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
      margin-top: 18px;
      display: flex;
      gap: 10px;
      flex-wrap: wrap;
    }
    .button {
      display: inline-flex;
      align-items: center;
      justify-content: center;
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
    .job-runner,
    .cluster-runner {
      display: grid;
      grid-template-columns: repeat(4, minmax(0, 1fr));
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
    input {
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
    .primary {
      background: var(--accent);
      border-color: var(--accent);
      color: #ffffff;
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
    code {
      font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
      font-size: 13px;
    }
    @media (max-width: 640px) {
      header, main { padding-left: 18px; padding-right: 18px; }
      table { display: block; overflow-x: auto; }
      .job-runner, .cluster-runner { grid-template-columns: 1fr; }
    }
  </style>
</head>
<body data-active-jobs="{{hasActiveJobs .Jobs}}">
  <header>
    <h1>CMesh</h1>
    <p class="sub">Decentralized-ready AI compute cluster manager</p>
  </header>
  <main>
    <div class="actions">
      <a class="button" href="{{.InviteURL}}">Invite worker</a>
    </div>
    <div class="grid">
      <div class="metric"><span>Workers online</span><strong>{{.Summary.WorkersOnline}} / {{.Summary.WorkersTotal}}</strong></div>
      <div class="metric"><span>Allowed CPU cores</span><strong>{{.Summary.Resources.CPU.CoresAllowed}}</strong></div>
      <div class="metric"><span>Allowed memory</span><strong>{{printf "%.1f" (gb .Summary.Resources.Memory.AllowedBytes)}} GB</strong></div>
      <div class="metric"><span>GPUs</span><strong>{{.Summary.GPUs}}</strong></div>
      <div class="metric"><span>Allowed VRAM</span><strong>{{printf "%.1f" (gb .Summary.VRAMAllowedBytes)}} GB</strong></div>
      <div class="metric"><span>Allowed storage</span><strong>{{printf "%.1f" (gb .Summary.Resources.Storage.AllowedBytes)}} GB</strong></div>
      <div class="metric"><span>Benchmark score</span><strong>{{printf "%.0f" .Summary.BenchmarkScore}}</strong></div>
    </div>
    <section>
      <div class="section-head">
        <h2>Online Workers</h2>
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
              <th>GPU</th>
              <th>Benchmark</th>
              <th>Last seen</th>
            </tr>
          </thead>
          <tbody>
          {{range .OnlineNodes}}
            <tr>
              <td><code>{{.Name}}</code><br><span class="sub">{{.ID}}</span></td>
              <td><span class="pill">{{.Status}}</span></td>
              <td>{{.Resources.CPU.CoresAllowed}} / {{.Resources.CPU.CoresTotal}} cores</td>
              <td>{{printf "%.1f" (gb .Resources.Memory.AllowedBytes)}} / {{printf "%.1f" (gb .Resources.Memory.TotalBytes)}} GB</td>
              <td>{{printf "%.1f" (gb .Resources.Storage.AllowedBytes)}} GB allowed</td>
              <td>{{range .Resources.GPU}}<div>{{.Name}}</div>{{else}}0{{end}}</td>
              <td>{{with index $.Benchmarks .ID}}{{printf "%.0f" .TotalScore}}{{else}}Not run{{end}}</td>
              <td>{{.UpdatedAt.Format "15:04:05 MST"}}</td>
            </tr>
          {{end}}
          </tbody>
        </table>
      </div>
      {{else}}
      <div class="empty">No workers are online right now.</div>
      {{end}}
    </section>
    <section style="margin-top: 20px;">
      <div class="section-head">
        <h2>Cluster Benchmark</h2>
        <code>{{len .ClusterBenchmarks}} recent runs</code>
      </div>
      <form class="cluster-runner" id="cluster-benchmark-form">
        <div class="field">
          <label for="cluster-size">Matrix size</label>
          <input id="cluster-size" name="size" type="number" min="16" max="2048" step="16" value="512">
        </div>
        <div class="field">
          <label for="cluster-iterations">Iterations</label>
          <input id="cluster-iterations" name="iterations" type="number" min="1" max="100" step="1" value="6">
        </div>
        <div class="field">
          <label for="cluster-requested-by">Label</label>
          <input id="cluster-requested-by" name="requested_by" value="dashboard">
        </div>
        <div class="field">
          <label>&nbsp;</label>
          <button class="button primary" type="submit">Run cluster benchmark</button>
        </div>
      </form>
      <div class="runner-status" id="cluster-benchmark-status">Run one compute job on each online worker and aggregate total GFLOPS.</div>
      {{if .ClusterBenchmarks}}
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
            </tr>
          {{end}}
          </tbody>
        </table>
      </div>
      {{else}}
      <div class="empty">No cluster benchmark runs yet.</div>
      {{end}}
    </section>
    <section style="margin-top: 20px;">
      <div class="section-head">
        <h2>Jobs</h2>
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
          <label>&nbsp;</label>
          <button class="button primary" type="submit">Run compute job</button>
        </div>
      </form>
      <div class="runner-status" id="compute-job-status">Submit a benchmark-style compute job to the current online worker pool.</div>
      {{if .Jobs}}
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Job</th>
              <th>Status</th>
              <th>Type</th>
              <th>Assigned</th>
              <th>Updated</th>
              <th>Result</th>
              <th>Output</th>
            </tr>
          </thead>
          <tbody>
          {{range .Jobs}}
            <tr>
              <td><code>{{shortID .ID}}</code></td>
              <td><span class="{{jobPillClass .Status}}">{{.Status}}</span></td>
              <td><code>{{.Type}}</code></td>
              <td>{{if .AssignedTo}}<code>{{shortID .AssignedTo}}</code>{{else}}-{{end}}</td>
              <td>{{.UpdatedAt.Format "15:04:05 MST"}}</td>
              <td>
                <div class="result-grid">
                  <div><span>Duration</span><strong>{{jobMetric . "duration_ms"}} ms</strong></div>
                  <div><span>GFLOPS</span><strong>{{jobMetric . "gflops"}}</strong></div>
                  <div><span>Runtime</span><strong>{{jobMetric . "worker_runtime"}}</strong></div>
                </div>
              </td>
              <td class="mono-output"><code>{{clip (jobOutput .) 160}}</code></td>
            </tr>
          {{end}}
          </tbody>
        </table>
      </div>
      {{else}}
      <div class="empty">No jobs have been submitted yet.</div>
      {{end}}
    </section>
  </main>
  <script>
    var form = document.getElementById("compute-job-form");
    var status = document.getElementById("compute-job-status");
    if (form) {
      form.addEventListener("submit", function(event) {
        event.preventDefault();
        var size = parseInt(form.elements.size.value, 10);
        var iterations = parseInt(form.elements.iterations.value, 10);
        var requestedBy = String(form.elements.requested_by.value || "dashboard").trim();
        if (!Number.isFinite(size) || size < 16 || !Number.isFinite(iterations) || iterations < 1) {
          status.innerText = "Use a valid matrix size and iteration count.";
          return;
        }
        status.innerText = "Submitting compute job...";
        fetch("/v1/jobs", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            type: "compute.matrix_multiply",
            input: JSON.stringify({ size: size, iterations: iterations }),
            requested_by: requestedBy
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
    var clusterForm = document.getElementById("cluster-benchmark-form");
    var clusterStatus = document.getElementById("cluster-benchmark-status");
    if (clusterForm) {
      clusterForm.addEventListener("submit", function(event) {
        event.preventDefault();
        var size = parseInt(clusterForm.elements.size.value, 10);
        var iterations = parseInt(clusterForm.elements.iterations.value, 10);
        var requestedBy = String(clusterForm.elements.requested_by.value || "dashboard").trim();
        if (!Number.isFinite(size) || size < 16 || !Number.isFinite(iterations) || iterations < 1) {
          clusterStatus.innerText = "Use a valid matrix size and iteration count.";
          return;
        }
        clusterStatus.innerText = "Starting cluster benchmark...";
        fetch("/v1/cluster-benchmarks", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            size: size,
            iterations: iterations,
            requested_by: requestedBy
          })
        }).then(function(response) {
          if (!response.ok) {
            return response.text().then(function(text) { throw new Error(text || response.statusText); });
          }
          return response.json();
        }).then(function(run) {
          clusterStatus.innerText = "Started " + run.id + " on " + run.workers + " workers. Refreshing results...";
          setTimeout(function() { window.location.reload(); }, 1200);
        }).catch(function(error) {
          clusterStatus.innerText = "Cluster benchmark failed: " + error.message;
        });
      });
    }
    if (document.body.dataset.activeJobs === "true") {
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
    }
    main {
      padding: 24px 32px 40px;
      max-width: 980px;
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
    .hint {
      margin: 10px 16px 0;
      color: var(--muted);
      font-size: 13px;
    }
    @media (max-width: 640px) {
      header, main { padding-left: 18px; padding-right: 18px; }
    }
  </style>
</head>
<body>
  <header>
    <h1>Invite Worker</h1>
    <p class="sub">Copy one command and run it on the machine that should join this cluster. Resource limits can be edited before running.</p>
  </header>
  <main>
    <div class="toolbar">
      <a class="button" href="/">Dashboard</a>
    </div>

    <section>
      <div class="section-head">
        <h2>Worker desktop app</h2>
        <button type="button" data-copy="desktop-invite">Copy invite link</button>
      </div>
      <p class="sub">Install the worker app, then open this invite link on the worker machine.</p>
      <pre><code id="desktop-invite">{{.DesktopInviteURL}}</code></pre>
      <div class="actions">
        <a class="button primary" href="{{.DesktopInviteHref}}">Open Worker App</a>
        <a class="button" id="worker-download" href="{{.DownloadURL}}">Download Worker App</a>
        <a class="button" href="https://github.com/NythralHome/cmesh/releases/latest">Other platforms</a>
      </div>
      <p class="hint" id="worker-download-hint">Direct downloads use the latest CMesh release.</p>
    </section>

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

    <section>
      <div class="section-head">
        <h2>Worker control</h2>
        <button type="button" data-copy="worker-control">Copy</button>
      </div>
      <pre><code id="worker-control">curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | sh -s -- status
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | sudo sh -s -- stop
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | sudo sh -s -- start
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | sudo sh -s -- uninstall</code></pre>
    </section>

    <section>
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
          label: "Download for macOS Apple Silicon",
          asset: "CMesh-Worker-macos-arm64.zip",
          hint: "Best for M1, M2, M3, and newer Apple Silicon Macs."
        },
        macIntel: {
          label: "Download for macOS Intel",
          asset: "CMesh-Worker-macos-amd64.zip",
          hint: "Best for older Intel-based Macs."
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
