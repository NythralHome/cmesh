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

	"github.com/cmesh/cmesh/internal/cdip"
	"github.com/cmesh/cmesh/internal/cluster"
	"github.com/cmesh/cmesh/internal/config"
	"github.com/cmesh/cmesh/internal/jobs"
	"github.com/cmesh/cmesh/internal/manager"
	"github.com/cmesh/cmesh/internal/membership"
	"github.com/cmesh/cmesh/internal/models"
	"github.com/cmesh/cmesh/internal/resources"
	"github.com/cmesh/cmesh/internal/runtimes"
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
			Addr:                  *addr,
			JoinToken:             *joinToken,
			OperatorToken:         *operatorToken,
			PublicURL:             *publicURL,
			BackgroundCDIPAdvance: true,
			BackgroundRPCHealth:   true,
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

	result, jobErr := executeWorkerJob(*resp.Job, snapshot, cacheDir, nodeID, startedAt, managerURL)
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
	if isModelJobType(resp.Job.Type) {
		refreshed := snapshot
		refreshed.Models = resources.DiscoverInstalledModels(cacheDir)
		if err := sendHeartbeat(managerURL, nodeID, refreshed); err != nil {
			fmt.Fprintf(os.Stderr, "failed to refresh worker model inventory: %v\n", err)
		}
	}
	return true, nil
}

func isModelJobType(jobType string) bool {
	return jobType == models.JobInstall || jobType == models.JobDelete || jobType == models.JobGenerate || jobType == models.JobGenerateDistributedRPC || jobType == models.JobGenerateDistributed || jobType == models.JobGenerateStage || jobType == models.JobRepair || jobType == models.JobCleanup
}

func executeJob(job jobs.Job) (string, error) {
	return executeJobWithResources(job, cluster.ResourceSnapshot{})
}

func executeJobWithResources(job jobs.Job, snapshot cluster.ResourceSnapshot) (string, error) {
	return executeJobWithRuntime(job, snapshot, defaultCacheDir())
}

func executeJobWithRuntime(job jobs.Job, snapshot cluster.ResourceSnapshot, cacheDir string) (string, error) {
	return executeWorkerJob(job, snapshot, cacheDir, "", time.Time{}, "")
}

func executeWorkerJob(job jobs.Job, snapshot cluster.ResourceSnapshot, cacheDir string, nodeID string, startedAt time.Time, managerURL string) (string, error) {
	if err := validateWorkerCanRunJob(job, snapshot); err != nil {
		return "", err
	}
	switch job.Type {
	case "echo":
		return job.Input, nil
	case "compute.matrix_multiply":
		return executeMatrixMultiplyJob(job.Input)
	case models.JobInstall:
		return executeModelInstallJob(job.Input, cacheDir, modelInstallProgressWriter(managerURL, cacheDir, nodeID, job, startedAt))
	case models.JobRepair:
		return executeModelRepairJob(job.Input, cacheDir, modelInstallProgressWriter(managerURL, cacheDir, nodeID, job, startedAt))
	case models.JobCleanup:
		return executeModelCleanupJob(job.Input, cacheDir)
	case models.JobDelete:
		return executeModelDeleteJob(job.Input, cacheDir)
	case models.JobGenerate:
		return executeModelGenerateJob(job.Input, cacheDir)
	case models.JobGenerateDistributedRPC:
		return executeModelDistributedRPCGenerateJob(job.Input, cacheDir)
	case models.JobGenerateDistributed:
		return "", fmt.Errorf("distributed model generate parent jobs are coordinator-owned; workers execute distributed stage jobs")
	case models.JobGenerateStage:
		return executeDistributedStageJob(job.Input, snapshot)
	default:
		return "", fmt.Errorf("unsupported job type %q", job.Type)
	}
}

type distributedStageResult struct {
	Kind               string `json:"kind"`
	ParentJobID        string `json:"parent_job_id"`
	StageIndex         int    `json:"stage_index"`
	ModelID            string `json:"model_id"`
	Runtime            string `json:"runtime"`
	LayerStart         int    `json:"layer_start"`
	LayerEnd           int    `json:"layer_end"`
	UpstreamNodeID     string `json:"upstream_node_id,omitempty"`
	DownstreamNodeID   string `json:"downstream_node_id,omitempty"`
	Materialization    string `json:"materialization"`
	SourceArtifact     string `json:"source_artifact,omitempty"`
	TargetArtifact     string `json:"target_artifact,omitempty"`
	ActivationProtocol string `json:"activation_protocol"`
}

