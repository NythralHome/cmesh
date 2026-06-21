package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
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
	"github.com/cmesh/cmesh/internal/protocol"
	"github.com/cmesh/cmesh/internal/resources"
	"github.com/cmesh/cmesh/internal/runtimes"
	"github.com/cmesh/cmesh/internal/transport"
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
	case "model":
		return runModel(args[1:])
	case "job":
		return runJob(args[1:])
	case "stage-runner":
		return runStageRunner(args[1:])
	case "dev":
		return runDev(args[1:])
	case "help", "--help", "-h":
		printUsage()
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}

	return nil
}

func runModel(args []string) error {
	if len(args) == 0 {
		printModelUsage()
		return nil
	}
	switch args[0] {
	case "catalog":
		fs := flag.NewFlagSet("model catalog", flag.ContinueOnError)
		family := fs.String("family", "", "filter by model family")
		jsonOut := fs.Bool("json", false, "print JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		catalog := models.Catalog()
		filtered := make([]models.Model, 0, len(catalog))
		for _, model := range catalog {
			if strings.TrimSpace(*family) != "" && !strings.EqualFold(model.Family, strings.TrimSpace(*family)) {
				continue
			}
			filtered = append(filtered, model)
		}
		if *jsonOut {
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(filtered)
		}
		for _, model := range filtered {
			adapter := model.Adapter
			if adapter == "" {
				adapter = "-"
			}
			fmt.Printf("%s\t%s\t%s\t%s\t%s\n", model.ID, model.Name, model.Parameters, model.Quant, adapter)
		}
	case "adapters":
		fs := flag.NewFlagSet("model adapters", flag.ContinueOnError)
		jsonOut := fs.Bool("json", false, "print JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		adapters := models.AdapterSpecs()
		if *jsonOut {
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(adapters)
		}
		for _, adapter := range adapters {
			fmt.Printf("%s\t%s\t%s\n", adapter.ID, adapter.Family, adapter.PromptTemplate)
		}
	case "help", "--help", "-h":
		printModelUsage()
	default:
		return fmt.Errorf("unknown model command %q", args[0])
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
		addr := fs.String("addr", "127.0.0.1:8080", "HTTP listen address")
		joinToken := fs.String("join-token", os.Getenv("CMESH_JOIN_TOKEN"), "worker join token")
		operatorToken := fs.String("operator-token", os.Getenv("CMESH_OPERATOR_TOKEN"), "operator token for protected dashboard actions")
		publicURL := fs.String("public-url", os.Getenv("CMESH_PUBLIC_URL"), "public manager URL used in generated worker invites")
		databaseURL := fs.String("database-url", os.Getenv("DATABASE_URL"), "Postgres database URL")
		statePath := fs.String("state-path", defaultStatePath(), "local manager state file path")
		memoryState := fs.Bool("memory", false, "use in-memory manager state")
		cdipAutoAdvance := fs.Bool("cdip-auto-advance", true, "automatically advance active CDIP mock lifecycle jobs")
		allowInsecurePublic := fs.Bool("allow-insecure-public-manager", false, "allow a public manager without required production tokens")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if err := validateManagerSecurityOptions(managerSecurityOptions{
			Addr:                *addr,
			PublicURL:           *publicURL,
			JoinToken:           *joinToken,
			OperatorToken:       *operatorToken,
			AllowInsecurePublic: *allowInsecurePublic,
		}); err != nil {
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
			BackgroundCDIPAdvance: *cdipAutoAdvance,
			BackgroundRPCHealth:   true,
			BackgroundJobRecovery: true,
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

type managerSecurityOptions struct {
	Addr                string
	PublicURL           string
	JoinToken           string
	OperatorToken       string
	AllowInsecurePublic bool
}

func validateManagerSecurityOptions(options managerSecurityOptions) error {
	if options.AllowInsecurePublic || !managerLooksPublic(options.Addr, options.PublicURL) {
		return nil
	}
	var missing []string
	if strings.TrimSpace(options.JoinToken) == "" {
		missing = append(missing, "join token")
	}
	if strings.TrimSpace(options.OperatorToken) == "" {
		missing = append(missing, "operator token")
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("refusing to start public manager without %s; set CMESH_JOIN_TOKEN and CMESH_OPERATOR_TOKEN, pass --join-token/--operator-token, or use --allow-insecure-public-manager for an isolated dev test", strings.Join(missing, " and "))
}

func managerLooksPublic(addr string, publicURL string) bool {
	if strings.TrimSpace(publicURL) != "" {
		return true
	}
	host := managerListenHost(addr)
	if host == "" {
		return true
	}
	if strings.EqualFold(host, "localhost") {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return true
	}
	return !ip.IsLoopback()
}

func managerListenHost(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		parsed, err := url.Parse(addr)
		if err != nil {
			return ""
		}
		return parsed.Hostname()
	}
	host, _, err := net.SplitHostPort(addr)
	if err == nil {
		return strings.Trim(host, "[]")
	}
	if strings.HasPrefix(addr, ":") {
		return ""
	}
	return strings.Trim(addr, "[]")
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
	case "poll-once":
		options, err := parseWorkerOptions("worker poll-once", args[1:])
		if err != nil {
			return err
		}
		if options.nodeID == "" {
			return fmt.Errorf("--node-id is required")
		}
		return workerPollOnce(options)
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
  cmesh model catalog       Print model catalog metadata
  cmesh model adapters      Print model adapter metadata
  cmesh job submit          Submit a job
  cmesh job list            List jobs
  cmesh stage-runner        Validate CDIP stage runtime adapter contracts
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

func printModelUsage() {
	fmt.Println(`Usage:
  cmesh model catalog [--family qwen] [--json]   Print model catalog metadata
  cmesh model adapters [--json]                  Print model adapter metadata`)
}

func printStageRunnerUsage() {
	fmt.Println(`Usage:
  cmesh stage-runner prepare --input stage.json [--mode logical|llama.cpp-stage] [--llama-cli path]
  cmesh stage-runner prefill --parent-job id --stage-job id --stage-index 0 [--step n]
  cmesh stage-runner decode --parent-job id --stage-job id --stage-index 0 --payload data [--downstream-node id] [--manager url]
  cmesh stage-runner receive --parent-job id --stage-job upstream-id --manager url --node-id downstream-node
  cmesh stage-runner relay-decode --parent-job id --upstream-stage upstream-id --stage-job current-id --downstream-stage next-id --runner-bin /path/cmesh-stage-runner --model model.gguf --stage-start 1 --stage-end 15 --manager url --node-id current-node --downstream-node next-node
  cmesh stage-runner daemon --addr 127.0.0.1:19781 --session-dir /var/lib/cmesh/stage-sessions
  cmesh stage-runner complete --parent-job id --stage-job id --stage-index 0
  cmesh stage-runner abort --parent-job id --stage-job id --stage-index 0
  cmesh stage-runner probe-llamacpp --llama-cli path

The stage runner is a local adapter spike for CDIP layer-stage execution. The
logical mode validates stage contracts and emits cdip.stage_ready. The
llama.cpp-stage relay-decode path can bridge manager-relayed activation frames
through a local patched cmesh-stage-runner binary. Decode can send activation
frames through the manager relay when --manager is provided.`)
}

type workerOptions struct {
	managerURL       string
	nodeID           string
	nodeAuthToken    string
	name             string
	token            string
	cacheDir         string
	modelID          string
	modelPath        string
	modelLayers      int
	runtimeName      string
	limits           config.ResourceLimits
	runBenchmark     bool
	runOnce          bool
	rpcEnabled       bool
	rpcHost          string
	rpcAdvertiseHost string
	rpcPort          int
	rpcCache         bool
	stageDaemonURL   string
}

func parseWorkerOptions(name string, args []string) (workerOptions, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	managerURL := fs.String("manager", "http://127.0.0.1:8080", "manager API URL")
	nodeID := fs.String("node-id", "", "existing worker node id for poll-once")
	nodeAuthToken := fs.String("node-auth-token", os.Getenv("CMESH_NODE_AUTH_TOKEN"), "worker node auth token for poll-once")
	nodeName := fs.String("name", defaultNodeName(), "worker display name")
	token := fs.String("token", "", "cluster join token")
	cacheDir := fs.String("cache-dir", defaultCacheDir(), "worker artifact cache directory")
	modelID := fs.String("model-id", "", "poll-once model id override")
	modelPath := fs.String("model-path", "", "poll-once model path override")
	modelLayers := fs.Int("model-layers", 0, "poll-once model layer count override")
	runtimeName := fs.String("runtime", string(models.RuntimeLlamaCPP), "poll-once ready runtime override")
	cpuAllowed := fs.Int("cpu", runtime.NumCPU(), "allowed CPU cores")
	memoryGB := fs.Uint64("memory-gb", 2, "allowed memory in GB")
	diskGB := fs.Uint64("disk-gb", 10, "allowed disk in GB")
	gpu := fs.Bool("gpu", true, "allow GPU discovery and use")
	vramGB := fs.Uint64("vram-gb", 0, "allowed VRAM in GB")
	jobSlots := fs.Int("job-slots", 1, "maximum active jobs this worker can accept")
	benchmark := fs.Bool("benchmark", false, "run benchmarks after joining")
	once := fs.Bool("once", false, "join, send one heartbeat, optionally benchmark, then exit")
	rpcEnabled := fs.Bool("rpc", false, "start llama.cpp rpc-server and advertise this worker as an RPC backend")
	rpcHost := fs.String("rpc-host", "0.0.0.0", "llama.cpp rpc-server bind host")
	rpcAdvertiseHost := fs.String("rpc-advertise-host", "", "host or private IP other workers should use to reach this RPC backend")
	rpcPort := fs.Int("rpc-port", 50052, "llama.cpp rpc-server port")
	rpcCache := fs.Bool("rpc-cache", true, "enable llama.cpp rpc-server local file cache")
	stageDaemonURL := fs.String("stage-daemon-url", os.Getenv("CMESH_STAGE_DAEMON_URL"), "stage daemon base URL to advertise for resident CDIP stage sessions")
	if err := fs.Parse(args); err != nil {
		return workerOptions{}, err
	}

	return workerOptions{
		managerURL:       strings.TrimRight(*managerURL, "/"),
		nodeID:           strings.TrimSpace(*nodeID),
		nodeAuthToken:    strings.TrimSpace(*nodeAuthToken),
		name:             *nodeName,
		token:            *token,
		cacheDir:         *cacheDir,
		modelID:          strings.TrimSpace(*modelID),
		modelPath:        strings.TrimSpace(*modelPath),
		modelLayers:      *modelLayers,
		runtimeName:      strings.TrimSpace(*runtimeName),
		runBenchmark:     *benchmark,
		runOnce:          *once,
		rpcEnabled:       *rpcEnabled,
		rpcHost:          strings.TrimSpace(*rpcHost),
		rpcAdvertiseHost: strings.TrimSpace(*rpcAdvertiseHost),
		rpcPort:          *rpcPort,
		rpcCache:         *rpcCache,
		stageDaemonURL:   strings.TrimRight(strings.TrimSpace(*stageDaemonURL), "/"),
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

func workerPollOnce(options workerOptions) error {
	snapshot := discoverWorkerResources(options)
	snapshot = applyWorkerRuntimeModelOverrides(snapshot, options)
	ran, err := pollAndExecuteJob(options.managerURL, options.nodeID, options.nodeAuthToken, options.cacheDir, snapshot)
	if err != nil {
		return err
	}
	if ran {
		fmt.Printf("worker %s executed one assigned job\n", options.nodeID)
		return nil
	}
	fmt.Printf("worker %s had no assigned job\n", options.nodeID)
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
	rpcProcess, err := startWorkerRPCBackend(ctx, options)
	if err != nil {
		return err
	}
	if rpcProcess != nil {
		defer rpcProcess.Stop()
	}
	defer func() {
		if err := sendLeave(options.managerURL, resp.NodeID, resp.NodeAuthToken); err != nil {
			fmt.Fprintf(os.Stderr, "failed to mark worker offline: %v\n", err)
		}
	}()

	ticker := time.NewTicker(heartbeatEvery)
	defer ticker.Stop()

	snapshot := applyWorkerRuntimeModelOverrides(discoverWorkerResources(options), options)
	if err := sendHeartbeat(options.managerURL, resp.NodeID, resp.NodeAuthToken, snapshot); err != nil {
		return err
	}
	if options.runBenchmark {
		if err := runAndSubmitBenchmarks(options.managerURL, resp.NodeID, options.cacheDir); err != nil {
			return err
		}
	}
	if options.runOnce {
		if _, err := pollAndExecuteJob(options.managerURL, resp.NodeID, resp.NodeAuthToken, options.cacheDir, snapshot); err != nil {
			return err
		}
		fmt.Printf("worker %s completed one-shot run\n", resp.NodeID)
		return nil
	}

	jobRunner := newWorkerJobRunner(options.managerURL, resp.NodeID, resp.NodeAuthToken, options.cacheDir, snapshot.JobSlots)
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
			snapshot := applyWorkerRuntimeModelOverrides(discoverWorkerResources(options), options)
			if err := sendHeartbeat(options.managerURL, resp.NodeID, resp.NodeAuthToken, snapshot); err != nil {
				return err
			}
			if err := jobRunner.PollAvailable(snapshot); err != nil {
				return err
			}
			fmt.Printf("heartbeat sent for %s\n", resp.NodeID)
		}
	}
}

func applyWorkerRuntimeModelOverrides(snapshot cluster.ResourceSnapshot, options workerOptions) cluster.ResourceSnapshot {
	if options.runtimeName != "" && !workerRuntimeReady(snapshot, options.runtimeName) {
		snapshot.Runtimes = append(snapshot.Runtimes, cluster.RuntimeResource{
			Name:         options.runtimeName,
			Ready:        true,
			Capabilities: runtimes.LogicalStageCapabilities(),
		})
	}
	snapshot = advertiseWorkerStageDaemon(snapshot, options)
	if options.modelID != "" && !workerModelReady(snapshot, options.modelID) {
		snapshot.Models = append(snapshot.Models, cluster.ModelResource{
			ID:      options.modelID,
			Runtime: firstNonEmptyString(options.runtimeName, string(models.RuntimeLlamaCPP)),
			Path:    options.modelPath,
			Layers:  options.modelLayers,
			Ready:   true,
		})
	}
	return snapshot
}

func advertiseWorkerStageDaemon(snapshot cluster.ResourceSnapshot, options workerOptions) cluster.ResourceSnapshot {
	stageDaemonURL := strings.TrimSpace(options.stageDaemonURL)
	if stageDaemonURL == "" {
		return snapshot
	}
	runtimeName := firstNonEmptyString(options.runtimeName, string(models.RuntimeLlamaCPP))
	ready, blocker := probeStageDaemonURL(stageDaemonURL)
	for index := range snapshot.Runtimes {
		if snapshot.Runtimes[index].Name != runtimeName {
			continue
		}
		snapshot.Runtimes[index].Capabilities = appendMissingString(snapshot.Runtimes[index].Capabilities, runtimes.CapabilityLogicalStageRuntime)
		snapshot.Runtimes[index].StageRuntimes = upsertAdvertisedStageDaemon(snapshot.Runtimes[index].StageRuntimes, stageDaemonURL, ready, blocker)
		return snapshot
	}
	resource := cluster.RuntimeResource{
		Name:          runtimeName,
		Ready:         ready,
		Capabilities:  runtimes.LogicalStageCapabilities(),
		StageRuntimes: upsertAdvertisedStageDaemon(nil, stageDaemonURL, ready, blocker),
	}
	if !ready {
		resource.Error = blocker
	}
	snapshot.Runtimes = append(snapshot.Runtimes, resource)
	return snapshot
}

func upsertAdvertisedStageDaemon(in []cluster.StageRuntimeResource, endpoint string, ready bool, blocker string) []cluster.StageRuntimeResource {
	out := append([]cluster.StageRuntimeResource(nil), in...)
	blockers := []string(nil)
	if !ready && strings.TrimSpace(blocker) != "" {
		blockers = []string{blocker}
	}
	item := cluster.StageRuntimeResource{
		Name:     "cmesh-stage-daemon",
		Ready:    ready,
		Endpoint: strings.TrimRight(strings.TrimSpace(endpoint), "/"),
		Protocol: runtimes.StageSessionV1,
		Blockers: blockers,
	}
	for index := range out {
		if out[index].Name == item.Name || strings.TrimRight(strings.TrimSpace(out[index].Endpoint), "/") == item.Endpoint {
			out[index] = item
			return out
		}
	}
	return append(out, item)
}

func probeStageDaemonURL(stageDaemonURL string) (bool, string) {
	endpoint, err := url.JoinPath(strings.TrimRight(strings.TrimSpace(stageDaemonURL), "/"), "/health")
	if err != nil {
		return false, "invalid stage daemon url: " + err.Error()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, err.Error()
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, resp.Status
	}
	var payload struct {
		Protocol      string                   `json:"protocol"`
		Status        string                   `json:"status"`
		BackendStatus stageDaemonBackendStatus `json:"backend_status"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return false, err.Error()
	}
	if payload.Protocol != runtimes.StageSessionV1 {
		return false, "unexpected stage daemon protocol " + payload.Protocol
	}
	if strings.TrimSpace(payload.BackendStatus.Kind) != "" && !payload.BackendStatus.Ready {
		blocker := strings.TrimSpace(payload.BackendStatus.Blocker)
		if blocker == "" && len(payload.BackendStatus.MissingHooks) > 0 {
			blocker = strings.Join(payload.BackendStatus.MissingHooks, "; ")
		}
		if blocker == "" {
			blocker = "stage daemon backend is not ready"
		}
		return false, blocker
	}
	return true, ""
}

func appendMissingString(in []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return in
	}
	for _, item := range in {
		if item == value {
			return in
		}
	}
	return append(in, value)
}

func runStageRunner(args []string) error {
	if len(args) == 0 {
		printStageRunnerUsage()
		return nil
	}
	switch args[0] {
	case "prepare":
		fs := flag.NewFlagSet("stage-runner prepare", flag.ContinueOnError)
		inputPath := fs.String("input", "", "path to DistributedStageJobInput JSON; reads stdin when empty")
		mode := fs.String("mode", "logical", "stage runner mode: logical or llama.cpp-stage")
		llamaCLI := fs.String("llama-cli", "", "llama-cli path for llama.cpp-stage probing")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		input, err := readStageRunnerInput(*inputPath)
		if err != nil {
			return err
		}
		result, err := executeStageRunnerPrepare(input, *mode, *llamaCLI)
		if err != nil {
			return err
		}
		fmt.Println(result)
		return nil
	case "prefill", "decode", "complete", "abort":
		fs := flag.NewFlagSet("stage-runner "+args[0], flag.ContinueOnError)
		mode := fs.String("mode", "logical", "stage runner mode: logical or llama.cpp-stage")
		llamaCLI := fs.String("llama-cli", "", "llama-cli path for llama.cpp-stage probing")
		parentJobID := fs.String("parent-job", "", "CDIP parent job id")
		stageJobID := fs.String("stage-job", "", "CDIP stage job id")
		stageIndex := fs.Int("stage-index", 0, "CDIP stage index")
		step := fs.Uint64("step", 0, "prefill/decode step")
		upstreamStageJobID := fs.String("upstream-stage", "", "upstream CDIP stage job id")
		downstreamStageJobID := fs.String("downstream-stage", "", "downstream CDIP stage job id")
		downstreamNodeID := fs.String("downstream-node", "", "downstream node id for activation output")
		payload := fs.String("payload", "", "activation payload text for decode")
		payloadFile := fs.String("payload-file", "", "activation payload file for decode")
		encoding := fs.String("encoding", "mock", "activation payload encoding")
		dtype := fs.String("dtype", "u8", "activation dtype")
		shape := fs.String("shape", "", "activation shape as comma-separated integers")
		checksum := fs.String("checksum", "", "activation checksum")
		managerURL := fs.String("manager", "", "manager URL for HTTP activation relay")
		operatorToken := fs.String("operator-token", os.Getenv("CMESH_OPERATOR_TOKEN"), "operator token for HTTP activation relay")
		nodeID := fs.String("node-id", "", "worker node id for HTTP activation relay")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		result, err := executeStageRunnerCommand(stageRunnerCommandOptions{
			Action:            args[0],
			Mode:              *mode,
			LlamaCLI:          *llamaCLI,
			ParentJobID:       *parentJobID,
			StageJobID:        *stageJobID,
			StageIndex:        *stageIndex,
			Step:              *step,
			UpstreamStageID:   *upstreamStageJobID,
			DownstreamStageID: *downstreamStageJobID,
			DownstreamNodeID:  *downstreamNodeID,
			Payload:           *payload,
			PayloadFile:       *payloadFile,
			Encoding:          *encoding,
			DType:             *dtype,
			Shape:             *shape,
			Checksum:          *checksum,
			ManagerURL:        *managerURL,
			OperatorToken:     *operatorToken,
			NodeID:            *nodeID,
		})
		if err != nil {
			return err
		}
		fmt.Println(result)
		return nil
	case "receive":
		fs := flag.NewFlagSet("stage-runner receive", flag.ContinueOnError)
		parentJobID := fs.String("parent-job", "", "CDIP parent job id")
		stageJobID := fs.String("stage-job", "", "upstream CDIP stage job id for the activation stream")
		stageIndex := fs.Int("stage-index", 0, "receiving CDIP stage index")
		upstreamStageJobID := fs.String("upstream-stage", "", "upstream CDIP stage job id")
		downstreamStageJobID := fs.String("downstream-stage", "", "receiving CDIP stage job id")
		managerURL := fs.String("manager", "", "manager URL for HTTP activation relay")
		operatorToken := fs.String("operator-token", os.Getenv("CMESH_OPERATOR_TOKEN"), "operator token for HTTP activation relay")
		nodeID := fs.String("node-id", "", "receiving worker node id for HTTP activation relay")
		timeoutMS := fs.Int("timeout-ms", 1000, "activation receive timeout in milliseconds")
		expectedDType := fs.String("dtype", "", "expected activation dtype")
		expectedShape := fs.String("shape", "", "expected activation shape as comma-separated integers")
		expectedChecksum := fs.String("checksum", "", "expected activation checksum")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		result, err := executeStageRunnerReceive(stageRunnerReceiveOptions{
			ParentJobID:       *parentJobID,
			StageJobID:        *stageJobID,
			StageIndex:        *stageIndex,
			UpstreamStageID:   *upstreamStageJobID,
			DownstreamStageID: *downstreamStageJobID,
			ManagerURL:        *managerURL,
			OperatorToken:     *operatorToken,
			NodeID:            *nodeID,
			TimeoutMS:         *timeoutMS,
			ExpectedDType:     *expectedDType,
			ExpectedShape:     *expectedShape,
			ExpectedChecksum:  *expectedChecksum,
		})
		if err != nil {
			return err
		}
		fmt.Println(result)
		return nil
	case "relay-decode":
		fs := flag.NewFlagSet("stage-runner relay-decode", flag.ContinueOnError)
		parentJobID := fs.String("parent-job", "", "CDIP parent job id")
		upstreamStageJobID := fs.String("upstream-stage", "", "upstream CDIP stage job id to read")
		stageJobID := fs.String("stage-job", "", "current CDIP stage job id to execute and write")
		stageIndex := fs.Int("stage-index", 0, "current CDIP stage index")
		downstreamStageJobID := fs.String("downstream-stage", "", "downstream CDIP stage job id")
		downstreamNodeID := fs.String("downstream-node", "", "downstream node id for activation output")
		managerURL := fs.String("manager", "", "manager URL for HTTP activation relay")
		operatorToken := fs.String("operator-token", os.Getenv("CMESH_OPERATOR_TOKEN"), "operator token for HTTP activation relay")
		nodeID := fs.String("node-id", "", "current worker node id")
		timeoutMS := fs.Int("timeout-ms", 1000, "activation receive timeout in milliseconds")
		runnerBin := fs.String("runner-bin", "", "cmesh-stage-runner binary path")
		modelPath := fs.String("model", "", "GGUF model path")
		stageStart := fs.Int("stage-start", -1, "inclusive stage layer start")
		stageEnd := fs.Int("stage-end", -1, "inclusive stage layer end")
		workDir := fs.String("work-dir", "", "directory for temporary activation files")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		result, err := executeStageRunnerRelayDecode(stageRunnerRelayDecodeOptions{
			ParentJobID:       *parentJobID,
			UpstreamStageID:   *upstreamStageJobID,
			StageJobID:        *stageJobID,
			StageIndex:        *stageIndex,
			DownstreamStageID: *downstreamStageJobID,
			DownstreamNodeID:  *downstreamNodeID,
			ManagerURL:        *managerURL,
			OperatorToken:     *operatorToken,
			NodeID:            *nodeID,
			TimeoutMS:         *timeoutMS,
			RunnerBin:         *runnerBin,
			ModelPath:         *modelPath,
			StageStart:        *stageStart,
			StageEnd:          *stageEnd,
			WorkDir:           *workDir,
		})
		if err != nil {
			return err
		}
		fmt.Println(result)
		return nil
	case "daemon":
		fs := flag.NewFlagSet("stage-runner daemon", flag.ContinueOnError)
		addr := fs.String("addr", "127.0.0.1:19781", "stage daemon HTTP listen address")
		sessionDir := fs.String("session-dir", defaultStageDaemonSessionDir(), "directory for stage daemon session metadata")
		backendName := fs.String("backend", os.Getenv("CMESH_STAGE_DAEMON_BACKEND"), "stage daemon backend: mock or llama.cpp-resident")
		runnerBin := fs.String("runner-bin", os.Getenv("CMESH_STAGE_RUNNER_BIN"), "cmesh-stage-runner binary for llama.cpp resident backend diagnostics")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		backend, err := newStageSessionBackend(*backendName, *runnerBin)
		if err != nil {
			return err
		}
		server := &http.Server{
			Addr:              *addr,
			Handler:           newStageRunnerDaemonHandlerWithBackend(*sessionDir, backend),
			ReadHeaderTimeout: 5 * time.Second,
		}
		fmt.Printf("starting CMesh stage daemon on %s\n", *addr)
		fmt.Printf("stage session dir: %s\n", *sessionDir)
		fmt.Printf("stage daemon backend: %s\n", backend.Kind())
		return server.ListenAndServe()
	case "probe-llamacpp":
		fs := flag.NewFlagSet("stage-runner probe-llamacpp", flag.ContinueOnError)
		llamaCLI := fs.String("llama-cli", "", "llama-cli path to probe")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		probe := runtimes.NewLlamaCPPStageRuntime(*llamaCLI).Probe(context.Background())
		body, err := json.MarshalIndent(probe, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(body))
		return nil
	case "help", "--help", "-h":
		printStageRunnerUsage()
		return nil
	default:
		return fmt.Errorf("unknown stage-runner command %q", args[0])
	}
}

func readStageRunnerInput(path string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" || path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

type stageRunnerPrepareResult struct {
	Kind               string                      `json:"kind"`
	RunnerMode         string                      `json:"runner_mode"`
	ParentJobID        string                      `json:"parent_job_id"`
	StageJobID         string                      `json:"stage_job_id,omitempty"`
	StageIndex         int                         `json:"stage_index"`
	ModelID            string                      `json:"model_id"`
	Runtime            string                      `json:"runtime"`
	LayerStart         int                         `json:"layer_start"`
	LayerEnd           int                         `json:"layer_end"`
	UpstreamNodeID     string                      `json:"upstream_node_id,omitempty"`
	DownstreamNodeID   string                      `json:"downstream_node_id,omitempty"`
	Materialization    string                      `json:"materialization"`
	SourceArtifact     string                      `json:"source_artifact,omitempty"`
	TargetArtifact     string                      `json:"target_artifact,omitempty"`
	Artifact           cdip.ShardArtifact          `json:"artifact,omitempty"`
	PhysicalShardPlan  *runtimes.PhysicalShardPlan `json:"physical_shard_plan,omitempty"`
	ActivationProtocol string                      `json:"activation_protocol"`
	RequiredHooks      []string                    `json:"required_hooks,omitempty"`
	Executable         bool                        `json:"executable"`
}

type stageRunnerCommandOptions struct {
	Action            string
	Mode              string
	LlamaCLI          string
	ParentJobID       string
	StageJobID        string
	StageIndex        int
	Step              uint64
	UpstreamStageID   string
	DownstreamStageID string
	DownstreamNodeID  string
	Payload           string
	PayloadFile       string
	Encoding          string
	DType             string
	Shape             string
	Checksum          string
	ManagerURL        string
	OperatorToken     string
	NodeID            string
}

type stageRunnerCommandResult struct {
	Kind              string                   `json:"kind"`
	RunnerMode        string                   `json:"runner_mode"`
	Action            string                   `json:"action"`
	ParentJobID       string                   `json:"parent_job_id"`
	StageJobID        string                   `json:"stage_job_id"`
	StageIndex        int                      `json:"stage_index"`
	Step              uint64                   `json:"step"`
	ActivationFrame   *cdip.ActivationChunk    `json:"activation_frame,omitempty"`
	ActivationBytes   int                      `json:"activation_bytes,omitempty"`
	ActivationRelay   string                   `json:"activation_relay,omitempty"`
	TensorEnvelope    *runtimes.TensorEnvelope `json:"tensor_envelope,omitempty"`
	UpstreamStageID   string                   `json:"upstream_stage_job_id,omitempty"`
	DownstreamStageID string                   `json:"downstream_stage_job_id,omitempty"`
	DownstreamNodeID  string                   `json:"downstream_node_id,omitempty"`
	StageSession      *runtimes.StageSession   `json:"stage_session,omitempty"`
	StageDaemonDecode map[string]any           `json:"stage_daemon_decode,omitempty"`
	Executable        bool                     `json:"executable"`
}

type stageRunnerReceiveOptions struct {
	ParentJobID       string
	StageJobID        string
	StageIndex        int
	UpstreamStageID   string
	DownstreamStageID string
	ManagerURL        string
	OperatorToken     string
	NodeID            string
	TimeoutMS         int
	ExpectedDType     string
	ExpectedShape     string
	ExpectedChecksum  string
}

type stageRunnerReceiveResult struct {
	Kind              string                   `json:"kind"`
	RunnerMode        string                   `json:"runner_mode"`
	Action            string                   `json:"action"`
	ParentJobID       string                   `json:"parent_job_id"`
	StageJobID        string                   `json:"stage_job_id"`
	StageIndex        int                      `json:"stage_index"`
	ActivationFrame   *cdip.ActivationChunk    `json:"activation_frame,omitempty"`
	ActivationBytes   int                      `json:"activation_bytes"`
	ActivationRelay   string                   `json:"activation_relay"`
	TensorEnvelope    *runtimes.TensorEnvelope `json:"tensor_envelope,omitempty"`
	UpstreamStageID   string                   `json:"upstream_stage_job_id,omitempty"`
	DownstreamStageID string                   `json:"downstream_stage_job_id,omitempty"`
	NodeID            string                   `json:"node_id,omitempty"`
	Executable        bool                     `json:"executable"`
}

type stageRunnerRelayDecodeOptions struct {
	ParentJobID        string
	UpstreamStageID    string
	StageJobID         string
	StageIndex         int
	Step               uint64
	KVCacheKey         string
	DownstreamStageID  string
	DownstreamNodeID   string
	ManagerURL         string
	OperatorToken      string
	NodeID             string
	TimeoutMS          int
	RunnerBin          string
	ModelID            string
	ModelPath          string
	StageStart         int
	StageEnd           int
	WorkDir            string
	StageDaemonURL     string
	StageSessionID     string
	TerminalForceFinal *bool
	PreviousTokenID    *int
	PreviousTokenText  string
}

func stageRunnerRecoveryInput(options stageRunnerRelayDecodeOptions) models.DistributedStageJobInput {
	stage := models.DistributedStageInput{
		Index:      options.StageIndex,
		NodeID:     strings.TrimSpace(options.NodeID),
		LayerStart: options.StageStart,
		LayerEnd:   options.StageEnd,
	}
	if options.StageEnd >= options.StageStart {
		stage.Layers = options.StageEnd - options.StageStart + 1
	}
	return models.DistributedStageJobInput{
		ParentJobID:       strings.TrimSpace(options.ParentJobID),
		StageJobID:        strings.TrimSpace(options.StageJobID),
		StageCommand:      "",
		Step:              stageCommandStep(options.Step),
		KVCacheKey:        strings.TrimSpace(options.KVCacheKey),
		Stage:             stage,
		StageDaemonURL:    strings.TrimSpace(options.StageDaemonURL),
		StageSessionID:    strings.TrimSpace(options.StageSessionID),
		ModelID:           strings.TrimSpace(options.ModelID),
		ModelPath:         strings.TrimSpace(options.ModelPath),
		PreviousTokenID:   options.PreviousTokenID,
		PreviousTokenText: strings.TrimSpace(options.PreviousTokenText),
	}
}

type stageRunnerRelayDecodeResult struct {
	Kind              string                     `json:"kind"`
	RunnerMode        string                     `json:"runner_mode"`
	Action            string                     `json:"action"`
	ParentJobID       string                     `json:"parent_job_id"`
	UpstreamStageID   string                     `json:"upstream_stage_job_id"`
	StageJobID        string                     `json:"stage_job_id"`
	StageIndex        int                        `json:"stage_index"`
	Step              uint64                     `json:"step"`
	KVCacheKey        string                     `json:"kv_cache_key,omitempty"`
	DownstreamStageID string                     `json:"downstream_stage_job_id"`
	DownstreamNodeID  string                     `json:"downstream_node_id"`
	ActivationRelay   string                     `json:"activation_relay"`
	InputFrame        *cdip.ActivationChunk      `json:"input_frame,omitempty"`
	OutputFrame       *cdip.ActivationChunk      `json:"output_frame,omitempty"`
	InputEnvelope     *runtimes.TensorEnvelope   `json:"input_tensor_envelope,omitempty"`
	OutputEnvelope    *runtimes.TensorEnvelope   `json:"output_tensor_envelope,omitempty"`
	RunnerReport      *llamaCPPStageDecodeReport `json:"runner_report,omitempty"`
	InputBytes        int                        `json:"input_bytes"`
	OutputBytes       int                        `json:"output_bytes"`
	WorkDir           string                     `json:"work_dir,omitempty"`
	StageDaemonDecode map[string]any             `json:"stage_daemon_decode,omitempty"`
	Timing            stageRunnerTiming          `json:"timing"`
	Executable        bool                       `json:"executable"`
}

type stageRunnerSourceDecodeResult struct {
	Kind              string                     `json:"kind"`
	RunnerMode        string                     `json:"runner_mode"`
	Action            string                     `json:"action"`
	ParentJobID       string                     `json:"parent_job_id"`
	StageJobID        string                     `json:"stage_job_id"`
	StageIndex        int                        `json:"stage_index"`
	Step              uint64                     `json:"step"`
	KVCacheKey        string                     `json:"kv_cache_key,omitempty"`
	DownstreamStageID string                     `json:"downstream_stage_job_id"`
	DownstreamNodeID  string                     `json:"downstream_node_id"`
	ActivationRelay   string                     `json:"activation_relay"`
	OutputFrame       *cdip.ActivationChunk      `json:"output_frame,omitempty"`
	OutputEnvelope    *runtimes.TensorEnvelope   `json:"output_tensor_envelope,omitempty"`
	RunnerReport      *llamaCPPStageDecodeReport `json:"runner_report,omitempty"`
	OutputBytes       int                        `json:"output_bytes"`
	WorkDir           string                     `json:"work_dir,omitempty"`
	StageDaemonDecode map[string]any             `json:"stage_daemon_decode,omitempty"`
	Timing            stageRunnerTiming          `json:"timing"`
	Executable        bool                       `json:"executable"`
	PreviousTokenID   *int                       `json:"previous_token_id,omitempty"`
	PreviousTokenText string                     `json:"previous_token_text,omitempty"`
}

type stageRunnerTerminalDecodeResult struct {
	Kind              string                       `json:"kind"`
	RunnerMode        string                       `json:"runner_mode"`
	Action            string                       `json:"action"`
	ParentJobID       string                       `json:"parent_job_id"`
	UpstreamStageID   string                       `json:"upstream_stage_job_id"`
	StageJobID        string                       `json:"stage_job_id"`
	StageIndex        int                          `json:"stage_index"`
	Step              uint64                       `json:"step"`
	KVCacheKey        string                       `json:"kv_cache_key,omitempty"`
	ActivationRelay   string                       `json:"activation_relay"`
	InputFrame        *cdip.ActivationChunk        `json:"input_frame,omitempty"`
	InputEnvelope     *runtimes.TensorEnvelope     `json:"input_tensor_envelope,omitempty"`
	RunnerReport      *llamaCPPStageTerminalReport `json:"runner_report,omitempty"`
	InputBytes        int                          `json:"input_bytes"`
	NextTokenID       int                          `json:"next_token_id"`
	NextTokenText     string                       `json:"next_token_text"`
	Tokens            []int                        `json:"tokens,omitempty"`
	Output            string                       `json:"output,omitempty"`
	Final             bool                         `json:"final"`
	WorkDir           string                       `json:"work_dir,omitempty"`
	StageDaemonDecode map[string]any               `json:"stage_daemon_decode,omitempty"`
	Timing            stageRunnerTiming            `json:"timing"`
	Executable        bool                         `json:"executable"`
}

type stageRunnerTiming struct {
	ReceiveWaitMS  int64 `json:"receive_wait_ms,omitempty"`
	StageComputeMS int64 `json:"stage_compute_ms,omitempty"`
	RelayWriteMS   int64 `json:"relay_write_ms,omitempty"`
	StageDaemonMS  int64 `json:"stage_daemon_ms,omitempty"`
	TotalMS        int64 `json:"total_ms,omitempty"`
}

type llamaCPPStageDecodeReport struct {
	Kind        string `json:"kind"`
	Status      string `json:"status"`
	Runtime     string `json:"runtime"`
	StageIndex  int    `json:"stage_index"`
	StageStart  int    `json:"stage_start"`
	StageEnd    int    `json:"stage_end"`
	InputTensor struct {
		DType string `json:"dtype"`
		Shape []int  `json:"shape"`
		Bytes int    `json:"bytes"`
	} `json:"input_tensor"`
	OutputTensor struct {
		DType string `json:"dtype"`
		Shape []int  `json:"shape"`
		Bytes int    `json:"bytes"`
		Path  string `json:"path"`
	} `json:"output_tensor"`
	DecodeStatus int `json:"decode_status"`
}

type llamaCPPStageTerminalReport struct {
	Kind        string `json:"kind"`
	Status      string `json:"status"`
	Runtime     string `json:"runtime"`
	StageIndex  int    `json:"stage_index"`
	StageStart  int    `json:"stage_start"`
	StageEnd    int    `json:"stage_end"`
	InputTensor struct {
		DType string `json:"dtype"`
		Shape []int  `json:"shape"`
		Bytes int    `json:"bytes"`
	} `json:"input_tensor"`
	Logits struct {
		DType string `json:"dtype"`
		Shape []int  `json:"shape"`
		Bytes int    `json:"bytes"`
	} `json:"logits"`
	NextTokenID   int    `json:"next_token_id"`
	NextTokenText string `json:"next_token_text"`
	Tokens        []int  `json:"tokens,omitempty"`
	Output        string `json:"output,omitempty"`
	Final         *bool  `json:"final,omitempty"`
	DecodeStatus  int    `json:"decode_status"`
}

func executeStageRunnerPrepare(input []byte, mode string, llamaCLI string) (string, error) {
	var req models.DistributedStageJobInput
	if err := json.Unmarshal(input, &req); err != nil {
		return "", fmt.Errorf("invalid distributed stage input: %w", err)
	}
	if strings.TrimSpace(req.ParentJobID) == "" {
		return "", fmt.Errorf("parent_job_id is required")
	}
	if strings.TrimSpace(req.ModelID) == "" {
		return "", fmt.Errorf("model_id is required")
	}
	stageReq := runtimes.StagePrepareRequest{
		ParentJobID:      req.ParentJobID,
		StageJobID:       fmt.Sprintf("%s-stage-%d", req.ParentJobID, req.Stage.Index),
		ModelID:          req.ModelID,
		ModelPath:        req.ModelPath,
		Stage:            req.Stage,
		Shard:            req.Shard,
		UpstreamNodeID:   req.UpstreamNodeID,
		DownstreamNodeID: req.DownstreamNodeID,
	}
	mode = strings.TrimSpace(strings.ToLower(mode))
	if mode == "" {
		mode = "logical"
	}
	switch mode {
	case "logical":
		prepared, err := runtimes.NewLogicalStageRuntime(req.Shard.Runtime).PrepareStage(context.Background(), stageReq)
		if err != nil {
			return "", err
		}
		return marshalStageRunnerPrepareResult(stageRunnerPrepareResult{
			Kind:               prepared.Kind,
			RunnerMode:         mode,
			ParentJobID:        prepared.ParentJobID,
			StageJobID:         stageReq.StageJobID,
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
			Artifact:           prepared.Artifact,
			PhysicalShardPlan:  prepared.PhysicalShardPlan,
			ActivationProtocol: prepared.ActivationProtocol,
			Executable:         true,
		})
	case "llama.cpp-stage", "llamacpp-stage":
		runtime := runtimes.NewLlamaCPPStageRuntime(llamaCLI)
		prepared, err := runtime.PrepareStage(context.Background(), stageReq)
		if err != nil {
			return "", fmt.Errorf("llama.cpp stage runner blocked: %w", err)
		}
		return marshalStageRunnerPrepareResult(stageRunnerPrepareResult{
			Kind:               prepared.Kind,
			RunnerMode:         mode,
			ParentJobID:        prepared.ParentJobID,
			StageJobID:         stageReq.StageJobID,
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
			Artifact:           prepared.Artifact,
			PhysicalShardPlan:  prepared.PhysicalShardPlan,
			ActivationProtocol: prepared.ActivationProtocol,
			Executable:         true,
		})
	default:
		return "", fmt.Errorf("unsupported stage runner mode %q", mode)
	}
}

func executeStageRunnerCommand(options stageRunnerCommandOptions) (string, error) {
	if strings.TrimSpace(options.ParentJobID) == "" {
		return "", fmt.Errorf("parent-job is required")
	}
	if strings.TrimSpace(options.StageJobID) == "" {
		return "", fmt.Errorf("stage-job is required")
	}
	stageRuntime, runnerMode, err := stageRunnerRuntime(options.Mode, options.LlamaCLI)
	if err != nil {
		return "", err
	}
	payload, err := stageRunnerPayload(options.Payload, options.PayloadFile)
	if err != nil {
		return "", err
	}
	shape, err := parseStageRunnerShape(options.Shape)
	if err != nil {
		return "", err
	}
	var activationTransport transport.ActivationTransport
	activationRelay := "none"
	if strings.TrimSpace(options.ManagerURL) != "" {
		activationTransport = transport.NewHTTPActivationTransport(options.ManagerURL, options.OperatorToken).WithNodeID(options.NodeID)
		activationRelay = "http"
	}
	req := runtimes.StageCommandRequest{
		ParentJobID:          strings.TrimSpace(options.ParentJobID),
		StageJobID:           strings.TrimSpace(options.StageJobID),
		StageIndex:           options.StageIndex,
		Step:                 options.Step,
		ActivationTransport:  activationTransport,
		UpstreamStageJobID:   strings.TrimSpace(options.UpstreamStageID),
		DownstreamStageJobID: strings.TrimSpace(options.DownstreamStageID),
		DownstreamNodeID:     strings.TrimSpace(options.DownstreamNodeID),
		ActivationPayload:    payload,
		ActivationEncoding:   options.Encoding,
		ActivationShape:      shape,
		ActivationDType:      options.DType,
		ActivationChecksum:   strings.TrimSpace(options.Checksum),
	}
	var commandResult runtimes.StageCommandResult
	switch strings.TrimSpace(strings.ToLower(options.Action)) {
	case "prefill":
		commandResult, err = stageRuntime.PrefillStage(context.Background(), req)
	case "decode":
		commandResult, err = stageRuntime.DecodeStage(context.Background(), req)
	case "complete":
		commandResult, err = stageRuntime.CompleteStage(context.Background(), req)
	case "abort":
		commandResult, err = stageRuntime.AbortStage(context.Background(), req)
	default:
		return "", fmt.Errorf("unsupported stage runner action %q", options.Action)
	}
	if err != nil {
		return "", err
	}
	body, err := json.MarshalIndent(stageRunnerCommandResult{
		Kind:              commandResult.Kind,
		RunnerMode:        runnerMode,
		Action:            options.Action,
		ParentJobID:       commandResult.ParentJobID,
		StageJobID:        commandResult.StageJobID,
		StageIndex:        commandResult.StageIndex,
		Step:              commandResult.Step,
		ActivationFrame:   commandResult.ActivationFrame,
		ActivationBytes:   commandResult.ActivationBytes,
		ActivationRelay:   activationRelay,
		TensorEnvelope:    commandResult.TensorEnvelope,
		UpstreamStageID:   strings.TrimSpace(options.UpstreamStageID),
		DownstreamStageID: strings.TrimSpace(options.DownstreamStageID),
		DownstreamNodeID:  strings.TrimSpace(options.DownstreamNodeID),
		Executable:        true,
	}, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func executeStageRunnerReceive(options stageRunnerReceiveOptions) (string, error) {
	parentJobID := strings.TrimSpace(options.ParentJobID)
	stageJobID := strings.TrimSpace(options.StageJobID)
	managerURL := strings.TrimSpace(options.ManagerURL)
	nodeID := strings.TrimSpace(options.NodeID)
	if parentJobID == "" {
		return "", fmt.Errorf("parent-job is required")
	}
	if stageJobID == "" {
		return "", fmt.Errorf("stage-job is required")
	}
	if managerURL == "" {
		return "", fmt.Errorf("manager is required")
	}
	expectedShape, err := parseStageRunnerShape(options.ExpectedShape)
	if err != nil {
		return "", err
	}
	timeout := time.Duration(options.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = time.Second
	}
	activationTransport := transport.NewHTTPActivationTransport(managerURL, options.OperatorToken).
		WithNodeID(nodeID).
		WithPollTimeout(timeout)
	reader, err := activationTransport.OpenReader(context.Background(), transport.StreamID{ParentJobID: parentJobID, StageJobID: stageJobID})
	if err != nil {
		return "", err
	}
	started := time.Now()
	frame, err := reader.Receive(context.Background())
	if err != nil {
		return "", err
	}
	envelope := runtimes.TensorEnvelopeFromActivation(frame.Header, frame.Payload, options.StageIndex, options.UpstreamStageID, options.DownstreamStageID, nodeID, time.Since(started).Milliseconds())
	if err := envelope.ValidatePayload(frame.Payload); err != nil {
		return "", err
	}
	if expected := strings.TrimSpace(options.ExpectedDType); expected != "" && envelope.DType != expected {
		return "", fmt.Errorf("activation dtype mismatch: expected=%s actual=%s", expected, envelope.DType)
	}
	if len(expectedShape) > 0 && !intSlicesEqual(envelope.Shape, expectedShape) {
		return "", fmt.Errorf("activation shape mismatch: expected=%v actual=%v", expectedShape, envelope.Shape)
	}
	if expected := strings.TrimSpace(options.ExpectedChecksum); expected != "" && envelope.Checksum != expected {
		return "", fmt.Errorf("activation checksum mismatch: expected=%s actual=%s", expected, envelope.Checksum)
	}
	body, err := json.MarshalIndent(stageRunnerReceiveResult{
		Kind:              "cdip.stage_receive",
		RunnerMode:        "logical",
		Action:            "receive",
		ParentJobID:       parentJobID,
		StageJobID:        stageJobID,
		StageIndex:        options.StageIndex,
		ActivationFrame:   &frame.Header,
		ActivationBytes:   len(frame.Payload),
		ActivationRelay:   "http",
		TensorEnvelope:    &envelope,
		UpstreamStageID:   strings.TrimSpace(options.UpstreamStageID),
		DownstreamStageID: strings.TrimSpace(options.DownstreamStageID),
		NodeID:            nodeID,
		Executable:        true,
	}, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func executeStageRunnerRelayDecode(options stageRunnerRelayDecodeOptions) (string, error) {
	parentJobID := strings.TrimSpace(options.ParentJobID)
	upstreamStageID := strings.TrimSpace(options.UpstreamStageID)
	stageJobID := strings.TrimSpace(options.StageJobID)
	downstreamStageID := strings.TrimSpace(options.DownstreamStageID)
	managerURL := strings.TrimSpace(options.ManagerURL)
	nodeID := strings.TrimSpace(options.NodeID)
	runnerBin := strings.TrimSpace(options.RunnerBin)
	modelPath := strings.TrimSpace(options.ModelPath)
	if parentJobID == "" {
		return "", fmt.Errorf("parent-job is required")
	}
	if upstreamStageID == "" {
		return "", fmt.Errorf("upstream-stage is required")
	}
	if stageJobID == "" {
		return "", fmt.Errorf("stage-job is required")
	}
	if downstreamStageID == "" {
		return "", fmt.Errorf("downstream-stage is required")
	}
	if strings.TrimSpace(options.DownstreamNodeID) == "" {
		return "", fmt.Errorf("downstream-node is required")
	}
	if managerURL == "" {
		return "", fmt.Errorf("manager is required")
	}
	if runnerBin == "" {
		return "", fmt.Errorf("runner-bin is required")
	}
	if modelPath == "" {
		return "", fmt.Errorf("model is required")
	}
	if options.StageStart < 0 || options.StageEnd < options.StageStart {
		return "", fmt.Errorf("valid stage-start and stage-end are required")
	}

	timeout := time.Duration(options.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = time.Second
	}
	totalStarted := time.Now()
	activationTransport := transport.NewHTTPActivationTransport(managerURL, options.OperatorToken).
		WithNodeID(nodeID).
		WithPollTimeout(timeout)

	reader, err := activationTransport.OpenReader(context.Background(), transport.StreamID{ParentJobID: parentJobID, StageJobID: upstreamStageID})
	if err != nil {
		return "", err
	}
	receiveStarted := time.Now()
	inputFrame, err := reader.Receive(context.Background())
	if err != nil {
		return "", err
	}
	receiveWaitMS := time.Since(receiveStarted).Milliseconds()
	inputEnvelope := runtimes.TensorEnvelopeFromActivation(inputFrame.Header, inputFrame.Payload, options.StageIndex, upstreamStageID, stageJobID, nodeID, receiveWaitMS)
	inputEnvelope.KVCacheKey = strings.TrimSpace(options.KVCacheKey)
	if err := inputEnvelope.ValidatePayload(inputFrame.Payload); err != nil {
		return "", err
	}
	bridgePlan, err := runtimes.BuildLlamaCPPEmbeddingBatchPlan(inputEnvelope)
	if err != nil {
		return "", err
	}

	workDir := strings.TrimSpace(options.WorkDir)
	if workDir == "" {
		workDir, err = os.MkdirTemp("", "cmesh-stage-relay-decode-*")
		if err != nil {
			return "", err
		}
	} else if err := os.MkdirAll(workDir, 0o755); err != nil {
		return "", err
	}
	inputPath := filepath.Join(workDir, "activation-in.bin")
	outputPath := filepath.Join(workDir, "activation-out.bin")
	reportPath := filepath.Join(workDir, "stage-runner-decode.json")
	if err := os.WriteFile(inputPath, inputFrame.Payload, 0o600); err != nil {
		return "", err
	}

	args := []string{
		"--command", "decode",
		"--model", modelPath,
		"--stage-start", strconv.Itoa(options.StageStart),
		"--stage-end", strconv.Itoa(options.StageEnd),
		"--stage-index", strconv.Itoa(options.StageIndex),
		"--activation-file", inputPath,
		"--dtype", inputEnvelope.DType,
		"--shape", joinInts(inputEnvelope.Shape),
		"--output-file", outputPath,
	}
	ctx, cancel := context.WithTimeout(context.Background(), maxDuration(30*time.Second, timeout*10))
	defer cancel()
	cmd := exec.CommandContext(ctx, runnerBin, args...)
	cmd.Env = stageRunnerCommandEnv(os.Environ(), options)
	computeStarted := time.Now()
	reportBytes, err := cmd.CombinedOutput()
	stageComputeMS := time.Since(computeStarted).Milliseconds()
	if writeErr := os.WriteFile(reportPath, reportBytes, 0o600); writeErr != nil && err == nil {
		err = writeErr
	}
	if err != nil {
		return "", fmt.Errorf("stage runner decode failed: %w: %s", err, strings.TrimSpace(string(reportBytes)))
	}
	var report llamaCPPStageDecodeReport
	if err := json.Unmarshal(stageRunnerJSONReport(reportBytes, "cmesh.llamacpp_stage_decode"), &report); err != nil {
		return "", fmt.Errorf("parse stage runner decode report: %w", err)
	}
	if report.Status != "executed" || report.OutputTensor.Bytes <= 0 || len(report.OutputTensor.Shape) == 0 {
		return "", fmt.Errorf("stage runner decode did not produce an output activation: %#v", report)
	}
	outputPayload, err := os.ReadFile(outputPath)
	if err != nil {
		return "", err
	}
	if len(outputPayload) != report.OutputTensor.Bytes {
		return "", fmt.Errorf("stage runner output byte mismatch: report=%d actual=%d", report.OutputTensor.Bytes, len(outputPayload))
	}
	sum := sha256.Sum256(outputPayload)
	outputChecksum := "sha256:" + hex.EncodeToString(sum[:])

	outputFrame := transport.ActivationFrame{
		Header: cdip.ActivationChunk{
			Envelope:     cdip.NewEnvelope(cdip.MessageActivationChunk),
			ParentJobID:  parentJobID,
			StageJobID:   stageJobID,
			Sequence:     stageCommandStep(options.Step),
			ContentType:  "application/vnd.cmesh.activation+binary",
			Encoding:     "raw",
			Shape:        report.OutputTensor.Shape,
			DType:        report.OutputTensor.DType,
			PayloadBytes: uint64(len(outputPayload)),
			Checksum:     outputChecksum,
		},
		Payload: outputPayload,
	}
	outputEnvelope := runtimes.TensorEnvelopeFromActivation(outputFrame.Header, outputFrame.Payload, options.StageIndex, stageJobID, downstreamStageID, options.DownstreamNodeID, 0)
	outputEnvelope.KVCacheKey = strings.TrimSpace(options.KVCacheKey)
	if err := outputEnvelope.ValidatePayload(outputFrame.Payload); err != nil {
		return "", err
	}
	writer, err := activationTransport.OpenWriter(context.Background(), transport.StreamID{ParentJobID: parentJobID, StageJobID: stageJobID}, options.DownstreamNodeID)
	if err != nil {
		return "", err
	}
	relayWriteStarted := time.Now()
	if err := writer.Send(context.Background(), outputFrame); err != nil {
		return "", err
	}
	relayWriteMS := time.Since(relayWriteStarted).Milliseconds()
	recoveryInput := stageRunnerRecoveryInput(options)
	stageDaemonStarted := time.Now()
	stageDaemonDecode, err := postStageDaemonDecodeForStageWithRecovery(context.Background(), recoveryInput, options.StageJobID, "relay_decode", &outputEnvelope, outputPayload, "", nil, "")
	if err != nil {
		return "", err
	}
	stageDaemonMS := time.Since(stageDaemonStarted).Milliseconds()
	body, err := json.MarshalIndent(stageRunnerRelayDecodeResult{
		Kind:              "cdip.stage_relay_decode",
		RunnerMode:        "llama.cpp-stage",
		Action:            "relay-decode",
		ParentJobID:       parentJobID,
		UpstreamStageID:   upstreamStageID,
		StageJobID:        stageJobID,
		StageIndex:        options.StageIndex,
		Step:              stageCommandStep(options.Step),
		KVCacheKey:        strings.TrimSpace(options.KVCacheKey),
		DownstreamStageID: downstreamStageID,
		DownstreamNodeID:  strings.TrimSpace(options.DownstreamNodeID),
		ActivationRelay:   "http",
		InputFrame:        &inputFrame.Header,
		OutputFrame:       &outputFrame.Header,
		InputEnvelope:     &inputEnvelope,
		OutputEnvelope:    &outputEnvelope,
		RunnerReport:      &report,
		InputBytes:        bridgePlan.SourceByteCount,
		OutputBytes:       len(outputPayload),
		WorkDir:           workDir,
		StageDaemonDecode: stageDaemonDecode,
		Timing: stageRunnerTiming{
			ReceiveWaitMS:  receiveWaitMS,
			StageComputeMS: stageComputeMS,
			RelayWriteMS:   relayWriteMS,
			StageDaemonMS:  stageDaemonMS,
			TotalMS:        time.Since(totalStarted).Milliseconds(),
		},
		Executable: true,
	}, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func executeStageRunnerSourceDecode(options stageRunnerRelayDecodeOptions, prompt string) (string, error) {
	parentJobID := strings.TrimSpace(options.ParentJobID)
	stageJobID := strings.TrimSpace(options.StageJobID)
	downstreamStageID := strings.TrimSpace(options.DownstreamStageID)
	managerURL := strings.TrimSpace(options.ManagerURL)
	nodeID := strings.TrimSpace(options.NodeID)
	runnerBin := strings.TrimSpace(options.RunnerBin)
	modelPath := strings.TrimSpace(options.ModelPath)
	if parentJobID == "" {
		return "", fmt.Errorf("parent-job is required")
	}
	if stageJobID == "" {
		return "", fmt.Errorf("stage-job is required")
	}
	if downstreamStageID == "" {
		return "", fmt.Errorf("downstream-stage is required")
	}
	if strings.TrimSpace(options.DownstreamNodeID) == "" {
		return "", fmt.Errorf("downstream-node is required")
	}
	if managerURL == "" {
		return "", fmt.Errorf("manager is required")
	}
	if runnerBin == "" {
		return "", fmt.Errorf("runner-bin is required")
	}
	if modelPath == "" {
		return "", fmt.Errorf("model is required")
	}
	if strings.TrimSpace(prompt) == "" {
		return "", fmt.Errorf("prompt is required")
	}
	if options.StageStart != 0 || options.StageEnd < options.StageStart {
		return "", fmt.Errorf("source decode requires a first-stage range starting at layer 0")
	}

	timeout := time.Duration(options.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = time.Second
	}
	totalStarted := time.Now()
	workDir := strings.TrimSpace(options.WorkDir)
	var err error
	if workDir == "" {
		workDir, err = os.MkdirTemp("", "cmesh-stage-source-decode-*")
		if err != nil {
			return "", err
		}
	} else if err := os.MkdirAll(workDir, 0o755); err != nil {
		return "", err
	}
	outputPath := filepath.Join(workDir, "activation-out.bin")
	reportPath := filepath.Join(workDir, "stage-runner-source-decode.json")
	args := []string{
		"--command", "source-decode",
		"--model", modelPath,
		"--stage-start", strconv.Itoa(options.StageStart),
		"--stage-end", strconv.Itoa(options.StageEnd),
		"--stage-index", strconv.Itoa(options.StageIndex),
		"--prompt", prompt,
		"--output-file", outputPath,
	}
	if options.PreviousTokenID != nil {
		args = append(args, "--token-id", strconv.Itoa(*options.PreviousTokenID))
	}
	ctx, cancel := context.WithTimeout(context.Background(), maxDuration(30*time.Second, timeout*10))
	defer cancel()
	cmd := exec.CommandContext(ctx, runnerBin, args...)
	cmd.Env = stageRunnerCommandEnv(os.Environ(), options)
	computeStarted := time.Now()
	reportBytes, err := cmd.CombinedOutput()
	stageComputeMS := time.Since(computeStarted).Milliseconds()
	if writeErr := os.WriteFile(reportPath, reportBytes, 0o600); writeErr != nil && err == nil {
		err = writeErr
	}
	if err != nil {
		return "", fmt.Errorf("stage runner source decode failed: %w: %s", err, strings.TrimSpace(string(reportBytes)))
	}
	var report llamaCPPStageDecodeReport
	if err := json.Unmarshal(stageRunnerJSONReport(reportBytes, "cmesh.llamacpp_stage_source_decode"), &report); err != nil {
		return "", fmt.Errorf("parse stage runner source decode report: %w", err)
	}
	if report.Status != "executed" || report.OutputTensor.Bytes <= 0 || len(report.OutputTensor.Shape) == 0 {
		return "", fmt.Errorf("stage runner source decode did not produce an output activation: %#v", report)
	}
	outputPayload, err := os.ReadFile(outputPath)
	if err != nil {
		return "", err
	}
	if len(outputPayload) != report.OutputTensor.Bytes {
		return "", fmt.Errorf("stage runner source output byte mismatch: report=%d actual=%d", report.OutputTensor.Bytes, len(outputPayload))
	}
	sum := sha256.Sum256(outputPayload)
	outputChecksum := "sha256:" + hex.EncodeToString(sum[:])
	outputFrame := transport.ActivationFrame{
		Header: cdip.ActivationChunk{
			Envelope:     cdip.NewEnvelope(cdip.MessageActivationChunk),
			ParentJobID:  parentJobID,
			StageJobID:   stageJobID,
			Sequence:     stageCommandStep(options.Step),
			ContentType:  "application/vnd.cmesh.activation+binary",
			Encoding:     "raw",
			Shape:        report.OutputTensor.Shape,
			DType:        report.OutputTensor.DType,
			PayloadBytes: uint64(len(outputPayload)),
			Checksum:     outputChecksum,
		},
		Payload: outputPayload,
	}
	outputEnvelope := runtimes.TensorEnvelopeFromActivation(outputFrame.Header, outputFrame.Payload, options.StageIndex, stageJobID, downstreamStageID, options.DownstreamNodeID, 0)
	outputEnvelope.KVCacheKey = strings.TrimSpace(options.KVCacheKey)
	if err := outputEnvelope.ValidatePayload(outputFrame.Payload); err != nil {
		return "", err
	}
	activationTransport := transport.NewHTTPActivationTransport(managerURL, options.OperatorToken).
		WithNodeID(nodeID).
		WithPollTimeout(timeout)
	writer, err := activationTransport.OpenWriter(context.Background(), transport.StreamID{ParentJobID: parentJobID, StageJobID: stageJobID}, options.DownstreamNodeID)
	if err != nil {
		return "", err
	}
	relayWriteStarted := time.Now()
	if err := writer.Send(context.Background(), outputFrame); err != nil {
		return "", err
	}
	relayWriteMS := time.Since(relayWriteStarted).Milliseconds()
	recoveryInput := stageRunnerRecoveryInput(options)
	stageDaemonStarted := time.Now()
	stageDaemonDecode, err := postStageDaemonDecodeForStageWithRecovery(context.Background(), recoveryInput, options.StageJobID, "source_decode", &outputEnvelope, outputPayload, prompt, options.PreviousTokenID, options.PreviousTokenText)
	if err != nil {
		return "", err
	}
	stageDaemonMS := time.Since(stageDaemonStarted).Milliseconds()
	body, err := json.MarshalIndent(stageRunnerSourceDecodeResult{
		Kind:              "cdip.stage_source_decode",
		RunnerMode:        "llama.cpp-stage",
		Action:            "source-decode",
		ParentJobID:       parentJobID,
		StageJobID:        stageJobID,
		StageIndex:        options.StageIndex,
		Step:              stageCommandStep(options.Step),
		KVCacheKey:        strings.TrimSpace(options.KVCacheKey),
		DownstreamStageID: downstreamStageID,
		DownstreamNodeID:  strings.TrimSpace(options.DownstreamNodeID),
		ActivationRelay:   "http",
		OutputFrame:       &outputFrame.Header,
		OutputEnvelope:    &outputEnvelope,
		RunnerReport:      &report,
		OutputBytes:       len(outputPayload),
		WorkDir:           workDir,
		StageDaemonDecode: stageDaemonDecode,
		Timing: stageRunnerTiming{
			StageComputeMS: stageComputeMS,
			RelayWriteMS:   relayWriteMS,
			StageDaemonMS:  stageDaemonMS,
			TotalMS:        time.Since(totalStarted).Milliseconds(),
		},
		Executable:        true,
		PreviousTokenID:   options.PreviousTokenID,
		PreviousTokenText: strings.TrimSpace(options.PreviousTokenText),
	}, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func executeStageRunnerTerminalDecode(options stageRunnerRelayDecodeOptions) (string, error) {
	parentJobID := strings.TrimSpace(options.ParentJobID)
	upstreamStageID := strings.TrimSpace(options.UpstreamStageID)
	stageJobID := strings.TrimSpace(options.StageJobID)
	managerURL := strings.TrimSpace(options.ManagerURL)
	nodeID := strings.TrimSpace(options.NodeID)
	runnerBin := strings.TrimSpace(options.RunnerBin)
	modelPath := strings.TrimSpace(options.ModelPath)
	if parentJobID == "" {
		return "", fmt.Errorf("parent-job is required")
	}
	if upstreamStageID == "" {
		return "", fmt.Errorf("upstream-stage is required")
	}
	if stageJobID == "" {
		return "", fmt.Errorf("stage-job is required")
	}
	if managerURL == "" {
		return "", fmt.Errorf("manager is required")
	}
	if runnerBin == "" {
		return "", fmt.Errorf("runner-bin is required")
	}
	if modelPath == "" {
		return "", fmt.Errorf("model is required")
	}
	if options.StageStart < 0 || options.StageEnd < options.StageStart {
		return "", fmt.Errorf("valid stage-start and stage-end are required")
	}

	timeout := time.Duration(options.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = time.Second
	}
	totalStarted := time.Now()
	activationTransport := transport.NewHTTPActivationTransport(managerURL, options.OperatorToken).
		WithNodeID(nodeID).
		WithPollTimeout(timeout)
	reader, err := activationTransport.OpenReader(context.Background(), transport.StreamID{ParentJobID: parentJobID, StageJobID: upstreamStageID})
	if err != nil {
		return "", err
	}
	receiveStarted := time.Now()
	inputFrame, err := reader.Receive(context.Background())
	if err != nil {
		return "", err
	}
	receiveWaitMS := time.Since(receiveStarted).Milliseconds()
	inputEnvelope := runtimes.TensorEnvelopeFromActivation(inputFrame.Header, inputFrame.Payload, options.StageIndex, upstreamStageID, stageJobID, nodeID, receiveWaitMS)
	inputEnvelope.KVCacheKey = strings.TrimSpace(options.KVCacheKey)
	if err := inputEnvelope.ValidatePayload(inputFrame.Payload); err != nil {
		return "", err
	}
	if _, err := runtimes.BuildLlamaCPPEmbeddingBatchPlan(inputEnvelope); err != nil {
		return "", err
	}

	workDir := strings.TrimSpace(options.WorkDir)
	if workDir == "" {
		workDir, err = os.MkdirTemp("", "cmesh-stage-terminal-decode-*")
		if err != nil {
			return "", err
		}
	} else if err := os.MkdirAll(workDir, 0o755); err != nil {
		return "", err
	}
	inputPath := filepath.Join(workDir, "activation-in.bin")
	reportPath := filepath.Join(workDir, "stage-runner-terminal-decode.json")
	if err := os.WriteFile(inputPath, inputFrame.Payload, 0o600); err != nil {
		return "", err
	}
	recoveryInput := stageRunnerRecoveryInput(options)
	stageDaemonStarted := time.Now()
	stageDaemonDecode, err := postStageDaemonDecodeForStageWithRecovery(context.Background(), recoveryInput, options.StageJobID, "terminal_decode", &inputEnvelope, inputFrame.Payload, "", nil, options.PreviousTokenText)
	if err != nil {
		return "", err
	}
	stageDaemonMS := time.Since(stageDaemonStarted).Milliseconds()
	if tokenID, ok := stageDaemonDecodeInt(stageDaemonDecode, "next_token_id"); ok {
		tokenText, _ := stageDaemonDecodeString(stageDaemonDecode, "next_token_text")
		final, _ := stageDaemonDecodeBool(stageDaemonDecode, "final")
		if options.TerminalForceFinal != nil {
			final = *options.TerminalForceFinal
		}
		outputText := options.PreviousTokenText + tokenText
		if outputText == "" {
			outputText = tokenText
		}
		body, err := json.MarshalIndent(stageRunnerTerminalDecodeResult{
			Kind:              "cdip.stage_terminal_decode",
			RunnerMode:        "llama.cpp-stage-daemon",
			Action:            "terminal-decode",
			ParentJobID:       parentJobID,
			UpstreamStageID:   upstreamStageID,
			StageJobID:        stageJobID,
			StageIndex:        options.StageIndex,
			Step:              stageCommandStep(options.Step),
			KVCacheKey:        strings.TrimSpace(options.KVCacheKey),
			ActivationRelay:   "http",
			InputFrame:        &inputFrame.Header,
			InputEnvelope:     &inputEnvelope,
			InputBytes:        len(inputFrame.Payload),
			NextTokenID:       tokenID,
			NextTokenText:     tokenText,
			Tokens:            []int{tokenID},
			Output:            outputText,
			Final:             final,
			WorkDir:           workDir,
			StageDaemonDecode: stageDaemonDecode,
			Timing: stageRunnerTiming{
				ReceiveWaitMS: receiveWaitMS,
				StageDaemonMS: stageDaemonMS,
				TotalMS:       time.Since(totalStarted).Milliseconds(),
			},
			Executable: true,
		}, "", "  ")
		if err != nil {
			return "", err
		}
		return string(body), nil
	}
	args := []string{
		"--command", "terminal-decode",
		"--model", modelPath,
		"--stage-start", strconv.Itoa(options.StageStart),
		"--stage-end", strconv.Itoa(options.StageEnd),
		"--stage-index", strconv.Itoa(options.StageIndex),
		"--activation-file", inputPath,
		"--dtype", inputEnvelope.DType,
		"--shape", joinInts(inputEnvelope.Shape),
	}
	ctx, cancel := context.WithTimeout(context.Background(), maxDuration(30*time.Second, timeout*10))
	defer cancel()
	cmd := exec.CommandContext(ctx, runnerBin, args...)
	cmd.Env = stageRunnerCommandEnv(os.Environ(), options)
	computeStarted := time.Now()
	reportBytes, err := cmd.CombinedOutput()
	stageComputeMS := time.Since(computeStarted).Milliseconds()
	if writeErr := os.WriteFile(reportPath, reportBytes, 0o600); writeErr != nil && err == nil {
		err = writeErr
	}
	if err != nil {
		return "", fmt.Errorf("stage runner terminal decode failed: %w: %s", err, strings.TrimSpace(string(reportBytes)))
	}
	var report llamaCPPStageTerminalReport
	if err := json.Unmarshal(stageRunnerJSONReport(reportBytes, "cmesh.llamacpp_stage_terminal_decode"), &report); err != nil {
		return "", fmt.Errorf("parse stage runner terminal decode report: %w", err)
	}
	if report.Status != "executed" || report.Logits.Bytes <= 0 || len(report.Logits.Shape) == 0 {
		return "", fmt.Errorf("stage runner terminal decode did not produce logits: %#v", report)
	}
	body, err := json.MarshalIndent(stageRunnerTerminalDecodeResult{
		Kind:              "cdip.stage_terminal_decode",
		RunnerMode:        "llama.cpp-stage",
		Action:            "terminal-decode",
		ParentJobID:       parentJobID,
		UpstreamStageID:   upstreamStageID,
		StageJobID:        stageJobID,
		StageIndex:        options.StageIndex,
		Step:              stageCommandStep(options.Step),
		KVCacheKey:        strings.TrimSpace(options.KVCacheKey),
		ActivationRelay:   "http",
		InputFrame:        &inputFrame.Header,
		InputEnvelope:     &inputEnvelope,
		RunnerReport:      &report,
		InputBytes:        len(inputFrame.Payload),
		NextTokenID:       report.NextTokenID,
		NextTokenText:     report.NextTokenText,
		Tokens:            terminalTokenSequence(report),
		Output:            terminalOutputText(report),
		Final:             terminalReportFinal(report),
		WorkDir:           workDir,
		StageDaemonDecode: stageDaemonDecode,
		Timing: stageRunnerTiming{
			ReceiveWaitMS:  receiveWaitMS,
			StageComputeMS: stageComputeMS,
			StageDaemonMS:  stageDaemonMS,
			TotalMS:        time.Since(totalStarted).Milliseconds(),
		},
		Executable: true,
	}, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func terminalTokenSequence(report llamaCPPStageTerminalReport) []int {
	if len(report.Tokens) > 0 {
		return append([]int(nil), report.Tokens...)
	}
	return []int{report.NextTokenID}
}

func terminalOutputText(report llamaCPPStageTerminalReport) string {
	if strings.TrimSpace(report.Output) != "" {
		return report.Output
	}
	return report.NextTokenText
}

func terminalReportFinal(report llamaCPPStageTerminalReport) bool {
	if report.Final == nil {
		return true
	}
	return *report.Final
}

func stageDaemonDecodeInt(decoded map[string]any, key string) (int, bool) {
	if decoded == nil {
		return 0, false
	}
	switch value := decoded[key].(type) {
	case float64:
		return int(value), true
	case int:
		return value, true
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	default:
		return 0, false
	}
}

func stageDaemonDecodeString(decoded map[string]any, key string) (string, bool) {
	if decoded == nil {
		return "", false
	}
	value, ok := decoded[key].(string)
	return value, ok
}

func stageDaemonDecodeBool(decoded map[string]any, key string) (bool, bool) {
	if decoded == nil {
		return false, false
	}
	value, ok := decoded[key].(bool)
	return value, ok
}

func intSlicesEqual(a []int, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func joinInts(values []int) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ",")
}

func stageRunnerJSONReport(data []byte, kind string) []byte {
	trimmed := bytes.TrimSpace(data)
	if bytes.HasPrefix(trimmed, []byte("{")) {
		return trimmed
	}
	marker := []byte(`"kind": "` + kind + `"`)
	markerAt := bytes.LastIndex(trimmed, marker)
	if markerAt < 0 {
		return trimmed
	}
	start := bytes.LastIndex(trimmed[:markerAt], []byte("{"))
	if start < 0 {
		return trimmed
	}
	return bytes.TrimSpace(trimmed[start:])
}

func maxDuration(a time.Duration, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func stageRunnerRuntime(mode string, llamaCLI string) (runtimes.DistributedStageRuntime, string, error) {
	mode = strings.TrimSpace(strings.ToLower(mode))
	if mode == "" {
		mode = "logical"
	}
	switch mode {
	case "logical":
		return runtimes.NewLogicalStageRuntime(runtimes.LlamaCPPName), mode, nil
	case "llama.cpp-stage", "llamacpp-stage":
		return runtimes.NewLlamaCPPStageRuntime(llamaCLI), mode, nil
	default:
		return nil, "", fmt.Errorf("unsupported stage runner mode %q", mode)
	}
}

func stageRunnerPayload(payload string, payloadFile string) ([]byte, error) {
	if strings.TrimSpace(payloadFile) != "" {
		return os.ReadFile(payloadFile)
	}
	if payload == "" {
		return nil, nil
	}
	return []byte(payload), nil
}

func parseStageRunnerShape(raw string) ([]int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	shape := make([]int, 0, len(parts))
	for _, part := range parts {
		value, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || value <= 0 {
			return nil, fmt.Errorf("invalid activation shape %q", raw)
		}
		shape = append(shape, value)
	}
	return shape, nil
}

func marshalStageRunnerPrepareResult(result stageRunnerPrepareResult) (string, error) {
	body, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

type workerRPCProcess struct {
	cmd      *exec.Cmd
	cacheDir string
	done     chan error
}

func startWorkerRPCBackend(ctx context.Context, options workerOptions) (*workerRPCProcess, error) {
	if !options.rpcEnabled {
		return nil, nil
	}
	if options.rpcPort <= 0 || options.rpcPort > 65535 {
		return nil, fmt.Errorf("rpc port must be between 1 and 65535")
	}
	binary, runtimeStatus, err := runtimes.EnsureLlamaCPP(ctx, options.cacheDir)
	if err != nil {
		return nil, fmt.Errorf("llama.cpp runtime is not ready for RPC backend: %w", err)
	}
	bindEndpoint := net.JoinHostPort(defaultString(options.rpcHost, "0.0.0.0"), strconv.Itoa(options.rpcPort))
	probe := runtimes.NewLlamaCPPRPCRuntime(binary, bindEndpoint).Probe(ctx)
	if !probe.Ready {
		return nil, fmt.Errorf("llama.cpp rpc runtime is not ready: %s", strings.Join(probe.Blockers, "; "))
	}
	advertiseHost := strings.TrimSpace(options.rpcAdvertiseHost)
	if advertiseHost == "" {
		advertiseHost = inferWorkerRPCAdvertiseHost(options.managerURL, options.rpcHost)
	}
	advertiseEndpoint := net.JoinHostPort(advertiseHost, strconv.Itoa(options.rpcPort))
	args := []string{"--host", defaultString(options.rpcHost, "0.0.0.0"), "--port", strconv.Itoa(options.rpcPort)}
	if options.rpcCache {
		args = append(args, "-c")
	}
	cmd := exec.CommandContext(ctx, probe.ServerPath, args...)
	cmd.Env = llamaRuntimeEnv(os.Environ(), probe.ServerPath)
	if options.rpcCache {
		cmd.Env = append(cmd.Env, "LLAMA_CACHE="+filepath.Join(options.cacheDir, "runtimes", "llama.cpp-rpc-cache"))
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Dir = filepath.Dir(probe.ServerPath)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if err := resources.WriteLlamaCPPRPCState(options.cacheDir, resources.LlamaCPPRPCState{
		Running:           true,
		Endpoint:          advertiseEndpoint,
		BindEndpoint:      bindEndpoint,
		AdvertiseEndpoint: advertiseEndpoint,
		PID:               cmd.Process.Pid,
		StartedAt:         now,
	}); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	fmt.Printf("llama.cpp rpc-server started pid=%d endpoint=%s runtime=%s\n", cmd.Process.Pid, advertiseEndpoint, runtimeStatus.Source)
	process := &workerRPCProcess{cmd: cmd, cacheDir: options.cacheDir, done: make(chan error, 1)}
	go process.wait()
	return process, nil
}

func (p *workerRPCProcess) Stop() {
	if p == nil {
		return
	}
	_ = resources.ClearLlamaCPPRPCState(p.cacheDir)
	if p.cmd == nil || p.cmd.Process == nil {
		return
	}
	_ = p.cmd.Process.Signal(os.Interrupt)
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

func (p *workerRPCProcess) wait() {
	err := p.cmd.Wait()
	_ = resources.ClearLlamaCPPRPCState(p.cacheDir)
	p.done <- err
	if err != nil {
		fmt.Fprintf(os.Stderr, "llama.cpp rpc-server exited: %v\n", err)
	}
}

func inferWorkerRPCAdvertiseHost(managerURL string, bindHost string) string {
	bindHost = strings.TrimSpace(bindHost)
	if bindHost != "" && bindHost != "0.0.0.0" && bindHost != "::" {
		return bindHost
	}
	targetHost := "8.8.8.8"
	targetPort := "80"
	if parsed, err := url.Parse(strings.TrimSpace(managerURL)); err == nil && parsed.Hostname() != "" {
		targetHost = parsed.Hostname()
		targetPort = parsed.Port()
		if targetPort == "" {
			targetPort = "80"
			if parsed.Scheme == "https" {
				targetPort = "443"
			}
		}
	}
	conn, err := net.DialTimeout("udp", net.JoinHostPort(targetHost, targetPort), time.Second)
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok && addr.IP != nil {
		return addr.IP.String()
	}
	return "127.0.0.1"
}

func defaultString(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
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
		Resources: applyWorkerRuntimeModelOverrides(discoverWorkerResources(options), options),
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

func sendHeartbeat(managerURL string, nodeID string, nodeAuthToken string, snapshot cluster.ResourceSnapshot) error {
	body, err := json.Marshal(membership.Heartbeat{
		NodeID:    nodeID,
		At:        time.Now().UTC(),
		Resources: snapshot,
	})
	if err != nil {
		return err
	}

	httpReq, err := http.NewRequest(http.MethodPost, managerURL+"/v1/workers/heartbeat", bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	setWorkerAuthHeader(httpReq, nodeAuthToken)
	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return fmt.Errorf("manager returned %s", httpResp.Status)
	}

	return nil
}

func sendLeave(managerURL string, nodeID string, nodeAuthToken string) error {
	body, err := json.Marshal(membership.LeaveRequest{
		NodeID: nodeID,
		At:     time.Now().UTC(),
	})
	if err != nil {
		return err
	}

	httpReq, err := http.NewRequest(http.MethodPost, managerURL+"/v1/workers/leave", bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	setWorkerAuthHeader(httpReq, nodeAuthToken)
	httpResp, err := http.DefaultClient.Do(httpReq)
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

func setWorkerAuthHeader(req *http.Request, token string) {
	if token == "" {
		return
	}
	req.Header.Set("X-CMesh-Worker-Token", token)
}

type workerJobRunner struct {
	managerURL    string
	nodeID        string
	nodeAuthToken string
	cacheDir      string
	slots         int

	mu      sync.Mutex
	active  int
	lastErr error
}

func newWorkerJobRunner(managerURL string, nodeID string, nodeAuthToken string, cacheDir string, slots int) *workerJobRunner {
	if slots <= 0 {
		slots = 1
	}
	return &workerJobRunner{
		managerURL:    managerURL,
		nodeID:        nodeID,
		nodeAuthToken: nodeAuthToken,
		cacheDir:      cacheDir,
		slots:         slots,
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
	_, err := pollAndExecuteJob(r.managerURL, r.nodeID, r.nodeAuthToken, r.cacheDir, snapshot)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active--
	if err != nil && r.lastErr == nil {
		r.lastErr = err
	}
}

func pollAndExecuteJob(managerURL string, nodeID string, nodeAuthToken string, cacheDir string, snapshot cluster.ResourceSnapshot) (bool, error) {
	nextReq, err := http.NewRequest(http.MethodGet, managerURL+"/v1/workers/"+nodeID+"/jobs/next", nil)
	if err != nil {
		return false, err
	}
	setWorkerAuthHeader(nextReq, nodeAuthToken)
	httpResp, err := http.DefaultClient.Do(nextReq)
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

	completeReq, err := http.NewRequest(http.MethodPost, managerURL+"/v1/jobs/"+resp.Job.ID+"/complete", bytes.NewReader(body))
	if err != nil {
		return true, err
	}
	completeReq.Header.Set("Content-Type", "application/json")
	setWorkerAuthHeader(completeReq, nodeAuthToken)
	completeResp, err := http.DefaultClient.Do(completeReq)
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
		if err := sendHeartbeat(managerURL, nodeID, nodeAuthToken, refreshed); err != nil {
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
		return executeModelDistributedRPCGenerateJob(job.Input, cacheDir, nodeID)
	case models.JobGenerateDistributed:
		return "", fmt.Errorf("distributed model generate parent jobs are coordinator-owned; workers execute distributed stage jobs")
	case models.JobGenerateStage:
		return executeDistributedStageJob(job, snapshot, cacheDir, nodeID, managerURL)
	default:
		return "", fmt.Errorf("unsupported job type %q", job.Type)
	}
}

type distributedStageResult struct {
	Kind               string                      `json:"kind"`
	ParentJobID        string                      `json:"parent_job_id"`
	StageIndex         int                         `json:"stage_index"`
	ModelID            string                      `json:"model_id"`
	Runtime            string                      `json:"runtime"`
	LayerStart         int                         `json:"layer_start"`
	LayerEnd           int                         `json:"layer_end"`
	UpstreamNodeID     string                      `json:"upstream_node_id,omitempty"`
	DownstreamNodeID   string                      `json:"downstream_node_id,omitempty"`
	Materialization    string                      `json:"materialization"`
	SourceArtifact     string                      `json:"source_artifact,omitempty"`
	TargetArtifact     string                      `json:"target_artifact,omitempty"`
	Artifact           cdip.ShardArtifact          `json:"artifact,omitempty"`
	PhysicalShardPlan  *runtimes.PhysicalShardPlan `json:"physical_shard_plan,omitempty"`
	ActivationProtocol string                      `json:"activation_protocol"`
	StageSession       *runtimes.StageSession      `json:"stage_session,omitempty"`
}

type llamaCPPShardBundleReport struct {
	Kind                string `json:"kind"`
	Status              string `json:"status"`
	Protocol            string `json:"protocol"`
	OutputFile          string `json:"output_file"`
	StageIndex          int    `json:"stage_index"`
	StageStart          int    `json:"stage_start"`
	StageEnd            int    `json:"stage_end"`
	SelectedTensorCount int64  `json:"selected_tensor_count"`
	SelectedBytes       uint64 `json:"selected_bytes"`
	BundleBytes         uint64 `json:"bundle_bytes"`
	LoadableGGUF        bool   `json:"loadable_gguf"`
}

type llamaCPPStageGGUFLoadProbeReport struct {
	Kind                 string `json:"kind"`
	Status               string `json:"status"`
	Runtime              string `json:"runtime"`
	ModelPath            string `json:"model_path"`
	Loaded               bool   `json:"loaded"`
	CMeshStageMetadata   bool   `json:"cmesh_stage_metadata"`
	StageStart           int    `json:"stage_start"`
	StageEnd             int    `json:"stage_end"`
	SelectedTensorCount  int64  `json:"selected_tensor_count"`
	AllowlistTensorCount int64  `json:"allowlist_tensor_count"`
	NLayer               int    `json:"n_layer"`
	ModelSize            uint64 `json:"model_size"`
	LoadableFullModel    bool   `json:"loadable_full_model"`
	Guardrail            string `json:"guardrail"`
}

func executeDistributedStageJob(job jobs.Job, snapshot cluster.ResourceSnapshot, cacheDir string, nodeID string, managerURL string) (string, error) {
	var req models.DistributedStageJobInput
	if err := json.Unmarshal([]byte(job.Input), &req); err != nil {
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
	command := strings.ToLower(strings.TrimSpace(req.StageCommand))
	if command == "" {
		command = "prepare"
	}
	switch command {
	case "prepare":
		return executeDistributedStagePrepareJob(job, req, cacheDir)
	case "source_decode", "source-decode":
		return executeDistributedStageSourceDecodeJob(job, req, snapshot, cacheDir, nodeID, managerURL)
	case "relay_decode", "relay-decode":
		return executeDistributedStageRelayDecodeJob(job, req, snapshot, cacheDir, nodeID, managerURL)
	case "terminal_decode", "terminal-decode":
		return executeDistributedStageTerminalDecodeJob(job, req, snapshot, cacheDir, nodeID, managerURL)
	default:
		return "", fmt.Errorf("unsupported distributed stage command %q", req.StageCommand)
	}
}

func executeDistributedStagePrepareJob(job jobs.Job, req models.DistributedStageJobInput, cacheDir string) (string, error) {
	stageJobID := strings.TrimSpace(req.StageJobID)
	if stageJobID == "" {
		stageJobID = strings.TrimSpace(job.ID)
	}
	if stageJobID == "" {
		stageJobID = "worker-local-stage-prepare"
	}
	stageSession, err := ensureStageDaemonSession(context.Background(), req, stageJobID)
	if err != nil {
		return "", err
	}
	runnerBin := firstNonEmptyString(req.StageRunnerBin, os.Getenv("CMESH_STAGE_RUNNER_BIN"), defaultStageRunnerBinaryPath())
	modelPath := strings.TrimSpace(req.ModelPath)
	if runnerBin != "" && modelPath != "" {
		if _, err := os.Stat(runnerBin); err != nil {
			if strings.TrimSpace(req.StageRunnerBin) != "" || strings.TrimSpace(os.Getenv("CMESH_STAGE_RUNNER_BIN")) != "" {
				return "", fmt.Errorf("stage runner binary is not accessible for prepare: %w", err)
			}
		} else {
			prepared, err := runtimes.NewLlamaCPPStageRuntime(runnerBin).PrepareStage(context.Background(), runtimes.StagePrepareRequest{
				ParentJobID:      req.ParentJobID,
				StageJobID:       stageJobID,
				ModelID:          req.ModelID,
				ModelPath:        modelPath,
				Stage:            req.Stage,
				Shard:            req.Shard,
				UpstreamNodeID:   req.UpstreamNodeID,
				DownstreamNodeID: req.DownstreamNodeID,
			})
			if err != nil {
				prepared, ok, probeErr := prepareAlreadyMaterializedStageGGUFShard(context.Background(), runnerBin, req, stageJobID)
				if probeErr != nil {
					return "", fmt.Errorf("%w; physical stage GGUF probe also failed: %v", err, probeErr)
				}
				if !ok {
					return "", err
				}
				return marshalDistributedStagePrepareResult(prepared, stageSession)
			}
			prepared, err = writeStagePrepareMaterializationArtifact(prepared, distributedStagePrepareArtifactDir(req.WorkDir, cacheDir, stageJobID, req.Stage.Index), runnerBin)
			if err != nil {
				return "", err
			}
			return marshalDistributedStagePrepareResult(prepared, stageSession)
		}
	}
	stageRuntime := runtimes.NewLogicalStageRuntime(req.Shard.Runtime)
	prepared, err := stageRuntime.PrepareStage(context.Background(), runtimes.StagePrepareRequest{
		ParentJobID:      req.ParentJobID,
		StageJobID:       stageJobID,
		ModelID:          req.ModelID,
		ModelPath:        modelPath,
		Stage:            req.Stage,
		Shard:            req.Shard,
		UpstreamNodeID:   req.UpstreamNodeID,
		DownstreamNodeID: req.DownstreamNodeID,
	})
	if err != nil {
		return "", err
	}
	prepared, err = writeStagePrepareMaterializationArtifact(prepared, distributedStagePrepareArtifactDir(req.WorkDir, cacheDir, stageJobID, req.Stage.Index), "")
	if err != nil {
		return "", err
	}
	return marshalDistributedStagePrepareResult(prepared, stageSession)
}

func prepareAlreadyMaterializedStageGGUFShard(ctx context.Context, runnerBin string, req models.DistributedStageJobInput, stageJobID string) (runtimes.StagePrepareResult, bool, error) {
	modelPath := strings.TrimSpace(req.ModelPath)
	if strings.TrimSpace(runnerBin) == "" || modelPath == "" {
		return runtimes.StagePrepareResult{}, false, nil
	}
	output, err := exec.CommandContext(ctx, runnerBin,
		"--command", "probe-stage-gguf-load",
		"--model", modelPath,
	).CombinedOutput()
	if err != nil {
		return runtimes.StagePrepareResult{}, false, fmt.Errorf("stage GGUF load probe failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	var report llamaCPPStageGGUFLoadProbeReport
	if err := json.Unmarshal(stageRunnerJSONReport(output, "cmesh.llamacpp_stage_gguf_load_probe"), &report); err != nil {
		return runtimes.StagePrepareResult{}, false, fmt.Errorf("parse stage GGUF load probe: %w", err)
	}
	if strings.TrimSpace(report.Kind) != "cmesh.llamacpp_stage_gguf_load_probe" {
		return runtimes.StagePrepareResult{}, false, nil
	}
	if strings.TrimSpace(report.Status) != "stage_model_loaded_partial" || !report.Loaded || !report.CMeshStageMetadata {
		return runtimes.StagePrepareResult{}, false, fmt.Errorf("stage GGUF is not partial-load ready: %#v", report)
	}
	if report.StageStart != req.Stage.LayerStart || report.StageEnd != req.Stage.LayerEnd {
		return runtimes.StagePrepareResult{}, false, fmt.Errorf("stage GGUF layer range mismatch: shard=%d-%d request=%d-%d", report.StageStart, report.StageEnd, req.Stage.LayerStart, req.Stage.LayerEnd)
	}
	artifact := req.Shard.Artifact
	if strings.TrimSpace(artifact.Protocol) == "" {
		artifact.Protocol = "cdip.shard-artifact-v1"
	}
	artifact.Status = "physical_stage_gguf_ready"
	artifact.LayerStart = report.StageStart
	artifact.LayerEnd = report.StageEnd
	artifact.ExpectedBytes = report.ModelSize
	artifact.URI = (&url.URL{Scheme: "file", Path: modelPath}).String()
	artifact.PhysicalArtifactReady = true
	physicalPlan := runtimes.PhysicalShardPlan{
		Protocol:              runtimes.PhysicalShardPlanV1,
		Runtime:               runtimes.LlamaCPPName,
		ModelID:               req.ModelID,
		ModelPath:             modelPath,
		StageIndex:            req.Stage.Index,
		LayerStart:            req.Stage.LayerStart,
		LayerEnd:              req.Stage.LayerEnd,
		TargetURI:             artifact.URI,
		SelectedTensorCount:   report.SelectedTensorCount,
		SelectedBytes:         report.ModelSize,
		ArtifactKind:          "stage_gguf_shard",
		ArtifactBytes:         report.ModelSize,
		LoadableGGUF:          true,
		PhysicalArtifactReady: true,
		Status:                "physical_stage_gguf_ready",
	}
	return runtimes.StagePrepareResult{
		Kind:               "cdip.stage_ready",
		ParentJobID:        req.ParentJobID,
		StageIndex:         req.Stage.Index,
		ModelID:            req.ModelID,
		Runtime:            runtimes.LlamaCPPName,
		LayerStart:         req.Stage.LayerStart,
		LayerEnd:           req.Stage.LayerEnd,
		UpstreamNodeID:     req.UpstreamNodeID,
		DownstreamNodeID:   req.DownstreamNodeID,
		Materialization:    string(req.Shard.Materialization),
		SourceArtifact:     req.Shard.SourceArtifact,
		TargetArtifact:     artifact.URI,
		Artifact:           artifact,
		PhysicalShardPlan:  &physicalPlan,
		ActivationProtocol: runtimes.ActivationStreamV1,
	}, true, nil
}

func distributedStagePrepareArtifactDir(workDir string, cacheDir string, stageJobID string, stageIndex int) string {
	workDir = strings.TrimSpace(workDir)
	if workDir != "" {
		return workDir
	}
	cacheDir = strings.TrimSpace(cacheDir)
	if cacheDir == "" {
		return ""
	}
	stageJobID = strings.TrimSpace(stageJobID)
	if stageJobID == "" {
		stageJobID = fmt.Sprintf("stage-%d", stageIndex)
	}
	return filepath.Join(cacheDir, "stage-runs", stageJobID)
}

func writeStagePrepareMaterializationArtifact(prepared runtimes.StagePrepareResult, workDir string, runnerBin string) (runtimes.StagePrepareResult, error) {
	if prepared.MaterializationPlan == nil {
		return prepared, nil
	}
	if strings.TrimSpace(workDir) == "" {
		return prepared, nil
	}
	if err := prepared.MaterializationPlan.Validate(); err != nil {
		return runtimes.StagePrepareResult{}, err
	}
	body, err := json.Marshal(prepared.MaterializationPlan)
	if err != nil {
		return runtimes.StagePrepareResult{}, err
	}
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		return runtimes.StagePrepareResult{}, err
	}
	if err := os.MkdirAll(absWorkDir, 0o755); err != nil {
		return runtimes.StagePrepareResult{}, err
	}
	path := filepath.Join(absWorkDir, fmt.Sprintf("stage-%d-materialization-plan.json", prepared.StageIndex))
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return runtimes.StagePrepareResult{}, err
	}
	sum := sha256.Sum256(body)
	artifact := prepared.Artifact
	if strings.TrimSpace(artifact.Protocol) == "" {
		artifact.Protocol = "cdip.shard-artifact-v1"
	}
	artifact.Status = "selected_tensor_manifest_ready"
	artifact.LayerStart = prepared.MaterializationPlan.LayerStart
	artifact.LayerEnd = prepared.MaterializationPlan.LayerEnd
	artifact.ExpectedBytes = prepared.MaterializationPlan.SelectedBytes
	artifact.Checksum = "sha256:" + hex.EncodeToString(sum[:])
	artifact.URI = (&url.URL{Scheme: "file", Path: path}).String()
	artifact.PhysicalArtifactReady = false
	prepared.Artifact = artifact
	physicalPlan, err := runtimes.BuildPhysicalShardPlan(*prepared.MaterializationPlan, artifact.URI, artifact.Checksum, prepared.TargetArtifact)
	if err != nil {
		return runtimes.StagePrepareResult{}, err
	}
	physicalPath := filepath.Join(absWorkDir, fmt.Sprintf("stage-%d-physical-shard-plan.json", prepared.StageIndex))
	physicalPlan.PlanURI = (&url.URL{Scheme: "file", Path: physicalPath}).String()
	physicalBody, err := json.Marshal(physicalPlan)
	if err != nil {
		return runtimes.StagePrepareResult{}, err
	}
	if err := os.WriteFile(physicalPath, physicalBody, 0o644); err != nil {
		return runtimes.StagePrepareResult{}, err
	}
	runnerBin = strings.TrimSpace(runnerBin)
	if runnerBin != "" && strings.TrimSpace(prepared.MaterializationPlan.ModelPath) != "" {
		bundlePath := filepath.Join(absWorkDir, fmt.Sprintf("stage-%d.cmesh-shard", prepared.StageIndex))
		args := []string{
			"--command", "write-shard-bundle",
			"--model", prepared.MaterializationPlan.ModelPath,
			"--stage-start", strconv.Itoa(prepared.MaterializationPlan.LayerStart),
			"--stage-end", strconv.Itoa(prepared.MaterializationPlan.LayerEnd),
			"--stage-index", strconv.Itoa(prepared.StageIndex),
			"--output-file", bundlePath,
		}
		if prepared.MaterializationPlan.LayerStart == 0 {
			args = append(args, "--first-stage")
		}
		if prepared.MaterializationPlan.TotalLayers > 0 && prepared.MaterializationPlan.LayerEnd == prepared.MaterializationPlan.TotalLayers-1 {
			args = append(args, "--terminal-stage")
		}
		output, err := exec.Command(runnerBin, args...).CombinedOutput()
		if err != nil {
			return runtimes.StagePrepareResult{}, fmt.Errorf("stage shard bundle write failed: %w: %s", err, strings.TrimSpace(string(output)))
		}
		var bundle llamaCPPShardBundleReport
		if err := json.Unmarshal(llamaCPPStagePrepareJSON(output, "cmesh.llamacpp_stage_shard_bundle"), &bundle); err != nil {
			return runtimes.StagePrepareResult{}, fmt.Errorf("parse stage shard bundle report: %w", err)
		}
		if bundle.Status != "bundle_ready_not_loadable_gguf" || bundle.OutputFile == "" || bundle.SelectedBytes != prepared.MaterializationPlan.SelectedBytes {
			return runtimes.StagePrepareResult{}, fmt.Errorf("unexpected stage shard bundle report: %s", strings.TrimSpace(string(output)))
		}
		bundleBody, err := os.ReadFile(bundlePath)
		if err != nil {
			return runtimes.StagePrepareResult{}, err
		}
		bundleSum := sha256.Sum256(bundleBody)
		physicalPlan.Status = "physical_tensor_bundle_ready_not_loadable_gguf"
		physicalPlan.TargetURI = (&url.URL{Scheme: "file", Path: bundlePath}).String()
		physicalPlan.TargetChecksum = "sha256:" + hex.EncodeToString(bundleSum[:])
		physicalPlan.ArtifactBytes = uint64(len(bundleBody))
		physicalPlan.ArtifactKind = "cmesh_shard_bundle"
		physicalPlan.LoadableGGUF = false
		physicalPlan.PhysicalArtifactReady = true
		physicalPlan.Blockers = []string{
			"convert CMesh shard bundle into standalone GGUF shard metadata and tensor layout",
			"teach llama.cpp stage runtime to open the shard without original full model",
			"validate loadable GGUF shard checksum before stage session prepare",
		}
		physicalBody, err = json.Marshal(physicalPlan)
		if err != nil {
			return runtimes.StagePrepareResult{}, err
		}
		if err := os.WriteFile(physicalPath, physicalBody, 0o644); err != nil {
			return runtimes.StagePrepareResult{}, err
		}
	}
	prepared.PhysicalShardPlan = &physicalPlan
	return prepared, nil
}

func llamaCPPStagePrepareJSON(data []byte, kind string) []byte {
	trimmed := bytes.TrimSpace(data)
	if bytes.HasPrefix(trimmed, []byte("{")) {
		return trimmed
	}
	marker := []byte(`"kind": "` + kind + `"`)
	markerAt := bytes.LastIndex(trimmed, marker)
	if markerAt < 0 {
		return trimmed
	}
	start := bytes.LastIndex(trimmed[:markerAt], []byte("{"))
	if start < 0 {
		return trimmed
	}
	return bytes.TrimSpace(trimmed[start:])
}

func marshalDistributedStagePrepareResult(prepared runtimes.StagePrepareResult, stageSession *runtimes.StageSession) (string, error) {
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
		Artifact:           prepared.Artifact,
		PhysicalShardPlan:  prepared.PhysicalShardPlan,
		ActivationProtocol: prepared.ActivationProtocol,
		StageSession:       stageSession,
	})
	if err != nil {
		return "", err
	}
	return string(result), nil
}

func executeDistributedStageSourceDecodeJob(job jobs.Job, req models.DistributedStageJobInput, snapshot cluster.ResourceSnapshot, cacheDir string, nodeID string, managerURL string) (string, error) {
	stageJobID := strings.TrimSpace(req.StageJobID)
	if stageJobID == "" {
		stageJobID = strings.TrimSpace(job.ID)
	}
	if stageJobID == "" {
		return "", fmt.Errorf("stage job id is required for source decode")
	}
	downstreamStageID := strings.TrimSpace(req.DownstreamStageID)
	if downstreamStageID == "" {
		return "", fmt.Errorf("downstream_stage_id is required for source decode")
	}
	if strings.TrimSpace(req.DownstreamNodeID) == "" {
		return "", fmt.Errorf("downstream_node_id is required for source decode")
	}
	managerURL = strings.TrimSpace(managerURL)
	if managerURL == "" {
		return "", fmt.Errorf("manager URL is required for source decode")
	}
	nodeID = firstNonEmptyString(nodeID, req.Stage.NodeID)
	envRunnerBin := strings.TrimSpace(os.Getenv("CMESH_STAGE_RUNNER_BIN"))
	runnerBin := firstNonEmptyString(req.StageRunnerBin, envRunnerBin, defaultStageRunnerBinaryPath())
	modelPath := firstNonEmptyString(req.ModelPath, workerModelPath(snapshot, req.ModelID))
	if runnerBin != "" && modelPath != "" {
		if _, err := os.Stat(runnerBin); err != nil {
			if strings.TrimSpace(req.StageRunnerBin) != "" || envRunnerBin != "" {
				return "", fmt.Errorf("stage runner binary is not accessible for source decode: %w", err)
			}
		} else {
			workDir := strings.TrimSpace(req.WorkDir)
			if workDir == "" && strings.TrimSpace(cacheDir) != "" {
				workDir = filepath.Join(cacheDir, "stage-runs", job.ID)
			}
			return executeStageRunnerSourceDecode(stageRunnerRelayDecodeOptions{
				ParentJobID:       req.ParentJobID,
				StageJobID:        stageJobID,
				StageIndex:        req.Stage.Index,
				Step:              req.Step,
				KVCacheKey:        req.KVCacheKey,
				DownstreamStageID: downstreamStageID,
				DownstreamNodeID:  req.DownstreamNodeID,
				ManagerURL:        managerURL,
				NodeID:            nodeID,
				TimeoutMS:         req.TimeoutMS,
				RunnerBin:         runnerBin,
				ModelID:           req.ModelID,
				ModelPath:         modelPath,
				StageStart:        req.Stage.LayerStart,
				StageEnd:          req.Stage.LayerEnd,
				WorkDir:           workDir,
				StageDaemonURL:    req.StageDaemonURL,
				StageSessionID:    stageJobSessionID(req, stageJobID),
				PreviousTokenID:   req.PreviousTokenID,
				PreviousTokenText: req.PreviousTokenText,
			}, req.Prompt)
		}
	}
	if strings.TrimSpace(req.StageRunnerBin) != "" || envRunnerBin != "" {
		return "", fmt.Errorf("model path is required for source decode")
	}
	payload, checksum := deterministicSourceActivation(req, stageJobID)
	stageRuntime := runtimes.NewLogicalStageRuntime(req.Shard.Runtime)
	result, err := stageRuntime.DecodeStage(context.Background(), runtimes.StageCommandRequest{
		ParentJobID:          req.ParentJobID,
		StageJobID:           stageJobID,
		StageIndex:           req.Stage.Index,
		Step:                 stageCommandStep(req.Step),
		ActivationTransport:  transport.NewHTTPActivationTransport(managerURL, "").WithNodeID(nodeID).WithPollTimeout(time.Second),
		UpstreamStageJobID:   stageJobID,
		DownstreamStageJobID: downstreamStageID,
		DownstreamNodeID:     req.DownstreamNodeID,
		ActivationPayload:    payload,
		ActivationEncoding:   "raw",
		ActivationShape:      []int{1, 1, 1},
		ActivationDType:      "f32",
		ActivationChecksum:   checksum,
		KVCacheKey:           req.KVCacheKey,
	})
	if err != nil {
		return "", err
	}
	stageDaemonDecode, err := postStageDaemonDecodeForStageWithRecovery(context.Background(), req, stageJobID, "", result.TensorEnvelope, nil, "", nil, "")
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(stageRunnerCommandResult{
		Kind:              "cdip.stage_source_decode",
		RunnerMode:        "logical-source",
		Action:            "source-decode",
		ParentJobID:       req.ParentJobID,
		StageJobID:        stageJobID,
		StageIndex:        req.Stage.Index,
		Step:              stageCommandStep(req.Step),
		ActivationFrame:   result.ActivationFrame,
		ActivationBytes:   result.ActivationBytes,
		ActivationRelay:   "http",
		TensorEnvelope:    result.TensorEnvelope,
		StageDaemonDecode: stageDaemonDecode,
		UpstreamStageID:   stageJobID,
		DownstreamStageID: downstreamStageID,
		DownstreamNodeID:  req.DownstreamNodeID,
		Executable:        true,
	})
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func deterministicSourceActivation(req models.DistributedStageJobInput, stageJobID string) ([]byte, string) {
	seed := strings.Join([]string{
		req.ParentJobID,
		stageJobID,
		req.ModelID,
		req.ConversationID,
		req.Prompt,
		strconv.Itoa(optionalIntValue(req.PreviousTokenID, -1)),
		req.PreviousTokenText,
		strconv.Itoa(req.Stage.Index),
		strconv.FormatUint(stageCommandStep(req.Step), 10),
		req.KVCacheKey,
	}, "\x00")
	sum := sha256.Sum256([]byte(seed))
	payload := append([]byte(nil), sum[:4]...)
	payloadSum := sha256.Sum256(payload)
	return payload, "sha256:" + hex.EncodeToString(payloadSum[:])
}

func optionalIntValue(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

func stageCommandStep(step uint64) uint64 {
	if step == 0 {
		return 1
	}
	return step
}

func stageRunnerCommandEnv(base []string, options stageRunnerRelayDecodeOptions) []string {
	env := append([]string(nil), base...)
	env = append(env, "CMESH_STAGE_STEP="+strconv.FormatUint(stageCommandStep(options.Step), 10))
	if kv := strings.TrimSpace(options.KVCacheKey); kv != "" {
		env = append(env, "CMESH_KV_CACHE_KEY="+kv)
		if sessionFile := stageRunnerSessionFile(options); sessionFile != "" {
			env = append(env, "CMESH_STAGE_SESSION_FILE="+sessionFile)
		}
	}
	if options.TerminalForceFinal != nil {
		env = append(env, "CMESH_TERMINAL_FORCE_FINAL="+strconv.FormatBool(*options.TerminalForceFinal))
	}
	return env
}

func stageRunnerSessionFile(options stageRunnerRelayDecodeOptions) string {
	if strings.TrimSpace(options.WorkDir) == "" || strings.TrimSpace(options.KVCacheKey) == "" {
		return ""
	}
	sessionDir := filepath.Join(options.WorkDir, "sessions")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(options.KVCacheKey)))
	return filepath.Join(sessionDir, fmt.Sprintf("stage-%d-%s.seq", options.StageIndex, hex.EncodeToString(sum[:8])))
}

func stageJobSessionID(req models.DistributedStageJobInput, stageJobID string) string {
	if id := strings.TrimSpace(req.StageSessionID); id != "" {
		return id
	}
	seed := strings.Join([]string{
		strings.TrimSpace(req.ParentJobID),
		strings.TrimSpace(stageJobID),
		strings.TrimSpace(req.ModelID),
		strconv.Itoa(req.Stage.Index),
		strings.TrimSpace(req.KVCacheKey),
	}, "\x00")
	sum := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("stage-%d-%s", req.Stage.Index, hex.EncodeToString(sum[:8]))
}

func ensureStageDaemonSession(ctx context.Context, req models.DistributedStageJobInput, stageJobID string) (*runtimes.StageSession, error) {
	daemonURL := strings.TrimSpace(req.StageDaemonURL)
	if daemonURL == "" {
		return nil, nil
	}
	timeout := stageDaemonHTTPTimeout(req.TimeoutMS)
	endpoint, err := url.JoinPath(daemonURL, "/v1/sessions")
	if err != nil {
		return nil, fmt.Errorf("invalid stage daemon url: %w", err)
	}
	body, err := json.Marshal(stageDaemonSessionRequest{
		SessionID:   stageJobSessionID(req, stageJobID),
		ParentJobID: req.ParentJobID,
		StageJobID:  stageJobID,
		ModelID:     req.ModelID,
		ModelPath:   req.ModelPath,
		StageIndex:  req.Stage.Index,
		LayerStart:  req.Stage.LayerStart,
		LayerEnd:    req.Stage.LayerEnd,
		KVCacheKey:  req.KVCacheKey,
	})
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: timeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("stage daemon session create failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("stage daemon session create returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var session runtimes.StageSession
	if err := json.Unmarshal(respBody, &session); err != nil {
		return nil, fmt.Errorf("decode stage daemon session: %w", err)
	}
	if err := session.Validate(); err != nil {
		return nil, fmt.Errorf("invalid stage daemon session: %w", err)
	}
	return &session, nil
}

func postStageDaemonDecodeForStage(ctx context.Context, req models.DistributedStageJobInput, stageJobID string, envelope *runtimes.TensorEnvelope) (map[string]any, error) {
	return postStageDaemonDecodeWithPayload(ctx, req.StageDaemonURL, stageJobSessionID(req, stageJobID), stageCommandStep(req.Step), "", envelope, nil, "", nil, "", req.TimeoutMS)
}

func postStageDaemonDecodeForStageWithRecovery(ctx context.Context, req models.DistributedStageJobInput, stageJobID string, command string, envelope *runtimes.TensorEnvelope, payload []byte, prompt string, previousTokenID *int, previousTokenText string) (map[string]any, error) {
	sessionID := stageJobSessionID(req, stageJobID)
	decoded, err := postStageDaemonDecodeWithPayload(ctx, req.StageDaemonURL, sessionID, stageCommandStep(req.Step), command, envelope, payload, prompt, previousTokenID, previousTokenText, req.TimeoutMS)
	if err == nil || !errors.Is(err, errStageDaemonSessionMissing) {
		return decoded, err
	}
	if _, prepareErr := ensureStageDaemonSession(ctx, req, stageJobID); prepareErr != nil {
		return nil, fmt.Errorf("stage daemon session missing and recreate failed: %w", prepareErr)
	}
	return postStageDaemonDecodeWithPayload(ctx, req.StageDaemonURL, sessionID, stageCommandStep(req.Step), command, envelope, payload, prompt, previousTokenID, previousTokenText, req.TimeoutMS)
}

func postStageDaemonDecode(ctx context.Context, daemonURL string, sessionID string, step uint64, envelope *runtimes.TensorEnvelope) (map[string]any, error) {
	return postStageDaemonDecodeWithPayload(ctx, daemonURL, sessionID, step, "", envelope, nil, "", nil, "", 0)
}

func postStageDaemonDecodeWithPayload(ctx context.Context, daemonURL string, sessionID string, step uint64, command string, envelope *runtimes.TensorEnvelope, payload []byte, prompt string, previousTokenID *int, previousTokenText string, timeoutMS int) (map[string]any, error) {
	daemonURL = strings.TrimSpace(daemonURL)
	if daemonURL == "" {
		return nil, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("stage session id is required for daemon decode")
	}
	endpoint, err := url.JoinPath(daemonURL, "/v1/sessions", sessionID, "decode")
	if err != nil {
		return nil, fmt.Errorf("invalid stage daemon url: %w", err)
	}
	payloadBase64 := ""
	if len(payload) > 0 {
		payloadBase64 = base64.StdEncoding.EncodeToString(payload)
	}
	body, err := json.Marshal(stageDaemonDecodeRequest{
		Step:                    stageCommandStep(step),
		StageCommand:            strings.TrimSpace(command),
		TensorEnvelope:          envelope,
		ActivationPayloadBase64: payloadBase64,
		Prompt:                  strings.TrimSpace(prompt),
		PreviousTokenID:         previousTokenID,
		PreviousTokenText:       strings.TrimSpace(previousTokenText),
	})
	if err != nil {
		return nil, err
	}
	timeout := stageDaemonHTTPTimeout(timeoutMS)
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: timeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("stage daemon decode failed: %w", err)
	}
	defer resp.Body.Close()
	responseLimit := stageDaemonDecodeResponseLimitBytes()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, responseLimit+1))
	if int64(len(respBody)) > responseLimit {
		return nil, fmt.Errorf("stage daemon decode response exceeded %d byte limit for session=%s command=%s step=%d", responseLimit, sessionID, strings.TrimSpace(command), stageCommandStep(step))
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: %s", errStageDaemonSessionMissing, strings.TrimSpace(string(respBody)))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("stage daemon decode returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if len(bytes.TrimSpace(respBody)) == 0 {
		return nil, fmt.Errorf("stage daemon decode returned %d with empty response body for session=%s command=%s step=%d", resp.StatusCode, sessionID, strings.TrimSpace(command), stageCommandStep(step))
	}
	var decoded map[string]any
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return nil, fmt.Errorf("decode stage daemon response status=%d session=%s command=%s step=%d: %w: %s", resp.StatusCode, sessionID, strings.TrimSpace(command), stageCommandStep(step), err, strings.TrimSpace(string(respBody)))
	}
	return decoded, nil
}

func stageDaemonHTTPTimeout(timeoutMS int) time.Duration {
	if timeoutMS > 0 {
		timeout := time.Duration(timeoutMS) * time.Millisecond
		if timeout > 10*time.Minute {
			return 10 * time.Minute
		}
		return timeout
	}
	return 2 * time.Minute
}

func stageDaemonDecodeResponseLimitBytes() int64 {
	const defaultLimit = int64(256 << 20)
	raw := strings.TrimSpace(os.Getenv("CMESH_STAGE_DAEMON_DECODE_RESPONSE_LIMIT_BYTES"))
	if raw == "" {
		return defaultLimit
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed <= 0 {
		return defaultLimit
	}
	const minLimit = int64(1 << 20)
	if parsed < minLimit {
		return minLimit
	}
	return parsed
}

var errStageDaemonSessionMissing = errors.New("stage daemon session is missing")

func defaultStageDaemonSessionDir() string {
	if base, err := os.UserCacheDir(); err == nil && strings.TrimSpace(base) != "" {
		return filepath.Join(base, "cmesh", "stage-sessions")
	}
	return filepath.Join(os.TempDir(), "cmesh-stage-sessions")
}

type stageDaemonSessionRequest struct {
	SessionID   string `json:"session_id,omitempty"`
	ParentJobID string `json:"parent_job_id,omitempty"`
	StageJobID  string `json:"stage_job_id,omitempty"`
	ModelID     string `json:"model_id"`
	ModelPath   string `json:"model_path,omitempty"`
	StageIndex  int    `json:"stage_index"`
	LayerStart  int    `json:"layer_start"`
	LayerEnd    int    `json:"layer_end"`
	KVCacheKey  string `json:"kv_cache_key,omitempty"`
}

type stageDaemonDecodeRequest struct {
	Step                    uint64                   `json:"step,omitempty"`
	StageCommand            string                   `json:"stage_command,omitempty"`
	TensorEnvelope          *runtimes.TensorEnvelope `json:"tensor_envelope,omitempty"`
	ActivationPayloadBase64 string                   `json:"activation_payload_base64,omitempty"`
	Prompt                  string                   `json:"prompt,omitempty"`
	PreviousTokenID         *int                     `json:"previous_token_id,omitempty"`
	PreviousTokenText       string                   `json:"previous_token_text,omitempty"`
}

type stageDaemonSessionRecord struct {
	Session          runtimes.StageSession `json:"session"`
	DecodeSteps      uint64                `json:"decode_steps"`
	LastStep         uint64                `json:"last_step,omitempty"`
	LastStageCommand string                `json:"last_stage_command,omitempty"`
	LastPayloadBytes int                   `json:"last_payload_bytes,omitempty"`
	BackendKind      string                `json:"backend_kind"`
	NativeKV         bool                  `json:"native_kv"`
	LastSequence     uint64                `json:"last_sequence,omitempty"`
	LastChecksum     string                `json:"last_checksum,omitempty"`
	CreatedAt        time.Time             `json:"created_at"`
	UpdatedAt        time.Time             `json:"updated_at"`
}

type stageDaemonState struct {
	sessionDir string
	backend    stageSessionBackend
	mu         sync.Mutex
	sessions   map[string]*stageDaemonSessionRecord
}

func newStageRunnerDaemonHandler(sessionDir string) http.Handler {
	return newStageRunnerDaemonHandlerWithBackend(sessionDir, mockStageSessionBackend{})
}

type stageSessionBackend interface {
	Kind() string
	NativeKV() bool
	Status() stageDaemonBackendStatus
	Prepare(context.Context, runtimes.StageSession, stageDaemonSessionRequest) (runtimes.StageSession, error)
	Decode(context.Context, runtimes.StageSession, stageDaemonDecodeRequest) (stageDaemonBackendDecodeResult, error)
	Close(context.Context, runtimes.StageSession) error
}

type stageDaemonBackendStatus struct {
	Kind             string   `json:"kind"`
	NativeKV         bool     `json:"native_kv"`
	Ready            bool     `json:"ready"`
	RunnerBin        string   `json:"runner_bin,omitempty"`
	RunnerReady      bool     `json:"runner_ready,omitempty"`
	ResidentProtocol string   `json:"resident_protocol,omitempty"`
	DecodeReady      bool     `json:"decode_ready,omitempty"`
	MissingHooks     []string `json:"missing_hooks,omitempty"`
	Blocker          string   `json:"blocker,omitempty"`
}

type stageDaemonBackendDecodeResult struct {
	Sequence            uint64 `json:"sequence,omitempty"`
	Checksum            string `json:"checksum,omitempty"`
	OutputPayloadBase64 string `json:"output_payload_base64,omitempty"`
	OutputBytes         int    `json:"output_bytes,omitempty"`
	OutputChecksum      string `json:"output_checksum,omitempty"`
	OutputDType         string `json:"output_dtype,omitempty"`
	OutputShape         []int  `json:"output_shape,omitempty"`
	NextTokenID         *int   `json:"next_token_id,omitempty"`
	NextTokenText       string `json:"next_token_text,omitempty"`
	Final               *bool  `json:"final,omitempty"`
}

type mockStageSessionBackend struct{}

func (mockStageSessionBackend) Kind() string { return "mock-resident" }

func (mockStageSessionBackend) NativeKV() bool { return false }

func (mockStageSessionBackend) Status() stageDaemonBackendStatus {
	return stageDaemonBackendStatus{
		Kind:     "mock-resident",
		NativeKV: false,
		Ready:    true,
	}
}

func (mockStageSessionBackend) Prepare(_ context.Context, session runtimes.StageSession, _ stageDaemonSessionRequest) (runtimes.StageSession, error) {
	session.RuntimeBackend = "mock-resident"
	session.RuntimeStatus = "resident-session-scaffold"
	session.PersistentModel = true
	session.PersistentKVInMemory = true
	session.Ready = true
	return session, nil
}

func (mockStageSessionBackend) Decode(_ context.Context, _ runtimes.StageSession, req stageDaemonDecodeRequest) (stageDaemonBackendDecodeResult, error) {
	result := stageDaemonBackendDecodeResult{}
	if req.TensorEnvelope != nil {
		result.Sequence = req.TensorEnvelope.Sequence
		result.Checksum = strings.TrimSpace(req.TensorEnvelope.Checksum)
	}
	return result, nil
}

func (mockStageSessionBackend) Close(_ context.Context, _ runtimes.StageSession) error {
	return nil
}

type llamaCPPResidentStageBackend struct {
	runnerBin string
	mu        sync.Mutex
	loop      *llamaCPPResidentLoop
}

type llamaCPPResidentLoop struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr *bytes.Buffer
	mu     sync.Mutex
}

type llamaCPPResidentCapabilities struct {
	Kind               string `json:"kind"`
	Protocol           string `json:"protocol,omitempty"`
	Ready              bool   `json:"ready"`
	NativeKV           bool   `json:"native_kv"`
	PersistentModel    bool   `json:"persistent_model"`
	PersistentKV       bool   `json:"persistent_kv_in_memory"`
	PrepareHook        bool   `json:"prepare_hook,omitempty"`
	DecodeHook         bool   `json:"decode_hook,omitempty"`
	SourceDecodeHook   bool   `json:"source_decode_hook,omitempty"`
	RelayDecodeHook    bool   `json:"relay_decode_hook,omitempty"`
	TerminalDecodeHook bool   `json:"terminal_decode_hook,omitempty"`
	Blocker            string `json:"blocker,omitempty"`
}

type llamaCPPResidentDecodeReport struct {
	Kind     string `json:"kind"`
	Status   string `json:"status"`
	Sequence uint64 `json:"sequence,omitempty"`
	Checksum string `json:"checksum,omitempty"`
}

type llamaCPPResidentLoopCapabilities struct {
	Kind               string `json:"kind"`
	Protocol           string `json:"protocol"`
	RunnerProtocol     string `json:"runner_protocol"`
	Ready              bool   `json:"ready"`
	PersistentProcess  bool   `json:"persistent_process"`
	NativeKV           bool   `json:"native_kv"`
	PersistentModel    bool   `json:"persistent_model"`
	PersistentKV       bool   `json:"persistent_kv_in_memory"`
	PrepareHook        bool   `json:"prepare_hook,omitempty"`
	DecodeHook         bool   `json:"decode_hook,omitempty"`
	SourceDecodeHook   bool   `json:"source_decode_hook,omitempty"`
	RelayDecodeHook    bool   `json:"relay_decode_hook,omitempty"`
	TerminalDecodeHook bool   `json:"terminal_decode_hook,omitempty"`
	Blocker            string `json:"blocker,omitempty"`
}

type llamaCPPResidentLoopPrepareReport struct {
	Kind              string `json:"kind"`
	Protocol          string `json:"protocol"`
	Status            string `json:"status"`
	SessionRegistered bool   `json:"session_registered"`
	SessionID         string `json:"session_id"`
	PersistentModel   bool   `json:"persistent_model"`
	PersistentKV      bool   `json:"persistent_kv_in_memory"`
	SelectedBytes     uint64 `json:"selected_bytes,omitempty"`
	SelectedTensors   uint64 `json:"selected_tensor_count,omitempty"`
	NLayer            int    `json:"n_layer,omitempty"`
	NEmbd             int    `json:"n_embd,omitempty"`
}

type llamaCPPResidentLoopDecodeReport struct {
	Kind          string  `json:"kind"`
	Protocol      string  `json:"protocol"`
	Status        string  `json:"status"`
	SessionID     string  `json:"session_id"`
	SessionFound  bool    `json:"session_found"`
	DecodeSteps   uint64  `json:"decode_steps,omitempty"`
	Sequence      uint64  `json:"sequence,omitempty"`
	Checksum      string  `json:"checksum,omitempty"`
	PayloadBytes  int     `json:"payload_bytes,omitempty"`
	OutputFile    string  `json:"output_file,omitempty"`
	OutputBytes   int     `json:"output_bytes,omitempty"`
	DecodeStatus  int     `json:"decode_status,omitempty"`
	TokenCount    uint64  `json:"token_count,omitempty"`
	NextTokenID   int     `json:"next_token_id,omitempty"`
	NextTokenText string  `json:"next_token_text,omitempty"`
	Final         bool    `json:"final,omitempty"`
	NextLogit     float64 `json:"next_token_logit,omitempty"`
}

func (b *llamaCPPResidentLoop) request(ctx context.Context, line string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, err := io.WriteString(b.stdin, line+"\n"); err != nil {
		return nil, err
	}
	type readResult struct {
		line string
		err  error
	}
	ch := make(chan readResult, 1)
	go func() {
		line, err := b.stdout.ReadString('\n')
		ch <- readResult{line: line, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-ch:
		if result.err != nil {
			stderr := ""
			if b.stderr != nil {
				stderr = strings.TrimSpace(b.stderr.String())
			}
			if stderr != "" {
				return nil, fmt.Errorf("%w: %s", result.err, stderr)
			}
			return nil, result.err
		}
		return []byte(strings.TrimSpace(result.line)), nil
	}
}

func (b *llamaCPPResidentLoop) close() error {
	if b == nil || b.cmd == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _ = b.request(ctx, "command=shutdown")
	if b.stdin != nil {
		_ = b.stdin.Close()
	}
	done := make(chan error, 1)
	go func() {
		done <- b.cmd.Wait()
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		if b.cmd.Process != nil {
			_ = b.cmd.Process.Kill()
		}
		return ctx.Err()
	}
}

func (b *llamaCPPResidentStageBackend) Kind() string { return "llama.cpp-resident" }

func (b *llamaCPPResidentStageBackend) NativeKV() bool { return true }

func (b *llamaCPPResidentStageBackend) Status() stageDaemonBackendStatus {
	status := stageDaemonBackendStatus{
		Kind:      "llama.cpp-resident",
		NativeKV:  true,
		Ready:     false,
		RunnerBin: strings.TrimSpace(b.runnerBin),
		MissingHooks: []string{
			"native model-stage load into a long-lived llama.cpp context",
			"per-stage KV ownership inside the daemon process",
			"in-process source/relay/terminal decode hooks",
		},
		Blocker: "llama.cpp resident backend is scaffolded but native hooks are not wired yet",
	}
	if status.RunnerBin != "" {
		if info, err := os.Stat(status.RunnerBin); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			status.RunnerReady = true
		}
	}
	if status.RunnerReady {
		if caps, err := b.residentCapabilities(context.Background()); err == nil {
			status.ResidentProtocol = strings.TrimSpace(caps.Protocol)
			status.DecodeReady = caps.Ready && caps.NativeKV && caps.PersistentModel && caps.PersistentKV && caps.DecodeHook
			status.Ready = status.DecodeReady
			if status.Ready {
				status.MissingHooks = nil
				status.Blocker = ""
			} else if caps.PrepareHook {
				status.MissingHooks = []string{
					"per-stage KV ownership inside the daemon process",
					"in-process source/relay/terminal decode hooks",
				}
				status.Blocker = "llama.cpp resident backend can prepare a stage model/context, but resident decode hooks are not wired yet"
			}
		}
	}
	return status
}

func (b *llamaCPPResidentStageBackend) Prepare(ctx context.Context, session runtimes.StageSession, req stageDaemonSessionRequest) (runtimes.StageSession, error) {
	session.RuntimeBackend = "llama.cpp-resident"
	session.RuntimeStatus = "blocked_missing_native_hooks"
	session.PersistentModel = false
	session.PersistentKVInMemory = false
	session.Ready = false
	status := b.Status()
	if strings.TrimSpace(status.RunnerBin) == "" {
		return session, fmt.Errorf("llama.cpp resident stage backend requires --runner-bin or CMESH_STAGE_RUNNER_BIN before native hooks can be tested")
	}
	if !status.RunnerReady {
		return session, fmt.Errorf("llama.cpp resident stage backend runner is not executable: %s", status.RunnerBin)
	}
	modelPath := strings.TrimSpace(req.ModelPath)
	if modelPath == "" {
		return session, fmt.Errorf("llama.cpp resident stage backend requires model_path for stage prepare probe")
	}
	probeKind, err := b.probeStageModel(ctx, status.RunnerBin, modelPath, req)
	if err != nil {
		return session, err
	}
	if caps, err := b.residentCapabilities(ctx); err == nil && caps.Ready && caps.NativeKV && caps.PersistentModel && caps.PersistentKV && caps.DecodeHook {
		prepared, err := b.prepareResidentLoop(ctx, session, req, true)
		if err != nil {
			return session, fmt.Errorf("llama.cpp resident-loop native prepare failed after %s probe: %w", probeKind, err)
		}
		if !prepared.Ready || !prepared.PersistentModel || !prepared.PersistentKVInMemory {
			return session, fmt.Errorf("llama.cpp resident-loop native prepare did not report ready persistent model/KV after %s probe: status=%s", probeKind, prepared.RuntimeStatus)
		}
		session = prepared
	} else if prepared, err := b.prepareResidentLoop(ctx, session, req, true); err == nil && prepared.PersistentModel && prepared.PersistentKVInMemory && prepared.RuntimeStatus == "resident_loop_model_context_ready_missing_decode_hooks" {
		session = prepared
	} else {
		session.RuntimeStatus = "prepare_probe_ready_missing_native_decode_hooks"
	}
	return session, nil
}

func (b *llamaCPPResidentStageBackend) probeStageModel(ctx context.Context, runnerBin string, modelPath string, req stageDaemonSessionRequest) (string, error) {
	if output, err := exec.CommandContext(ctx, runnerBin,
		"--command", "probe-stage-gguf-load",
		"--model", modelPath,
	).CombinedOutput(); err == nil {
		var probed struct {
			Kind       string `json:"kind"`
			Status     string `json:"status"`
			StageStart int    `json:"stage_start"`
			StageEnd   int    `json:"stage_end"`
			Loaded     bool   `json:"loaded"`
		}
		if parseErr := json.Unmarshal(llamaCPPStagePrepareJSON(output, "cmesh.llamacpp_stage_gguf_load_probe"), &probed); parseErr == nil {
			if probed.Kind == "cmesh.llamacpp_stage_gguf_load_probe" && probed.Loaded && probed.Status != "" {
				if probed.StageStart >= 0 && probed.StageEnd >= 0 && (probed.StageStart != req.LayerStart || probed.StageEnd != req.LayerEnd) {
					return "", fmt.Errorf("llama.cpp resident stage GGUF probe layer range mismatch: probe=%d-%d request=%d-%d", probed.StageStart, probed.StageEnd, req.LayerStart, req.LayerEnd)
				}
				return "stage-gguf-load", nil
			}
		}
	}

	output, err := exec.CommandContext(ctx, runnerBin,
		"--command", "prepare",
		"--model", modelPath,
		"--stage-start", strconv.Itoa(req.LayerStart),
		"--stage-end", strconv.Itoa(req.LayerEnd),
		"--stage-index", strconv.Itoa(req.StageIndex),
	).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("llama.cpp resident stage prepare probe failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	var prepared struct {
		Kind       string `json:"kind"`
		Status     string `json:"status"`
		StageIndex int    `json:"stage_index"`
	}
	if err := json.Unmarshal(llamaCPPStagePrepareJSON(output, "cmesh.llamacpp_stage_prepare"), &prepared); err != nil {
		return "", fmt.Errorf("parse llama.cpp resident stage prepare probe: %w", err)
	}
	if prepared.Kind != "cmesh.llamacpp_stage_prepare" || prepared.Status == "" || prepared.StageIndex != req.StageIndex {
		return "", fmt.Errorf("unexpected llama.cpp resident stage prepare probe: %s", strings.TrimSpace(string(output)))
	}
	return "stage-prepare", nil
}

func (b *llamaCPPResidentStageBackend) Decode(ctx context.Context, session runtimes.StageSession, req stageDaemonDecodeRequest) (stageDaemonBackendDecodeResult, error) {
	if !session.PersistentModel || !session.PersistentKVInMemory {
		return stageDaemonBackendDecodeResult{}, fmt.Errorf("llama.cpp resident stage backend is not ready")
	}
	if !session.Ready && strings.TrimSpace(req.StageCommand) != "source_decode" && strings.TrimSpace(req.StageCommand) != "relay_decode" && strings.TrimSpace(req.StageCommand) != "terminal_decode" {
		return stageDaemonBackendDecodeResult{}, fmt.Errorf("llama.cpp resident stage backend is only source/relay/terminal-decode ready")
	}
	result, err := b.decodeResidentLoop(ctx, session, req)
	if err != nil {
		return stageDaemonBackendDecodeResult{}, fmt.Errorf("llama.cpp resident-loop decode failed: %w", err)
	}
	return result, nil
}

func (b *llamaCPPResidentStageBackend) decodeResidentCommand(ctx context.Context, session runtimes.StageSession, req stageDaemonDecodeRequest) (stageDaemonBackendDecodeResult, error) {
	args := []string{
		"--command", "resident-decode",
		"--session-id", session.SessionID,
		"--model", session.ModelPath,
		"--stage-start", strconv.Itoa(session.LayerStart),
		"--stage-end", strconv.Itoa(session.LayerEnd),
		"--stage-index", strconv.Itoa(session.StageIndex),
		"--step", strconv.FormatUint(stageCommandStep(req.Step), 10),
	}
	if command := strings.TrimSpace(req.StageCommand); command != "" {
		args = append(args, "--stage-command", command)
	}
	if req.TensorEnvelope != nil {
		args = append(args, "--dtype", req.TensorEnvelope.DType, "--shape", joinInts(req.TensorEnvelope.Shape))
	}
	if prompt := strings.TrimSpace(req.Prompt); prompt != "" {
		args = append(args, "--prompt", prompt)
	}
	if req.PreviousTokenID != nil {
		args = append(args, "--token-id", strconv.Itoa(*req.PreviousTokenID))
	}
	if text := strings.TrimSpace(req.PreviousTokenText); text != "" {
		args = append(args, "--token-text", text)
	}
	if payload := strings.TrimSpace(req.ActivationPayloadBase64); payload != "" {
		decoded, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return stageDaemonBackendDecodeResult{}, fmt.Errorf("decode resident activation payload: %w", err)
		}
		if req.TensorEnvelope != nil {
			if err := req.TensorEnvelope.ValidatePayload(decoded); err != nil {
				return stageDaemonBackendDecodeResult{}, err
			}
		}
		tempFile, err := os.CreateTemp("", "cmesh-resident-activation-*.bin")
		if err != nil {
			return stageDaemonBackendDecodeResult{}, err
		}
		tempPath := tempFile.Name()
		if _, err := tempFile.Write(decoded); err != nil {
			_ = tempFile.Close()
			_ = os.Remove(tempPath)
			return stageDaemonBackendDecodeResult{}, err
		}
		if err := tempFile.Close(); err != nil {
			_ = os.Remove(tempPath)
			return stageDaemonBackendDecodeResult{}, err
		}
		defer os.Remove(tempPath)
		args = append(args, "--activation-file", tempPath)
	}
	output, err := exec.CommandContext(ctx, strings.TrimSpace(b.runnerBin), args...).CombinedOutput()
	if err != nil {
		return stageDaemonBackendDecodeResult{}, fmt.Errorf("llama.cpp resident stage decode failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	var report llamaCPPResidentDecodeReport
	if err := json.Unmarshal(stageRunnerJSONReport(output, "cmesh.llamacpp_resident_decode"), &report); err != nil {
		return stageDaemonBackendDecodeResult{}, fmt.Errorf("parse llama.cpp resident stage decode: %w", err)
	}
	if report.Kind != "cmesh.llamacpp_resident_decode" || report.Status == "" {
		return stageDaemonBackendDecodeResult{}, fmt.Errorf("unexpected llama.cpp resident decode report: %s", strings.TrimSpace(string(output)))
	}
	return stageDaemonBackendDecodeResult{
		Sequence: report.Sequence,
		Checksum: strings.TrimSpace(report.Checksum),
	}, nil
}

func residentLoopKV(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if strings.ContainsAny(value, " \t\r\n=") {
		return "", fmt.Errorf("resident-loop line protocol value contains unsupported whitespace or equals: %q", value)
	}
	return value, nil
}

func (b *llamaCPPResidentStageBackend) startResidentLoop(ctx context.Context) (*llamaCPPResidentLoop, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.loop != nil && b.loop.cmd != nil && b.loop.cmd.Process != nil {
		return b.loop, nil
	}
	runner := strings.TrimSpace(b.runnerBin)
	if runner == "" {
		return nil, fmt.Errorf("runner-bin is required")
	}
	cmd := exec.Command(runner, "--command", "resident-loop")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	loop := &llamaCPPResidentLoop{
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReader(stdout),
		stderr: stderr,
	}
	line, err := loop.request(ctx, "command=capabilities")
	if err != nil {
		_ = loop.close()
		return nil, err
	}
	var caps llamaCPPResidentLoopCapabilities
	if err := json.Unmarshal(line, &caps); err != nil {
		_ = loop.close()
		return nil, err
	}
	if caps.Kind != "cmesh.llamacpp_resident_loop_capabilities" || caps.Protocol != "cdip.llamacpp-resident-loop-v1" {
		_ = loop.close()
		return nil, fmt.Errorf("unexpected resident-loop capabilities: %s", string(line))
	}
	b.loop = loop
	return loop, nil
}

func (b *llamaCPPResidentStageBackend) prepareResidentLoop(ctx context.Context, session runtimes.StageSession, req stageDaemonSessionRequest, nativePrepare bool) (runtimes.StageSession, error) {
	sessionID, err := residentLoopKV(session.SessionID)
	if err != nil || sessionID == "" {
		return session, fmt.Errorf("resident-loop requires safe session id: %w", err)
	}
	modelPath, err := residentLoopKV(strings.TrimSpace(req.ModelPath))
	if err != nil || modelPath == "" {
		return session, fmt.Errorf("resident-loop requires safe model path: %w", err)
	}
	loop, err := b.startResidentLoop(ctx)
	if err != nil {
		return session, err
	}
	line := fmt.Sprintf("command=prepare session_id=%s model=%s stage_index=%d stage_start=%d stage_end=%d",
		sessionID,
		modelPath,
		req.StageIndex,
		req.LayerStart,
		req.LayerEnd,
	)
	if nativePrepare {
		line += fmt.Sprintf(" native_prepare=1 ctx=%d", residentLoopPrepareContextSize())
	}
	output, err := loop.request(ctx, line)
	if err != nil {
		return session, err
	}
	var report llamaCPPResidentLoopPrepareReport
	if err := json.Unmarshal(output, &report); err != nil {
		return session, err
	}
	if report.Kind != "cmesh.llamacpp_resident_loop_prepare" || report.Protocol != "cdip.llamacpp-resident-loop-v1" || report.SessionID != session.SessionID {
		return session, fmt.Errorf("unexpected resident-loop prepare report: %s", string(output))
	}
	if report.Status == "resident_model_context_ready_missing_decode_hooks" {
		session.RuntimeBackend = "llama.cpp-resident"
		session.RuntimeStatus = "resident_loop_model_context_ready_missing_decode_hooks"
		session.PersistentModel = report.PersistentModel
		session.PersistentKVInMemory = report.PersistentKV
		session.Ready = false
		return session, nil
	}
	if report.Status != "resident_ready" && report.Status != "prepared" && report.Status != "ready" {
		return session, fmt.Errorf("resident-loop prepare not ready: %s", string(output))
	}
	session.RuntimeBackend = "llama.cpp-resident"
	session.RuntimeStatus = "resident_loop_ready"
	session.PersistentModel = report.PersistentModel
	session.PersistentKVInMemory = report.PersistentKV
	session.Ready = report.SessionRegistered && report.PersistentModel && report.PersistentKV
	return session, nil
}

func residentLoopPrepareContextSize() int {
	raw := strings.TrimSpace(os.Getenv("CMESH_RESIDENT_STAGE_CTX"))
	if raw == "" {
		return 2048
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 2048
	}
	return value
}

func (b *llamaCPPResidentStageBackend) decodeResidentLoop(ctx context.Context, session runtimes.StageSession, req stageDaemonDecodeRequest) (stageDaemonBackendDecodeResult, error) {
	sessionID, err := residentLoopKV(session.SessionID)
	if err != nil || sessionID == "" {
		return stageDaemonBackendDecodeResult{}, fmt.Errorf("resident-loop requires safe session id: %w", err)
	}
	stageCommand, err := residentLoopKV(strings.TrimSpace(req.StageCommand))
	if err != nil {
		return stageDaemonBackendDecodeResult{}, err
	}
	loop, err := b.startResidentLoop(ctx)
	if err != nil {
		return stageDaemonBackendDecodeResult{}, err
	}
	line := fmt.Sprintf("command=decode session_id=%s stage_command=%s step=%d", sessionID, stageCommand, stageCommandStep(req.Step))
	if req.TensorEnvelope != nil {
		dtype, err := residentLoopKV(req.TensorEnvelope.DType)
		if err != nil {
			return stageDaemonBackendDecodeResult{}, err
		}
		shape, err := residentLoopKV(joinInts(req.TensorEnvelope.Shape))
		if err != nil {
			return stageDaemonBackendDecodeResult{}, err
		}
		if dtype != "" {
			line += " dtype=" + dtype
		}
		if shape != "" {
			line += " shape=" + shape
		}
	}
	if req.PreviousTokenID != nil {
		line += fmt.Sprintf(" token_id=%d", *req.PreviousTokenID)
	}
	var promptPath string
	if strings.TrimSpace(req.Prompt) != "" {
		promptFile, err := os.CreateTemp("", "cmesh-resident-loop-prompt-*.txt")
		if err != nil {
			return stageDaemonBackendDecodeResult{}, err
		}
		promptPath = promptFile.Name()
		if _, err := promptFile.WriteString(req.Prompt); err != nil {
			_ = promptFile.Close()
			_ = os.Remove(promptPath)
			return stageDaemonBackendDecodeResult{}, err
		}
		if err := promptFile.Close(); err != nil {
			_ = os.Remove(promptPath)
			return stageDaemonBackendDecodeResult{}, err
		}
		defer os.Remove(promptPath)
		safePromptPath, err := residentLoopKV(promptPath)
		if err != nil {
			return stageDaemonBackendDecodeResult{}, err
		}
		line += " prompt_file=" + safePromptPath
	}
	var outputPath string
	if strings.TrimSpace(req.StageCommand) == "source_decode" || strings.TrimSpace(req.StageCommand) == "relay_decode" {
		outputFile, err := os.CreateTemp("", "cmesh-resident-loop-output-*.bin")
		if err != nil {
			return stageDaemonBackendDecodeResult{}, err
		}
		outputPath = outputFile.Name()
		if err := outputFile.Close(); err != nil {
			_ = os.Remove(outputPath)
			return stageDaemonBackendDecodeResult{}, err
		}
		defer os.Remove(outputPath)
		safeOutputPath, err := residentLoopKV(outputPath)
		if err != nil {
			return stageDaemonBackendDecodeResult{}, err
		}
		line += " output_file=" + safeOutputPath
	}
	var tempPath string
	if payload := strings.TrimSpace(req.ActivationPayloadBase64); payload != "" {
		decoded, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return stageDaemonBackendDecodeResult{}, fmt.Errorf("decode resident-loop activation payload: %w", err)
		}
		if req.TensorEnvelope != nil {
			if err := req.TensorEnvelope.ValidatePayload(decoded); err != nil {
				return stageDaemonBackendDecodeResult{}, err
			}
		}
		tempFile, err := os.CreateTemp("", "cmesh-resident-loop-activation-*.bin")
		if err != nil {
			return stageDaemonBackendDecodeResult{}, err
		}
		tempPath = tempFile.Name()
		if _, err := tempFile.Write(decoded); err != nil {
			_ = tempFile.Close()
			_ = os.Remove(tempPath)
			return stageDaemonBackendDecodeResult{}, err
		}
		if err := tempFile.Close(); err != nil {
			_ = os.Remove(tempPath)
			return stageDaemonBackendDecodeResult{}, err
		}
		defer os.Remove(tempPath)
		safePath, err := residentLoopKV(tempPath)
		if err != nil {
			return stageDaemonBackendDecodeResult{}, err
		}
		line += " activation_file=" + safePath
	}
	output, err := loop.request(ctx, line)
	if err != nil {
		return stageDaemonBackendDecodeResult{}, err
	}
	var report llamaCPPResidentLoopDecodeReport
	if err := json.Unmarshal(output, &report); err != nil {
		return stageDaemonBackendDecodeResult{}, err
	}
	if report.Kind != "cmesh.llamacpp_resident_loop_decode" || report.Protocol != "cdip.llamacpp-resident-loop-v1" || report.SessionID != session.SessionID {
		return stageDaemonBackendDecodeResult{}, fmt.Errorf("unexpected resident-loop decode report: %s", string(output))
	}
	if report.Status != "decoded" && report.Status != "resident_decoded" && report.Status != "resident_source_decoded" && report.Status != "resident_relay_decoded" && report.Status != "resident_terminal_decoded" {
		return stageDaemonBackendDecodeResult{}, fmt.Errorf("resident-loop decode not ready: %s", string(output))
	}
	result := stageDaemonBackendDecodeResult{
		Sequence: report.Sequence,
		Checksum: strings.TrimSpace(report.Checksum),
	}
	if report.Status == "resident_terminal_decoded" {
		tokenID := report.NextTokenID
		final := report.Final
		result.NextTokenID = &tokenID
		result.NextTokenText = report.NextTokenText
		result.Final = &final
		if result.Sequence == 0 {
			result.Sequence = stageCommandStep(req.Step)
		}
	}
	if outputPath != "" && report.OutputBytes > 0 {
		payload, err := os.ReadFile(outputPath)
		if err != nil {
			return stageDaemonBackendDecodeResult{}, err
		}
		if len(payload) != report.OutputBytes {
			return stageDaemonBackendDecodeResult{}, fmt.Errorf("resident-loop output byte mismatch: report=%d actual=%d", report.OutputBytes, len(payload))
		}
		sum := sha256.Sum256(payload)
		result.OutputPayloadBase64 = base64.StdEncoding.EncodeToString(payload)
		result.OutputBytes = len(payload)
		result.OutputChecksum = "sha256:" + hex.EncodeToString(sum[:])
		result.OutputDType = "f32"
		if result.OutputBytes > 0 && result.OutputBytes%4 == 0 {
			tokenCount := int(report.TokenCount)
			if tokenCount <= 0 {
				tokenCount = 1
			}
			embd := result.OutputBytes / 4
			if embd%tokenCount == 0 {
				embd = embd / tokenCount
			} else {
				tokenCount = 1
			}
			result.OutputShape = []int{1, tokenCount, embd}
		}
		if result.Sequence == 0 {
			result.Sequence = stageCommandStep(req.Step)
		}
		if result.Checksum == "" {
			result.Checksum = result.OutputChecksum
		}
	}
	return result, nil
}

func (b *llamaCPPResidentStageBackend) Close(context.Context, runtimes.StageSession) error {
	b.mu.Lock()
	loop := b.loop
	b.loop = nil
	b.mu.Unlock()
	if loop != nil {
		return loop.close()
	}
	return nil
}

func (b *llamaCPPResidentStageBackend) residentCapabilities(ctx context.Context) (llamaCPPResidentCapabilities, error) {
	runner := strings.TrimSpace(b.runnerBin)
	if runner == "" {
		return llamaCPPResidentCapabilities{}, fmt.Errorf("runner-bin is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, runner, "--command", "resident-capabilities").CombinedOutput()
	if err != nil {
		return llamaCPPResidentCapabilities{}, err
	}
	var caps llamaCPPResidentCapabilities
	if err := json.Unmarshal(stageRunnerJSONReport(output, "cmesh.llamacpp_resident_capabilities"), &caps); err != nil {
		return llamaCPPResidentCapabilities{}, err
	}
	if caps.Kind != "cmesh.llamacpp_resident_capabilities" {
		return llamaCPPResidentCapabilities{}, fmt.Errorf("unexpected resident capabilities report: %s", strings.TrimSpace(string(output)))
	}
	return caps, nil
}

func newStageSessionBackend(name string, runnerBin string) (stageSessionBackend, error) {
	switch strings.TrimSpace(strings.ToLower(name)) {
	case "", "mock", "mock-resident":
		return mockStageSessionBackend{}, nil
	case "llama.cpp-resident", "llamacpp-resident", "native":
		return &llamaCPPResidentStageBackend{runnerBin: strings.TrimSpace(runnerBin)}, nil
	default:
		return nil, fmt.Errorf("unsupported stage daemon backend %q", name)
	}
}

func newStageRunnerDaemonHandlerWithBackend(sessionDir string, backend stageSessionBackend) http.Handler {
	if backend == nil {
		backend = mockStageSessionBackend{}
	}
	state := &stageDaemonState{
		sessionDir: strings.TrimSpace(sessionDir),
		backend:    backend,
		sessions:   make(map[string]*stageDaemonSessionRecord),
	}
	if state.sessionDir == "" {
		state.sessionDir = defaultStageDaemonSessionDir()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", state.handleHealth)
	mux.HandleFunc("/v1/sessions", state.handleSessions)
	mux.HandleFunc("/v1/sessions/", state.handleSession)
	return mux
}

func (s *stageDaemonState) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	sessionCount := len(s.sessions)
	s.mu.Unlock()
	backendStatus := s.backend.Status()
	writeStageDaemonJSON(w, http.StatusOK, map[string]any{
		"kind":           "cmesh.stage_daemon",
		"status":         "ok",
		"protocol":       runtimes.StageSessionV1,
		"backend":        s.backend.Kind(),
		"backend_status": backendStatus,
		"native_kv":      s.backend.NativeKV(),
		"session_count":  sessionCount,
		"session_dir":    s.sessionDir,
	})
}

func (s *stageDaemonState) handleSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var req stageDaemonSessionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		session := s.sessionFromRequest(req, r)
		if err := session.Validate(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := os.MkdirAll(s.sessionDir, 0o755); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		session, err := s.backend.Prepare(r.Context(), session, req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if err := session.Validate(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		now := time.Now().UTC()
		record := &stageDaemonSessionRecord{
			Session:     session,
			BackendKind: s.backend.Kind(),
			NativeKV:    s.backend.NativeKV(),
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		s.sessions[session.SessionID] = record
		s.mu.Unlock()
		if err := s.writeSessionRecord(record); err != nil {
			s.mu.Lock()
			delete(s.sessions, session.SessionID)
			s.mu.Unlock()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeStageDaemonJSON(w, http.StatusCreated, session)
	case http.MethodGet:
		s.mu.Lock()
		sessions := make([]runtimes.StageSession, 0, len(s.sessions))
		for _, record := range s.sessions {
			sessions = append(sessions, record.Session)
		}
		s.mu.Unlock()
		writeStageDaemonJSON(w, http.StatusOK, map[string]any{
			"sessions": sessions,
		})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *stageDaemonState) handleSession(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/sessions/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	id := ""
	if len(parts) > 0 {
		id = strings.TrimSpace(parts[0])
	}
	id = strings.TrimSpace(id)
	if id == "" {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 2 && parts[1] == "decode" {
		s.handleSessionDecode(w, r, id)
		return
	}
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.mu.Lock()
		record, ok := s.sessions[id]
		s.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeStageDaemonJSON(w, http.StatusOK, record)
	case http.MethodDelete:
		s.mu.Lock()
		record, ok := s.sessions[id]
		if ok {
			delete(s.sessions, id)
		}
		s.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		if err := s.backend.Close(r.Context(), record.Session); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if err := s.removeSessionRecord(id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeStageDaemonJSON(w, http.StatusOK, map[string]any{
			"session_id": id,
			"closed":     true,
		})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *stageDaemonState) handleSessionDecode(w http.ResponseWriter, r *http.Request, id string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			fmt.Fprintf(os.Stderr, "stage daemon decode panic session=%s: %v\n", id, recovered)
			writeStageDaemonJSON(w, http.StatusInternalServerError, map[string]any{
				"kind":       "cmesh.stage_daemon_error",
				"session_id": id,
				"error":      fmt.Sprintf("stage daemon decode panic: %v", recovered),
			})
		}
	}()
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req stageDaemonDecodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.TensorEnvelope != nil && strings.TrimSpace(req.TensorEnvelope.Protocol) != runtimes.TensorEnvelopeV1 {
		http.Error(w, "unsupported tensor envelope protocol", http.StatusBadRequest)
		return
	}
	payloadBytes := 0
	if payload := strings.TrimSpace(req.ActivationPayloadBase64); payload != "" {
		decoded, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			http.Error(w, "invalid activation payload encoding", http.StatusBadRequest)
			return
		}
		payloadBytes = len(decoded)
		if req.TensorEnvelope != nil {
			if err := req.TensorEnvelope.ValidatePayload(decoded); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
	}
	step := req.Step
	if step == 0 {
		step = 1
	}
	s.mu.Lock()
	record, ok := s.sessions[id]
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	fmt.Fprintf(os.Stderr, "stage daemon decode start session=%s command=%s step=%d\n", id, strings.TrimSpace(req.StageCommand), step)
	backendResult, err := s.backend.Decode(r.Context(), record.Session, req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stage daemon decode failed session=%s command=%s step=%d: %v\n", id, strings.TrimSpace(req.StageCommand), step, err)
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	fmt.Fprintf(os.Stderr, "stage daemon decode ok session=%s command=%s step=%d output_bytes=%d final=%v\n", id, strings.TrimSpace(req.StageCommand), step, backendResult.OutputBytes, backendResult.Final)
	s.mu.Lock()
	record.DecodeSteps++
	record.LastStep = step
	record.LastStageCommand = strings.TrimSpace(req.StageCommand)
	record.LastPayloadBytes = payloadBytes
	record.LastSequence = backendResult.Sequence
	record.LastChecksum = backendResult.Checksum
	record.UpdatedAt = time.Now().UTC()
	recordCopy := *record
	decodeSteps := record.DecodeSteps
	lastStageCommand := record.LastStageCommand
	lastPayloadBytes := record.LastPayloadBytes
	lastSequence := record.LastSequence
	lastChecksum := record.LastChecksum
	session := record.Session
	backendKind := record.BackendKind
	nativeKV := record.NativeKV
	s.mu.Unlock()
	if err := s.writeSessionRecord(&recordCopy); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeStageDaemonJSON(w, http.StatusOK, map[string]any{
		"kind":                    "cmesh.stage_daemon_decode",
		"session_id":              id,
		"step":                    step,
		"decode_steps":            decodeSteps,
		"last_stage_command":      lastStageCommand,
		"last_payload_bytes":      lastPayloadBytes,
		"backend":                 backendKind,
		"native_kv":               nativeKV,
		"last_sequence":           lastSequence,
		"last_checksum":           lastChecksum,
		"output_payload_base64":   backendResult.OutputPayloadBase64,
		"output_bytes":            backendResult.OutputBytes,
		"output_checksum":         backendResult.OutputChecksum,
		"output_dtype":            backendResult.OutputDType,
		"output_shape":            backendResult.OutputShape,
		"next_token_id":           backendResult.NextTokenID,
		"next_token_text":         backendResult.NextTokenText,
		"final":                   backendResult.Final,
		"persistent_model":        session.PersistentModel,
		"persistent_kv_in_memory": session.PersistentKVInMemory,
		"ready":                   session.Ready,
	})
}

func (s *stageDaemonState) sessionRecordPath(sessionID string) string {
	return filepath.Join(s.sessionDir, url.PathEscape(strings.TrimSpace(sessionID))+".json")
}

func (s *stageDaemonState) writeSessionRecord(record *stageDaemonSessionRecord) error {
	if record == nil || strings.TrimSpace(record.Session.SessionID) == "" {
		return fmt.Errorf("stage daemon session record is missing session id")
	}
	if err := os.MkdirAll(s.sessionDir, 0o755); err != nil {
		return err
	}
	path := s.sessionRecordPath(record.Session.SessionID)
	tmp := path + ".tmp"
	body, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *stageDaemonState) removeSessionRecord(sessionID string) error {
	err := os.Remove(s.sessionRecordPath(sessionID))
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *stageDaemonState) sessionFromRequest(req stageDaemonSessionRequest, r *http.Request) runtimes.StageSession {
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		seed := strings.Join([]string{
			req.ParentJobID,
			req.StageJobID,
			req.ModelID,
			strconv.Itoa(req.StageIndex),
			strconv.Itoa(req.LayerStart),
			strconv.Itoa(req.LayerEnd),
			req.KVCacheKey,
		}, "\x00")
		sum := sha256.Sum256([]byte(seed))
		sessionID = fmt.Sprintf("stage-%d-%s", req.StageIndex, hex.EncodeToString(sum[:8]))
	}
	endpoint := "http://" + r.Host + "/v1/sessions/" + url.PathEscape(sessionID)
	return runtimes.StageSession{
		Protocol:             runtimes.StageSessionV1,
		Mode:                 runtimes.StageSessionModeDaemon,
		SessionID:            sessionID,
		ParentJobID:          strings.TrimSpace(req.ParentJobID),
		StageJobID:           strings.TrimSpace(req.StageJobID),
		ModelID:              strings.TrimSpace(req.ModelID),
		ModelPath:            strings.TrimSpace(req.ModelPath),
		StageIndex:           req.StageIndex,
		LayerStart:           req.LayerStart,
		LayerEnd:             req.LayerEnd,
		KVCacheKey:           strings.TrimSpace(req.KVCacheKey),
		Endpoint:             endpoint,
		RuntimeBackend:       "unprepared",
		RuntimeStatus:        "pending",
		PersistentModel:      true,
		PersistentKVInMemory: true,
		Ready:                true,
	}
}

func writeStageDaemonJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func executeDistributedStageRelayDecodeJob(job jobs.Job, req models.DistributedStageJobInput, snapshot cluster.ResourceSnapshot, cacheDir string, nodeID string, managerURL string) (string, error) {
	stageJobID := strings.TrimSpace(req.StageJobID)
	if stageJobID == "" {
		stageJobID = strings.TrimSpace(job.ID)
	}
	if stageJobID == "" {
		return "", fmt.Errorf("stage job id is required for relay decode")
	}
	upstreamStageID := strings.TrimSpace(req.UpstreamStageID)
	if upstreamStageID == "" {
		return "", fmt.Errorf("upstream_stage_id is required for relay decode")
	}
	downstreamStageID := strings.TrimSpace(req.DownstreamStageID)
	if downstreamStageID == "" {
		return "", fmt.Errorf("downstream_stage_id is required for relay decode")
	}
	if strings.TrimSpace(req.DownstreamNodeID) == "" {
		return "", fmt.Errorf("downstream_node_id is required for relay decode")
	}
	managerURL = strings.TrimSpace(managerURL)
	if managerURL == "" {
		return "", fmt.Errorf("manager URL is required for relay decode")
	}
	nodeID = firstNonEmptyString(nodeID, req.Stage.NodeID)
	modelPath := firstNonEmptyString(req.ModelPath, workerModelPath(snapshot, req.ModelID))
	if modelPath == "" {
		return "", fmt.Errorf("model path is required for relay decode")
	}
	runnerBin := firstNonEmptyString(req.StageRunnerBin, os.Getenv("CMESH_STAGE_RUNNER_BIN"), defaultStageRunnerBinaryPath())
	if runnerBin == "" {
		return "", fmt.Errorf("stage runner binary is required for relay decode")
	}
	workDir := strings.TrimSpace(req.WorkDir)
	if workDir == "" && strings.TrimSpace(cacheDir) != "" {
		workDir = filepath.Join(cacheDir, "stage-runs", job.ID)
	}
	return executeStageRunnerRelayDecode(stageRunnerRelayDecodeOptions{
		ParentJobID:       req.ParentJobID,
		UpstreamStageID:   upstreamStageID,
		StageJobID:        stageJobID,
		StageIndex:        req.Stage.Index,
		Step:              req.Step,
		KVCacheKey:        req.KVCacheKey,
		DownstreamStageID: downstreamStageID,
		DownstreamNodeID:  req.DownstreamNodeID,
		ManagerURL:        managerURL,
		NodeID:            nodeID,
		TimeoutMS:         req.TimeoutMS,
		RunnerBin:         runnerBin,
		ModelID:           req.ModelID,
		ModelPath:         modelPath,
		StageStart:        req.Stage.LayerStart,
		StageEnd:          req.Stage.LayerEnd,
		WorkDir:           workDir,
		StageDaemonURL:    req.StageDaemonURL,
		StageSessionID:    stageJobSessionID(req, stageJobID),
	})
}

func executeDistributedStageTerminalDecodeJob(job jobs.Job, req models.DistributedStageJobInput, snapshot cluster.ResourceSnapshot, cacheDir string, nodeID string, managerURL string) (string, error) {
	stageJobID := strings.TrimSpace(req.StageJobID)
	if stageJobID == "" {
		stageJobID = strings.TrimSpace(job.ID)
	}
	if stageJobID == "" {
		return "", fmt.Errorf("stage job id is required for terminal decode")
	}
	upstreamStageID := strings.TrimSpace(req.UpstreamStageID)
	if upstreamStageID == "" {
		return "", fmt.Errorf("upstream_stage_id is required for terminal decode")
	}
	managerURL = strings.TrimSpace(managerURL)
	if managerURL == "" {
		return "", fmt.Errorf("manager URL is required for terminal decode")
	}
	nodeID = firstNonEmptyString(nodeID, req.Stage.NodeID)
	modelPath := firstNonEmptyString(req.ModelPath, workerModelPath(snapshot, req.ModelID))
	if modelPath == "" {
		return "", fmt.Errorf("model path is required for terminal decode")
	}
	runnerBin := firstNonEmptyString(req.StageRunnerBin, os.Getenv("CMESH_STAGE_RUNNER_BIN"), defaultStageRunnerBinaryPath())
	if runnerBin == "" {
		return "", fmt.Errorf("stage runner binary is required for terminal decode")
	}
	workDir := strings.TrimSpace(req.WorkDir)
	if workDir == "" && strings.TrimSpace(cacheDir) != "" {
		workDir = filepath.Join(cacheDir, "stage-runs", job.ID)
	}
	return executeStageRunnerTerminalDecode(stageRunnerRelayDecodeOptions{
		ParentJobID:        req.ParentJobID,
		UpstreamStageID:    upstreamStageID,
		StageJobID:         stageJobID,
		StageIndex:         req.Stage.Index,
		Step:               req.Step,
		KVCacheKey:         req.KVCacheKey,
		ManagerURL:         managerURL,
		NodeID:             nodeID,
		TimeoutMS:          req.TimeoutMS,
		RunnerBin:          runnerBin,
		ModelID:            req.ModelID,
		ModelPath:          modelPath,
		StageStart:         req.Stage.LayerStart,
		StageEnd:           req.Stage.LayerEnd,
		WorkDir:            workDir,
		StageDaemonURL:     req.StageDaemonURL,
		StageSessionID:     stageJobSessionID(req, stageJobID),
		TerminalForceFinal: req.TerminalForceFinal,
		PreviousTokenText:  req.PreviousTokenText,
	})
}

func workerModelPath(snapshot cluster.ResourceSnapshot, modelID string) string {
	for _, model := range snapshot.Models {
		if model.ID == modelID && model.Ready {
			return strings.TrimSpace(model.Path)
		}
	}
	return ""
}

func defaultStageRunnerBinaryPath() string {
	if len(os.Args) == 0 {
		return ""
	}
	binaryPath, err := os.Executable()
	if err != nil {
		binaryPath = os.Args[0]
	}
	binaryPath = strings.TrimSpace(binaryPath)
	if binaryPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(binaryPath), "cmesh-stage-runner")
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
	Kind             string                                 `json:"kind"`
	ModelID          string                                 `json:"model_id"`
	Output           string                                 `json:"output"`
	Tokens           int                                    `json:"tokens,omitempty"`
	WorkerRuntime    string                                 `json:"worker_runtime"`
	ModelRuntime     string                                 `json:"model_runtime"`
	RuntimeVersion   string                                 `json:"runtime_version,omitempty"`
	RPCEndpoints     []string                               `json:"rpc_endpoints,omitempty"`
	RPCEndpointCount int                                    `json:"rpc_endpoint_count,omitempty"`
	ExecutionResult  protocol.DistributedRPCExecutionResult `json:"execution_result,omitempty"`
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
		if err := writeModelManifestWithStageMetadata(cacheDir, model, path, uint64(stat.Size()), stat.ModTime()); err != nil {
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
	if err := writeModelManifestWithStageMetadata(cacheDir, model, path, uint64(bytesWritten), time.Now().UTC()); err != nil {
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
		if err := writeModelManifestWithStageMetadata(cacheDir, model, path, uint64(stat.Size()), stat.ModTime()); err != nil {
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

func writeModelManifestWithStageMetadata(cacheDir string, model models.Model, path string, bytes uint64, installedAt time.Time) error {
	return resources.WriteModelManifestWithLayers(cacheDir, model, path, bytes, installedAt, probeModelLayerCount(path))
}

func probeModelLayerCount(modelPath string) int {
	modelPath = strings.TrimSpace(modelPath)
	if modelPath == "" {
		return 0
	}
	runnerBin := firstNonEmptyString(os.Getenv("CMESH_STAGE_RUNNER_BIN"), defaultStageRunnerBinaryPath())
	if runnerBin == "" {
		return 0
	}
	if info, err := os.Stat(runnerBin); err != nil || info.IsDir() {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, runnerBin,
		"--command", "prepare",
		"--model", modelPath,
		"--stage-start", "0",
		"--stage-end", "0",
		"--stage-index", "0",
	).CombinedOutput()
	if err != nil {
		return 0
	}
	var report struct {
		Kind   string `json:"kind"`
		Status string `json:"status"`
		NLayer int    `json:"n_layer"`
	}
	if err := json.Unmarshal(output, &report); err != nil {
		return 0
	}
	if report.Kind != "cmesh.llamacpp_stage_prepare" || report.Status != "metadata_ready" || report.NLayer <= 0 {
		return 0
	}
	return report.NLayer
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
	return executeModelGenerate(req, cacheDir, nil, string(models.JobGenerate), protocol.DistributedRPCExecutionPlan{})
}

func executeModelDistributedRPCGenerateJob(input string, cacheDir string, nodeID string) (string, error) {
	var req models.DistributedRPCGenerateInput
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		return "", fmt.Errorf("invalid distributed rpc model generate input: %w", err)
	}
	if err := protocol.ValidateDistributedRPCExecutionPlan(req.ExecutionPlan, req.ModelID, nodeID); err != nil {
		return "", fmt.Errorf("invalid distributed rpc execution plan: %w", err)
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
	return executeModelGenerate(generateReq, cacheDir, req.ExecutionPlan.RPCEndpoints, string(models.JobGenerateDistributedRPC), req.ExecutionPlan)
}

func executeModelGenerate(req models.GenerateInput, cacheDir string, rpcEndpoints []string, resultKind string, executionPlan protocol.DistributedRPCExecutionPlan) (string, error) {
	totalStarted := time.Now()
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
	statStarted := time.Now()
	modelStat, statErr := os.Stat(path)
	modelStatMS := time.Since(statStarted).Milliseconds()
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return "", fmt.Errorf("model %s is not installed on this worker", model.ID)
		}
		return "", statErr
	}
	modelBytes := modelStat.Size()
	runtimeStarted := time.Now()
	cli, runtimeStatus, err := ensureModelRuntime(model.Runtime, cacheDir)
	runtimePrepareMS := time.Since(runtimeStarted).Milliseconds()
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
	prompt := modelPrompt(model, req)
	args := []string{
		"-m", path,
		"-p", prompt,
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
	cmd.Env = llamaRuntimeEnv(os.Environ(), cli)
	cmd.Dir = filepath.Dir(cli)
	var stdout limitedBuffer
	var stderr limitedBuffer
	stdout.limit = 64 * 1024
	stderr.limit = 16 * 1024
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	started := time.Now()
	err = cmd.Run()
	llamaProcessMS := time.Since(started).Milliseconds()
	totalMS := time.Since(totalStarted).Milliseconds()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("%s timed out after %s", model.Runtime, timeout)
	}
	if err != nil {
		return "", fmt.Errorf("%s failed: %w: %s", model.Runtime, err, strings.TrimSpace(stderr.String()))
	}
	text := cleanLlamaOutput(stdout.String(), prompt)
	if text == "" {
		text = cleanLlamaOutput(stdout.String(), req.Prompt)
	}
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
	if resultKind == string(models.JobGenerateDistributedRPC) {
		result.ExecutionResult = protocol.DistributedRPCExecutionResult{
			Protocol:            protocol.DistributedRPCProtocol,
			ProtocolVersion:     protocol.DistributedRPCProtocolVersion,
			PlanSchemaVersion:   protocol.DistributedRPCPlanSchemaVersion,
			PlanID:              executionPlan.ID,
			Kind:                resultKind,
			ModelID:             model.ID,
			Output:              text,
			Runtime:             string(model.Runtime),
			RuntimeVersion:      runtimeStatus.Version,
			WorkerRuntime:       result.WorkerRuntime,
			CoordinatorNodeID:   executionPlan.CoordinatorNodeID,
			CoordinatorNodeName: executionPlan.CoordinatorNodeName,
			Backends:            append([]protocol.DistributedRPCBackend(nil), executionPlan.Backends...),
			RPCEndpoints:        cleanRPCEndpoints,
			RPCEndpointCount:    len(cleanRPCEndpoints),
			RPCEnabled:          len(cleanRPCEndpoints) > 0,
			ModelPath:           path,
			ModelBytes:          modelBytes,
			Timings: protocol.DistributedRPCTimings{
				ModelStatMS:      modelStatMS,
				RuntimePrepareMS: runtimePrepareMS,
				LlamaProcessMS:   llamaProcessMS,
				TotalMS:          totalMS,
			},
			DurationMS:  totalMS,
			StartedAt:   started.UTC().Format(time.RFC3339),
			CompletedAt: time.Now().UTC().Format(time.RFC3339),
		}
		if err := protocol.ValidateDistributedRPCExecutionResult(result.ExecutionResult, executionPlan); err != nil {
			return "", fmt.Errorf("invalid distributed rpc execution result: %w", err)
		}
	}
	body, err := json.Marshal(result)
	return string(body), err
}

func llamaRuntimeEnv(base []string, cli string) []string {
	env := append([]string(nil), base...)
	cliDir := filepath.Dir(strings.TrimSpace(cli))
	if cliDir == "." || cliDir == "" {
		return env
	}
	libDir := filepath.Clean(filepath.Join(cliDir, "..", "lib"))
	if info, err := os.Stat(libDir); err != nil || !info.IsDir() {
		return env
	}
	switch runtime.GOOS {
	case "darwin":
		env = prependLibraryPath(env, "DYLD_LIBRARY_PATH", libDir)
	case "linux":
		env = prependLibraryPath(env, "LD_LIBRARY_PATH", libDir)
	}
	return env
}

func prependLibraryPath(env []string, key string, value string) []string {
	prefix := key + "="
	for i, item := range env {
		if strings.HasPrefix(item, prefix) {
			current := strings.TrimPrefix(item, prefix)
			if current == "" {
				env[i] = prefix + value
			} else {
				env[i] = prefix + value + string(os.PathListSeparator) + current
			}
			return env
		}
	}
	return append(env, prefix+value)
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
	output, strippedPrompt := stripPromptEcho(output, prompt)
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	collected := make([]string, 0, len(lines))
	afterPrompt := strippedPrompt
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
		return removePromptEchoLines(sanitizeModelText(output), prompt)
	}
	text = sanitizeModelText(text)
	text = removePromptEchoLines(text, prompt)
	if len([]byte(text)) > 8192 {
		return string([]byte(text)[:8192])
	}
	return text
}

func stripPromptEcho(output string, prompt string) (string, bool) {
	text := strings.ReplaceAll(output, "\r\n", "\n")
	prompt = strings.TrimSpace(strings.ReplaceAll(prompt, "\r\n", "\n"))
	if prompt == "" {
		return text, false
	}
	if index := strings.LastIndex(text, prompt); index >= 0 {
		return text[index+len(prompt):], true
	}
	sanitizedPrompt := strings.TrimSpace(removeChatTemplateTokens(prompt))
	if sanitizedPrompt == "" {
		return text, false
	}
	sanitizedText := strings.TrimSpace(removeChatTemplateTokens(text))
	if strings.HasPrefix(sanitizedText, sanitizedPrompt) {
		return strings.TrimSpace(strings.TrimPrefix(sanitizedText, sanitizedPrompt)), true
	}
	return text, false
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
		if isPromptEchoNoise(lower) {
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

func removePromptEchoLines(text string, prompt string) string {
	prompt = strings.TrimSpace(removeChatTemplateTokens(prompt))
	if prompt == "" || strings.TrimSpace(text) == "" {
		return strings.TrimSpace(text)
	}
	echoLines := map[string]bool{}
	for _, line := range strings.Split(strings.ReplaceAll(prompt, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if isChatTemplateNoise(lower) || isRoleEcho(lower) || isPromptEchoNoise(lower) {
			continue
		}
		echoLines[line] = true
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			continue
		}
		if echoLines[trimmed] {
			continue
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
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

func isPromptEchoNoise(lower string) bool {
	lower = strings.TrimSpace(lower)
	return lower == "assist ... (truncated)" || lower == "assistant ... (truncated)" || lower == "assistant"
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
