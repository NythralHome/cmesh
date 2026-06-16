package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cmesh/cmesh/internal/cluster"
	"github.com/cmesh/cmesh/internal/config"
	"github.com/cmesh/cmesh/internal/jobs"
	"github.com/cmesh/cmesh/internal/manager"
	"github.com/cmesh/cmesh/internal/membership"
	"github.com/cmesh/cmesh/internal/models"
	"github.com/cmesh/cmesh/internal/resources"
	"github.com/cmesh/cmesh/internal/version"
	"github.com/cmesh/cmesh/internal/workercontrol"
	"github.com/cmesh/cmesh/internal/workerstatus"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "cmesh: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}

	switch args[0] {
	case "version":
		fmt.Println(version.String())
	case "manager":
		return runManager(args[1:])
	case "worker":
		return runWorker(args[1:])
	case "job":
		return runJob(args[1:])
	case "dev":
		return runDev(args[1:])
	case "help", "--help", "-h":
		printUsage()
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}

	return nil
}

func runManager(args []string) error {
	if len(args) == 0 {
		printManagerUsage()
		return nil
	}

	switch args[0] {
	case "start":
		fs := flag.NewFlagSet("manager start", flag.ContinueOnError)
		addr := fs.String("addr", ":8080", "HTTP listen address")
		joinToken := fs.String("join-token", os.Getenv("CMESH_JOIN_TOKEN"), "worker join token")
		operatorToken := fs.String("operator-token", os.Getenv("CMESH_OPERATOR_TOKEN"), "operator token for protected dashboard actions")
		publicURL := fs.String("public-url", os.Getenv("CMESH_PUBLIC_URL"), "public manager URL used in generated worker invites")
		databaseURL := fs.String("database-url", os.Getenv("DATABASE_URL"), "Postgres database URL")
		statePath := fs.String("state-path", defaultStatePath(), "local manager state file path")
		memoryState := fs.Bool("memory", false, "use in-memory manager state")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		var state manager.Store = manager.NewState()
		if *memoryState {
			fmt.Println("manager storage: in-memory")
		} else if *databaseURL != "" {
			postgresStore, err := manager.NewPostgresStore(ctx, *databaseURL)
			if err != nil {
				return err
			}
			defer postgresStore.Close()
			state = postgresStore
			fmt.Println("manager storage: postgres")
		} else {
			fileStore, err := manager.NewFileStore(*statePath)
			if err != nil {
				return err
			}
			state = fileStore
			fmt.Printf("manager storage: file (%s)\n", *statePath)
		}
		server := manager.NewServerWithOptions(manager.ServerOptions{
			Addr:          *addr,
			JoinToken:     *joinToken,
			OperatorToken: *operatorToken,
			PublicURL:     *publicURL,
		}, state)
		fmt.Println("starting CMesh manager in single-node bootstrap mode")
		fmt.Printf("manager API: %s\n", localHTTPURL(*addr))
		fmt.Printf("dashboard:   %s\n", localHTTPURL(*addr))
		if *joinToken == "" {
			fmt.Println("warning: manager join token is not set; any worker can join")
		}
		return server.Start(ctx)
	case "join":
		fmt.Println("manager join is reserved for the future multi-manager consensus flow")
	case "help", "--help", "-h":
		printManagerUsage()
	default:
		return fmt.Errorf("unknown manager command %q", args[0])
	}

	return nil
}

func localHTTPURL(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "http://127.0.0.1" + addr
	}
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return addr
	}
	return "http://" + addr
}

func runWorker(args []string) error {
	if len(args) == 0 {
		printWorkerUsage()
		return nil
	}

	switch args[0] {
	case "join":
		options, err := parseWorkerOptions("worker join", args[1:])
		if err != nil {
			return err
		}
		return workerJoinOnce(options)
	case "run":
		options, err := parseWorkerOptions("worker run", args[1:])
		if err != nil {
			return err
		}

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return workerRun(ctx, options)
	case "benchmark":
		fs := flag.NewFlagSet("worker benchmark", flag.ContinueOnError)
		managerURL := fs.String("manager", "http://127.0.0.1:8080", "manager API URL")
		nodeID := fs.String("node-id", "", "existing worker node ID for manager submission")
		cacheDir := fs.String("cache-dir", defaultCacheDir(), "worker artifact cache directory")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return workerBenchmark(strings.TrimRight(*managerURL, "/"), *nodeID, *cacheDir)
	case "control":
		fs := flag.NewFlagSet("worker control", flag.ContinueOnError)
		addr := fs.String("addr", "127.0.0.1:9781", "local worker control API listen address")
		configPath := fs.String("config", os.Getenv("CMESH_WORKER_CONTROL_CONFIG"), "local worker control config path")
		token := fs.String("token", os.Getenv("CMESH_WORKER_CONTROL_TOKEN"), "local worker control API token")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		server, err := workercontrol.NewServerWithToken(*addr, *configPath, *token)
		if err != nil {
			return err
		}
		return server.Start(ctx)
	case "help", "--help", "-h":
		printWorkerUsage()
	default:
		return fmt.Errorf("unknown worker command %q", args[0])
	}

	return nil
}

