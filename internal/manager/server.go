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

	nodes := s.state.Nodes()
	allJobs := s.state.Jobs()
	clusterBenchmarks := clusterBenchmarkSummaries(allJobs, 5)
	data := struct {
		Summary            ClusterSummary
		OnlineNodes        []cluster.Node
		OfflineWorkerCount int
		Benchmarks         map[string]NodeBenchmarkSummary
		ClusterBenchmarks  []ClusterBenchmarkSummary
		NodesByID          map[string]cluster.Node
		WorkerActiveJobs   map[string]int
		MaxClusterGFLOPS   float64
		Jobs               []jobs.Job
		InviteURL          string
	}{
		Summary:            s.state.ClusterSummary(),
		OnlineNodes:        onlineWorkerNodes(nodes),
		OfflineWorkerCount: offlineWorkerCount(nodes),
		Benchmarks:         s.state.BenchmarkSummaryByNode(),
		ClusterBenchmarks:  clusterBenchmarks,
		NodesByID:          nodesByID(nodes),
		WorkerActiveJobs:   activeJobsByWorker(allJobs),
		MaxClusterGFLOPS:   maxClusterBenchmarkGFLOPS(clusterBenchmarks),
		Jobs:               recentJobs(allJobs, 12),
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

func jobWorkload(job jobs.Job) string {
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
	"jobDetail": func(job jobs.Job) string {
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
		if runtimeValue, ok := result["worker_runtime"].(string); ok && strings.TrimSpace(runtimeValue) != "" {
			return "Completed on " + runtimeValue
		}
		return "Completed."
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
	"hasActiveJobs":   hasActiveJobs,
	"workerSlots":     workerJobSlots,
	"jobCanCancel":    jobCanBeCanceled,
	"jobDuration":     jobDuration,
	"jobTimeline":     jobTimeline,
	"jobWorkload":     jobWorkload,
	"jobRequirements": jobRequirements,
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
    select {
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
      .step { grid-template-columns: 30px 1fr; }
      .step .pill, .step .pill-job, .step .pill-muted { grid-column: 2; width: fit-content; }
      .first-test-form { grid-template-columns: 1fr; }
      .first-test-form .wide { grid-column: auto; }
      .job-runner, .cluster-runner { grid-template-columns: 1fr; }
      .growth-row { grid-template-columns: 1fr; }
      .worker-result { grid-template-columns: 1fr 1fr; }
    }
  </style>
</head>
<body data-active-jobs="{{hasActiveJobs .Jobs}}">
  <header>
    <div>
      <h1>CMesh</h1>
      <p class="sub">Decentralized-ready AI compute cluster manager</p>
    </div>
    <div class="actions">
      <a class="button" href="{{.InviteURL}}">Invite worker</a>
    </div>
  </header>
  <main>
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
            <a class="button primary" href="{{.InviteURL}}">Invite worker</a>
            <a class="button" href="#jobs">Open jobs</a>
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
            <button class="button primary wide" type="submit" {{if not .OnlineNodes}}disabled{{end}}>Run cluster test</button>
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
              <th>Job slots</th>
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
              <td>{{index $.WorkerActiveJobs .ID}} / {{workerSlots .}} active</td>
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
    <section id="jobs" style="margin-top: 20px;">
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
          <button class="button primary" type="submit" {{if not .OnlineNodes}}disabled{{end}}>Run compute job</button>
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
                  <button class="button danger" type="submit">Cancel</button>
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
          button.innerText = "Canceling...";
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
            button.innerText = "Cancel";
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
          <div class="step-card"><strong>2. Open invite</strong><span>The installed app receives the manager URL and join token automatically.</span></div>
          <div class="step-card"><strong>3. Start worker</strong><span>Choose resource limits, save settings, then connect to the cluster.</span></div>
        </div>
      </div>

      <section class="desktop-card">
        <div class="section-head">
          <h2>Install worker app</h2>
          <code>recommended</code>
        </div>
        <div class="desktop-primary">
          <h3>Use the installer first</h3>
          <p>Install CMesh Worker on this machine, then open the invite so the app can prefill the manager URL and join token.</p>
          <div class="actions">
            <a class="button primary" id="worker-download" href="{{.DownloadURL}}">Download Worker App</a>
            <a class="button secondary" href="{{.DesktopInviteHref}}">Open installed app</a>
            <a class="button" href="https://github.com/NythralHome/cmesh/releases/latest">Other platforms</a>
          </div>
          <p class="hint" id="worker-download-hint">Direct downloads use the latest CMesh release.</p>
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
          hint: "Install CMesh Worker, then open the invite to prefill this cluster."
        },
        macIntel: {
          label: "Download for Intel Mac",
          asset: "CMesh-Worker-Intel-Mac.dmg",
          hint: "Install CMesh Worker, then open the invite to prefill this cluster."
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
