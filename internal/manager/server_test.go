package manager

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cmesh/cmesh/internal/cluster"
	"github.com/cmesh/cmesh/internal/jobs"
	"github.com/cmesh/cmesh/internal/membership"
	"github.com/cmesh/cmesh/internal/resources"
)

func TestHealth(t *testing.T) {
	srv := NewServer(":0", NewState())
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
}

func TestWorkerJoinAndClusterSummary(t *testing.T) {
	srv := NewServer(":0", NewState())

	joinReq := membership.JoinRequest{
		NodeName: "worker-a",
		Role:     cluster.NodeRoleWorker,
		Resources: cluster.ResourceSnapshot{
			CPU: cluster.CPUResources{
				CoresTotal:   10,
				CoresAllowed: 4,
			},
			Memory: cluster.MemoryResources{
				TotalBytes:   32 * gb,
				AllowedBytes: 8 * gb,
			},
			Storage: cluster.StorageResources{
				TotalBytes:   500 * gb,
				AllowedBytes: 50 * gb,
				FreeBytes:    400 * gb,
			},
			GPU: []cluster.GPUResources{
				{
					Name:             "Test GPU",
					Vendor:           "test",
					TotalVRAMBytes:   12 * gb,
					AllowedVRAMBytes: 6 * gb,
				},
			},
		},
	}
	body, err := json.Marshal(joinReq)
	if err != nil {
		t.Fatal(err)
	}

	joinHTTPReq := httptest.NewRequest(http.MethodPost, "/v1/workers/join", bytes.NewReader(body))
	joinHTTPReq.Header.Set("Content-Type", "application/json")
	joinRec := httptest.NewRecorder()
	srv.ServeHTTP(joinRec, joinHTTPReq)

	if joinRec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", joinRec.Code, joinRec.Body.String())
	}

	clusterReq := httptest.NewRequest(http.MethodGet, "/v1/cluster", nil)
	clusterRec := httptest.NewRecorder()
	srv.ServeHTTP(clusterRec, clusterReq)

	if clusterRec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", clusterRec.Code)
	}

	var summary ClusterSummary
	if err := json.NewDecoder(clusterRec.Body).Decode(&summary); err != nil {
		t.Fatal(err)
	}

	if summary.WorkersOnline != 1 {
		t.Fatalf("expected 1 online worker, got %d", summary.WorkersOnline)
	}
	if summary.Resources.CPU.CoresAllowed != 4 {
		t.Fatalf("expected 4 allowed CPU cores, got %d", summary.Resources.CPU.CoresAllowed)
	}
	if summary.VRAMAllowedBytes != 6*gb {
		t.Fatalf("expected 6 GB allowed VRAM, got %d", summary.VRAMAllowedBytes)
	}
}

func TestWorkerJoinRequiresTokenWhenConfigured(t *testing.T) {
	srv := NewServerWithOptions(ServerOptions{
		Addr:      ":0",
		JoinToken: "secret",
	}, NewState())

	joinReq := membership.JoinRequest{
		NodeName:  "worker-token",
		Role:      cluster.NodeRoleWorker,
		JoinToken: "wrong",
	}
	body, err := json.Marshal(joinReq)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/workers/join", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rec.Code)
	}

	joinReq.JoinToken = "secret"
	body, err = json.Marshal(joinReq)
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/workers/join", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestWorkerHeartbeatUpdatesNodeAndOfflineStatus(t *testing.T) {
	state := NewState()
	state.heartbeatTimeout = time.Nanosecond
	srv := NewServer(":0", state)

	joinReq := membership.JoinRequest{
		NodeName: "worker-heartbeat",
		Role:     cluster.NodeRoleWorker,
		Resources: cluster.ResourceSnapshot{
			CPU: cluster.CPUResources{
				CoresTotal:   8,
				CoresAllowed: 2,
			},
		},
	}
	body, err := json.Marshal(joinReq)
	if err != nil {
		t.Fatal(err)
	}

	joinHTTPReq := httptest.NewRequest(http.MethodPost, "/v1/workers/join", bytes.NewReader(body))
	joinRec := httptest.NewRecorder()
	srv.ServeHTTP(joinRec, joinHTTPReq)
	if joinRec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d", joinRec.Code)
	}

	var joinResp membership.JoinResponse
	if err := json.NewDecoder(joinRec.Body).Decode(&joinResp); err != nil {
		t.Fatal(err)
	}

	time.Sleep(time.Millisecond)
	nodes := state.Nodes()
	if nodes[0].Status != cluster.NodeStatusOffline {
		t.Fatalf("expected stale worker to be offline, got %s", nodes[0].Status)
	}

	hb := membership.Heartbeat{
		NodeID: joinResp.NodeID,
		Resources: cluster.ResourceSnapshot{
			CPU: cluster.CPUResources{
				CoresTotal:   8,
				CoresAllowed: 4,
			},
		},
	}
	hbBody, err := json.Marshal(hb)
	if err != nil {
		t.Fatal(err)
	}

	hbReq := httptest.NewRequest(http.MethodPost, "/v1/workers/heartbeat", bytes.NewReader(hbBody))
	hbRec := httptest.NewRecorder()
	srv.ServeHTTP(hbRec, hbReq)
	if hbRec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", hbRec.Code)
	}

	state.heartbeatTimeout = time.Minute
	nodes = state.Nodes()
	if nodes[0].Status != cluster.NodeStatusOnline {
		t.Fatalf("expected fresh worker to be online, got %s", nodes[0].Status)
	}
	if nodes[0].Resources.CPU.CoresAllowed != 4 {
		t.Fatalf("expected heartbeat resource update")
	}
}