func printUsage() {
	fmt.Println(`CMesh decentralized-ready AI compute cluster

Usage:
  cmesh manager start       Start a manager node
  cmesh worker join         Join a cluster as a worker
  cmesh worker run          Join and keep a worker heartbeat running
  cmesh worker benchmark    Run worker benchmarks
  cmesh worker control      Start local worker control API
  cmesh job submit          Submit a job
  cmesh job list            List jobs
  cmesh dev local-cluster   Register multiple local test workers
  cmesh version             Print version

Use "cmesh <command> help" for command-specific help.`)
}

func printManagerUsage() {
	fmt.Println(`Usage:
  cmesh manager start       Start a single-manager bootstrap node
  cmesh manager join        Join an existing manager quorum`)
}

func printWorkerUsage() {
	fmt.Println(`Usage:
  cmesh worker join         Join a cluster as a worker
  cmesh worker run          Join and keep sending heartbeats
  cmesh worker benchmark    Run CPU, memory, disk, network, and AI benchmarks
  cmesh worker control      Start local worker control API for desktop apps`)
}

type workerOptions struct {
	managerURL   string
	name         string
	token        string
	cacheDir     string
	limits       config.ResourceLimits
	runBenchmark bool
	runOnce      bool
}

func parseWorkerOptions(name string, args []string) (workerOptions, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	managerURL := fs.String("manager", "http://127.0.0.1:8080", "manager API URL")
	nodeName := fs.String("name", defaultNodeName(), "worker display name")
	token := fs.String("token", "", "cluster join token")
	cacheDir := fs.String("cache-dir", defaultCacheDir(), "worker artifact cache directory")
	cpuAllowed := fs.Int("cpu", runtime.NumCPU(), "allowed CPU cores")
	memoryGB := fs.Uint64("memory-gb", 2, "allowed memory in GB")
	diskGB := fs.Uint64("disk-gb", 10, "allowed disk in GB")
	gpu := fs.Bool("gpu", true, "allow GPU discovery and use")
	vramGB := fs.Uint64("vram-gb", 0, "allowed VRAM in GB")
	jobSlots := fs.Int("job-slots", 1, "maximum active jobs this worker can accept")
	benchmark := fs.Bool("benchmark", false, "run benchmarks after joining")
	once := fs.Bool("once", false, "join, send one heartbeat, optionally benchmark, then exit")
	if err := fs.Parse(args); err != nil {
		return workerOptions{}, err
	}

	return workerOptions{
		managerURL:   strings.TrimRight(*managerURL, "/"),
		name:         *nodeName,
		token:        *token,
		cacheDir:     *cacheDir,
		runBenchmark: *benchmark,
		runOnce:      *once,
		limits: config.ResourceLimits{
			CPUCores:    *cpuAllowed,
			MemoryBytes: gbToBytes(*memoryGB),
			DiskBytes:   gbToBytes(*diskGB),
			GPUEnabled:  *gpu,
			VRAMBytes:   gbToBytes(*vramGB),
			JobSlots:    *jobSlots,
		},
	}, nil
}

func workerJoinOnce(options workerOptions) error {
	resp, err := joinWorker(options.managerURL, workerJoinRequest(options))
	if err != nil {
		return err
	}

	fmt.Printf("joined cluster as %s\n", resp.NodeID)
	fmt.Printf("manager peers: %v\n", resp.ManagerPeers)
	return nil
}