func executeDistributedStageJob(input string, snapshot cluster.ResourceSnapshot) (string, error) {
	var req models.DistributedStageJobInput
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		return "", fmt.Errorf("invalid distributed stage input: %w", err)
	}
	if strings.TrimSpace(req.ParentJobID) == "" {
		return "", fmt.Errorf("parent_job_id is required")
	}
	if strings.TrimSpace(req.ModelID) == "" {
		return "", fmt.Errorf("model_id is required")
	}
	if req.Shard.Stage.Index != req.Stage.Index || req.Shard.Stage.NodeID != req.Stage.NodeID || req.Shard.Stage.LayerStart != req.Stage.LayerStart || req.Shard.Stage.LayerEnd != req.Stage.LayerEnd {
		return "", fmt.Errorf("distributed stage shard does not match stage assignment")
	}
	if req.Shard.Materialization != cdip.ShardLogicalLayers {
		return "", fmt.Errorf("unsupported distributed shard materialization %q", req.Shard.Materialization)
	}
	if strings.TrimSpace(req.Shard.Runtime) == "" {
		return "", fmt.Errorf("distributed stage runtime is required")
	}
	if !workerRuntimeReady(snapshot, req.Shard.Runtime) {
		return "", fmt.Errorf("distributed stage runtime %s is not ready on this worker", req.Shard.Runtime)
	}
	if !workerModelReady(snapshot, req.ModelID) {
		return "", fmt.Errorf("distributed stage model %s is not installed on this worker", req.ModelID)
	}
	stageRuntime := runtimes.NewLogicalStageRuntime(req.Shard.Runtime)
	prepared, err := stageRuntime.PrepareStage(context.Background(), runtimes.StagePrepareRequest{
		ParentJobID:      req.ParentJobID,
		StageJobID:       "worker-local-stage-prepare",
		ModelID:          req.ModelID,
		Stage:            req.Stage,
		Shard:            req.Shard,
		UpstreamNodeID:   req.UpstreamNodeID,
		DownstreamNodeID: req.DownstreamNodeID,
	})
	if err != nil {
		return "", err
	}
	result, err := json.Marshal(distributedStageResult{
		Kind:               prepared.Kind,
		ParentJobID:        prepared.ParentJobID,
		StageIndex:         prepared.StageIndex,
		ModelID:            prepared.ModelID,
		Runtime:            prepared.Runtime,
		LayerStart:         prepared.LayerStart,
		LayerEnd:           prepared.LayerEnd,
		UpstreamNodeID:     prepared.UpstreamNodeID,
		DownstreamNodeID:   prepared.DownstreamNodeID,
		Materialization:    prepared.Materialization,
		SourceArtifact:     prepared.SourceArtifact,
		TargetArtifact:     prepared.TargetArtifact,
		ActivationProtocol: prepared.ActivationProtocol,
	})
	if err != nil {
		return "", err
	}
	return string(result), nil
}

func workerRuntimeReady(snapshot cluster.ResourceSnapshot, runtimeName string) bool {
	for _, runtime := range snapshot.Runtimes {
		if runtime.Name == runtimeName && runtime.Ready {
			return true
		}
	}
	return false
}

func workerModelReady(snapshot cluster.ResourceSnapshot, modelID string) bool {
	for _, model := range snapshot.Models {
		if model.ID == modelID && model.Ready {
			return true
		}
	}
	return false
}

func modelInstallProgressWriter(managerURL string, cacheDir string, nodeID string, job jobs.Job, startedAt time.Time) func(int64, int64) {
	if strings.TrimSpace(cacheDir) == "" || strings.TrimSpace(nodeID) == "" || startedAt.IsZero() {
		return nil
	}
	lastWrite := time.Time{}
	return func(written int64, total int64) {
		now := time.Now().UTC()
		if !lastWrite.IsZero() && now.Sub(lastWrite) < time.Second && written != total {
			return
		}
		lastWrite = now
		percent := 0.0
		if total > 0 {
			percent = (float64(written) / float64(total)) * 100
			if percent > 100 {
				percent = 100
			}
		}
		status := workerstatus.JobStatus{
			State:           "running",
			NodeID:          nodeID,
			JobID:           job.ID,
			Type:            job.Type,
			Input:           job.Input,
			ProgressBytes:   written,
			TotalBytes:      total,
			ProgressPercent: percent,
			ProgressLabel:   "Downloading model",
			StartedAt:       &startedAt,
		}
		if err := workerstatus.Write(cacheDir, status); err != nil {
			fmt.Fprintf(os.Stderr, "failed to write model install progress: %v\n", err)
		}
		if strings.TrimSpace(managerURL) != "" {
			postJobProgress(managerURL, job.ID, jobs.ProgressRequest{
				NodeID:          nodeID,
				ProgressBytes:   written,
				TotalBytes:      total,
				ProgressPercent: percent,
				ProgressLabel:   status.ProgressLabel,
			})
		}
	}
}