func TestBenchmarkSubmissionUpdatesClusterScore(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)

	joinReq := membership.JoinRequest{
		NodeName: "worker-benchmark",
		Role:     cluster.NodeRoleWorker,
	}
	body, err := json.Marshal(joinReq)
	if err != nil {
		t.Fatal(err)
	}

	joinHTTPReq := httptest.NewRequest(http.MethodPost, "/v1/workers/join", bytes.NewReader(body))
	joinRec := httptest.NewRecorder()
	srv.ServeHTTP(joinRec, joinHTTPReq)
	if joinRec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d", joinRec.Code)
	}

	var joinResp membership.JoinResponse
	if err := json.NewDecoder(joinRec.Body).Decode(&joinResp); err != nil {
		t.Fatal(err)
	}

	benchmark := resources.BenchmarkResult{
		NodeID: joinResp.NodeID,
		Kind:   resources.BenchmarkCPU,
		Score:  42,
		Unit:   "score",
	}
	benchmarkBody, err := json.Marshal(benchmark)
	if err != nil {
		t.Fatal(err)
	}

	benchmarkReq := httptest.NewRequest(http.MethodPost, "/v1/benchmarks", bytes.NewReader(benchmarkBody))
	benchmarkRec := httptest.NewRecorder()
	srv.ServeHTTP(benchmarkRec, benchmarkReq)
	if benchmarkRec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", benchmarkRec.Code, benchmarkRec.Body.String())
	}

	summary := state.ClusterSummary()
	if summary.BenchmarkScore != 42 {
		t.Fatalf("expected benchmark score 42, got %f", summary.BenchmarkScore)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/v1/benchmarks", nil)
	listRec := httptest.NewRecorder()
	srv.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", listRec.Code)
	}
}

func TestJobLifecycle(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)

	worker := joinWorkerForTest(t, srv, "job-worker")
	state.PutBenchmark(resources.BenchmarkResult{
		NodeID: worker.NodeID,
		Kind:   resources.BenchmarkCPU,
		Score:  10,
		Unit:   "score",
	})

	createReq := jobs.CreateRequest{
		Type:  "echo",
		Input: "hello cluster",
	}
	body, err := json.Marshal(createReq)
	if err != nil {
		t.Fatal(err)
	}

	createHTTPReq := httptest.NewRequest(http.MethodPost, "/v1/jobs", bytes.NewReader(body))
	createRec := httptest.NewRecorder()
	srv.ServeHTTP(createRec, createHTTPReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", createRec.Code, createRec.Body.String())
	}

	var created jobs.Job
	if err := json.NewDecoder(createRec.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Status != jobs.StatusScheduled {
		t.Fatalf("expected scheduled job, got %s", created.Status)
	}
	if created.AssignedTo != worker.NodeID {
		t.Fatalf("expected job assigned to %s, got %s", worker.NodeID, created.AssignedTo)
	}

	nextReq := httptest.NewRequest(http.MethodGet, "/v1/workers/"+worker.NodeID+"/jobs/next", nil)
	nextRec := httptest.NewRecorder()
	srv.ServeHTTP(nextRec, nextReq)
	if nextRec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", nextRec.Code)
	}

	var nextResp struct {
		Job *jobs.Job `json:"job"`
	}
	if err := json.NewDecoder(nextRec.Body).Decode(&nextResp); err != nil {
		t.Fatal(err)
	}
	if nextResp.Job == nil || nextResp.Job.Status != jobs.StatusRunning {
		t.Fatalf("expected running next job, got %#v", nextResp.Job)
	}

	completeReq := jobs.CompleteRequest{
		NodeID: worker.NodeID,
		Result: "hello cluster",
	}
	completeBody, err := json.Marshal(completeReq)
	if err != nil {
		t.Fatal(err)
	}
	completeHTTPReq := httptest.NewRequest(http.MethodPost, "/v1/jobs/"+created.ID+"/complete", bytes.NewReader(completeBody))
	completeRec := httptest.NewRecorder()
	srv.ServeHTTP(completeRec, completeHTTPReq)
	if completeRec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", completeRec.Code, completeRec.Body.String())
	}

	var completed jobs.Job
	if err := json.NewDecoder(completeRec.Body).Decode(&completed); err != nil {
		t.Fatal(err)
	}
	if completed.Status != jobs.StatusSucceeded {
		t.Fatalf("expected succeeded job, got %s", completed.Status)
	}
	if completed.Result != "hello cluster" {
		t.Fatalf("unexpected result %q", completed.Result)
	}
}

func joinWorkerForTest(t *testing.T, srv *Server, name string) membership.JoinResponse {
	t.Helper()

	joinReq := membership.JoinRequest{
		NodeName: name,
		Role:     cluster.NodeRoleWorker,
	}
	body, err := json.Marshal(joinReq)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/workers/join", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp membership.JoinResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

const gb = 1024 * 1024 * 1024
