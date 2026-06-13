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
	"syscall"

	"github.com/cmesh/cmesh/internal/cluster"
	"github.com/cmesh/cmesh/internal/manager"
	"github.com/cmesh/cmesh/internal/membership"
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
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		state := manager.NewState()
		server := manager.NewServer(*addr, state)
		fmt.Println("starting CMesh manager in single-node bootstrap mode")
		fmt.Printf("manager API: http://127.0.0.1%s\n", *addr)
		fmt.Printf("dashboard:   http://127.0.0.1%s\n", *addr)
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

func runWorker(args []string) error {
	if len(args) == 0 {
		printWorkerUsage()
		return nil
	}

	switch args[0] {
	case "join":
		fs := flag.NewFlagSet("worker join", flag.ContinueOnError)
		managerURL := fs.String("manager", "http://127.0.0.1:8080", "manager API URL")
		name := fs.String("name", defaultNodeName(), "worker display name")
		token := fs.String("token", "", "cluster join token")
		cpuAllowed := fs.Int("cpu", runtime.NumCPU(), "allowed CPU cores")
		memoryGB := fs.Uint64("memory-gb", 2, "allowed memory in GB")
		diskGB := fs.Uint64("disk-gb", 10, "allowed disk in GB")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}

		resp, err := joinWorker(*managerURL, membership.JoinRequest{
			NodeName:  *name,
			Role:      cluster.NodeRoleWorker,
			JoinToken: *token,
			Resources: cluster.ResourceSnapshot{
				CPU: cluster.CPUResources{
					CoresTotal:   runtime.NumCPU(),
					CoresAllowed: *cpuAllowed,
				},
				Memory: cluster.MemoryResources{
					AllowedBytes: gbToBytes(*memoryGB),
				},
				Storage: cluster.StorageResources{
					AllowedBytes: gbToBytes(*diskGB),
				},
			},
		})
		if err != nil {
			return err
		}
		fmt.Printf("joined cluster as %s\n", resp.NodeID)
		fmt.Printf("manager peers: %v\n", resp.ManagerPeers)
	case "benchmark":
		fmt.Println("running local worker benchmarks")
		fmt.Println("benchmark execution will be implemented in the resources package")
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
  cmesh worker benchmark    Run worker benchmarks
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
  cmesh worker benchmark    Run CPU, memory, disk, network, and AI benchmarks`)
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

func defaultNodeName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "worker"
	}
	return host
}

func gbToBytes(gb uint64) uint64 {
	return gb * 1024 * 1024 * 1024
}