func postJobProgress(managerURL string, jobID string, req jobs.ProgressRequest) {
	body, err := json.Marshal(req)
	if err != nil {
		return
	}
	resp, err := http.Post(strings.TrimRight(managerURL, "/")+"/v1/jobs/"+jobID+"/progress", "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to post job progress: %v\n", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "manager rejected job progress: %s\n", resp.Status)
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
	if job.Type == models.JobInstall && req.DiskBytes > 0 && snapshot.Storage.AllowedBytes > 0 && snapshot.Storage.UsedByModelsBytes > 0 && snapshot.Storage.UsedByModelsBytes+req.DiskBytes > snapshot.Storage.AllowedBytes {
		remaining := uint64(0)
		if snapshot.Storage.AllowedBytes > snapshot.Storage.UsedByModelsBytes {
			remaining = snapshot.Storage.AllowedBytes - snapshot.Storage.UsedByModelsBytes
		}
		return fmt.Errorf("worker resource guard rejected job: requires %.1f GB model quota, worker has %.1f GB remaining model quota", bytesToGB(req.DiskBytes), bytesToGB(remaining))
	}
	if req.DiskBytes > 0 && snapshot.Storage.FreeBytes > 0 && snapshot.Storage.FreeBytes < req.DiskBytes {
		return fmt.Errorf("worker resource guard rejected job: requires %.1f GB free disk, worker has %.1f GB", bytesToGB(req.DiskBytes), bytesToGB(snapshot.Storage.FreeBytes))
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
	FreedBytes    int64  `json:"freed_bytes,omitempty"`
	WorkerRuntime string `json:"worker_runtime"`
}

type modelRepairResult struct {
	Kind             string `json:"kind"`
	ModelID          string `json:"model_id"`
	ModelName        string `json:"model_name"`
	Path             string `json:"path"`
	Bytes            int64  `json:"bytes"`
	Runtime          string `json:"runtime"`
	WorkerRuntime    string `json:"worker_runtime"`
	ManifestRepaired bool   `json:"manifest_repaired"`
	TempCleaned      bool   `json:"temp_cleaned"`
	Reinstalled      bool   `json:"reinstalled"`
}

type modelCleanupResult struct {
	Kind                  string `json:"kind"`
	WorkerRuntime         string `json:"worker_runtime"`
	PartialFilesRemoved   int    `json:"partial_files_removed"`
	PartialBytesRemoved   int64  `json:"partial_bytes_removed"`
	OrphanDirsRemoved     int    `json:"orphan_dirs_removed"`
	OrphanBytesRemoved    int64  `json:"orphan_bytes_removed"`
	StaleManifestsRemoved int    `json:"stale_manifests_removed"`
	EmptyModelDirsRemoved int    `json:"empty_model_dirs_removed"`
	TotalBytesRemoved     int64  `json:"total_bytes_removed"`
}

type modelGenerateResult struct {
	Kind             string   `json:"kind"`
	ModelID          string   `json:"model_id"`
	Output           string   `json:"output"`
	Tokens           int      `json:"tokens,omitempty"`
	WorkerRuntime    string   `json:"worker_runtime"`
	ModelRuntime     string   `json:"model_runtime"`
	RuntimeVersion   string   `json:"runtime_version,omitempty"`
	RPCEndpoints     []string `json:"rpc_endpoints,omitempty"`
	RPCEndpointCount int      `json:"rpc_endpoint_count,omitempty"`
}

func executeModelInstallJob(input string, cacheDir string, progress func(int64, int64)) (string, error) {
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
		if err := resources.WriteModelManifest(cacheDir, model, path, uint64(stat.Size()), stat.ModTime()); err != nil {
			return "", fmt.Errorf("failed to write model manifest: %w", err)
		}
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
	totalBytes := resp.ContentLength
	if progress != nil {
		progress(0, totalBytes)
	}
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	reader := io.Reader(resp.Body)
	if progress != nil {
		reader = &progressReader{
			reader:   resp.Body,
			total:    totalBytes,
			progress: progress,
		}
	}
	bytesWritten, copyErr := io.Copy(out, reader)
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
	if err := resources.WriteModelManifest(cacheDir, model, path, uint64(bytesWritten), time.Now().UTC()); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("failed to write model manifest: %w", err)
	}
	if progress != nil {
		progress(bytesWritten, totalBytes)
	}
	return marshalModelInstallResult(model, path, bytesWritten)
}

func executeModelRepairJob(input string, cacheDir string, progress func(int64, int64)) (string, error) {
	var req models.RepairInput
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		return "", fmt.Errorf("invalid model repair input: %w", err)
	}
	model, err := models.MustFind(req.ModelID)
	if err != nil {
		return "", err
	}
	path := modelPath(cacheDir, model)
	tmp := path + ".tmp"
	tempCleaned := false
	if _, err := os.Stat(tmp); err == nil {
		if removeErr := os.Remove(tmp); removeErr != nil {
			return "", fmt.Errorf("failed to remove partial model download: %w", removeErr)
		}
		tempCleaned = true
	}
	if stat, err := os.Stat(path); err == nil && !stat.IsDir() && stat.Size() > 0 {
		if err := resources.WriteModelManifest(cacheDir, model, path, uint64(stat.Size()), stat.ModTime()); err != nil {
			return "", fmt.Errorf("failed to repair model manifest: %w", err)
		}
		result := modelRepairResult{
			Kind:             string(models.JobRepair),
			ModelID:          model.ID,
			ModelName:        model.Name,
			Path:             path,
			Bytes:            stat.Size(),
			Runtime:          string(model.Runtime),
			WorkerRuntime:    runtime.GOOS + "/" + runtime.GOARCH,
			ManifestRepaired: true,
			TempCleaned:      tempCleaned,
		}
		body, err := json.Marshal(result)
		return string(body), err
	}
	installInput, err := json.Marshal(models.InstallInput{ModelID: model.ID})
	if err != nil {
		return "", err
	}
	installResult, err := executeModelInstallJob(string(installInput), cacheDir, progress)
	if err != nil {
		return "", err
	}
	var installed modelInstallResult
	if err := json.Unmarshal([]byte(installResult), &installed); err != nil {
		return "", err
	}
	result := modelRepairResult{
		Kind:             string(models.JobRepair),
		ModelID:          model.ID,
		ModelName:        model.Name,
		Path:             installed.Path,
		Bytes:            installed.Bytes,
		Runtime:          installed.Runtime,
		WorkerRuntime:    installed.WorkerRuntime,
		ManifestRepaired: true,
		TempCleaned:      tempCleaned,
		Reinstalled:      true,
	}
	body, err := json.Marshal(result)
	return string(body), err
}