func workerRun(ctx context.Context, options workerOptions) error {
	resp, err := joinWorker(options.managerURL, workerJoinRequest(options))
	if err != nil {
		return err
	}

	heartbeatEvery := resp.HeartbeatEvery
	if heartbeatEvery <= 0 {
		heartbeatEvery = 10 * time.Second
	}

	fmt.Printf("worker %s joined as %s\n", options.name, resp.NodeID)
	fmt.Printf("heartbeat every %s\n", heartbeatEvery)
	defer func() {
		if err := sendLeave(options.managerURL, resp.NodeID); err != nil {
			fmt.Fprintf(os.Stderr, "failed to mark worker offline: %v\n", err)
		}
	}()

	ticker := time.NewTicker(heartbeatEvery)
	defer ticker.Stop()

	snapshot := discoverWorkerResources(options)
	if err := sendHeartbeat(options.managerURL, resp.NodeID, snapshot); err != nil {
		return err
	}
	if options.runBenchmark {
		if err := runAndSubmitBenchmarks(options.managerURL, resp.NodeID, options.cacheDir); err != nil {
			return err
		}
	}
	if options.runOnce {
		if _, err := pollAndExecuteJob(options.managerURL, resp.NodeID, options.cacheDir, snapshot); err != nil {
			return err
		}
		fmt.Printf("worker %s completed one-shot run\n", resp.NodeID)
		return nil
	}

	jobRunner := newWorkerJobRunner(options.managerURL, resp.NodeID, options.cacheDir, snapshot.JobSlots)
	if err := jobRunner.PollAvailable(snapshot); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := jobRunner.LastError(); err != nil {
				return err
			}
			snapshot := discoverWorkerResources(options)
			if err := sendHeartbeat(options.managerURL, resp.NodeID, snapshot); err != nil {
				return err
			}
			if err := jobRunner.PollAvailable(snapshot); err != nil {
				return err
			}
			fmt.Printf("heartbeat sent for %s\n", resp.NodeID)
		}
	}
}

func runJob(args []string) error {
	if len(args) == 0 {
		printJobUsage()
		return nil
	}

	switch args[0] {
	case "submit":
		fs := flag.NewFlagSet("job submit", flag.ContinueOnError)
		managerURL := fs.String("manager", "http://127.0.0.1:8080", "manager API URL")
		operatorToken := fs.String("operator-token", os.Getenv("CMESH_OPERATOR_TOKEN"), "manager operator token")
		jobType := fs.String("type", "echo", "job type")
		input := fs.String("input", "", "job input")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		job, err := submitJob(strings.TrimRight(*managerURL, "/"), *operatorToken, jobs.CreateRequest{
			Type:        *jobType,
			Input:       *input,
			RequestedBy: defaultNodeName(),
		})
		if err != nil {
			return err
		}
		fmt.Printf("submitted %s status=%s assigned_to=%s\n", job.ID, job.Status, job.AssignedTo)
	case "submit-compute":
		fs := flag.NewFlagSet("job submit-compute", flag.ContinueOnError)
		managerURL := fs.String("manager", "http://127.0.0.1:8080", "manager API URL")
		operatorToken := fs.String("operator-token", os.Getenv("CMESH_OPERATOR_TOKEN"), "manager operator token")
		size := fs.Int("size", 192, "square matrix size")
		iterations := fs.Int("iterations", 3, "number of matrix multiply iterations")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		input, err := newMatrixMultiplyInput(*size, *iterations)
		if err != nil {
			return err
		}
		job, err := submitJob(strings.TrimRight(*managerURL, "/"), *operatorToken, jobs.CreateRequest{
			Type:        "compute.matrix_multiply",
			Input:       input,
			RequestedBy: defaultNodeName(),
		})
		if err != nil {
			return err
		}
		fmt.Printf("submitted %s type=%s status=%s assigned_to=%s input=%s\n", job.ID, job.Type, job.Status, job.AssignedTo, job.Input)
	case "list":
		fs := flag.NewFlagSet("job list", flag.ContinueOnError)
		managerURL := fs.String("manager", "http://127.0.0.1:8080", "manager API URL")
		operatorToken := fs.String("operator-token", os.Getenv("CMESH_OPERATOR_TOKEN"), "manager operator token")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return listJobs(strings.TrimRight(*managerURL, "/"), *operatorToken)
	case "get":
		fs := flag.NewFlagSet("job get", flag.ContinueOnError)
		managerURL := fs.String("manager", "http://127.0.0.1:8080", "manager API URL")
		operatorToken := fs.String("operator-token", os.Getenv("CMESH_OPERATOR_TOKEN"), "manager operator token")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return fmt.Errorf("job id is required")
		}
		return getJob(strings.TrimRight(*managerURL, "/"), *operatorToken, fs.Arg(0))
	case "help", "--help", "-h":
		printJobUsage()
	default:
		return fmt.Errorf("unknown job command %q", args[0])
	}

	return nil
}

