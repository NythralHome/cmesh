package manager

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cmesh/cmesh/internal/cluster"
	"github.com/cmesh/cmesh/internal/membership"
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

const gb = 1024 * 1024 * 1024