func executeModelCleanupJob(input string, cacheDir string) (string, error) {
	var req models.CleanupInput
	if strings.TrimSpace(input) != "" {
		if err := json.Unmarshal([]byte(input), &req); err != nil {
			return "", fmt.Errorf("invalid model cleanup input: %w", err)
		}
	}
	if strings.TrimSpace(req.Scope) == "" {
		req.Scope = "cache"
	}
	if req.Scope != "cache" {
		return "", fmt.Errorf("unsupported model cleanup scope %q", req.Scope)
	}
	modelsDir := filepath.Join(cacheDir, "models")
	result := modelCleanupResult{
		Kind:          string(models.JobCleanup),
		WorkerRuntime: runtime.GOOS + "/" + runtime.GOARCH,
	}
	partialFiles, partialBytes, err := removePartialModelDownloads(modelsDir)
	if err != nil {
		return "", err
	}
	result.PartialFilesRemoved = partialFiles
	result.PartialBytesRemoved = partialBytes
	orphanDirs, orphanBytes, err := removeOrphanModelDirs(modelsDir)
	if err != nil {
		return "", err
	}
	result.OrphanDirsRemoved = orphanDirs
	result.OrphanBytesRemoved = orphanBytes
	staleManifests, emptyDirs, err := removeStaleModelManifestsAndEmptyDirs(cacheDir)
	if err != nil {
		return "", err
	}
	result.StaleManifestsRemoved = staleManifests
	result.EmptyModelDirsRemoved = emptyDirs
	result.TotalBytesRemoved = result.PartialBytesRemoved + result.OrphanBytesRemoved
	body, err := json.Marshal(result)
	return string(body), err
}

func removePartialModelDownloads(modelsDir string) (int, int64, error) {
	removed := 0
	var bytesRemoved int64
	err := filepath.WalkDir(modelsDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".tmp") {
			return err
		}
		info, statErr := entry.Info()
		if statErr == nil && info.Size() > 0 {
			bytesRemoved += info.Size()
		}
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			return removeErr
		}
		removed++
		return nil
	})
	if os.IsNotExist(err) {
		return 0, 0, nil
	}
	return removed, bytesRemoved, err
}

func removeOrphanModelDirs(modelsDir string) (int, int64, error) {
	entries, err := os.ReadDir(modelsDir)
	if os.IsNotExist(err) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	catalogIDs := map[string]bool{}
	for _, model := range models.Catalog() {
		catalogIDs[model.ID] = true
	}
	removed := 0
	var bytesRemoved int64
	for _, entry := range entries {
		if !entry.IsDir() || catalogIDs[entry.Name()] {
			continue
		}
		path := filepath.Join(modelsDir, entry.Name())
		size, err := directorySize(path)
		if err != nil {
			return removed, bytesRemoved, err
		}
		if err := os.RemoveAll(path); err != nil {
			return removed, bytesRemoved, err
		}
		bytesRemoved += size
		removed++
	}
	return removed, bytesRemoved, nil
}

func removeStaleModelManifestsAndEmptyDirs(cacheDir string) (int, int, error) {
	staleManifests := 0
	emptyDirs := 0
	for _, model := range models.Catalog() {
		modelFile := modelPath(cacheDir, model)
		modelDir := filepath.Dir(modelFile)
		if stat, err := os.Stat(modelFile); err == nil && !stat.IsDir() && stat.Size() > 0 {
			continue
		}
		manifest := resources.ModelManifestPath(cacheDir, model.ID)
		if err := os.Remove(manifest); err == nil {
			staleManifests++
		} else if err != nil && !os.IsNotExist(err) {
			return staleManifests, emptyDirs, err
		}
		if removed, err := removeDirIfEmpty(modelDir); err != nil {
			return staleManifests, emptyDirs, err
		} else if removed {
			emptyDirs++
		}
	}
	return staleManifests, emptyDirs, nil
}

func removeDirIfEmpty(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if len(entries) != 0 {
		return false, nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return false, err
	}
	return true, nil
}

