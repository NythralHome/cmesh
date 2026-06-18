package workercontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/cmesh/cmesh/internal/resources"
)

func TestConfigRoundTrip(t *testing.T) {
	server, baseURL, stop := startTestServer(t)
	defer stop()

	cfg := Config{
		ManagerURL: "https://cmesh.example.com/",
		JoinToken:  "join-token",
		NodeName:   "desktop-worker",
		CPU:        4,
		MemoryGB:   8,
		DiskGB:     50,
		GPUEnabled: true,
		VRAMGB:     6,
		Benchmark:  true,
	}
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPut, baseURL+"/v1/config", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %s", resp.Status)
	}

	var saved Config
	if err := json.NewDecoder(resp.Body).Decode(&saved); err != nil {
		t.Fatal(err)
	}
	if saved.ManagerURL != "https://cmesh.example.com" {
		t.Fatalf("expected normalized manager URL, got %q", saved.ManagerURL)
	}

	status := fetchStatus(t, baseURL)
	if status.Config.NodeName != "desktop-worker" {
		t.Fatalf("expected status config node name, got %q", status.Config.NodeName)
	}
	if status.ConfigPath != server.configPath {
		t.Fatalf("expected config path %q, got %q", server.configPath, status.ConfigPath)
	}
}

func TestConfigRejectsInvalidValues(t *testing.T) {
	_, baseURL, stop := startTestServer(t)
	defer stop()

	body := []byte(`{"manager_url":"https://cmesh.example.com","node_name":"bad","cpu":-1,"memory_gb":0,"disk_gb":0}`)
	req, err := http.NewRequest(http.MethodPut, baseURL+"/v1/config", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %s", resp.Status)
	}
}

func TestDisconnectStopsAndClearsJoinToken(t *testing.T) {
	server, baseURL, stop := startTestServer(t)
	defer stop()

	server.config = Config{
		ManagerURL: "https://cmesh.example.com",
		JoinToken:  "join-token",
		NodeName:   "desktop-worker",
		CPU:        2,
		MemoryGB:   4,
		DiskGB:     10,
	}

	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/disconnect", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %s", resp.Status)
	}

	var status Status
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Running {
		t.Fatalf("expected worker to be stopped, got %+v", status)
	}
	if status.Config.JoinToken != "" {
		t.Fatalf("expected join token to be cleared, got %q", status.Config.JoinToken)
	}

	saved, err := loadConfig(server.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if saved.JoinToken != "" {
		t.Fatalf("expected saved join token to be cleared, got %q", saved.JoinToken)
	}
}

func TestTokenProtectsControlRoutes(t *testing.T) {
	_, baseURL, stop := startTestServerWithToken(t, "secret")
	defer stop()

	resp, err := http.Get(baseURL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected health 200, got %s", resp.Status)
	}

	resp, err = http.Get(baseURL + "/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected status 401 without token, got %s", resp.Status)
	}

	req, err := http.NewRequest(http.MethodGet, baseURL+"/v1/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-CMesh-Control-Token", "secret")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 with token, got %s", resp.Status)
	}
}

func TestEnsureRuntimeRejectsWrongMethod(t *testing.T) {
	_, baseURL, stop := startTestServer(t)
	defer stop()

	for _, path := range []string{
		"/v1/runtime/llama.cpp/ensure",
		"/v1/runtime/llama.cpp/repair",
		"/v1/runtime/llama.cpp/rpc/start",
		"/v1/runtime/llama.cpp/rpc/stop",
		"/v1/runtime/llama.cpp/rpc/restart",
	} {
		req, err := http.NewRequest(http.MethodGet, baseURL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405 for %s, got %s", path, resp.Status)
		}
	}
}

func TestLlamaCPPRPCStatusRejectsWrongMethod(t *testing.T) {
	_, baseURL, stop := startTestServer(t)
	defer stop()

	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/runtime/llama.cpp/rpc/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %s", resp.Status)
	}
}

func TestStatusReportsInstalledModels(t *testing.T) {
	server, baseURL, stop := startTestServer(t)
	defer stop()

	cacheDir := t.TempDir()
	modelDir := filepath.Join(cacheDir, "models", "qwen2.5-0.5b-instruct-q4-k-m")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	modelPath := filepath.Join(modelDir, "qwen2.5-0.5b-instruct-q4_k_m.gguf")
	if err := os.WriteFile(modelPath, []byte("fake model"), 0o644); err != nil {
		t.Fatal(err)
	}
	server.config.WorkerCacheDir = cacheDir

	status := fetchStatus(t, baseURL)
	if len(status.Models) != 1 {
		t.Fatalf("expected one installed model, got %+v", status.Models)
	}
	if status.Models[0].ID != "qwen2.5-0.5b-instruct-q4-k-m" {
		t.Fatalf("expected qwen model, got %+v", status.Models[0])
	}
	if status.Models[0].Path != modelPath {
		t.Fatalf("expected model path %q, got %q", modelPath, status.Models[0].Path)
	}
}

