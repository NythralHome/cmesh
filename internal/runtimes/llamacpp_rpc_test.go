package runtimes

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLlamaCPPRPCRuntimeProbeRequiresRPCServer(t *testing.T) {
	probe := NewLlamaCPPRPCRuntime("", "").Probe(context.Background())
	if probe.Ready {
		t.Skipf("system rpc-server is already installed at %s", probe.ServerPath)
	}
	if len(probe.Blockers) == 0 || !strings.Contains(strings.Join(probe.Blockers, " "), "rpc-server") {
		t.Fatalf("expected rpc-server blocker, got %#v", probe)
	}
}

func TestLlamaCPPRPCRuntimeProbeFindsServerBesideCLI(t *testing.T) {
	dir := t.TempDir()
	cli := filepath.Join(dir, llamaBinaryName())
	server := filepath.Join(dir, llamaRPCServerBinaryName())
	if err := os.WriteFile(cli, []byte("cli"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(server, []byte("server"), 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(cli, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(server, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	probe := NewLlamaCPPRPCRuntime(cli, "127.0.0.1:50052").Probe(context.Background())
	if !probe.Ready {
		t.Fatalf("expected rpc probe ready, got %#v", probe)
	}
	if probe.ServerPath != server || probe.Endpoint != "127.0.0.1:50052" || probe.Protocol != "llama.cpp-rpc" {
		t.Fatalf("unexpected rpc probe: %#v", probe)
	}
}