func printJobUsage() {
	fmt.Println(`Usage:
  cmesh job submit --type echo --input "hello" [--operator-token token]
  cmesh job submit-compute --size 192 --iterations 3 [--operator-token token]
  cmesh job list [--operator-token token]
  cmesh job get <job-id> [--operator-token token]`)
}

func workerBenchmark(managerURL string, nodeID string, cacheDir string) error {
	results, err := resources.RunLocalBenchmarks(resources.BenchmarkOptions{
		NodeID:   nodeID,
		CacheDir: cacheDir,
	})
	if err != nil {
		return err
	}

	for _, result := range results {
		fmt.Printf("%s: %.2f %s (%s)\n", result.Kind, result.Score, result.Unit, result.Duration)
	}

	if nodeID == "" {
		fmt.Println("not submitted: pass --node-id to attach results to a registered worker")
		return nil
	}

	for _, result := range results {
		if err := submitBenchmark(managerURL, result); err != nil {
			return err
		}
	}

	fmt.Printf("submitted %d benchmark results for %s\n", len(results), nodeID)
	return nil
}

func runAndSubmitBenchmarks(managerURL string, nodeID string, cacheDir string) error {
	results, err := resources.RunLocalBenchmarks(resources.BenchmarkOptions{
		NodeID:   nodeID,
		CacheDir: cacheDir,
	})
	if err != nil {
		return err
	}

	for _, result := range results {
		fmt.Printf("%s benchmark: %.2f %s\n", result.Kind, result.Score, result.Unit)
		if err := submitBenchmark(managerURL, result); err != nil {
			return err
		}
	}

	return nil
}

func workerJoinRequest(options workerOptions) membership.JoinRequest {
	return membership.JoinRequest{
		NodeName:  options.name,
		Role:      cluster.NodeRoleWorker,
		JoinToken: options.token,
		Resources: discoverWorkerResources(options),
	}
}

func discoverWorkerResources(options workerOptions) cluster.ResourceSnapshot {
	return resources.DiscoverLocal(resources.DiscoveryOptions{
		Limits:   options.limits,
		CacheDir: options.cacheDir,
	})
}

func joinWorker(managerURL string, req membership.JoinRequest) (membership.JoinResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return membership.JoinResponse{}, err
	}

	httpResp, err := http.Post(managerURL+"/v1/workers/join", "application/json", bytes.NewReader(body))
	if err != nil {
		return membership.JoinResponse{}, err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return membership.JoinResponse{}, fmt.Errorf("manager returned %s", httpResp.Status)
	}

	var resp membership.JoinResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return membership.JoinResponse{}, err
	}

	return resp, nil
}

func sendHeartbeat(managerURL string, nodeID string, snapshot cluster.ResourceSnapshot) error {
	body, err := json.Marshal(membership.Heartbeat{
		NodeID:    nodeID,
		At:        time.Now().UTC(),
		Resources: snapshot,
	})
	if err != nil {
		return err
	}

	httpResp, err := http.Post(managerURL+"/v1/workers/heartbeat", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return fmt.Errorf("manager returned %s", httpResp.Status)
	}

	return nil
}

func sendLeave(managerURL string, nodeID string) error {
	body, err := json.Marshal(membership.LeaveRequest{
		NodeID: nodeID,
		At:     time.Now().UTC(),
	})
	if err != nil {
		return err
	}

	httpResp, err := http.Post(managerURL+"/v1/workers/leave", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return fmt.Errorf("manager returned %s", httpResp.Status)
	}

	return nil
}

func submitBenchmark(managerURL string, result resources.BenchmarkResult) error {
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}

	httpResp, err := http.Post(managerURL+"/v1/benchmarks", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return fmt.Errorf("manager returned %s", httpResp.Status)
	}

	return nil
}

func submitJob(managerURL string, operatorToken string, req jobs.CreateRequest) (jobs.Job, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return jobs.Job{}, err
	}

	httpReq, err := http.NewRequest(http.MethodPost, managerURL+"/v1/jobs", bytes.NewReader(body))
	if err != nil {
		return jobs.Job{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	setOperatorToken(httpReq, operatorToken)
	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return jobs.Job{}, err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return jobs.Job{}, fmt.Errorf("manager returned %s", httpResp.Status)
	}

	var job jobs.Job
	if err := json.NewDecoder(httpResp.Body).Decode(&job); err != nil {
		return jobs.Job{}, err
	}
	return job, nil
}

