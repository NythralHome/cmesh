package manager

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestInvitePageRequiresOperatorToken(t *testing.T) {
	srv := NewServerWithOptions(ServerOptions{
		Addr:          ":0",
		JoinToken:     "join-secret",
		OperatorToken: "operator-secret",
		PublicURL:     "https://cmesh.example.com",
	}, NewState())

	req := httptest.NewRequest(http.MethodGet, "/invite", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/invite?token=operator-secret", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "https://cmesh.example.com") {
		t.Fatalf("expected invite page to contain public URL")
	}
	if !strings.Contains(body, "join-secret") {
		t.Fatalf("expected invite page to contain join token")
	}
	if !strings.Contains(body, "cmesh://join?") {
		t.Fatalf("expected invite page to contain desktop invite link")
	}
	if !strings.Contains(body, "manager=https%3A%2F%2Fcmesh.example.com") {
		t.Fatalf("expected invite page to contain encoded manager URL")
	}
	if strings.Contains(body, "#ZgotmplZ") {
		t.Fatalf("expected invite page not to contain sanitized unsafe URL marker")
	}
}

func TestDesktopInviteURL(t *testing.T) {
	got := desktopInviteURL("https://cmesh.example.com", "join secret")
	if !strings.HasPrefix(got, "cmesh://join?") {
		t.Fatalf("expected cmesh scheme, got %q", got)
	}
	if !strings.Contains(got, "manager=https%3A%2F%2Fcmesh.example.com") {
		t.Fatalf("expected encoded manager URL, got %q", got)
	}
	if !strings.Contains(got, "token=join+secret") {
		t.Fatalf("expected encoded token, got %q", got)
	}
}

func TestDashboardRequiresOperatorTokenWhenConfigured(t *testing.T) {
	srv := NewServerWithOptions(ServerOptions{
		Addr:          ":0",
		OperatorToken: "operator-secret",
	}, NewState())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "CMesh Operator") {
		t.Fatalf("expected operator login page")
	}

	req = httptest.NewRequest(http.MethodGet, "/?token=operator-secret", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if len(rec.Result().Cookies()) == 0 {
		t.Fatalf("expected operator auth cookie")
	}
}

func TestReadAPIRequiresOperatorTokenWhenConfigured(t *testing.T) {
	srv := NewServerWithOptions(ServerOptions{
		Addr:          ":0",
		OperatorToken: "operator-secret",
	}, NewState())

	req := httptest.NewRequest(http.MethodGet, "/v1/cluster", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/cluster", nil)
	req.Header.Set("X-CMesh-Operator-Token", "operator-secret")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
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

func TestWorkerLeaveMarksNodeOfflineAndRemovesActiveCapacity(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)

	joinReq := membership.JoinRequest{
		NodeName: "worker-leave",
		Role:     cluster.NodeRoleWorker,
		Resources: cluster.ResourceSnapshot{
			CPU: cluster.CPUResources{
				CoresTotal:   8,
				CoresAllowed: 4,
			},
			Memory: cluster.MemoryResources{
				TotalBytes:   16 * gb,
				AllowedBytes: 8 * gb,
			},
		},
	}
	body, err := json.Marshal(joinReq)
	if err != nil {
		t.Fatal(err)
	}
	joinReqHTTP := httptest.NewRequest(http.MethodPost, "/v1/workers/join", bytes.NewReader(body))
	joinRec := httptest.NewRecorder()
	srv.ServeHTTP(joinRec, joinReqHTTP)
	if joinRec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d", joinRec.Code)
	}

	var joinResp membership.JoinResponse
	if err := json.NewDecoder(joinRec.Body).Decode(&joinResp); err != nil {
		t.Fatal(err)
	}
	if summary := state.ClusterSummary(); summary.Resources.CPU.CoresAllowed != 4 {
		t.Fatalf("expected online worker capacity before leave, got %d", summary.Resources.CPU.CoresAllowed)
	}

	leaveBody, err := json.Marshal(membership.LeaveRequest{NodeID: joinResp.NodeID})
	if err != nil {
		t.Fatal(err)
	}
	leaveReq := httptest.NewRequest(http.MethodPost, "/v1/workers/leave", bytes.NewReader(leaveBody))
	leaveRec := httptest.NewRecorder()
	srv.ServeHTTP(leaveRec, leaveReq)
	if leaveRec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", leaveRec.Code, leaveRec.Body.String())
	}

	nodes := state.Nodes()
	if nodes[0].Status != cluster.NodeStatusOffline {
		t.Fatalf("expected worker to be offline, got %s", nodes[0].Status)
	}
	summary := state.ClusterSummary()
	if summary.WorkersOnline != 0 {
		t.Fatalf("expected zero online workers, got %d", summary.WorkersOnline)
	}
	if summary.Resources.CPU.CoresAllowed != 0 {
		t.Fatalf("expected offline worker capacity to be excluded, got %d", summary.Resources.CPU.CoresAllowed)
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