type progressReader struct {
	reader   io.Reader
	written  int64
	total    int64
	progress func(int64, int64)
}

func (r *progressReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		r.written += int64(n)
		r.progress(r.written, r.total)
	}
	return n, err
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
	modelDir := filepath.Dir(path)
	freedBytes, _ := directorySize(modelDir)
	err = os.RemoveAll(modelDir)
	if err != nil {
		return "", err
	}
	removed := freedBytes > 0
	result := modelDeleteResult{
		Kind:          string(models.JobDelete),
		ModelID:       model.ID,
		Removed:       removed,
		Path:          path,
		FreedBytes:    freedBytes,
		WorkerRuntime: runtime.GOOS + "/" + runtime.GOARCH,
	}
	body, err := json.Marshal(result)
	return string(body), err
}

func directorySize(path string) (int64, error) {
	var total int64
	err := filepath.WalkDir(path, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if os.IsNotExist(err) {
		return 0, nil
	}
	return total, err
}

func executeModelGenerateJob(input string, cacheDir string) (string, error) {
	var req models.GenerateInput
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		return "", fmt.Errorf("invalid model generate input: %w", err)
	}
	return executeModelGenerate(req, cacheDir, nil, string(models.JobGenerate))
}

func executeModelDistributedRPCGenerateJob(input string, cacheDir string) (string, error) {
	var req models.DistributedRPCGenerateInput
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		return "", fmt.Errorf("invalid distributed rpc model generate input: %w", err)
	}
	if len(req.RPCEndpoints) == 0 {
		return "", fmt.Errorf("rpc_endpoints is required")
	}
	generateReq := models.GenerateInput{
		ModelID:        req.ModelID,
		Prompt:         req.Prompt,
		Messages:       req.Messages,
		SystemPrompt:   req.SystemPrompt,
		ConversationID: req.ConversationID,
		MaxTokens:      req.MaxTokens,
		Temperature:    req.Temperature,
	}
	return executeModelGenerate(generateReq, cacheDir, req.RPCEndpoints, string(models.JobGenerateDistributedRPC))
}

func executeModelGenerate(req models.GenerateInput, cacheDir string, rpcEndpoints []string, resultKind string) (string, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return "", fmt.Errorf("prompt is required")
	}
	cleanRPCEndpoints := cleanStringList(rpcEndpoints)
	rpcArg := strings.Join(cleanRPCEndpoints, ",")
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
	cli, runtimeStatus, err := ensureModelRuntime(model.Runtime, cacheDir)
	if err != nil {
		return "", fmt.Errorf("model %s is installed, but %s runtime is not ready: %w", model.ID, model.Runtime, err)
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 || maxTokens > 2048 {
		maxTokens = models.QualityPresetFor(model).MaxTokens
		if maxTokens <= 0 || maxTokens > 2048 {
			maxTokens = 512
		}
	}
	temperature := strings.TrimSpace(req.Temperature)
	if temperature == "" {
		temperature = models.QualityPresetFor(model).Temperature
	}
	timeout := modelGenerateTimeout(maxTokens)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	args := []string{
		"-m", path,
		"-p", modelPrompt(model, req),
		"-n", strconv.Itoa(maxTokens),
		"--temp", temperature,
		"--threads", "1",
		"--ctx-size", strconv.Itoa(modelContextSize(model)),
		"--log-disable",
		"--no-display-prompt",
		"--no-show-timings",
		"--simple-io",
		"--single-turn",
	}
	if rpcArg != "" {
		args = append(args, "--rpc", rpcArg)
	}
	for _, stop := range modelStopSequences(model) {
		args = append(args, "-r", stop)
	}
	cmd := exec.CommandContext(ctx, cli, args...)
	cmd.Env = os.Environ()
	cmd.Dir = filepath.Dir(cli)
	var stdout limitedBuffer
	var stderr limitedBuffer
	stdout.limit = 64 * 1024
	stderr.limit = 16 * 1024
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("%s timed out after %s", model.Runtime, timeout)
	}
	if err != nil {
		return "", fmt.Errorf("%s failed: %w: %s", model.Runtime, err, strings.TrimSpace(stderr.String()))
	}
	text := cleanLlamaOutput(stdout.String(), req.Prompt)
	if text == "" {
		text = sanitizeModelText(stdout.String())
	}
	if text == "" {
		return "", fmt.Errorf("%s returned an empty response", model.Runtime)
	}
	result := modelGenerateResult{
		Kind:             resultKind,
		ModelID:          model.ID,
		Output:           text,
		WorkerRuntime:    runtime.GOOS + "/" + runtime.GOARCH,
		ModelRuntime:     string(model.Runtime),
		RuntimeVersion:   runtimeStatus.Version,
		RPCEndpoints:     cleanRPCEndpoints,
		RPCEndpointCount: len(cleanRPCEndpoints),
	}
	body, err := json.Marshal(result)
	return string(body), err
}

func cleanStringList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