func listJobs(managerURL string, operatorToken string) error {
	httpReq, err := http.NewRequest(http.MethodGet, managerURL+"/v1/jobs", nil)
	if err != nil {
		return err
	}
	setOperatorToken(httpReq, operatorToken)
	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return fmt.Errorf("manager returned %s", httpResp.Status)
	}

	var resp struct {
		Jobs []jobs.Job `json:"jobs"`
	}
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return err
	}

	for _, job := range resp.Jobs {
		fmt.Printf("%s type=%s status=%s assigned_to=%s result=%q error=%q\n", job.ID, job.Type, job.Status, job.AssignedTo, job.Result, job.Error)
	}
	return nil
}

func getJob(managerURL string, operatorToken string, jobID string) error {
	httpReq, err := http.NewRequest(http.MethodGet, managerURL+"/v1/jobs/"+jobID, nil)
	if err != nil {
		return err
	}
	setOperatorToken(httpReq, operatorToken)
	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return fmt.Errorf("manager returned %s", httpResp.Status)
	}

	var job jobs.Job
	if err := json.NewDecoder(httpResp.Body).Decode(&job); err != nil {
		return err
	}

	fmt.Printf("%s type=%s status=%s assigned_to=%s input=%q result=%q error=%q\n", job.ID, job.Type, job.Status, job.AssignedTo, job.Input, job.Result, job.Error)
	return nil
}

func setOperatorToken(req *http.Request, token string) {
	if token == "" {
		return
	}
	req.Header.Set("X-CMesh-Operator-Token", token)
}

type workerJobRunner struct {
	managerURL string
	nodeID     string
	cacheDir   string
	slots      int

	mu      sync.Mutex
	active  int
	lastErr error
}

func newWorkerJobRunner(managerURL string, nodeID string, cacheDir string, slots int) *workerJobRunner {
	if slots <= 0 {
		slots = 1
	}
	return &workerJobRunner{
		managerURL: managerURL,
		nodeID:     nodeID,
		cacheDir:   cacheDir,
		slots:      slots,
	}
}

func (r *workerJobRunner) PollAvailable(snapshot cluster.ResourceSnapshot) error {
	r.mu.Lock()
	available := r.slots - r.active
	if r.lastErr != nil {
		err := r.lastErr
		r.mu.Unlock()
		return err
	}
	if available <= 0 {
		r.mu.Unlock()
		return nil
	}
	r.active += available
	r.mu.Unlock()

	for range available {
		go r.pollOne(snapshot)
	}
	return nil
}

func (r *workerJobRunner) LastError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastErr
}

func (r *workerJobRunner) pollOne(snapshot cluster.ResourceSnapshot) {
	_, err := pollAndExecuteJob(r.managerURL, r.nodeID, r.cacheDir, snapshot)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active--
	if err != nil && r.lastErr == nil {
		r.lastErr = err
	}
}

