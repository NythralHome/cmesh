package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"time"

	"github.com/cmesh/cmesh/internal/membership"
)

type Server struct {
	addr   string
	state  *State
	mux    *http.ServeMux
	server *http.Server
}

func NewServer(addr string, state *State) *Server {
	mux := http.NewServeMux()
	s := &Server{
		addr:  addr,
		state: state,
		mux:   mux,
	}

	mux.HandleFunc("/", s.handleDashboard)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/v1/cluster", s.handleCluster)
	mux.HandleFunc("/v1/nodes", s.handleNodes)
	mux.HandleFunc("/v1/workers/join", s.handleWorkerJoin)
	mux.HandleFunc("/v1/workers/heartbeat", s.handleWorkerHeartbeat)

	s.server = &http.Server{
		Addr:              addr,
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

	data := struct {
		Summary ClusterSummary
		Nodes   any
	}{
		Summary: s.state.ClusterSummary(),
		Nodes:   s.state.Nodes(),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := dashboardTemplate.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
		"role":   "manager",
		"mode":   "single-node-bootstrap",
	})
}

func (s *Server) handleCluster(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.state.ClusterSummary())
}

func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"nodes": s.state.Nodes(),
	})
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

var dashboardTemplate = template.Must(template.New("dashboard").Funcs(template.FuncMap{
	"gb": func(bytes uint64) float64 {
		return float64(bytes) / 1024 / 1024 / 1024
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
    code {
      font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
      font-size: 13px;
    }
    @media (max-width: 640px) {
      header, main { padding-left: 18px; padding-right: 18px; }
      table { display: block; overflow-x: auto; }
    }
  </style>
</head>
<body>
  <header>
    <h1>CMesh</h1>
    <p class="sub">Decentralized-ready AI compute cluster manager</p>
  </header>
  <main>
    <div class="grid">
      <div class="metric"><span>Workers online</span><strong>{{.Summary.WorkersOnline}} / {{.Summary.WorkersTotal}}</strong></div>
      <div class="metric"><span>Allowed CPU cores</span><strong>{{.Summary.Resources.CPU.CoresAllowed}}</strong></div>
      <div class="metric"><span>Allowed memory</span><strong>{{printf "%.1f" (gb .Summary.Resources.Memory.AllowedBytes)}} GB</strong></div>
      <div class="metric"><span>GPUs</span><strong>{{.Summary.GPUs}}</strong></div>
      <div class="metric"><span>Allowed VRAM</span><strong>{{printf "%.1f" (gb .Summary.VRAMAllowedBytes)}} GB</strong></div>
      <div class="metric"><span>Allowed storage</span><strong>{{printf "%.1f" (gb .Summary.Resources.Storage.AllowedBytes)}} GB</strong></div>
    </div>
    <section>
      <div class="section-head">
        <h2>Worker Nodes</h2>
        <code>POST /v1/workers/join</code>
      </div>
      {{if .Nodes}}
      <table>
        <thead>
          <tr>
            <th>Name</th>
            <th>Status</th>
            <th>CPU</th>
            <th>Memory</th>
            <th>Storage</th>
            <th>GPU</th>
          </tr>
        </thead>
        <tbody>
        {{range .Nodes}}
          <tr>
            <td><code>{{.Name}}</code><br><span class="sub">{{.ID}}</span></td>
            <td><span class="pill">{{.Status}}</span></td>
            <td>{{.Resources.CPU.CoresAllowed}} / {{.Resources.CPU.CoresTotal}} cores</td>
            <td>{{printf "%.1f" (gb .Resources.Memory.AllowedBytes)}} / {{printf "%.1f" (gb .Resources.Memory.TotalBytes)}} GB</td>
            <td>{{printf "%.1f" (gb .Resources.Storage.AllowedBytes)}} GB allowed</td>
            <td>{{len .Resources.GPU}}</td>
          </tr>
        {{end}}
        </tbody>
      </table>
      {{else}}
      <div class="empty">No workers have joined this cluster yet.</div>
      {{end}}
    </section>
  </main>
</body>
</html>`))