type limitedBuffer struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		return len(p), nil
	}
	remaining := b.limit - b.Buffer.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		b.truncated = true
		_, _ = b.Buffer.Write(p[:remaining])
		return len(p), nil
	}
	_, _ = b.Buffer.Write(p)
	return len(p), nil
}

func modelGenerateTimeout(maxTokens int) time.Duration {
	if raw := strings.TrimSpace(os.Getenv("CMESH_MODEL_GENERATE_TIMEOUT")); raw != "" {
		if duration, err := time.ParseDuration(raw); err == nil && duration > 0 {
			return duration
		}
	}
	timeout := 2*time.Minute + time.Duration(maxTokens)*2*time.Second
	if timeout > 10*time.Minute {
		return 10 * time.Minute
	}
	return timeout
}

func modelContextSize(model models.Model) int {
	if raw := strings.TrimSpace(os.Getenv("CMESH_MODEL_CONTEXT_SIZE")); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 {
			return value
		}
	}
	return modelAdapterFor(model).ContextSize(model)
}

func modelSystemPrompt(model models.Model) string {
	return modelAdapterFor(model).SystemPrompt(model)
}

func modelPrompt(model models.Model, req models.GenerateInput) string {
	systemPrompt := strings.TrimSpace(req.SystemPrompt)
	if systemPrompt == "" {
		systemPrompt = modelSystemPrompt(model)
	}
	messages := normalizePromptMessages(req.Messages, req.Prompt)
	if len(messages) == 0 {
		messages = []models.ChatMessage{{Role: "user", Content: strings.TrimSpace(req.Prompt)}}
	}

	return modelAdapterFor(model).Prompt(systemPrompt, messages)
}

type ModelAdapter struct {
	Name               string
	Prompt             func(systemPrompt string, messages []models.ChatMessage) string
	SystemPromptSuffix string
	StopSequences      []string
	ContextFloor       int
}

func (a ModelAdapter) SystemPrompt(model models.Model) string {
	if preset := strings.TrimSpace(models.QualityPresetFor(model).SystemPrompt); preset != "" {
		return preset
	}
	base := "You are CMesh's local AI assistant. Continue the conversation using the provided history. Answer the latest user message directly. If the user shared personal details earlier in this conversation, remember and use them. Do not print role names, chat template tokens, or hidden reasoning."
	suffix := strings.TrimSpace(a.SystemPromptSuffix)
	if suffix == "" {
		return base
	}
	return base + " " + suffix
}

func (a ModelAdapter) ContextSize(model models.Model) int {
	floor := a.ContextFloor
	if floor <= 0 {
		floor = 4096
	}
	if model.Context > 0 && model.Context < floor {
		return model.Context
	}
	return floor
}

func modelAdapterFor(model models.Model) ModelAdapter {
	id := strings.ToLower(model.ID)
	family := strings.ToLower(model.Family)
	if strings.Contains(id, "deepseek") {
		return deepSeekQwenAdapter()
	}
	switch family {
	case "qwen":
		return qwenAdapter()
	case "gemma":
		return gemmaAdapter()
	case "mistral":
		return mistralAdapter()
	case "phi":
		return phiAdapter()
	default:
		return llamaAdapter()
	}
}

func qwenAdapter() ModelAdapter {
	return ModelAdapter{
		Name:               "qwen",
		Prompt:             qwenChatPrompt,
		SystemPromptSuffix: "Prefer concise, natural answers.",
		StopSequences:      []string{"<|im_end|>", "<|im_start|>user", "<|im_start|>system"},
		ContextFloor:       4096,
	}
}

func deepSeekQwenAdapter() ModelAdapter {
	adapter := qwenAdapter()
	adapter.Name = "deepseek-qwen"
	adapter.SystemPromptSuffix = "Return only the final answer unless the user explicitly asks for reasoning."
	return adapter
}

func gemmaAdapter() ModelAdapter {
	return ModelAdapter{
		Name:               "gemma",
		Prompt:             gemmaChatPrompt,
		SystemPromptSuffix: "Keep answers clear and conversational.",
		StopSequences:      []string{"<end_of_turn>", "<start_of_turn>user"},
		ContextFloor:       4096,
	}
}

func mistralAdapter() ModelAdapter {
	return ModelAdapter{
		Name:               "mistral",
		Prompt:             mistralChatPrompt,
		SystemPromptSuffix: "Be practical and concise.",
		StopSequences:      []string{"</s>", "[INST]"},
		ContextFloor:       4096,
	}
}

func phiAdapter() ModelAdapter {
	return ModelAdapter{
		Name:               "phi",
		Prompt:             phiChatPrompt,
		SystemPromptSuffix: "Keep answers short unless more detail is needed.",
		StopSequences:      []string{"<|end|>", "<|user|>"},
		ContextFloor:       4096,
	}
}

func llamaAdapter() ModelAdapter {
	return ModelAdapter{
		Name:          "llama",
		Prompt:        llamaChatPrompt,
		StopSequences: []string{"</s>", "User:"},
		ContextFloor:  4096,
	}
}