func TestWorkerArgs(t *testing.T) {
	args := workerArgs(Config{
		ManagerURL:     "https://cmesh.example.com",
		JoinToken:      "token",
		NodeName:       "node",
		CPU:            3,
		MemoryGB:       4,
		DiskGB:         5,
		GPUEnabled:     false,
		VRAMGB:         0,
		Benchmark:      true,
		WorkerCacheDir: "/tmp/cmesh-cache",
	})
	got := stringsJoin(args)
	for _, want := range []string{
		"worker run",
		"--manager https://cmesh.example.com",
		"--token token",
		"--cpu 3",
		"--memory-gb 4",
		"--disk-gb 5",
		"--gpu=false",
		"--benchmark",
	} {
		if !bytes.Contains([]byte(got), []byte(want)) {
			t.Fatalf("expected args to contain %q, got %q", want, got)
		}
	}
}

func TestLlamaCPPRPCArgs(t *testing.T) {
	args := llamaCPPRPCArgs(Config{
		RPCHost:  "127.0.0.1",
		RPCPort:  50052,
		RPCCache: true,
	})
	got := stringsJoin(args)
	for _, want := range []string{
		"--host 127.0.0.1",
		"--port 50052",
		"-c",
	} {
		if !bytes.Contains([]byte(got), []byte(want)) {
			t.Fatalf("expected args to contain %q, got %q", want, got)
		}
	}
}

func TestStopWorkerStopsProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based process signal test is unix-only")
	}

	dir := t.TempDir()
	binary := filepath.Join(dir, "fake-worker")
	script := []byte("#!/bin/sh\ntrap 'exit 0' INT TERM\nwhile :; do sleep 1; done\n")
	if err := os.WriteFile(binary, script, 0o755); err != nil {
		t.Fatal(err)
	}

	server, err := NewServerWithToken("127.0.0.1:0", filepath.Join(dir, "worker-control.json"), "")
	if err != nil {
		t.Fatal(err)
	}
	server.config = Config{
		ManagerURL:   "https://cmesh.example.com",
		JoinToken:    "join-token",
		NodeName:     "test-worker",
		CPU:          1,
		MemoryGB:     1,
		DiskGB:       1,
		WorkerBinary: binary,
	}

	if err := server.startWorker(); err != nil {
		t.Fatal(err)
	}
	if status := server.status(); !status.Running || status.PID == 0 {
		t.Fatalf("expected worker to be running, got %+v", status)
	}
	if err := server.stopWorker(); err != nil {
		t.Fatal(err)
	}
	if status := server.status(); status.Running {
		t.Fatalf("expected worker to stop, got %+v", status)
	}
}

func TestStartStopLlamaCPPRPCServer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based process signal test is unix-only")
	}

	dir := t.TempDir()
	cli := filepath.Join(dir, "llama-cli")
	rpcServer := filepath.Join(dir, "rpc-server")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := []byte("#!/bin/sh\ntrap 'exit 0' INT TERM\nprintf 'rpc ready %s\\n' \"$*\"\nwhile :; do sleep 1; done\n")
	if err := os.WriteFile(rpcServer, script, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	server, err := NewServerWithToken("127.0.0.1:0", filepath.Join(dir, "worker-control.json"), "")
	if err != nil {
		t.Fatal(err)
	}
	server.config = Config{
		ManagerURL:     "https://cmesh.example.com",
		NodeName:       "test-worker",
		CPU:            1,
		MemoryGB:       1,
		DiskGB:         1,
		RPCHost:        "127.0.0.1",
		RPCPort:        50123,
		RPCCache:       true,
		WorkerCacheDir: filepath.Join(dir, "cache"),
	}

	if err := server.startLlamaCPPRPC(); err != nil {
		t.Fatal(err)
	}
	status := server.status()
	if !status.RPC.Running || status.RPC.PID == 0 {
		t.Fatalf("expected rpc server running, got %+v", status.RPC)
	}
	if status.RPC.Endpoint != "127.0.0.1:50123" {
		t.Fatalf("expected rpc endpoint, got %+v", status.RPC)
	}
	state, ok := resources.ReadLlamaCPPRPCState(server.config.WorkerCacheDir)
	if !ok || state.Endpoint != "127.0.0.1:50123" || state.PID == 0 {
		t.Fatalf("expected rpc state file, got ok=%v state=%+v", ok, state)
	}
	if err := server.stopLlamaCPPRPC(); err != nil {
		t.Fatal(err)
	}
	if status := server.status(); status.RPC.Running {
		t.Fatalf("expected rpc server stopped, got %+v", status.RPC)
	}
	if _, ok := resources.ReadLlamaCPPRPCState(server.config.WorkerCacheDir); ok {
		t.Fatal("expected rpc state file to be cleared")
	}
}

func startTestServer(t *testing.T) (*Server, string, func()) {
	return startTestServerWithToken(t, "")
}

func startTestServerWithToken(t *testing.T, token string) (*Server, string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	server, err := NewServerWithToken(addr, t.TempDir()+"/worker-control.json", token)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Start(ctx)
	}()
	baseURL := "http://" + addr
	waitForHealth(t, baseURL)
	return server, baseURL, func() {
		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("server did not stop")
		}
	}
}

func waitForHealth(t *testing.T, baseURL string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("control server did not become healthy")
}

func fetchStatus(t *testing.T, baseURL string) Status {
	t.Helper()
	resp, err := http.Get(baseURL + "/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %s", resp.Status)
	}
	var status Status
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func stringsJoin(values []string) string {
	var buf bytes.Buffer
	for i, value := range values {
		if i > 0 {
			buf.WriteByte(' ')
		}
		buf.WriteString(value)
	}
	return buf.String()
}
