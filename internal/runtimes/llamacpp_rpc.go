package runtimes

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	LlamaCPPRPCRuntimeName       = "llama.cpp-rpc"
	CapabilityLlamaCPPRPCBackend = "llama.cpp-rpc-backend"
	CapabilityLlamaCPPRPCClient  = "llama.cpp-rpc-client"
)

type RPCRuntimeProbe struct {
	Name       string   `json:"name"`
	Runtime    string   `json:"runtime"`
	Ready      bool     `json:"ready"`
	ServerPath string   `json:"server_path,omitempty"`
	Endpoint   string   `json:"endpoint,omitempty"`
	Protocol   string   `json:"protocol,omitempty"`
	Blockers   []string `json:"blockers,omitempty"`
}

type LlamaCPPRPCRuntime struct {
	BinaryPath string
	Endpoint   string
}

func NewLlamaCPPRPCRuntime(binaryPath string, endpoint string) LlamaCPPRPCRuntime {
	return LlamaCPPRPCRuntime{
		BinaryPath: strings.TrimSpace(binaryPath),
		Endpoint:   strings.TrimSpace(endpoint),
	}
}

func (r LlamaCPPRPCRuntime) Probe(ctx context.Context) RPCRuntimeProbe {
	probe := RPCRuntimeProbe{
		Name:     LlamaCPPRPCRuntimeName,
		Runtime:  LlamaCPPName,
		Protocol: "llama.cpp-rpc",
		Endpoint: r.Endpoint,
	}
	if err := ctx.Err(); err != nil {
		probe.Blockers = append(probe.Blockers, err.Error())
		return probe
	}
	serverPath, err := findLlamaCPPRPCServer(r.BinaryPath)
	if err != nil {
		probe.Blockers = append(probe.Blockers, err.Error())
		return probe
	}
	probe.ServerPath = serverPath
	probe.Ready = true
	return probe
}

func findLlamaCPPRPCServer(llamaCLIPath string) (string, error) {
	if server, err := exec.LookPath(llamaRPCServerBinaryName()); err == nil {
		return server, nil
	}
	if strings.TrimSpace(llamaCLIPath) != "" {
		dir := filepath.Dir(llamaCLIPath)
		if server, err := findBinary(dir, llamaRPCServerBinaryName()); err == nil {
			return server, nil
		}
	}
	for _, candidate := range llamaRPCServerCandidates() {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("llama.cpp rpc-server is not installed")
}

func llamaRPCServerCandidates() []string {
	candidates := []string{
		"/opt/homebrew/bin/rpc-server",
		"/usr/local/bin/rpc-server",
		"/opt/local/bin/rpc-server",
		"/usr/bin/rpc-server",
	}
	if runtime.GOOS == "windows" {
		candidates = append([]string{
			`C:\Program Files\llama.cpp\rpc-server.exe`,
			`C:\Program Files\CMesh\rpc-server.exe`,
		}, candidates...)
	}
	return candidates
}

func llamaRPCServerBinaryName() string {
	if runtime.GOOS == "windows" {
		return "rpc-server.exe"
	}
	return "rpc-server"
}
