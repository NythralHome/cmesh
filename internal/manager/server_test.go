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
	"github.com/cmesh/cmesh/internal/models"
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
	if !strings.Contains(body, "CMesh-Worker-Apple-Silicon.dmg") {
		t.Fatalf("expected invite page to contain Apple Silicon installer download")
	}
	if !strings.Contains(body, "Download for Apple Silicon") {
		t.Fatalf("expected invite page to contain platform-specific download label")
	}
	if !strings.Contains(body, "Install worker app") || !strings.Contains(body, "Manual invite link") {
		t.Fatalf("expected invite page to contain installer-first worker flow")
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

func TestReleaseDownloadBaseUsesVersionedReleaseForTags(t *testing.T) {
	got := releaseDownloadBase("v0.1.0-alpha.22")
	want := "https://github.com/NythralHome/cmesh/releases/download/v0.1.0-alpha.22/"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
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

func TestDashboardShowsOnlineWorkersAndJobs(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)

	offline := joinWorkerForTest(t, srv, "offline-worker")
	if !state.MarkWorkerOffline(offline.NodeID) {
		t.Fatal("expected offline worker to be marked offline")
	}
	online := joinWorkerForTest(t, srv, "online-worker")

	job, err := state.CreateJob(jobs.CreateRequest{
		Type:  "compute.matrix_multiply",
		Input: `{"size":32,"iterations":1}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.NextJobForWorker(online.NodeID); !ok {
		t.Fatal("expected job to start")
	}
	state.CompleteJob(job.ID, jobs.CompleteRequest{
		NodeID: online.NodeID,
		Result: `{"duration_ms":42,"gflops":1.23,"worker_runtime":"test/runtime"}`,
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "online-worker") {
		t.Fatalf("expected online worker in dashboard")
	}
	if strings.Contains(body, "offline-worker") {
		t.Fatalf("expected offline worker to be hidden from dashboard")
	}
	if !strings.Contains(body, "1 offline hidden") {
		t.Fatalf("expected offline worker count in dashboard")
	}
	if !strings.Contains(body, "compute.matrix_multiply") {
		t.Fatalf("expected job type in dashboard")
	}
	if !strings.Contains(body, "Worker") {
		t.Fatalf("expected worker column in jobs dashboard")
	}
	if !strings.Contains(body, "succeeded") {
		t.Fatalf("expected job status in dashboard")
	}
	if !strings.Contains(body, "Run compute job") {
		t.Fatalf("expected compute job runner in dashboard")
	}
	if !strings.Contains(body, "Benchmark History") {
		t.Fatalf("expected benchmark history in dashboard")
	}
	if !strings.Contains(body, "Cluster Console") {
		t.Fatalf("expected cluster console in dashboard")
	}
	if !strings.Contains(body, "Run cluster test") {
		t.Fatalf("expected cluster test action in dashboard")
	}
	if !strings.Contains(body, "1.23") || !strings.Contains(body, "test/runtime") {
		t.Fatalf("expected parsed compute result metrics in dashboard")
	}
	if !strings.Contains(body, "Completed on test/runtime") {
		t.Fatalf("expected human job detail in dashboard")
	}
	if !strings.Contains(body, "32x32 x 1") {
		t.Fatalf("expected parsed workload in dashboard")
	}
	if !strings.Contains(body, "duration") || !strings.Contains(body, "created") || !strings.Contains(body, "finished") {
		t.Fatalf("expected job timeline and duration in dashboard")
	}
}

func TestRecentChatJobsOnlyIncludesSuccessfulDashboardChat(t *testing.T) {
	now := time.Now()
	in := []jobs.Job{
		{
			ID:          "old-failed",
			Type:        models.JobGenerate,
			Status:      jobs.StatusFailed,
			RequestedBy: "dashboard-chat",
			Error:       "llama-cli is not available on PATH",
			UpdatedAt:   now.Add(2 * time.Minute),
		},
		{
			ID:          "external-success",
			Type:        models.JobGenerate,
			Status:      jobs.StatusSucceeded,
			RequestedBy: "api",
			Result:      `{"output":"external"}`,
			UpdatedAt:   now.Add(3 * time.Minute),
		},
		{
			ID:          "chat-success",
			Type:        models.JobGenerate,
			Status:      jobs.StatusSucceeded,
			RequestedBy: "dashboard-chat",
			Result:      `{"output":"hello"}`,
			UpdatedAt:   now,
		},
	}

	got := recentChatJobs(in, 6)
	if len(got) != 1 {
		t.Fatalf("expected 1 chat job, got %d", len(got))
	}
	if got[0].ID != "chat-success" {
		t.Fatalf("expected chat-success, got %q", got[0].ID)
	}
}

func TestConversationStoresAssistantMessageAfterGenerateCompletes(t *testing.T) {
	state := NewState()
	conversation := state.AppendConversationMessage("conv-test", "qwen2.5-0.5b-instruct-q4-k-m", "node-test", "system", models.ChatMessage{
		Role:    "user",
		Content: "Мене звати Сергій.",
	})
	if len(conversation.Messages) != 1 {
		t.Fatalf("expected one user message, got %d", len(conversation.Messages))
	}

	input, err := json.Marshal(models.GenerateInput{
		ModelID:        "qwen2.5-0.5b-instruct-q4-k-m",
		ConversationID: "conv-test",
		SystemPrompt:   "system",
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := state.CreateJob(jobs.CreateRequest{
		Type:        models.JobGenerate,
		Input:       string(input),
		RequestedBy: "dashboard-chat",
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	state.jobs[job.ID] = job
	state.jobs[job.ID] = jobs.Job{
		ID:          job.ID,
		Type:        job.Type,
		Status:      jobs.StatusRunning,
		Input:       job.Input,
		AssignedTo:  "node-test",
		RequestedBy: "dashboard-chat",
	}

	result, err := json.Marshal(map[string]string{"output": "Тебе звати Сергій."})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.CompleteJob(job.ID, jobs.CompleteRequest{NodeID: "node-test", Result: string(result)}); !ok {
		t.Fatal("expected job completion to succeed")
	}
	conversation, ok := state.Conversation("conv-test")
	if !ok {
		t.Fatal("expected conversation to exist")
	}
	if len(conversation.Messages) != 2 {
		t.Fatalf("expected user and assistant messages, got %#v", conversation.Messages)
	}
	if conversation.Messages[1].Role != "assistant" || conversation.Messages[1].Content != "Тебе звати Сергій." {
		t.Fatalf("expected assistant output in conversation, got %#v", conversation.Messages[1])
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

func TestOfflineWorkerFailsActiveJob(t *testing.T) {
	state := NewState()
	worker := joinWorkerForTest(t, NewServer(":0", state), "job-worker")

	job, err := state.CreateJob(jobs.CreateRequest{
		Type:        "echo",
		Input:       "hello cluster",
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.NextJobForWorker(worker.NodeID); !ok {
		t.Fatal("expected job to start")
	}

	if !state.MarkWorkerOffline(worker.NodeID) {
		t.Fatal("expected worker to be marked offline")
	}

	failed, ok := state.Job(job.ID)
	if !ok {
		t.Fatal("expected job")
	}
	if failed.Status != jobs.StatusFailed {
		t.Fatalf("expected failed job, got %s", failed.Status)
	}
	if failed.Error != "worker went offline" {
		t.Fatalf("unexpected error %q", failed.Error)
	}
	if failed.FinishedAt.IsZero() {
		t.Fatal("expected finished_at on failed job")
	}

	if completed, ok := state.CompleteJob(job.ID, jobs.CompleteRequest{
		NodeID: worker.NodeID,
		Result: "late result",
	}); ok || completed.Status == jobs.StatusSucceeded {
		t.Fatalf("late completion should not revive failed job: %#v", completed)
	}
}

func TestOfflineWorkerReschedulesActiveJob(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	workerA := joinWorkerForTest(t, srv, "job-worker-a")
	workerB := joinWorkerForTest(t, srv, "job-worker-b")

	job, err := state.CreateJob(jobs.CreateRequest{
		Type:        "echo",
		Input:       "hello cluster",
		AssignedTo:  workerA.NodeID,
		MaxAttempts: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.Attempts != 1 || job.MaxAttempts != 3 {
		t.Fatalf("expected first attempt metadata, got attempts=%d max=%d", job.Attempts, job.MaxAttempts)
	}
	if _, ok := state.NextJobForWorker(workerA.NodeID); !ok {
		t.Fatal("expected worker A job to start")
	}

	if !state.MarkWorkerOffline(workerA.NodeID) {
		t.Fatal("expected worker A to be marked offline")
	}

	rescheduled, ok := state.Job(job.ID)
	if !ok {
		t.Fatal("expected job")
	}
	if rescheduled.Status != jobs.StatusScheduled {
		t.Fatalf("expected rescheduled job, got %s", rescheduled.Status)
	}
	if rescheduled.AssignedTo != workerB.NodeID {
		t.Fatalf("expected job assigned to worker B, got %s", rescheduled.AssignedTo)
	}
	if rescheduled.Attempts != 2 {
		t.Fatalf("expected second attempt, got %d", rescheduled.Attempts)
	}
	if rescheduled.LastFailure != "worker went offline" {
		t.Fatalf("unexpected last failure %q", rescheduled.LastFailure)
	}

	if completed, ok := state.CompleteJob(job.ID, jobs.CompleteRequest{
		NodeID: workerA.NodeID,
		Result: "late result",
	}); ok || completed.Status == jobs.StatusSucceeded {
		t.Fatalf("old worker should not complete rescheduled job: %#v", completed)
	}

	if _, ok := state.NextJobForWorker(workerB.NodeID); !ok {
		t.Fatal("expected worker B job to start")
	}
	completed, ok := state.CompleteJob(job.ID, jobs.CompleteRequest{
		NodeID: workerB.NodeID,
		Result: "hello cluster",
	})
	if !ok {
		t.Fatal("expected worker B to complete job")
	}
	if completed.Status != jobs.StatusSucceeded || completed.Result != "hello cluster" {
		t.Fatalf("unexpected completed job: %#v", completed)
	}
}

func TestWorkerErrorReschedulesActiveJob(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	workerA := joinWorkerForTest(t, srv, "job-worker-a")
	workerB := joinWorkerForTest(t, srv, "job-worker-b")

	job, err := state.CreateJob(jobs.CreateRequest{
		Type:        "echo",
		Input:       "hello cluster",
		AssignedTo:  workerA.NodeID,
		MaxAttempts: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.NextJobForWorker(workerA.NodeID); !ok {
		t.Fatal("expected worker A job to start")
	}

	rescheduled, ok := state.CompleteJob(job.ID, jobs.CompleteRequest{
		NodeID: workerA.NodeID,
		Error:  "worker resource guard rejected job: requires 4 CPU cores, worker allows 2",
	})
	if !ok {
		t.Fatal("expected worker A completion to be accepted")
	}
	if rescheduled.Status != jobs.StatusScheduled {
		t.Fatalf("expected rescheduled job, got %s", rescheduled.Status)
	}
	if rescheduled.AssignedTo != workerB.NodeID {
		t.Fatalf("expected job assigned to worker B, got %s", rescheduled.AssignedTo)
	}
	if rescheduled.Attempts != 2 {
		t.Fatalf("expected second attempt, got %d", rescheduled.Attempts)
	}
	if rescheduled.LastFailure == "" || !strings.Contains(rescheduled.LastFailure, "worker resource guard rejected job") {
		t.Fatalf("expected worker error to be recorded, got %q", rescheduled.LastFailure)
	}
	if rescheduled.Error != "" || rescheduled.Result != "" || !rescheduled.StartedAt.IsZero() || !rescheduled.FinishedAt.IsZero() {
		t.Fatalf("expected clean rescheduled job, got %#v", rescheduled)
	}
}

func TestWorkerSlotQueuesUntilActiveJobCompletes(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	worker := joinWorkerForTest(t, srv, "slot-worker")

	first, err := state.CreateJob(jobs.CreateRequest{
		Type:  "echo",
		Input: "first",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != jobs.StatusScheduled || first.AssignedTo != worker.NodeID {
		t.Fatalf("expected first job scheduled to worker, got %#v", first)
	}

	second, err := state.CreateJob(jobs.CreateRequest{
		Type:  "echo",
		Input: "second",
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != jobs.StatusQueued || second.AssignedTo != "" || second.Attempts != 0 {
		t.Fatalf("expected second job queued while worker slot is busy, got %#v", second)
	}
	if second.LastFailure != "waiting for available worker capacity" {
		t.Fatalf("unexpected queue reason %q", second.LastFailure)
	}

	if next, ok := state.NextJobForWorker(worker.NodeID); !ok || next.ID != first.ID {
		t.Fatalf("expected first job from worker queue, got %#v ok=%v", next, ok)
	}
	if unexpected, ok := state.NextJobForWorker(worker.NodeID); ok {
		t.Fatalf("worker should not receive second job before first completes: %#v", unexpected)
	}

	completed, ok := state.CompleteJob(first.ID, jobs.CompleteRequest{
		NodeID: worker.NodeID,
		Result: "first-ok",
	})
	if !ok || completed.Status != jobs.StatusSucceeded {
		t.Fatalf("expected first completion, got %#v ok=%v", completed, ok)
	}

	scheduled, ok := state.Job(second.ID)
	if !ok {
		t.Fatal("expected second job")
	}
	if scheduled.Status != jobs.StatusScheduled || scheduled.AssignedTo != worker.NodeID || scheduled.Attempts != 1 {
		t.Fatalf("expected second job scheduled after slot frees, got %#v", scheduled)
	}
	if scheduled.LastFailure != "" {
		t.Fatalf("expected capacity reason cleared after scheduling, got %q", scheduled.LastFailure)
	}
}

func TestWorkerConfiguredSlotsAllowMultipleActiveJobs(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	worker := joinWorkerWithResourcesForTest(t, srv, "two-slot-worker", cluster.ResourceSnapshot{
		CPU:      cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
		Memory:   cluster.MemoryResources{TotalBytes: 8 * gb, AllowedBytes: 4 * gb},
		Storage:  cluster.StorageResources{TotalBytes: 20 * gb, AllowedBytes: 8 * gb, FreeBytes: 12 * gb},
		JobSlots: 2,
	})

	first, err := state.CreateJob(jobs.CreateRequest{Type: "echo", Input: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := state.CreateJob(jobs.CreateRequest{Type: "echo", Input: "second"})
	if err != nil {
		t.Fatal(err)
	}
	third, err := state.CreateJob(jobs.CreateRequest{Type: "echo", Input: "third"})
	if err != nil {
		t.Fatal(err)
	}

	if first.Status != jobs.StatusScheduled || second.Status != jobs.StatusScheduled {
		t.Fatalf("expected first two jobs scheduled, got %#v %#v", first, second)
	}
	if first.AssignedTo != worker.NodeID || second.AssignedTo != worker.NodeID {
		t.Fatalf("expected first two jobs assigned to worker, got %q %q", first.AssignedTo, second.AssignedTo)
	}
	if third.Status != jobs.StatusQueued || third.AssignedTo != "" {
		t.Fatalf("expected third job queued, got %#v", third)
	}
}

func TestCancelJobFreesWorkerSlot(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	worker := joinWorkerForTest(t, srv, "cancel-slot-worker")

	first, err := state.CreateJob(jobs.CreateRequest{Type: "echo", Input: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := state.CreateJob(jobs.CreateRequest{Type: "echo", Input: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != jobs.StatusQueued {
		t.Fatalf("expected second queued, got %#v", second)
	}

	canceled, ok := state.CancelJob(first.ID)
	if !ok || canceled.Status != jobs.StatusCanceled || canceled.FinishedAt.IsZero() {
		t.Fatalf("expected first canceled, got %#v ok=%v", canceled, ok)
	}
	scheduled, ok := state.Job(second.ID)
	if !ok {
		t.Fatal("expected second job")
	}
	if scheduled.Status != jobs.StatusScheduled || scheduled.AssignedTo != worker.NodeID {
		t.Fatalf("expected second scheduled after cancel frees slot, got %#v", scheduled)
	}
}

func TestCanceledRunningJobAcceptsLateWorkerCompletion(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	worker := joinWorkerForTest(t, srv, "late-cancel-worker")

	job, err := state.CreateJob(jobs.CreateRequest{Type: "echo", Input: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if next, ok := state.NextJobForWorker(worker.NodeID); !ok || next.ID != job.ID {
		t.Fatalf("expected worker to start job, got %#v ok=%v", next, ok)
	}
	canceled, ok := state.CancelJob(job.ID)
	if !ok || canceled.Status != jobs.StatusCanceled {
		t.Fatalf("expected canceled job, got %#v ok=%v", canceled, ok)
	}
	completed, ok := state.CompleteJob(job.ID, jobs.CompleteRequest{
		NodeID: worker.NodeID,
		Result: "late result",
	})
	if !ok || completed.Status != jobs.StatusCanceled || completed.Result != "" {
		t.Fatalf("expected late completion to keep job canceled, got %#v ok=%v", completed, ok)
	}
}

func TestOfflineWorkerFailsJobAfterMaxAttempts(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	workerA := joinWorkerForTest(t, srv, "job-worker-a")
	workerB := joinWorkerForTest(t, srv, "job-worker-b")

	job, err := state.CreateJob(jobs.CreateRequest{
		Type:        "echo",
		Input:       "hello cluster",
		AssignedTo:  workerA.NodeID,
		MaxAttempts: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.NextJobForWorker(workerA.NodeID); !ok {
		t.Fatal("expected worker A job to start")
	}
	state.MarkWorkerOffline(workerA.NodeID)

	rescheduled, ok := state.Job(job.ID)
	if !ok {
		t.Fatal("expected job")
	}
	if rescheduled.Status != jobs.StatusScheduled || rescheduled.AssignedTo != workerB.NodeID || rescheduled.Attempts != 2 {
		t.Fatalf("expected second attempt on worker B, got %#v", rescheduled)
	}
	if _, ok := state.NextJobForWorker(workerB.NodeID); !ok {
		t.Fatal("expected worker B job to start")
	}
	state.MarkWorkerOffline(workerB.NodeID)

	failed, ok := state.Job(job.ID)
	if !ok {
		t.Fatal("expected job")
	}
	if failed.Status != jobs.StatusFailed {
		t.Fatalf("expected failed after max attempts, got %s", failed.Status)
	}
	if failed.Attempts != 2 || failed.MaxAttempts != 2 {
		t.Fatalf("unexpected attempts metadata: attempts=%d max=%d", failed.Attempts, failed.MaxAttempts)
	}
	if failed.Error != "worker went offline" {
		t.Fatalf("unexpected error %q", failed.Error)
	}
}

func TestJobQueuesUntilCapableWorkerJoins(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	weak := joinWorkerWithResourcesForTest(t, srv, "weak-worker", cluster.ResourceSnapshot{
		CPU: cluster.CPUResources{
			CoresTotal:   2,
			CoresAllowed: 1,
		},
		Memory: cluster.MemoryResources{
			TotalBytes:   2 * gb,
			AllowedBytes: 1 * gb,
		},
	})

	job, err := state.CreateJob(jobs.CreateRequest{
		Type:  "compute.matrix_multiply",
		Input: `{"size":512,"iterations":2}`,
		Requirements: jobs.Requirements{
			CPUCores:    4,
			MemoryBytes: 2 * gb,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != jobs.StatusQueued || job.AssignedTo != "" || job.Attempts != 0 {
		t.Fatalf("expected job queued without assignment, got %#v", job)
	}
	if _, ok := state.NextJobForWorker(weak.NodeID); ok {
		t.Fatal("weak worker should not receive the job")
	}

	strong := joinWorkerWithResourcesForTest(t, srv, "strong-worker", cluster.ResourceSnapshot{
		CPU: cluster.CPUResources{
			CoresTotal:   8,
			CoresAllowed: 4,
		},
		Memory: cluster.MemoryResources{
			TotalBytes:   16 * gb,
			AllowedBytes: 4 * gb,
		},
	})

	scheduled, ok := state.Job(job.ID)
	if !ok {
		t.Fatal("expected job")
	}
	if scheduled.Status != jobs.StatusScheduled || scheduled.AssignedTo != strong.NodeID || scheduled.Attempts != 1 {
		t.Fatalf("expected job scheduled on capable worker, got %#v", scheduled)
	}
	if scheduled.LastFailure != "" {
		t.Fatalf("expected waiting reason to be cleared after scheduling, got %q", scheduled.LastFailure)
	}
}

func TestOfflineWorkerReschedulesOnlyToCapableWorker(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	workerA := joinWorkerWithResourcesForTest(t, srv, "capable-a", cluster.ResourceSnapshot{
		CPU: cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
	})
	workerB := joinWorkerWithResourcesForTest(t, srv, "weak-b", cluster.ResourceSnapshot{
		CPU: cluster.CPUResources{CoresTotal: 2, CoresAllowed: 1},
	})

	job, err := state.CreateJob(jobs.CreateRequest{
		Type:       "echo",
		Input:      "hello cluster",
		AssignedTo: workerA.NodeID,
		Requirements: jobs.Requirements{
			CPUCores: 4,
		},
		MaxAttempts: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.NextJobForWorker(workerA.NodeID); !ok {
		t.Fatal("expected worker A job to start")
	}

	state.MarkWorkerOffline(workerA.NodeID)

	queued, ok := state.Job(job.ID)
	if !ok {
		t.Fatal("expected job")
	}
	if queued.Status != jobs.StatusQueued || queued.AssignedTo != "" || queued.Attempts != 1 {
		t.Fatalf("expected job queued after capable worker loss, got %#v", queued)
	}
	if _, ok := state.NextJobForWorker(workerB.NodeID); ok {
		t.Fatal("weak worker should not receive rescheduled job")
	}

	workerC := joinWorkerWithResourcesForTest(t, srv, "capable-c", cluster.ResourceSnapshot{
		CPU: cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
	})

	rescheduled, ok := state.Job(job.ID)
	if !ok {
		t.Fatal("expected job")
	}
	if rescheduled.Status != jobs.StatusScheduled || rescheduled.AssignedTo != workerC.NodeID || rescheduled.Attempts != 2 {
		t.Fatalf("expected job rescheduled on worker C, got %#v", rescheduled)
	}
}

func TestStaleWorkerFailsActiveJob(t *testing.T) {
	state := NewState()
	worker := joinWorkerForTest(t, NewServer(":0", state), "stale-job-worker")

	job, err := state.CreateJob(jobs.CreateRequest{
		Type:        "echo",
		Input:       "hello cluster",
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.NextJobForWorker(worker.NodeID); !ok {
		t.Fatal("expected job to start")
	}
	state.heartbeatTimeout = time.Nanosecond
	time.Sleep(time.Millisecond)

	nodes := state.Nodes()
	if len(nodes) != 1 || nodes[0].Status != cluster.NodeStatusOffline {
		t.Fatalf("expected stale worker offline, got %#v", nodes)
	}

	failed, ok := state.Job(job.ID)
	if !ok {
		t.Fatal("expected job")
	}
	if failed.Status != jobs.StatusFailed {
		t.Fatalf("expected failed job, got %s", failed.Status)
	}
	if failed.Error != "worker heartbeat timed out" {
		t.Fatalf("unexpected error %q", failed.Error)
	}
}

func TestClusterBenchmarkCreatesJobPerOnlineWorker(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)

	workerA := joinWorkerForTest(t, srv, "cluster-worker-a")
	workerB := joinWorkerForTest(t, srv, "cluster-worker-b")
	offline := joinWorkerForTest(t, srv, "cluster-worker-offline")
	state.MarkWorkerOffline(offline.NodeID)

	reqBody := bytes.NewReader([]byte(`{"size":128,"iterations":3,"requested_by":"test-run"}`))
	req := httptest.NewRequest(http.MethodPost, "/v1/cluster-benchmarks", reqBody)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var summary ClusterBenchmarkSummary
	if err := json.NewDecoder(rec.Body).Decode(&summary); err != nil {
		t.Fatal(err)
	}
	if summary.Workers != 2 {
		t.Fatalf("expected 2 online workers in benchmark, got %d", summary.Workers)
	}
	if summary.Active != 2 || summary.Status != "running" {
		t.Fatalf("expected running benchmark with 2 active jobs, got status=%s active=%d", summary.Status, summary.Active)
	}
	if summary.Size != 128 || summary.Iterations != 3 {
		t.Fatalf("unexpected workload %dx%d iterations=%d", summary.Size, summary.Size, summary.Iterations)
	}

	assigned := map[string]bool{}
	for _, job := range summary.Jobs {
		assigned[job.AssignedTo] = true
		if job.RequestedBy != summary.RequestedBy {
			t.Fatalf("expected requested_by %q, got %q", summary.RequestedBy, job.RequestedBy)
		}
	}
	if !assigned[workerA.NodeID] || !assigned[workerB.NodeID] || assigned[offline.NodeID] {
		t.Fatalf("unexpected benchmark assignments: %#v", assigned)
	}

	jobsByID := map[string]jobs.Job{}
	for _, job := range summary.Jobs {
		jobsByID[job.AssignedTo] = job
	}
	if _, ok := state.NextJobForWorker(workerA.NodeID); !ok {
		t.Fatal("expected worker A job to start")
	}
	if _, ok := state.NextJobForWorker(workerB.NodeID); !ok {
		t.Fatal("expected worker B job to start")
	}
	state.CompleteJob(jobsByID[workerA.NodeID].ID, jobs.CompleteRequest{
		NodeID: workerA.NodeID,
		Result: `{"duration_ms":20,"gflops":2.5,"worker_runtime":"test/a"}`,
	})
	state.CompleteJob(jobsByID[workerB.NodeID].ID, jobs.CompleteRequest{
		NodeID: workerB.NodeID,
		Result: `{"duration_ms":30,"gflops":3.25,"worker_runtime":"test/b"}`,
	})

	listReq := httptest.NewRequest(http.MethodGet, "/v1/cluster-benchmarks", nil)
	listRec := httptest.NewRecorder()
	srv.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", listRec.Code, listRec.Body.String())
	}

	var listResp struct {
		ClusterBenchmarks []ClusterBenchmarkSummary `json:"cluster_benchmarks"`
	}
	if err := json.NewDecoder(listRec.Body).Decode(&listResp); err != nil {
		t.Fatal(err)
	}
	if len(listResp.ClusterBenchmarks) != 1 {
		t.Fatalf("expected 1 benchmark summary, got %d", len(listResp.ClusterBenchmarks))
	}
	completed := listResp.ClusterBenchmarks[0]
	if completed.Status != "succeeded" || completed.Completed != 2 {
		t.Fatalf("expected succeeded summary, got status=%s completed=%d", completed.Status, completed.Completed)
	}
	if completed.TotalGFLOPS != 5.75 {
		t.Fatalf("expected total GFLOPS 5.75, got %f", completed.TotalGFLOPS)
	}

	dashboardReq := httptest.NewRequest(http.MethodGet, "/", nil)
	dashboardRec := httptest.NewRecorder()
	srv.ServeHTTP(dashboardRec, dashboardReq)
	if dashboardRec.Code != http.StatusOK {
		t.Fatalf("expected dashboard status 200, got %d", dashboardRec.Code)
	}
	dashboard := dashboardRec.Body.String()
	for _, expected := range []string{
		"Total GFLOPS",
		"5.75 GFLOPS",
		"cluster-worker-a",
		"cluster-worker-b",
		"Job slots",
		"test/a",
		"test/b",
	} {
		if !strings.Contains(dashboard, expected) {
			t.Fatalf("expected dashboard to contain %q", expected)
		}
	}
}

func TestClusterBenchmarkRequiresOnlineWorkers(t *testing.T) {
	srv := NewServer(":0", NewState())

	req := httptest.NewRequest(http.MethodPost, "/v1/cluster-benchmarks", bytes.NewReader([]byte(`{"size":128,"iterations":3}`)))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d: %s", rec.Code, rec.Body.String())
	}

	dashboardReq := httptest.NewRequest(http.MethodGet, "/", nil)
	dashboardRec := httptest.NewRecorder()
	srv.ServeHTTP(dashboardRec, dashboardReq)
	if dashboardRec.Code != http.StatusOK {
		t.Fatalf("expected dashboard status 200, got %d", dashboardRec.Code)
	}
	if !strings.Contains(dashboardRec.Body.String(), "Connect at least one worker") {
		t.Fatalf("expected dashboard to explain benchmark worker requirement")
	}
}

func TestModelCatalogAndInstallJob(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	worker := joinWorkerWithResourcesForTest(t, srv, "model-worker", cluster.ResourceSnapshot{
		CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
		Memory:  cluster.MemoryResources{TotalBytes: 16 * gb, AllowedBytes: 8 * gb},
		Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 16 * gb, FreeBytes: 80 * gb},
	})

	listReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	listRec := httptest.NewRecorder()
	srv.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", listRec.Code, listRec.Body.String())
	}
	if !strings.Contains(listRec.Body.String(), "qwen2.5-0.5b-instruct-q4-k-m") {
		t.Fatalf("expected model catalog to include qwen")
	}

	installReq := httptest.NewRequest(http.MethodPost, "/v1/models/qwen2.5-0.5b-instruct-q4-k-m/install", bytes.NewReader([]byte(`{}`)))
	installRec := httptest.NewRecorder()
	srv.ServeHTTP(installRec, installReq)
	if installRec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", installRec.Code, installRec.Body.String())
	}

	var job jobs.Job
	if err := json.NewDecoder(installRec.Body).Decode(&job); err != nil {
		t.Fatal(err)
	}
	if job.Type != models.JobInstall {
		t.Fatalf("expected model install job, got %s", job.Type)
	}
	if job.AssignedTo != worker.NodeID {
		t.Fatalf("expected install assigned to worker, got %q", job.AssignedTo)
	}
	if job.Requirements.MemoryBytes == 0 || job.Requirements.DiskBytes == 0 {
		t.Fatalf("expected model resource requirements, got %#v", job.Requirements)
	}
}

func TestModelGenerateRequiresPrompt(t *testing.T) {
	srv := NewServer(":0", NewState())

	req := httptest.NewRequest(http.MethodPost, "/v1/models/qwen2.5-0.5b-instruct-q4-k-m/generate", bytes.NewReader([]byte(`{"prompt":""}`)))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "prompt is required") {
		t.Fatalf("expected prompt validation error, got %q", rec.Body.String())
	}
}

func joinWorkerForTest(t *testing.T, srv *Server, name string) membership.JoinResponse {
	t.Helper()
	return joinWorkerWithResourcesForTest(t, srv, name, cluster.ResourceSnapshot{
		CPU: cluster.CPUResources{
			CoresTotal:   4,
			CoresAllowed: 2,
		},
		Memory: cluster.MemoryResources{
			TotalBytes:   8 * gb,
			AllowedBytes: 2 * gb,
		},
		Storage: cluster.StorageResources{
			TotalBytes:   128 * gb,
			AllowedBytes: 8 * gb,
			FreeBytes:    64 * gb,
		},
	})
}

func joinWorkerWithResourcesForTest(t *testing.T, srv *Server, name string, resources cluster.ResourceSnapshot) membership.JoinResponse {
	t.Helper()

	joinReq := membership.JoinRequest{
		NodeName:  name,
		Role:      cluster.NodeRoleWorker,
		Resources: resources,
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
