package workercontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"
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