func normalizePromptMessages(messages []models.ChatMessage, fallbackPrompt string) []models.ChatMessage {
	out := make([]models.ChatMessage, 0, len(messages)+1)
	for _, message := range messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		if role != "assistant" && role != "system" {
			role = "user"
		}
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		out = append(out, models.ChatMessage{Role: role, Content: content})
	}
	if len(out) == 0 && strings.TrimSpace(fallbackPrompt) != "" {
		out = append(out, models.ChatMessage{Role: "user", Content: strings.TrimSpace(fallbackPrompt)})
	}
	return out
}

func qwenChatPrompt(systemPrompt string, messages []models.ChatMessage) string {
	var b strings.Builder
	b.WriteString("<|im_start|>system\n")
	b.WriteString(systemPrompt)
	b.WriteString("<|im_end|>\n")
	for _, message := range messages {
		if message.Role == "system" {
			continue
		}
		b.WriteString("<|im_start|>")
		b.WriteString(qwenRole(message.Role))
		b.WriteString("\n")
		b.WriteString(message.Content)
		b.WriteString("<|im_end|>\n")
	}
	b.WriteString("<|im_start|>assistant\n")
	return b.String()
}

func qwenRole(role string) string {
	if role == "assistant" {
		return "assistant"
	}
	return "user"
}

func gemmaChatPrompt(systemPrompt string, messages []models.ChatMessage) string {
	var b strings.Builder
	if systemPrompt != "" {
		b.WriteString("<start_of_turn>user\n")
		b.WriteString(systemPrompt)
		b.WriteString("<end_of_turn>\n<start_of_turn>model\nUnderstood.<end_of_turn>\n")
	}
	for _, message := range messages {
		if message.Role == "system" {
			continue
		}
		role := "user"
		if message.Role == "assistant" {
			role = "model"
		}
		b.WriteString("<start_of_turn>")
		b.WriteString(role)
		b.WriteString("\n")
		b.WriteString(message.Content)
		b.WriteString("<end_of_turn>\n")
	}
	b.WriteString("<start_of_turn>model\n")
	return b.String()
}

func mistralChatPrompt(systemPrompt string, messages []models.ChatMessage) string {
	var b strings.Builder
	pendingUser := ""
	for _, message := range messages {
		if message.Role == "system" {
			continue
		}
		if message.Role == "assistant" {
			if pendingUser != "" {
				b.WriteString("[INST] ")
				if systemPrompt != "" {
					b.WriteString(systemPrompt)
					b.WriteString("\n\n")
					systemPrompt = ""
				}
				b.WriteString(pendingUser)
				b.WriteString(" [/INST] ")
				pendingUser = ""
			}
			b.WriteString(message.Content)
			b.WriteString(" ")
			continue
		}
		if pendingUser != "" {
			pendingUser += "\n\n" + message.Content
		} else {
			pendingUser = message.Content
		}
	}
	if pendingUser != "" {
		b.WriteString("[INST] ")
		if systemPrompt != "" {
			b.WriteString(systemPrompt)
			b.WriteString("\n\n")
		}
		b.WriteString(pendingUser)
		b.WriteString(" [/INST]")
	}
	return b.String()
}

func phiChatPrompt(systemPrompt string, messages []models.ChatMessage) string {
	var b strings.Builder
	b.WriteString("<|system|>\n")
	b.WriteString(systemPrompt)
	b.WriteString("<|end|>\n")
	for _, message := range messages {
		if message.Role == "system" {
			continue
		}
		role := "user"
		if message.Role == "assistant" {
			role = "assistant"
		}
		b.WriteString("<|")
		b.WriteString(role)
		b.WriteString("|>\n")
		b.WriteString(message.Content)
		b.WriteString("<|end|>\n")
	}
	b.WriteString("<|assistant|>\n")
	return b.String()
}

func llamaChatPrompt(systemPrompt string, messages []models.ChatMessage) string {
	var b strings.Builder
	b.WriteString("System: ")
	b.WriteString(systemPrompt)
	b.WriteString("\n\n")
	for _, message := range messages {
		if message.Role == "system" {
			continue
		}
		role := "User"
		if message.Role == "assistant" {
			role = "Assistant"
		}
		b.WriteString(role)
		b.WriteString(": ")
		b.WriteString(message.Content)
		b.WriteString("\n\n")
	}
	b.WriteString("Assistant:")
	return b.String()
}

func modelStopSequences(model models.Model) []string {
	stops := modelAdapterFor(model).StopSequences
	if len(stops) == 0 {
		return llamaAdapter().StopSequences
	}
	return stops
}

