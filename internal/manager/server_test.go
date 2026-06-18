package manager

import (
	"bytes"
	"encoding/json"
	"net"
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

func TestDashboardStatusEndpointReturnsSidebarCounters(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	joinWorkerWithResourcesForTest(t, srv, "worker-a", cluster.ResourceSnapshot{
		CPU:     cluster.CPUResources{CoresTotal: 4, CoresAllowed: 2},
		Memory:  cluster.MemoryResources{TotalBytes: 8 * gb, AllowedBytes: 4 * gb},
		Storage: cluster.StorageResources{TotalBytes: 20 * gb, AllowedBytes: 10 * gb},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/dashboard/status", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["workers_online"] != float64(1) || payload["workers_total"] != float64(1) {
		t.Fatalf("expected worker counters, got %#v", payload)
	}
	if payload["readiness_status"] == "" || payload["jobs_total"] == nil || payload["ready_models"] == nil {
		t.Fatalf("expected sidebar status fields, got %#v", payload)
	}
}

func TestDashboardModelCatalogControls(t *testing.T) {
	srv := NewServer(":0", NewState())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	for _, want := range []string{
		`id="model-catalog-search"`,
		`id="model-catalog-status"`,
		`id="model-catalog-family"`,
		`id="model-catalog-sort"`,
		`id="model-catalog-capable"`,
		`id="model-catalog-clear"`,
		`id="model-catalog-empty"`,
		`class="button model-detail"`,
		`data-model-placement=`,
		`data-placement-mode=`,
		`Blocked`,
		`id="model-detail-panel"`,
		`class="model-detail-dialog" role="dialog" aria-modal="true"`,
		`id="model-detail-placement"`,
		`Placement plan`,
		`id="model-detail-actions"`,
		`model-detail-install`,
		`modal-open`,
		`/placement`,
		`/v1/models/`,
		`cmesh.modelCatalog.filters`,
		`#models?`,
		`applyModelCatalogFiltersFromHash`,
		`No models match these filters`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected dashboard model catalog to contain %q", want)
		}
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

func TestConversationExtractsAndInjectsModelMemory(t *testing.T) {
	state := NewState()
	state.AppendConversationMessage("conv-test", "qwen2.5-0.5b-instruct-q4-k-m", "node-test", "system", models.ChatMessage{
		Role:    "user",
		Content: "Мене звати Сергій.",
	})
	memories := state.Memories("qwen2.5-0.5b-instruct-q4-k-m")
	if len(memories) != 1 {
		t.Fatalf("expected one memory, got %#v", memories)
	}
	if memories[0].Key != "user.name" || memories[0].Value != "Сергій" {
		t.Fatalf("unexpected memory: %#v", memories[0])
	}

	prompt := systemPromptWithMemory("Base prompt.", "qwen2.5-0.5b-instruct-q4-k-m", state)
	if !strings.Contains(prompt, "Known memory") || !strings.Contains(prompt, "user.name: Сергій") {
		t.Fatalf("expected injected memory, got %q", prompt)
	}
}

func TestModelDeleteCleansModelPersistence(t *testing.T) {
	state := NewState()
	state.AppendConversationMessage("conv-test", "qwen2.5-0.5b-instruct-q4-k-m", "node-test", "system", models.ChatMessage{
		Role:    "user",
		Content: "My name is Sergiy.",
	})
	if len(state.Memories("qwen2.5-0.5b-instruct-q4-k-m")) != 1 {
		t.Fatal("expected memory before delete")
	}

	input, err := json.Marshal(models.DeleteInput{ModelID: "qwen2.5-0.5b-instruct-q4-k-m"})
	if err != nil {
		t.Fatal(err)
	}
	job, err := state.CreateJob(jobs.CreateRequest{
		Type:        models.JobDelete,
		Input:       string(input),
		RequestedBy: "dashboard-models",
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	state.jobs[job.ID] = jobs.Job{
		ID:         job.ID,
		Type:       job.Type,
		Status:     jobs.StatusRunning,
		Input:      job.Input,
		AssignedTo: "node-test",
	}
	completed, ok := state.CompleteJob(job.ID, jobs.CompleteRequest{NodeID: "node-test", Result: `{"removed":true,"freed_bytes":1024}`})
	if !ok {
		t.Fatal("expected delete completion to succeed")
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(completed.Result), &result); err != nil {
		t.Fatal(err)
	}
	if result["deleted_memories"] != float64(1) || result["deleted_conversations"] != float64(1) || result["freed_bytes"] != float64(1024) {
		t.Fatalf("expected delete cleanup metadata, got %#v", result)
	}
	if got := state.Memories("qwen2.5-0.5b-instruct-q4-k-m"); len(got) != 0 {
		t.Fatalf("expected memories to be removed, got %#v", got)
	}
	if _, ok := state.Conversation("conv-test"); ok {
		t.Fatal("expected model conversation to be removed")
	}
}

func TestModelDeleteKeepsPersistenceWhenAnotherWorkerHasModel(t *testing.T) {
	state := NewState()
	state.nodes["node-a"] = cluster.Node{
		ID:     "node-a",
		Name:   "worker-a",
		Role:   cluster.NodeRoleWorker,
		Status: cluster.NodeStatusOnline,
		Resources: cluster.ResourceSnapshot{
			Models: []cluster.ModelResource{{ID: "qwen2.5-0.5b-instruct-q4-k-m", Name: "Qwen2.5 0.5B Instruct", Ready: true}},
		},
	}
	state.nodes["node-b"] = cluster.Node{
		ID:     "node-b",
		Name:   "worker-b",
		Role:   cluster.NodeRoleWorker,
		Status: cluster.NodeStatusOnline,
		Resources: cluster.ResourceSnapshot{
			Models: []cluster.ModelResource{{ID: "qwen2.5-0.5b-instruct-q4-k-m", Name: "Qwen2.5 0.5B Instruct", Ready: true}},
		},
	}
	state.AppendConversationMessage("conv-test", "qwen2.5-0.5b-instruct-q4-k-m", "node-a", "system", models.ChatMessage{
		Role:    "user",
		Content: "My name is Sergiy.",
	})
	if len(state.Memories("qwen2.5-0.5b-instruct-q4-k-m")) != 1 {
		t.Fatal("expected memory before delete")
	}

	input, err := json.Marshal(models.DeleteInput{ModelID: "qwen2.5-0.5b-instruct-q4-k-m"})
	if err != nil {
		t.Fatal(err)
	}
	job, err := state.CreateJob(jobs.CreateRequest{
		Type:        models.JobDelete,
		Input:       string(input),
		RequestedBy: "dashboard-models",
		AssignedTo:  "node-a",
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	state.jobs[job.ID] = jobs.Job{
		ID:         job.ID,
		Type:       job.Type,
		Status:     jobs.StatusRunning,
		Input:      job.Input,
		AssignedTo: "node-a",
	}

	completed, ok := state.CompleteJob(job.ID, jobs.CompleteRequest{NodeID: "node-a", Result: `{"removed":true,"freed_bytes":1024}`})
	if !ok {
		t.Fatal("expected delete completion to succeed")
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(completed.Result), &result); err != nil {
		t.Fatal(err)
	}
	if result["deleted_memories"] != float64(0) || result["deleted_conversations"] != float64(0) {
		t.Fatalf("expected no persistence cleanup while another worker has model, got %#v", result)
	}
	if got := state.Memories("qwen2.5-0.5b-instruct-q4-k-m"); len(got) != 1 {
		t.Fatalf("expected memory to remain, got %#v", got)
	}
	if _, ok := state.Conversation("conv-test"); !ok {
		t.Fatal("expected model conversation to remain")
	}
}

func TestJobDetailSummarizesModelDeleteCleanup(t *testing.T) {
	job := jobs.Job{
		Type:   models.JobDelete,
		Result: `{"removed":true,"freed_bytes":2147483648,"deleted_memories":2,"deleted_conversations":1}`,
	}

	got := jobDetail(job)
	for _, want := range []string{
		"Model files removed",
		"freed 2.0 GB",
		"cleared 2 memory item(s)",
		"cleared 1 conversation(s)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in detail %q", want, got)
		}
	}
}

func TestConversationAPIListsAndReadsGeneratedChatContext(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	worker := joinWorkerWithResourcesForTest(t, srv, "worker-a", cluster.ResourceSnapshot{
		CPU: cluster.CPUResources{
			CoresTotal:   4,
			CoresAllowed: 2,
		},
		Memory: cluster.MemoryResources{
			TotalBytes:   64 * gb,
			AllowedBytes: 48 * gb,
		},
		Storage: cluster.StorageResources{
			TotalBytes:   256 * gb,
			AllowedBytes: 64 * gb,
			FreeBytes:    128 * gb,
		},
		Models: []cluster.ModelResource{{
			ID:    "qwen2.5-0.5b-instruct-q4-k-m",
			Name:  "Qwen2.5 0.5B Instruct",
			Ready: true,
		}},
		Runtimes: []cluster.RuntimeResource{{
			Name:  "llama.cpp",
			Ready: true,
		}},
	})

	body := bytes.NewReader([]byte(`{"node_id":"` + worker.NodeID + `","prompt":"Мене звати Сергій.","system_prompt":"Remember names."}`))
	req := httptest.NewRequest(http.MethodPost, "/v1/models/qwen2.5-0.5b-instruct-q4-k-m/generate", body)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var job jobs.Job
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	var input models.GenerateInput
	if err := json.Unmarshal([]byte(job.Input), &input); err != nil {
		t.Fatal(err)
	}
	if input.ConversationID == "" {
		t.Fatal("expected conversation id in generated job input")
	}

	listReq := httptest.NewRequest(http.MethodGet, "/v1/conversations", nil)
	listRec := httptest.NewRecorder()
	srv.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", listRec.Code, listRec.Body.String())
	}
	if !strings.Contains(listRec.Body.String(), input.ConversationID) {
		t.Fatalf("expected conversation list to include id %q, got %s", input.ConversationID, listRec.Body.String())
	}

	readReq := httptest.NewRequest(http.MethodGet, "/v1/conversations/"+input.ConversationID, nil)
	readRec := httptest.NewRecorder()
	srv.ServeHTTP(readRec, readReq)
	if readRec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", readRec.Code, readRec.Body.String())
	}
	if !strings.Contains(readRec.Body.String(), "Мене звати Сергій.") || !strings.Contains(readRec.Body.String(), "Remember names.") {
		t.Fatalf("expected conversation body to include saved prompt and system prompt, got %s", readRec.Body.String())
	}
}

func TestModelGenerateUsesQualityPresetDefaults(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	worker := joinWorkerWithResourcesForTest(t, srv, "worker-a", cluster.ResourceSnapshot{
		CPU:     cluster.CPUResources{CoresTotal: 4, CoresAllowed: 2},
		Memory:  cluster.MemoryResources{TotalBytes: 64 * gb, AllowedBytes: 48 * gb},
		Storage: cluster.StorageResources{TotalBytes: 256 * gb, AllowedBytes: 64 * gb, FreeBytes: 128 * gb},
		Models: []cluster.ModelResource{{
			ID:    "qwen2.5-coder-7b-instruct-q4-k-m",
			Name:  "Qwen2.5 Coder 7B Instruct",
			Ready: true,
		}},
		Runtimes: []cluster.RuntimeResource{{
			Name:  "llama.cpp",
			Ready: true,
		}},
	})

	body := bytes.NewReader([]byte(`{"node_id":"` + worker.NodeID + `","prompt":"write a test"}`))
	req := httptest.NewRequest(http.MethodPost, "/v1/models/qwen2.5-coder-7b-instruct-q4-k-m/generate", body)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var job jobs.Job
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	var input models.GenerateInput
	if err := json.Unmarshal([]byte(job.Input), &input); err != nil {
		t.Fatal(err)
	}
	preset := models.QualityPresetFor(models.Model{ID: "qwen2.5-coder-7b-instruct-q4-k-m", Family: "Qwen"})
	if input.Temperature != preset.Temperature || input.MaxTokens != preset.MaxTokens {
		t.Fatalf("expected preset generation settings, got %#v preset %#v", input, preset)
	}
	if !strings.Contains(input.SystemPrompt, "precise code") {
		t.Fatalf("expected coder system prompt, got %q", input.SystemPrompt)
	}
}

func TestMemoryPreviewIncludesBudgetedPromptContext(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	state.AppendConversationMessage("conv-debug", "qwen2.5-0.5b-instruct-q4-k-m", "node-test", "Base prompt.", models.ChatMessage{
		Role:    "user",
		Content: strings.Repeat("old ", 3000),
	})
	state.AppendConversationMessage("conv-debug", "qwen2.5-0.5b-instruct-q4-k-m", "node-test", "Base prompt.", models.ChatMessage{
		Role:    "assistant",
		Content: strings.Repeat("older ", 3000),
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/memories/preview?model_id=qwen2.5-0.5b-instruct-q4-k-m&conversation_id=conv-debug&prompt=latest-question&max_tokens=2048", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Context PromptContextPreview `json:"context"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Context.ModelID != "qwen2.5-0.5b-instruct-q4-k-m" {
		t.Fatalf("expected model id in context, got %#v", payload.Context)
	}
	if payload.Context.DroppedMessages == 0 {
		t.Fatalf("expected old messages to be dropped, got %#v", payload.Context)
	}
	if len(payload.Context.IncludedMessages) == 0 || payload.Context.IncludedMessages[len(payload.Context.IncludedMessages)-1].Content != "latest-question" {
		t.Fatalf("expected draft prompt to be included last, got %#v", payload.Context.IncludedMessages)
	}
}

func TestModelGenerateRejectsWorkerWithoutReadyRuntime(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	worker := joinWorkerWithResourcesForTest(t, srv, "worker-a", cluster.ResourceSnapshot{
		CPU:     cluster.CPUResources{CoresTotal: 4, CoresAllowed: 2},
		Memory:  cluster.MemoryResources{TotalBytes: 64 * gb, AllowedBytes: 48 * gb},
		Storage: cluster.StorageResources{TotalBytes: 256 * gb, AllowedBytes: 64 * gb, FreeBytes: 128 * gb},
		Models: []cluster.ModelResource{{
			ID:    "qwen2.5-0.5b-instruct-q4-k-m",
			Name:  "Qwen2.5 0.5B Instruct",
			Ready: true,
		}},
		Runtimes: []cluster.RuntimeResource{{
			Name:  "llama.cpp",
			Ready: false,
			Error: "llama-cli missing",
		}},
	})

	body := bytes.NewReader([]byte(`{"node_id":"` + worker.NodeID + `","prompt":"hello"}`))
	req := httptest.NewRequest(http.MethodPost, "/v1/models/qwen2.5-0.5b-instruct-q4-k-m/generate", body)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "runtime is not ready") {
		t.Fatalf("expected runtime error, got %s", rec.Body.String())
	}
}

func TestModelGenerateRejectsConcurrentConversationJob(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	worker := joinWorkerWithResourcesForTest(t, srv, "worker-a", cluster.ResourceSnapshot{
		CPU:     cluster.CPUResources{CoresTotal: 4, CoresAllowed: 2},
		Memory:  cluster.MemoryResources{TotalBytes: 64 * gb, AllowedBytes: 48 * gb},
		Storage: cluster.StorageResources{TotalBytes: 256 * gb, AllowedBytes: 64 * gb, FreeBytes: 128 * gb},
		Models: []cluster.ModelResource{{
			ID:    "qwen2.5-0.5b-instruct-q4-k-m",
			Name:  "Qwen2.5 0.5B Instruct",
			Ready: true,
		}},
		Runtimes: []cluster.RuntimeResource{{
			Name:  "llama.cpp",
			Ready: true,
		}},
	})

	input, err := json.Marshal(models.GenerateInput{
		ModelID:        "qwen2.5-0.5b-instruct-q4-k-m",
		ConversationID: "conv-active",
		Prompt:         "first",
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := state.CreateJob(jobs.CreateRequest{
		Type:        models.JobGenerate,
		Input:       string(input),
		RequestedBy: "dashboard-chat",
		AssignedTo:  worker.NodeID,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if active.Status != jobs.StatusScheduled {
		t.Fatalf("expected scheduled active job, got %#v", active)
	}

	body := bytes.NewReader([]byte(`{"node_id":"` + worker.NodeID + `","conversation_id":"conv-active","prompt":"second"}`))
	req := httptest.NewRequest(http.MethodPost, "/v1/models/qwen2.5-0.5b-instruct-q4-k-m/generate", body)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "active generate job") {
		t.Fatalf("expected active generate conflict, got %q", rec.Body.String())
	}
}

func TestConversationAPIDeletesConversation(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	state.AppendConversationMessage("conv-delete", "qwen2.5-0.5b-instruct-q4-k-m", "node-test", "system", models.ChatMessage{
		Role:    "user",
		Content: "delete this conversation",
	})

	deleteReq := httptest.NewRequest(http.MethodDelete, "/v1/conversations/conv-delete", nil)
	deleteRec := httptest.NewRecorder()
	srv.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", deleteRec.Code, deleteRec.Body.String())
	}

	readReq := httptest.NewRequest(http.MethodGet, "/v1/conversations/conv-delete", nil)
	readRec := httptest.NewRecorder()
	srv.ServeHTTP(readRec, readReq)
	if readRec.Code != http.StatusNotFound {
		t.Fatalf("expected status 404 after delete, got %d: %s", readRec.Code, readRec.Body.String())
	}
}

func TestMemoryAPIDeletesMemory(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	state.AppendConversationMessage("conv-test", "qwen2.5-0.5b-instruct-q4-k-m", "node-test", "system", models.ChatMessage{
		Role:    "user",
		Content: "My name is Sergiy.",
	})
	memories := state.Memories("qwen2.5-0.5b-instruct-q4-k-m")
	if len(memories) != 1 {
		t.Fatalf("expected one memory, got %#v", memories)
	}

	req := httptest.NewRequest(http.MethodDelete, "/v1/memories/"+memories[0].ID, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := state.Memories("qwen2.5-0.5b-instruct-q4-k-m"); len(got) != 0 {
		t.Fatalf("expected memory deleted, got %#v", got)
	}
}

func TestMemoryAPIPreviewsAndClearsModelMemory(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	state.AppendConversationMessage("conv-test", "qwen2.5-0.5b-instruct-q4-k-m", "node-test", "Base prompt.", models.ChatMessage{
		Role:    "user",
		Content: "My name is Sergiy.",
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/memories/preview?model_id=qwen2.5-0.5b-instruct-q4-k-m&system_prompt=Base+prompt.", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var preview struct {
		MemoryContext         string `json:"memory_context"`
		EffectiveSystemPrompt string `json:"effective_system_prompt"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview.MemoryContext, "user.name: Sergiy") {
		t.Fatalf("expected memory context, got %#v", preview)
	}
	if !strings.Contains(preview.EffectiveSystemPrompt, "Base prompt.") || !strings.Contains(preview.EffectiveSystemPrompt, "Known memory") {
		t.Fatalf("expected effective prompt with memory, got %#v", preview)
	}

	req = httptest.NewRequest(http.MethodDelete, "/v1/memories?model_id=qwen2.5-0.5b-instruct-q4-k-m", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := state.Memories("qwen2.5-0.5b-instruct-q4-k-m"); len(got) != 0 {
		t.Fatalf("expected model memory cleared, got %#v", got)
	}
}

func TestMemoryAPICreatesAndUpdatesManualMemory(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)

	body := bytes.NewBufferString(`{"model_id":"qwen2.5-0.5b-instruct-q4-k-m","key":"reply_language","value":"ukrainian","source":"manual"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/memories", body)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created Memory
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Key != "reply_language" || created.Value != "ukrainian" || created.Source != "manual" {
		t.Fatalf("unexpected created memory: %#v", created)
	}

	body = bytes.NewBufferString(`{"model_id":"qwen2.5-0.5b-instruct-q4-k-m","key":"reply_language","value":"українська","source":"manual"}`)
	req = httptest.NewRequest(http.MethodPost, "/v1/memories/"+created.ID, body)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	memories := state.Memories("qwen2.5-0.5b-instruct-q4-k-m")
	if len(memories) != 1 || memories[0].Value != "українська" {
		t.Fatalf("expected updated memory, got %#v", memories)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/memories/preview?model_id=qwen2.5-0.5b-instruct-q4-k-m", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "reply_language: українська") {
		t.Fatalf("expected manual memory in preview, got %s", rec.Body.String())
	}
}

func TestGenerateCompletionKeepsStoredSystemPromptClean(t *testing.T) {
	state := NewState()
	state.AppendConversationMessage("conv-test", "qwen2.5-0.5b-instruct-q4-k-m", "node-test", "Base prompt.", models.ChatMessage{
		Role:    "user",
		Content: "My name is Sergiy.",
	})
	input, err := json.Marshal(models.GenerateInput{
		ModelID:        "qwen2.5-0.5b-instruct-q4-k-m",
		ConversationID: "conv-test",
		SystemPrompt:   systemPromptWithMemory("Base prompt.", "qwen2.5-0.5b-instruct-q4-k-m", state),
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := state.CreateJob(jobs.CreateRequest{
		Type:        models.JobGenerate,
		Input:       string(input),
		RequestedBy: "test",
		AssignedTo:  "node-test",
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	state.jobs[job.ID] = jobs.Job{
		ID:         job.ID,
		Type:       job.Type,
		Status:     jobs.StatusRunning,
		Input:      job.Input,
		AssignedTo: "node-test",
	}
	if _, ok := state.CompleteJob(job.ID, jobs.CompleteRequest{NodeID: "node-test", Result: `{"output":"hello"}`}); !ok {
		t.Fatal("expected generate completion to succeed")
	}
	conversation, ok := state.Conversation("conv-test")
	if !ok {
		t.Fatal("expected conversation")
	}
	if conversation.SystemPrompt != "Base prompt." {
		t.Fatalf("expected clean system prompt, got %q", conversation.SystemPrompt)
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

func TestDashboardShowsWorkerHealthAndRuntimeInventory(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	joinWorkerWithResourcesForTest(t, srv, "health-worker", cluster.ResourceSnapshot{
		CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
		Memory:  cluster.MemoryResources{TotalBytes: 16 * gb, AllowedBytes: 8 * gb},
		Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 64 * gb, FreeBytes: 32 * gb},
		Models: []cluster.ModelResource{{
			ID:          "qwen2.5-0.5b-instruct-q4-k-m",
			Name:        "Qwen2.5 0.5B Instruct",
			Family:      "Qwen",
			Runtime:     "llama.cpp",
			Path:        "/tmp/cmesh/models/qwen/model.gguf",
			Bytes:       512 * 1024 * 1024,
			Ready:       true,
			InstalledAt: time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC),
		}},
		Runtimes: []cluster.RuntimeResource{{
			Name:    "llama.cpp",
			Ready:   true,
			Version: "b9672",
			Source:  "cmesh-runtime-cache",
		}},
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected dashboard status 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, expected := range []string{
		"health-worker",
		"heartbeat",
		"llama.cpp ready b9672",
		"total 512 MB",
		"Model Inventory",
		"model ready",
		"runtime ready",
		"/tmp/cmesh/models/qwen/model.gguf",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected dashboard to contain %q", expected)
		}
	}
}

func TestRuntimeStageProbesEndpointReportsWorkerDiagnostics(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	joinWorkerWithResourcesForTest(t, srv, "stage-worker", cluster.ResourceSnapshot{
		CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
		Memory:  cluster.MemoryResources{TotalBytes: 16 * gb, AllowedBytes: 8 * gb},
		Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 64 * gb, FreeBytes: 32 * gb},
		Runtimes: []cluster.RuntimeResource{{
			Name:       "llama.cpp",
			Ready:      true,
			Version:    "b9672",
			BinaryPath: "/tmp/llama-cli",
			StageRuntimes: []cluster.StageRuntimeResource{{
				Name:          "llama.cpp-stage-experimental",
				Ready:         false,
				CLIReady:      true,
				BinaryPath:    "/tmp/llama-cli",
				RequiredHooks: []string{"load logical layer range"},
				Blockers:      []string{"public llama-cli does not expose CDIP layer-stage activation hooks"},
			}},
		}},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/runtime/stage-probes", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Summary RuntimeStageProbeSummary  `json:"summary"`
		Workers []RuntimeStageProbeWorker `json:"workers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Summary.Workers != 1 || payload.Summary.StageProbeWorkers != 1 || payload.Summary.BlockedStageRuntimeWorkers != 1 {
		t.Fatalf("unexpected probe summary: %#v", payload.Summary)
	}
	if len(payload.Workers) != 1 || payload.Workers[0].Runtime != "llama.cpp" || len(payload.Workers[0].Probes) != 1 {
		t.Fatalf("unexpected probe workers: %#v", payload.Workers)
	}
	probe := payload.Workers[0].Probes[0]
	if probe.Ready || !probe.CLIReady || !strings.Contains(strings.Join(probe.Blockers, " "), "does not expose CDIP") {
		t.Fatalf("unexpected stage probe: %#v", probe)
	}
}

func TestRuntimeRPCPoolEndpointReportsReadyEndpoints(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	joinWorkerWithResourcesForTest(t, srv, "rpc-worker-a", cluster.ResourceSnapshot{
		CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
		Memory:  cluster.MemoryResources{TotalBytes: 16 * gb, AllowedBytes: 8 * gb},
		Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 64 * gb, FreeBytes: 32 * gb},
		Runtimes: []cluster.RuntimeResource{{
			Name:         "llama.cpp",
			Ready:        true,
			Capabilities: []string{"llama.cpp-rpc-client", "llama.cpp-rpc-backend"},
			RPCRuntimes: []cluster.RPCRuntimeResource{{
				Name:       "llama.cpp-rpc",
				Ready:      true,
				Endpoint:   "10.0.0.10:50052",
				Protocol:   "llama.cpp-rpc",
				ServerPath: "/tmp/rpc-server",
			}},
		}},
	})
	joinWorkerWithResourcesForTest(t, srv, "rpc-worker-b", cluster.ResourceSnapshot{
		CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
		Memory:  cluster.MemoryResources{TotalBytes: 16 * gb, AllowedBytes: 8 * gb},
		Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 64 * gb, FreeBytes: 32 * gb},
		Runtimes: []cluster.RuntimeResource{{
			Name:  "llama.cpp",
			Ready: true,
			RPCRuntimes: []cluster.RPCRuntimeResource{{
				Name:     "llama.cpp-rpc",
				Ready:    false,
				Endpoint: "10.0.0.11:50052",
				Blockers: []string{"llama.cpp rpc-server is not running"},
			}},
		}},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/runtime/rpc-pool", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Summary        RuntimeRPCPoolSummary  `json:"summary"`
		Workers        []RuntimeRPCPoolWorker `json:"workers"`
		Endpoints      []string               `json:"endpoints"`
		LlamaCLIRPCArg string                 `json:"llama_cli_rpc_arg"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Summary.Workers != 2 || payload.Summary.RuntimeReadyWorkers != 2 || payload.Summary.RPCReadyWorkers != 1 || payload.Summary.Endpoints != 1 {
		t.Fatalf("unexpected rpc pool summary: %#v", payload.Summary)
	}
	if len(payload.Workers) != 2 || payload.Workers[0].RPC.Endpoint != "10.0.0.10:50052" || payload.Workers[1].RPC.Endpoint != "10.0.0.11:50052" || payload.Workers[1].RPC.Ready {
		t.Fatalf("unexpected rpc pool workers: %#v", payload.Workers)
	}
	if len(payload.Endpoints) != 1 || payload.Endpoints[0] != "10.0.0.10:50052" || payload.LlamaCLIRPCArg != "10.0.0.10:50052" {
		t.Fatalf("unexpected rpc endpoints: endpoints=%#v arg=%q", payload.Endpoints, payload.LlamaCLIRPCArg)
	}
}

func TestRuntimeRPCPoolSmokeEndpoint(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	state := NewState()
	srv := NewServer(":0", state)
	joinWorkerWithResourcesForTest(t, srv, "rpc-smoke-worker", cluster.ResourceSnapshot{
		CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
		Memory:  cluster.MemoryResources{TotalBytes: 16 * gb, AllowedBytes: 8 * gb},
		Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 64 * gb, FreeBytes: 32 * gb},
		Runtimes: []cluster.RuntimeResource{{
			Name:  "llama.cpp",
			Ready: true,
			RPCRuntimes: []cluster.RPCRuntimeResource{{
				Name:     "llama.cpp-rpc",
				Ready:    true,
				Endpoint: listener.Addr().String(),
			}},
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/runtime/rpc-pool/smoke?timeout_ms=500", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var payload RuntimeRPCSmokeReport
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Checked != 1 || payload.Ready != 1 || payload.Failed != 0 || !payload.RunnableNow {
		t.Fatalf("unexpected smoke report: %#v", payload)
	}
	if len(payload.Results) != 1 || !payload.Results[0].Ready || payload.Results[0].Endpoint != listener.Addr().String() {
		t.Fatalf("unexpected smoke results: %#v", payload.Results)
	}
}

func TestRuntimeRPCPoolSmokeEndpointRequiresActiveEndpoints(t *testing.T) {
	srv := NewServer(":0", NewState())
	req := httptest.NewRequest(http.MethodPost, "/v1/runtime/rpc-pool/smoke", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestModelDistributedRPCGenerateCreatesWorkerJob(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	joinWorkerWithResourcesForTest(t, srv, "rpc-worker", cluster.ResourceSnapshot{
		CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
		Memory:  cluster.MemoryResources{TotalBytes: 16 * gb, AllowedBytes: 8 * gb},
		Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 64 * gb, FreeBytes: 32 * gb},
		Models: []cluster.ModelResource{{
			ID:      "qwen2.5-0.5b-instruct-q4-k-m",
			Name:    "Qwen2.5 0.5B Instruct",
			Runtime: "llama.cpp",
			Path:    "/tmp/qwen.gguf",
			Ready:   true,
		}},
		Runtimes: []cluster.RuntimeResource{{
			Name:  "llama.cpp",
			Ready: true,
			RPCRuntimes: []cluster.RPCRuntimeResource{{
				Name:     "llama.cpp-rpc",
				Ready:    true,
				Endpoint: "10.0.0.10:50052",
			}},
		}},
	})

	body := bytes.NewBufferString(`{"prompt":"hello","max_tokens":8}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/models/qwen2.5-0.5b-instruct-q4-k-m/distributed-rpc-generate", body)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var job jobs.Job
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if job.Type != models.JobGenerateDistributedRPC || job.AssignedTo == "" {
		t.Fatalf("unexpected job: %#v", job)
	}
	var input models.DistributedRPCGenerateInput
	if err := json.Unmarshal([]byte(job.Input), &input); err != nil {
		t.Fatal(err)
	}
	if input.ModelID != "qwen2.5-0.5b-instruct-q4-k-m" || input.Prompt != "hello" || len(input.RPCEndpoints) != 1 || input.RPCEndpoints[0] != "10.0.0.10:50052" {
		t.Fatalf("unexpected distributed rpc input: %#v", input)
	}
}

func TestModelDistributedRPCReadinessEndpoint(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	joinWorkerWithResourcesForTest(t, srv, "rpc-ready-worker", cluster.ResourceSnapshot{
		CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
		Memory:  cluster.MemoryResources{TotalBytes: 16 * gb, AllowedBytes: 8 * gb},
		Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 64 * gb, FreeBytes: 32 * gb},
		Models: []cluster.ModelResource{{
			ID:      "qwen2.5-0.5b-instruct-q4-k-m",
			Name:    "Qwen2.5 0.5B Instruct",
			Runtime: "llama.cpp",
			Path:    "/tmp/qwen.gguf",
			Ready:   true,
		}},
		Runtimes: []cluster.RuntimeResource{{
			Name:  "llama.cpp",
			Ready: true,
			RPCRuntimes: []cluster.RPCRuntimeResource{{
				Name:     "llama.cpp-rpc",
				Ready:    true,
				Endpoint: "10.0.0.10:50052",
			}},
		}},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models/qwen2.5-0.5b-instruct-q4-k-m/distributed-rpc-readiness", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var payload ModelDistributedRPCReadiness
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Ready || payload.NodeID == "" || len(payload.RPCEndpoints) != 1 || len(payload.Blockers) != 0 {
		t.Fatalf("unexpected readiness: %#v", payload)
	}
}

func TestModelDistributedRPCReadinessReportsBlockers(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	joinWorkerWithResourcesForTest(t, srv, "rpc-blocked-worker", cluster.ResourceSnapshot{
		CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
		Memory:  cluster.MemoryResources{TotalBytes: 16 * gb, AllowedBytes: 8 * gb},
		Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 64 * gb, FreeBytes: 32 * gb},
		Models: []cluster.ModelResource{{
			ID:      "qwen2.5-0.5b-instruct-q4-k-m",
			Name:    "Qwen2.5 0.5B Instruct",
			Runtime: "llama.cpp",
			Path:    "/tmp/qwen.gguf",
			Ready:   true,
		}},
		Runtimes: []cluster.RuntimeResource{{
			Name:  "llama.cpp",
			Ready: true,
		}},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models/qwen2.5-0.5b-instruct-q4-k-m/distributed-rpc-readiness", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var payload ModelDistributedRPCReadiness
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Ready || len(payload.Blockers) == 0 || !strings.Contains(strings.Join(payload.Blockers, " "), "no active llama.cpp rpc endpoints") {
		t.Fatalf("expected rpc endpoint blocker, got %#v", payload)
	}
}

func TestClusterReadinessReadyWhenRuntimeAndModelAreReady(t *testing.T) {
	node := cluster.Node{
		ID:     "node-ready",
		Name:   "ready-worker",
		Role:   cluster.NodeRoleWorker,
		Status: cluster.NodeStatusOnline,
		Resources: cluster.ResourceSnapshot{
			Models: []cluster.ModelResource{{ID: "qwen-test", Name: "Qwen Test", Ready: true}},
			Runtimes: []cluster.RuntimeResource{{
				Name:  "llama.cpp",
				Ready: true,
			}},
		},
	}
	modelsView := modelSummaries([]models.Model{{ID: "qwen-test", Name: "Qwen Test", Runtime: "llama.cpp"}}, nil, []cluster.Node{node})

	readiness := clusterReadiness([]cluster.Node{node}, modelsView, nil)
	if readiness.Status != "ready" {
		t.Fatalf("expected ready status, got %#v", readiness)
	}
	if readiness.RuntimeReadyWorkers != 1 || readiness.GeneratableModels != 1 {
		t.Fatalf("expected ready runtime and model counts, got %#v", readiness)
	}
}

func TestDashboardShowsReadinessTab(t *testing.T) {
	srv := NewServer(":0", NewState())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected dashboard status 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, expected := range []string{
		"Cluster Readiness",
		"Cluster model capacity",
		"Next unlock",
		"Short by",
		"Worker capacity contributors",
		"Capacity snapshots",
		`id="capacity-save-snapshot"`,
		"No workers are online.",
		"data-tab-target=\"readiness\"",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected dashboard to contain %q", expected)
		}
	}
}

func TestCapacityEndpointSummarizesModelCapacity(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	joinWorkerWithResourcesForTest(t, srv, "capacity-worker", cluster.ResourceSnapshot{
		CPU:     cluster.CPUResources{CoresTotal: 12, CoresAllowed: 8},
		Memory:  cluster.MemoryResources{TotalBytes: 64 * gb, AllowedBytes: 48 * gb},
		Storage: cluster.StorageResources{TotalBytes: 256 * gb, AllowedBytes: 80 * gb, FreeBytes: 120 * gb},
		GPU: []cluster.GPUResources{{
			Name:             "test-gpu",
			AllowedVRAMBytes: 16 * gb,
		}},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/capacity", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Summary  ClusterSummary  `json:"summary"`
		Capacity CapacitySummary `json:"capacity"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Capacity.WorkersOnline != 1 || payload.Capacity.AllowedCPUCores != 8 {
		t.Fatalf("expected aggregate worker capacity, got %#v", payload.Capacity)
	}
	if payload.Capacity.AllowedMemoryBytes != 48*gb || payload.Capacity.AllowedStorageBytes != 80*gb || payload.Capacity.AllowedVRAMBytes != 16*gb {
		t.Fatalf("expected aggregate resource bytes, got %#v", payload.Capacity)
	}
	if payload.Capacity.CatalogModels == 0 || payload.Capacity.SingleWorkerRunnableModels == 0 {
		t.Fatalf("expected runnable catalog capacity, got %#v", payload.Capacity)
	}
	if payload.Capacity.LargestSingleWorkerModel.ID == "" {
		t.Fatalf("expected largest runnable model, got %#v", payload.Capacity)
	}
	if len(payload.Capacity.Workers) != 1 {
		t.Fatalf("expected one worker contribution, got %#v", payload.Capacity.Workers)
	}
	contributor := payload.Capacity.Workers[0]
	if contributor.Name != "capacity-worker" || contributor.AllowedMemoryBytes != 48*gb || contributor.RunnableModels == 0 {
		t.Fatalf("expected worker contributor details, got %#v", contributor)
	}
	if contributor.LargestRunnableModel.ID == "" || contributor.MemorySharePercent != 100 {
		t.Fatalf("expected largest model and share metrics, got %#v", contributor)
	}
	if payload.Summary.WorkersOnline != 1 {
		t.Fatalf("expected summary payload, got %#v", payload.Summary)
	}
}

func TestCapacityEndpointReportsNextUnlockTargets(t *testing.T) {
	srv := NewServer(":0", NewState())

	req := httptest.NewRequest(http.MethodGet, "/v1/capacity", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Capacity CapacitySummary `json:"capacity"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Capacity.BlockedModels == 0 {
		t.Fatalf("expected blocked catalog models without workers, got %#v", payload.Capacity)
	}
	if len(payload.Capacity.UnlockTargets) == 0 {
		t.Fatalf("expected blocked model unlock targets, got %#v", payload.Capacity)
	}
	target := payload.Capacity.UnlockTargets[0]
	if target.Model.ID == "" || target.MemoryShortBytes == 0 || target.DiskShortBytes == 0 {
		t.Fatalf("expected unlock target model shortfall, got %#v", target)
	}
}

func TestCapacitySnapshotsCompareClusterGrowth(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)

	req := httptest.NewRequest(http.MethodPost, "/v1/capacity/snapshots", bytes.NewReader([]byte(`{"label":"before"}`)))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected snapshot status 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Snapshot CapacitySnapshot `json:"snapshot"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Snapshot.ID == "" || created.Snapshot.Label != "before" {
		t.Fatalf("expected named snapshot, got %#v", created.Snapshot)
	}

	joinWorkerWithResourcesForTest(t, srv, "growth-worker", cluster.ResourceSnapshot{
		CPU:     cluster.CPUResources{CoresTotal: 12, CoresAllowed: 8},
		Memory:  cluster.MemoryResources{TotalBytes: 64 * gb, AllowedBytes: 48 * gb},
		Storage: cluster.StorageResources{TotalBytes: 256 * gb, AllowedBytes: 80 * gb, FreeBytes: 120 * gb},
	})

	req = httptest.NewRequest(http.MethodGet, "/v1/capacity?baseline="+created.Snapshot.ID, nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected capacity compare status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var compared struct {
		Capacity CapacitySummary  `json:"capacity"`
		Baseline CapacitySnapshot `json:"baseline"`
		Delta    CapacityDelta    `json:"delta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &compared); err != nil {
		t.Fatal(err)
	}
	if compared.Delta.BaselineID != created.Snapshot.ID {
		t.Fatalf("expected baseline id in delta, got %#v", compared.Delta)
	}
	if compared.Delta.WorkersOnlineDelta != 1 || compared.Delta.AllowedMemoryBytesDelta != int64(48*gb) {
		t.Fatalf("expected worker and RAM growth, got %#v", compared.Delta)
	}
	if compared.Delta.SingleWorkerRunnableDelta <= 0 || len(compared.Delta.NewSingleWorkerRunnableModels) == 0 {
		t.Fatalf("expected newly runnable model growth, got %#v", compared.Delta)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/capacity/snapshots", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected snapshot list status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), created.Snapshot.ID) {
		t.Fatalf("expected snapshot list to include created snapshot, got %s", rec.Body.String())
	}
}

func TestDashboardShowsSchedulerTab(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	_, err := state.CreateJob(jobs.CreateRequest{
		Type: "compute.matrix_multiply",
		Requirements: jobs.Requirements{
			CPUCores: 4,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected dashboard status 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, expected := range []string{
		"Scheduler",
		"active decisions",
		"waiting for capable worker",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected dashboard to contain %q", expected)
		}
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

func TestModelCatalogEndpointSupportsFiltersAndSort(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	joinWorkerWithResourcesForTest(t, srv, "catalog-worker", cluster.ResourceSnapshot{
		CPU:     cluster.CPUResources{CoresTotal: 12, CoresAllowed: 8},
		Memory:  cluster.MemoryResources{TotalBytes: 64 * gb, AllowedBytes: 48 * gb},
		Storage: cluster.StorageResources{TotalBytes: 256 * gb, AllowedBytes: 80 * gb, FreeBytes: 120 * gb},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models?q=qwen&family=qwen&capable=true&sort=ram-desc", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Models  []ModelSummary      `json:"models"`
		Total   int                 `json:"total"`
		Count   int                 `json:"count"`
		Filters ModelCatalogFilters `json:"filters"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Total <= payload.Count || payload.Count == 0 {
		t.Fatalf("expected filtered subset, got total=%d count=%d", payload.Total, payload.Count)
	}
	if payload.Filters.Query != "qwen" || payload.Filters.Family != "qwen" || !payload.Filters.CapableOnly || payload.Filters.Sort != "ram-desc" {
		t.Fatalf("expected response filters to echo query, got %#v", payload.Filters)
	}
	for i, summary := range payload.Models {
		if !strings.Contains(strings.ToLower(summary.Model.ID+" "+summary.Model.Name+" "+summary.Model.Family), "qwen") {
			t.Fatalf("expected qwen model, got %#v", summary.Model)
		}
		if summary.CapableNodes == 0 {
			t.Fatalf("expected capable model, got %#v", summary)
		}
		if i > 0 && payload.Models[i-1].Model.MemoryBytes < summary.Model.MemoryBytes {
			t.Fatalf("expected RAM desc sort, got %s before %s", payload.Models[i-1].Model.ID, summary.Model.ID)
		}
	}
}

func TestModelDetailEndpoint(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	joinWorkerWithResourcesForTest(t, srv, "detail-worker", cluster.ResourceSnapshot{
		CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
		Memory:  cluster.MemoryResources{TotalBytes: 16 * gb, AllowedBytes: 8 * gb},
		Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 16 * gb, FreeBytes: 80 * gb},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models/qwen2.5-0.5b-instruct-q4-k-m", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Model ModelSummary `json:"model"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Model.Model.ID != "qwen2.5-0.5b-instruct-q4-k-m" {
		t.Fatalf("expected qwen model detail, got %#v", payload.Model.Model)
	}
	if len(payload.Model.Capabilities) == 0 || payload.Model.CapableNodes == 0 {
		t.Fatalf("expected model detail capabilities, got %#v", payload.Model)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/models/qwen2.5-0.5b-instruct-q4-k-m", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status 405, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestModelPlacementEndpoint(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	joinWorkerWithResourcesForTest(t, srv, "placement-worker", cluster.ResourceSnapshot{
		CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
		Memory:  cluster.MemoryResources{TotalBytes: 16 * gb, AllowedBytes: 8 * gb},
		Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 16 * gb, FreeBytes: 80 * gb},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models/qwen2.5-0.5b-instruct-q4-k-m/placement", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Placement ModelPlacementPlan `json:"placement"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Placement.ModelID != "qwen2.5-0.5b-instruct-q4-k-m" {
		t.Fatalf("expected placement model id, got %#v", payload.Placement)
	}
	if payload.Placement.Mode != "single_worker" || !payload.Placement.RunnableNow {
		t.Fatalf("expected runnable single-worker placement, got %#v", payload.Placement)
	}
}

func TestModelInstallExplainsNoEligibleWorker(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	joinWorkerWithResourcesForTest(t, srv, "tiny-worker", cluster.ResourceSnapshot{
		CPU:     cluster.CPUResources{CoresTotal: 2, CoresAllowed: 1},
		Memory:  cluster.MemoryResources{TotalBytes: 2 * gb, AllowedBytes: 1 * gb},
		Storage: cluster.StorageResources{TotalBytes: 8 * gb, AllowedBytes: 1 * gb, FreeBytes: 1 * gb},
	})

	installReq := httptest.NewRequest(http.MethodPost, "/v1/models/gemma-3-12b-it-q4-k-m/install", bytes.NewReader([]byte(`{}`)))
	installRec := httptest.NewRecorder()
	srv.ServeHTTP(installRec, installReq)
	if installRec.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d: %s", installRec.Code, installRec.Body.String())
	}
	if got := installRec.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("expected JSON conflict response, got %q", got)
	}
	var conflict modelInstallConflictResponse
	if err := json.Unmarshal(installRec.Body.Bytes(), &conflict); err != nil {
		t.Fatal(err)
	}
	if conflict.Error != "no eligible worker for model install" {
		t.Fatalf("expected structured install error, got %#v", conflict)
	}
	if !strings.Contains(conflict.Reason, "RAM short") || !strings.Contains(conflict.Reason, "disk short") {
		t.Fatalf("expected actionable eligibility explanation, got %#v", conflict)
	}
	if conflict.Placement.ModelID != "gemma-3-12b-it-q4-k-m" || conflict.Placement.Feasible {
		t.Fatalf("expected blocked placement response, got %#v", conflict.Placement)
	}
	if len(conflict.Placement.Blockers) == 0 {
		t.Fatalf("expected placement blockers, got %#v", conflict.Placement)
	}
}

func TestModelInstallRejectsWorkerWithLowFreeDisk(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	joinWorkerWithResourcesForTest(t, srv, "full-worker", cluster.ResourceSnapshot{
		CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
		Memory:  cluster.MemoryResources{TotalBytes: 16 * gb, AllowedBytes: 8 * gb},
		Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 16 * gb, FreeBytes: 512 * 1024 * 1024},
	})

	installReq := httptest.NewRequest(http.MethodPost, "/v1/models/qwen2.5-0.5b-instruct-q4-k-m/install", bytes.NewReader([]byte(`{}`)))
	installRec := httptest.NewRecorder()
	srv.ServeHTTP(installRec, installReq)
	if installRec.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d: %s", installRec.Code, installRec.Body.String())
	}
	var conflict modelInstallConflictResponse
	if err := json.Unmarshal(installRec.Body.Bytes(), &conflict); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conflict.Reason, "free disk short") {
		t.Fatalf("expected free disk explanation, got %#v", conflict)
	}
}

func TestModelInstallRejectsAlreadyInstalledWorker(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	worker := joinWorkerWithResourcesForTest(t, srv, "worker-a", cluster.ResourceSnapshot{
		CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
		Memory:  cluster.MemoryResources{TotalBytes: 16 * gb, AllowedBytes: 8 * gb},
		Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 16 * gb, FreeBytes: 16 * gb},
		Models: []cluster.ModelResource{{
			ID:    "qwen2.5-0.5b-instruct-q4-k-m",
			Name:  "Qwen2.5 0.5B Instruct",
			Ready: true,
		}},
	})

	installReq := httptest.NewRequest(http.MethodPost, "/v1/models/qwen2.5-0.5b-instruct-q4-k-m/install", bytes.NewReader([]byte(`{"node_id":"`+worker.NodeID+`"}`)))
	installRec := httptest.NewRecorder()
	srv.ServeHTTP(installRec, installReq)
	if installRec.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d: %s", installRec.Code, installRec.Body.String())
	}
	if !strings.Contains(installRec.Body.String(), "already installed") {
		t.Fatalf("expected already installed explanation, got %q", installRec.Body.String())
	}
}

func TestModelInstallRejectsActiveSameModelJobOnWorker(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	worker := joinWorkerWithResourcesForTest(t, srv, "worker-a", cluster.ResourceSnapshot{
		CPU:      cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
		Memory:   cluster.MemoryResources{TotalBytes: 16 * gb, AllowedBytes: 8 * gb},
		Storage:  cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 16 * gb, FreeBytes: 16 * gb},
		JobSlots: 2,
	})
	active, err := state.CreateJob(jobs.CreateRequest{
		Type:        models.JobInstall,
		Input:       `{"model_id":"qwen2.5-0.5b-instruct-q4-k-m"}`,
		RequestedBy: "dashboard-models",
		AssignedTo:  worker.NodeID,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if active.Status != jobs.StatusScheduled {
		t.Fatalf("expected scheduled active job, got %#v", active)
	}

	installReq := httptest.NewRequest(http.MethodPost, "/v1/models/qwen2.5-0.5b-instruct-q4-k-m/install", bytes.NewReader([]byte(`{"node_id":"`+worker.NodeID+`"}`)))
	installRec := httptest.NewRecorder()
	srv.ServeHTTP(installRec, installReq)
	if installRec.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d: %s", installRec.Code, installRec.Body.String())
	}
	if !strings.Contains(installRec.Body.String(), "model job already active") {
		t.Fatalf("expected active model job explanation, got %q", installRec.Body.String())
	}
}

func TestModelRepairCreatesJobForInstalledWorker(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	worker := joinWorkerWithResourcesForTest(t, srv, "worker-a", cluster.ResourceSnapshot{
		CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
		Memory:  cluster.MemoryResources{TotalBytes: 16 * gb, AllowedBytes: 8 * gb},
		Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 16 * gb, FreeBytes: 16 * gb},
		Models: []cluster.ModelResource{{
			ID:    "qwen2.5-0.5b-instruct-q4-k-m",
			Name:  "Qwen2.5 0.5B Instruct",
			Ready: true,
		}},
	})

	repairReq := httptest.NewRequest(http.MethodPost, "/v1/models/qwen2.5-0.5b-instruct-q4-k-m/repair", bytes.NewReader([]byte(`{"node_id":"`+worker.NodeID+`"}`)))
	repairRec := httptest.NewRecorder()
	srv.ServeHTTP(repairRec, repairReq)
	if repairRec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", repairRec.Code, repairRec.Body.String())
	}
	var job jobs.Job
	if err := json.Unmarshal(repairRec.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if job.Type != models.JobRepair || job.AssignedTo != worker.NodeID {
		t.Fatalf("unexpected repair job: %#v", job)
	}
	if !strings.Contains(job.Input, "qwen2.5-0.5b-instruct-q4-k-m") {
		t.Fatalf("expected repair input to include model id, got %q", job.Input)
	}
}

func TestJobProgressEndpointUpdatesRunningJob(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	worker := joinWorkerWithResourcesForTest(t, srv, "worker-a", cluster.ResourceSnapshot{})
	job, err := state.CreateJob(jobs.CreateRequest{
		Type:        models.JobInstall,
		Input:       `{"model_id":"qwen2.5-0.5b-instruct-q4-k-m"}`,
		RequestedBy: "test",
		AssignedTo:  worker.NodeID,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.NextJobForWorker(worker.NodeID); !ok {
		t.Fatal("expected job to start")
	}

	progressReq := httptest.NewRequest(http.MethodPost, "/v1/jobs/"+job.ID+"/progress", bytes.NewReader([]byte(`{"node_id":"`+worker.NodeID+`","progress_bytes":1073741824,"total_bytes":2147483648,"progress_percent":50,"progress_label":"Downloading model"}`)))
	progressRec := httptest.NewRecorder()
	srv.ServeHTTP(progressRec, progressReq)
	if progressRec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", progressRec.Code, progressRec.Body.String())
	}
	updated, ok := state.Job(job.ID)
	if !ok {
		t.Fatal("expected job")
	}
	if !strings.Contains(jobDetail(updated), "Downloading model 50.0%") {
		t.Fatalf("expected progress detail, got %q", jobDetail(updated))
	}
}

func TestJobDetailSummarizesProgress(t *testing.T) {
	job := jobs.Job{
		Type:   models.JobInstall,
		Status: jobs.StatusRunning,
		Result: `{"kind":"job.progress","progress_bytes":1073741824,"total_bytes":2147483648,"progress_percent":50,"progress_label":"Downloading model"}`,
	}
	got := jobDetail(job)
	if !strings.Contains(got, "Downloading model 50.0%") || !strings.Contains(got, "1.0 / 2.0 GB") {
		t.Fatalf("expected progress detail, got %q", got)
	}
}

func TestWorkerModelCleanupCreatesJob(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	worker := joinWorkerWithResourcesForTest(t, srv, "worker-a", cluster.ResourceSnapshot{
		CPU: cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
		Storage: cluster.StorageResources{
			PartialModelFiles: 1,
			PartialModelBytes: 1024,
			OrphanModelDirs:   1,
			OrphanModelBytes:  2048,
		},
	})

	cleanupReq := httptest.NewRequest(http.MethodPost, "/v1/workers/"+worker.NodeID+"/model-cleanup", bytes.NewReader([]byte(`{}`)))
	cleanupRec := httptest.NewRecorder()
	srv.ServeHTTP(cleanupRec, cleanupReq)
	if cleanupRec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", cleanupRec.Code, cleanupRec.Body.String())
	}
	var job jobs.Job
	if err := json.Unmarshal(cleanupRec.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if job.Type != models.JobCleanup || job.AssignedTo != worker.NodeID {
		t.Fatalf("unexpected cleanup job: %#v", job)
	}
	if !strings.Contains(job.Input, "cache") {
		t.Fatalf("expected cleanup scope in input, got %q", job.Input)
	}
}

func TestModelDeleteRejectsMissingInstallOnWorker(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	worker := joinWorkerWithResourcesForTest(t, srv, "worker-a", cluster.ResourceSnapshot{
		CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
		Memory:  cluster.MemoryResources{TotalBytes: 16 * gb, AllowedBytes: 8 * gb},
		Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 16 * gb, FreeBytes: 16 * gb},
	})

	deleteReq := httptest.NewRequest(http.MethodPost, "/v1/models/qwen2.5-0.5b-instruct-q4-k-m/delete", bytes.NewReader([]byte(`{"node_id":"`+worker.NodeID+`"}`)))
	deleteRec := httptest.NewRecorder()
	srv.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d: %s", deleteRec.Code, deleteRec.Body.String())
	}
	if !strings.Contains(deleteRec.Body.String(), "not installed") {
		t.Fatalf("expected not installed explanation, got %q", deleteRec.Body.String())
	}
}

func TestModelDeleteRejectsActiveModelJobOnWorker(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	worker := joinWorkerWithResourcesForTest(t, srv, "worker-a", cluster.ResourceSnapshot{
		CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
		Memory:  cluster.MemoryResources{TotalBytes: 16 * gb, AllowedBytes: 8 * gb},
		Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 16 * gb, FreeBytes: 16 * gb},
		Models: []cluster.ModelResource{{
			ID:    "qwen2.5-0.5b-instruct-q4-k-m",
			Name:  "Qwen2.5 0.5B Instruct",
			Ready: true,
		}},
	})
	input, err := json.Marshal(models.GenerateInput{
		ModelID:        "qwen2.5-0.5b-instruct-q4-k-m",
		ConversationID: "conv-active",
		Prompt:         "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := state.CreateJob(jobs.CreateRequest{
		Type:        models.JobGenerate,
		Input:       string(input),
		RequestedBy: "dashboard-chat",
		AssignedTo:  worker.NodeID,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if active.Status != jobs.StatusScheduled {
		t.Fatalf("expected scheduled active job, got %#v", active)
	}

	deleteReq := httptest.NewRequest(http.MethodPost, "/v1/models/qwen2.5-0.5b-instruct-q4-k-m/delete", bytes.NewReader([]byte(`{"node_id":"`+worker.NodeID+`"}`)))
	deleteRec := httptest.NewRecorder()
	srv.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d: %s", deleteRec.Code, deleteRec.Body.String())
	}
	if !strings.Contains(deleteRec.Body.String(), "active job") {
		t.Fatalf("expected active job explanation, got %q", deleteRec.Body.String())
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