func pollAndExecuteJob(managerURL string, nodeID string, cacheDir string, snapshot cluster.ResourceSnapshot) (bool, error) {
	httpResp, err := http.Get(managerURL + "/v1/workers/" + nodeID + "/jobs/next")
	if err != nil {
		return false, err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return false, fmt.Errorf("manager returned %s", httpResp.Status)
	}

	var resp struct {
		Job *jobs.Job `json:"job"`
	}
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return false, err
	}
	if resp.Job == nil {
		if err := workerstatus.MarkIdle(cacheDir, nodeID); err != nil {
			fmt.Fprintf(os.Stderr, "failed to write worker job status: %v\n", err)
		}
		return false, nil
	}

	startedAt := time.Now().UTC()
	if err := workerstatus.Write(cacheDir, workerstatus.JobStatus{
		State:     "running",
		NodeID:    nodeID,
		JobID:     resp.Job.ID,
		Type:      resp.Job.Type,
		Input:     resp.Job.Input,
		StartedAt: &startedAt,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write worker job status: %v\n", err)
	}

	result, jobErr := executeJobWithRuntime(*resp.Job, snapshot, cacheDir)
	complete := jobs.CompleteRequest{
		NodeID: nodeID,
		Result: result,
	}
	if jobErr != nil {
		complete.Error = jobErr.Error()
	}

	body, err := json.Marshal(complete)
	if err != nil {
		return true, err
	}

	completeResp, err := http.Post(managerURL+"/v1/jobs/"+resp.Job.ID+"/complete", "application/json", bytes.NewReader(body))
	if err != nil {
		return true, err
	}
	defer completeResp.Body.Close()

	if completeResp.StatusCode < 200 || completeResp.StatusCode >= 300 {
		return true, fmt.Errorf("manager returned %s", completeResp.Status)
	}

	finishedAt := time.Now().UTC()
	state := "succeeded"
	errorText := ""
	if jobErr != nil {
		state = "failed"
		errorText = jobErr.Error()
	}
	if err := workerstatus.Write(cacheDir, workerstatus.JobStatus{
		State:      state,
		NodeID:     nodeID,
		JobID:      resp.Job.ID,
		Type:       resp.Job.Type,
		Input:      resp.Job.Input,
		Result:     result,
		Error:      errorText,
		StartedAt:  &startedAt,
		FinishedAt: &finishedAt,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write worker job status: %v\n", err)
	}

	if jobErr != nil {
		fmt.Printf("job %s failed: %v\n", resp.Job.ID, jobErr)
	} else {
		fmt.Printf("job %s completed\n", resp.Job.ID)
	}
	return true, nil
}

func executeJob(job jobs.Job) (string, error) {
	return executeJobWithResources(job, cluster.ResourceSnapshot{})
}

func executeJobWithResources(job jobs.Job, snapshot cluster.ResourceSnapshot) (string, error) {
	return executeJobWithRuntime(job, snapshot, defaultCacheDir())
}

func executeJobWithRuntime(job jobs.Job, snapshot cluster.ResourceSnapshot, cacheDir string) (string, error) {
	if err := validateWorkerCanRunJob(job, snapshot); err != nil {
		return "", err
	}
	switch job.Type {
	case "echo":
		return job.Input, nil
	case "compute.matrix_multiply":
		return executeMatrixMultiplyJob(job.Input)
	case models.JobInstall:
		return executeModelInstallJob(job.Input, cacheDir)
	case models.JobDelete:
		return executeModelDeleteJob(job.Input, cacheDir)
	case models.JobGenerate:
		return executeModelGenerateJob(job.Input, cacheDir)
	default:
		return "", fmt.Errorf("unsupported job type %q", job.Type)
	}
}

func validateWorkerCanRunJob(job jobs.Job, snapshot cluster.ResourceSnapshot) error {
	req := job.Requirements
	if req.CPUCores > 0 && snapshot.CPU.CoresAllowed > 0 && snapshot.CPU.CoresAllowed < req.CPUCores {
		return fmt.Errorf("worker resource guard rejected job: requires %d CPU cores, worker allows %d", req.CPUCores, snapshot.CPU.CoresAllowed)
	}
	if req.MemoryBytes > 0 && snapshot.Memory.AllowedBytes > 0 && snapshot.Memory.AllowedBytes < req.MemoryBytes {
		return fmt.Errorf("worker resource guard rejected job: requires %.1f GB RAM, worker allows %.1f GB", bytesToGB(req.MemoryBytes), bytesToGB(snapshot.Memory.AllowedBytes))
	}
	if req.DiskBytes > 0 && snapshot.Storage.AllowedBytes > 0 && snapshot.Storage.AllowedBytes < req.DiskBytes {
		return fmt.Errorf("worker resource guard rejected job: requires %.1f GB disk, worker allows %.1f GB", bytesToGB(req.DiskBytes), bytesToGB(snapshot.Storage.AllowedBytes))
	}
	if !req.GPURequired && req.VRAMBytes == 0 {
		return nil
	}
	for _, gpu := range snapshot.GPU {
		if !gpu.ComputeCompatible {
			continue
		}
		if req.VRAMBytes == 0 || gpu.AllowedVRAMBytes >= req.VRAMBytes {
			return nil
		}
	}
	if req.VRAMBytes > 0 {
		return fmt.Errorf("worker resource guard rejected job: requires compute GPU with %.1f GB VRAM", bytesToGB(req.VRAMBytes))
	}
	return fmt.Errorf("worker resource guard rejected job: requires compute GPU")
}

type modelInstallResult struct {
	Kind          string `json:"kind"`
	ModelID       string `json:"model_id"`
	ModelName     string `json:"model_name"`
	Path          string `json:"path"`
	Bytes         int64  `json:"bytes"`
	Runtime       string `json:"runtime"`
	WorkerRuntime string `json:"worker_runtime"`
}

type modelDeleteResult struct {
	Kind          string `json:"kind"`
	ModelID       string `json:"model_id"`
	Removed       bool   `json:"removed"`
	Path          string `json:"path"`
	WorkerRuntime string `json:"worker_runtime"`
}

type modelGenerateResult struct {
	Kind          string `json:"kind"`
	ModelID       string `json:"model_id"`
	Output        string `json:"output"`
	Tokens        int    `json:"tokens,omitempty"`
	WorkerRuntime string `json:"worker_runtime"`
}

func executeModelInstallJob(input string, cacheDir string) (string, error) {
	var req models.InstallInput
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		return "", fmt.Errorf("invalid model install input: %w", err)
	}
	model, err := models.MustFind(req.ModelID)
	if err != nil {
		return "", err
	}
	path := modelPath(cacheDir, model)
	if stat, err := os.Stat(path); err == nil && stat.Size() > 0 {
		return marshalModelInstallResult(model, path, stat.Size())
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}

	tmp := path + ".tmp"
	resp, err := http.Get(model.URL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("model download returned %s", resp.Status)
	}
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	bytesWritten, copyErr := io.Copy(out, resp.Body)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return "", copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return "", closeErr
	}
	if bytesWritten == 0 {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("model download wrote 0 bytes")
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return marshalModelInstallResult(model, path, bytesWritten)
}