func cleanLlamaOutput(output string, prompt string) string {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	collected := make([]string, 0, len(lines))
	afterPrompt := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			if afterPrompt && len(collected) > 0 {
				collected = append(collected, "")
			}
			continue
		case strings.HasPrefix(trimmed, "> "):
			afterPrompt = true
			continue
		case strings.Contains(trimmed, "<|im_start|>assistant"):
			afterPrompt = true
			continue
		case strings.Contains(trimmed, "<|im_start|>") || strings.Contains(trimmed, "<|im_end|>"):
			if afterPrompt {
				line = removeChatTemplateTokens(line)
				trimmed = strings.TrimSpace(line)
				if trimmed == "" {
					continue
				}
				break
			}
			continue
		case trimmed == "Exiting..." || trimmed == "Loading model..." || strings.HasPrefix(trimmed, "build      :") || strings.HasPrefix(trimmed, "model      :") || strings.HasPrefix(trimmed, "modalities :"):
			continue
		case strings.Contains(trimmed, "available commands:") || strings.HasPrefix(trimmed, "/exit ") || strings.HasPrefix(trimmed, "/regen") || strings.HasPrefix(trimmed, "/clear") || strings.HasPrefix(trimmed, "/read ") || strings.HasPrefix(trimmed, "/glob "):
			continue
		case strings.Contains(trimmed, "▄▄") || strings.Contains(trimmed, "██") || strings.Contains(trimmed, "▀▀"):
			continue
		}
		if !afterPrompt {
			continue
		}
		collected = append(collected, line)
	}
	text := strings.TrimSpace(strings.Join(collected, "\n"))
	if text == "" {
		return sanitizeModelText(output)
	}
	text = sanitizeModelText(text)
	if len([]byte(text)) > 8192 {
		return string([]byte(text)[:8192])
	}
	return text
}

func sanitizeModelText(text string) string {
	text = removeChatTemplateTokens(text)
	text = removeReasoningText(text)
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	cleaned := make([]string, 0, len(lines))
	seenContent := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if len(cleaned) > 0 && cleaned[len(cleaned)-1] != "" {
				cleaned = append(cleaned, "")
			}
			continue
		}
		lower := strings.ToLower(trimmed)
		if isChatTemplateNoise(lower) {
			continue
		}
		if isRoleEcho(lower) {
			continue
		}
		if !seenContent && strings.HasPrefix(lower, "you will answer the user's question") {
			continue
		}
		seenContent = true
		cleaned = append(cleaned, line)
	}
	return strings.TrimSpace(strings.Join(cleaned, "\n"))
}

func isChatTemplateNoise(lower string) bool {
	if lower == "<" || lower == "</" {
		return true
	}
	if strings.HasPrefix(lower, "<|im_start|") || strings.HasPrefix(lower, "<|im_end|") {
		return true
	}
	if strings.HasPrefix(lower, "</|im_start|") || strings.HasPrefix(lower, "</|im_end|") {
		return true
	}
	if strings.HasPrefix(lower, "<start_of_turn>") || strings.HasPrefix(lower, "<end_of_turn>") {
		return true
	}
	if strings.HasPrefix(lower, "<|system|>") || strings.HasPrefix(lower, "<|user|>") || strings.HasPrefix(lower, "<|assistant|>") || strings.HasPrefix(lower, "<|end|>") {
		return true
	}
	return false
}

func isRoleEcho(lower string) bool {
	trimmed := strings.Trim(strings.TrimSpace(lower), ":")
	switch trimmed {
	case "user", "assistant", "system", "model":
		return true
	default:
		return false
	}
}

func removeReasoningText(text string) string {
	for {
		start := strings.Index(strings.ToLower(text), "<think>")
		if start < 0 {
			break
		}
		end := strings.Index(strings.ToLower(text[start:]), "</think>")
		if end < 0 {
			text = text[:start]
			break
		}
		text = text[:start] + text[start+end+len("</think>"):]
	}
	lower := strings.ToLower(text)
	if idx := strings.LastIndex(lower, "[start thinking]"); idx >= 0 {
		after := text[idx+len("[start thinking]"):]
		if trimmed := strings.TrimSpace(after); trimmed != "" {
			text = trimmed
		}
	}
	return strings.TrimSpace(strings.ReplaceAll(text, "[Start thinking]", ""))
}

func removeChatTemplateTokens(text string) string {
	replacer := strings.NewReplacer(
		"<|im_start|>system", "",
		"<|im_start|>user", "",
		"<|im_start|>assistant", "",
		"<|im_start|>", "",
		"<|im_end|>", "",
		"</|im_start|>", "",
		"</|im_end|>", "",
		"<start_of_turn>user", "",
		"<start_of_turn>model", "",
		"<start_of_turn>", "",
		"<end_of_turn>", "",
		"<|system|>", "",
		"<|user|>", "",
		"<|assistant|>", "",
		"<|end|>", "",
	)
	return replacer.Replace(text)
}

func ensureModelRuntime(modelRuntime models.Runtime, cacheDir string) (string, runtimes.RuntimeStatus, error) {
	switch modelRuntime {
	case models.RuntimeLlamaCPP:
		return runtimes.EnsureLlamaCPP(context.Background(), cacheDir)
	default:
		return "", runtimes.RuntimeStatus{}, fmt.Errorf("unsupported runtime %q", modelRuntime)
	}
}

func modelPath(cacheDir string, model models.Model) string {
	if strings.TrimSpace(cacheDir) == "" {
		cacheDir = defaultCacheDir()
	}
	return resources.ModelFilePath(cacheDir, model)
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
