package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/cmesh/cmesh/internal/cluster"
	"github.com/cmesh/cmesh/internal/config"
	"github.com/cmesh/cmesh/internal/jobs"
	"github.com/cmesh/cmesh/internal/manager"
	"github.com/cmesh/cmesh/internal/membership"
	"github.com/cmesh/cmesh/internal/resources"
	"github.com/cmesh/cmesh/internal/version"
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
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		var state manager.Store
		state = manager.NewState()
		if *databaseURL != "" {
			postgresStore, err := manager.NewPostgresStore(ctx, *databaseURL)
			if err != nil {
				return err
			}
			defer postgresStore.Close()
			state = postgresStore
			fmt.Println("manager storage: postgres")
		} else {
			fmt.Println("manager storage: in-memory")
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
  cmesh worker benchmark    Run CPU, memory, disk, network, and AI benchmarks`)
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

	ticker := time.NewTicker(heartbeatEvery)
	defer ticker.Stop()

	if err := sendHeartbeat(options.managerURL, resp.NodeID, discoverWorkerResources(options)); err != nil {
		return err
	}
	if options.runBenchmark {
		if err := runAndSubmitBenchmarks(options.managerURL, resp.NodeID, options.cacheDir); err != nil {
			return err
		}
	}
	if err := pollAndExecuteJob(options.managerURL, resp.NodeID); err != nil {
		return err
	}
	if options.runOnce {
		fmt.Printf("worker %s completed one-shot run\n", resp.NodeID)
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := sendHeartbeat(options.managerURL, resp.NodeID, discoverWorkerResources(options)); err != nil {
				return err
			}
			if err := pollAndExecuteJob(options.managerURL, resp.NodeID); err != nil {
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

func pollAndExecuteJob(managerURL string, nodeID string) error {
	httpResp, err := http.Get(managerURL + "/v1/workers/" + nodeID + "/jobs/next")
	if err != nil {
		return err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return fmt.Errorf("manager returned %s", httpResp.Status)
	}

	var resp struct {
		Job *jobs.Job `json:"job"`
	}
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return err
	}
	if resp.Job == nil {
		return nil
	}

	result, jobErr := executeJob(*resp.Job)
	complete := jobs.CompleteRequest{
		NodeID: nodeID,
		Result: result,
	}
	if jobErr != nil {
		complete.Error = jobErr.Error()
	}

	body, err := json.Marshal(complete)
	if err != nil {
		return err
	}

	completeResp, err := http.Post(managerURL+"/v1/jobs/"+resp.Job.ID+"/complete", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer completeResp.Body.Close()

	if completeResp.StatusCode < 200 || completeResp.StatusCode >= 300 {
		return fmt.Errorf("manager returned %s", completeResp.Status)
	}

	if jobErr != nil {
		fmt.Printf("job %s failed: %v\n", resp.Job.ID, jobErr)
	} else {
		fmt.Printf("job %s completed\n", resp.Job.ID)
	}
	return nil
}

func executeJob(job jobs.Job) (string, error) {
	switch job.Type {
	case "echo":
		return job.Input, nil
	default:
		return "", fmt.Errorf("unsupported job type %q", job.Type)
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

func gbToBytes(gb uint64) uint64 {
	return gb * 1024 * 1024 * 1024
}