func marshalModelInstallResult(model models.Model, path string, size int64) (string, error) {
	result := modelInstallResult{
		Kind:          string(models.JobInstall),
		ModelID:       model.ID,
		ModelName:     model.Name,
		Path:          path,
		Bytes:         size,
		Runtime:       string(model.Runtime),
		WorkerRuntime: runtime.GOOS + "/" + runtime.GOARCH,
	}
	body, err := json.Marshal(result)
	return string(body), err
}

func executeModelDeleteJob(input string, cacheDir string) (string, error) {
	var req models.DeleteInput
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		return "", fmt.Errorf("invalid model delete input: %w", err)
	}
	model, err := models.MustFind(req.ModelID)
	if err != nil {
		return "", err
	}
	path := modelPath(cacheDir, model)
	err = os.Remove(path)
	removed := err == nil
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	result := modelDeleteResult{
		Kind:          string(models.JobDelete),
		ModelID:       model.ID,
		Removed:       removed,
		Path:          path,
		WorkerRuntime: runtime.GOOS + "/" + runtime.GOARCH,
	}
	body, err := json.Marshal(result)
	return string(body), err
}

func executeModelGenerateJob(input string, cacheDir string) (string, error) {
	var req models.GenerateInput
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		return "", fmt.Errorf("invalid model generate input: %w", err)
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return "", fmt.Errorf("prompt is required")
	}
	model, err := models.MustFind(req.ModelID)
	if err != nil {
		return "", err
	}
	path := modelPath(cacheDir, model)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("model %s is not installed on this worker", model.ID)
		}
		return "", err
	}
	cli, err := findLlamaCLI()
	if err != nil {
		return "", fmt.Errorf("model %s is installed, but llama-cli is not available. Install llama.cpp or add llama-cli to PATH", model.ID)
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 || maxTokens > 2048 {
		maxTokens = 256
	}
	temperature := strings.TrimSpace(req.Temperature)
	if temperature == "" {
		temperature = "0.7"
	}
	cmd := exec.Command(cli, "-m", path, "-p", req.Prompt, "-n", strconv.Itoa(maxTokens), "--temp", temperature)
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("llama-cli failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	text := strings.TrimSpace(string(output))
	result := modelGenerateResult{
		Kind:          string(models.JobGenerate),
		ModelID:       model.ID,
		Output:        text,
		WorkerRuntime: runtime.GOOS + "/" + runtime.GOARCH,
	}
	body, err := json.Marshal(result)
	return string(body), err
}

func findLlamaCLI() (string, error) {
	if cli, err := exec.LookPath("llama-cli"); err == nil {
		return cli, nil
	}
	candidates := []string{
		"/opt/homebrew/bin/llama-cli",
		"/usr/local/bin/llama-cli",
		"/opt/local/bin/llama-cli",
		"/usr/bin/llama-cli",
	}
	if runtime.GOOS == "windows" {
		candidates = append([]string{
			`C:\Program Files\llama.cpp\llama-cli.exe`,
			`C:\Program Files\CMesh\llama-cli.exe`,
		}, candidates...)
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", exec.ErrNotFound
}

func modelPath(cacheDir string, model models.Model) string {
	if strings.TrimSpace(cacheDir) == "" {
		cacheDir = defaultCacheDir()
	}
	return filepath.Join(cacheDir, "models", model.ID, model.File)
}

type matrixMultiplyInput struct {
	Size       int `json:"size"`
	Iterations int `json:"iterations"`
}

type matrixMultiplyResult struct {
	Kind          string  `json:"kind"`
	Size          int     `json:"size"`
	Iterations    int     `json:"iterations"`
	Operations    int64   `json:"operations"`
	DurationMS    int64   `json:"duration_ms"`
	GFLOPS        float64 `json:"gflops"`
	Checksum      float64 `json:"checksum"`
	WorkerRuntime string  `json:"worker_runtime"`
}

func newMatrixMultiplyInput(size int, iterations int) (string, error) {
	input := matrixMultiplyInput{Size: size, Iterations: iterations}
	if err := validateMatrixMultiplyInput(input); err != nil {
		return "", err
	}
	body, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func executeMatrixMultiplyJob(input string) (string, error) {
	var req matrixMultiplyInput
	if strings.TrimSpace(input) == "" {
		req = matrixMultiplyInput{Size: 192, Iterations: 3}
	} else if err := json.Unmarshal([]byte(input), &req); err != nil {
		return "", fmt.Errorf("invalid matrix multiply input: %w", err)
	}
	if err := validateMatrixMultiplyInput(req); err != nil {
		return "", err
	}

	a := make([]float64, req.Size*req.Size)
	b := make([]float64, req.Size*req.Size)
	c := make([]float64, req.Size*req.Size)
	for i := range a {
		a[i] = math.Sin(float64(i%97)) * 0.5
		b[i] = math.Cos(float64(i%89)) * 0.5
	}

	start := time.Now()
	for iteration := 0; iteration < req.Iterations; iteration++ {
		for i := range c {
			c[i] = 0
		}
		multiplySquareMatrices(a, b, c, req.Size)
	}
	duration := time.Since(start)

	var checksum float64
	for i, value := range c {
		if i%req.Size == 0 {
			checksum += value
		}
	}
	operations := int64(2) * int64(req.Size) * int64(req.Size) * int64(req.Size) * int64(req.Iterations)
	result := matrixMultiplyResult{
		Kind:          "matrix_multiply",
		Size:          req.Size,
		Iterations:    req.Iterations,
		Operations:    operations,
		DurationMS:    duration.Milliseconds(),
		GFLOPS:        float64(operations) / duration.Seconds() / 1_000_000_000,
		Checksum:      checksum,
		WorkerRuntime: runtime.GOOS + "/" + runtime.GOARCH,
	}
	body, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func validateMatrixMultiplyInput(input matrixMultiplyInput) error {
	if input.Size < 16 || input.Size > 2048 {
		return fmt.Errorf("size must be between 16 and 2048")
	}
	if input.Iterations < 1 || input.Iterations > 100 {
		return fmt.Errorf("iterations must be between 1 and 100")
	}
	return nil
}

func multiplySquareMatrices(a []float64, b []float64, c []float64, size int) {
	for i := 0; i < size; i++ {
		row := i * size
		for k := 0; k < size; k++ {
			av := a[row+k]
			brow := k * size
			for j := 0; j < size; j++ {
				c[row+j] += av * b[brow+j]
			}
		}
	}
}

func defaultNodeName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "worker"
	}
	return host
}

func defaultCacheDir() string {
	dir, err := os.UserCacheDir()
	if err != nil || dir == "" {
		return "./data/cache"
	}
	return dir + "/cmesh/cache"
}

func defaultStatePath() string {
	if value := os.Getenv("CMESH_STATE_PATH"); value != "" {
		return value
	}
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return "./data/cmesh-state.json"
	}
	return dir + "/cmesh/cmesh-state.json"
}

func gbToBytes(gb uint64) uint64 {
	return gb * 1024 * 1024 * 1024
}

func bytesToGB(bytes uint64) float64 {
	return float64(bytes) / 1024 / 1024 / 1024
}
