package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cmesh/cmesh/internal/cdip"
)

func TestActivationFrameValidation(t *testing.T) {
	frame := ActivationFrame{
		Header: cdip.ActivationChunk{
			Envelope:     cdip.NewEnvelope(cdip.MessageActivationChunk),
			ParentJobID:  "job-parent",
			StageJobID:   "job-stage",
			Sequence:     1,
			ContentType:  "application/vnd.cmesh.activation+binary",
			Encoding:     "raw",
			Shape:        []int{1, 16, 128},
			DType:        "f16",
			PayloadBytes: 4,
		},
		Payload: []byte{1, 2, 3, 4},
	}
	if err := frame.Validate(); err != nil {
		t.Fatal(err)
	}
	frame.Payload = []byte{1, 2}
	if err := frame.Validate(); err == nil {
		t.Fatal("expected payload size mismatch")
	}
}

func TestMemoryActivationTransportSendsFrames(t *testing.T) {
	ctx := context.Background()
	stream := StreamID{ParentJobID: "job-parent", StageJobID: "job-stage-0"}
	bus := NewMemoryActivationTransport(1)
	reader, err := bus.OpenReader(ctx, stream)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := bus.OpenWriter(ctx, stream, "node-b")
	if err != nil {
		t.Fatal(err)
	}
	frame := ActivationFrame{
		Header: cdip.ActivationChunk{
			Envelope:     cdip.NewEnvelope(cdip.MessageActivationChunk),
			ParentJobID:  stream.ParentJobID,
			StageJobID:   stream.StageJobID,
			Sequence:     7,
			ContentType:  "application/vnd.cmesh.activation+binary",
			Encoding:     "raw",
			Shape:        []int{1, 1, 4},
			DType:        "f16",
			PayloadBytes: 4,
		},
		Payload: []byte{4, 3, 2, 1},
	}
	if err := writer.Send(ctx, frame); err != nil {
		t.Fatal(err)
	}
	got, err := reader.Receive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Header.Sequence != frame.Header.Sequence || string(got.Payload) != string(frame.Payload) {
		t.Fatalf("unexpected activation frame: %#v", got)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Receive(ctx); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after close, got %v", err)
	}
}

func TestMemoryActivationTransportRejectsInvalidFrame(t *testing.T) {
	ctx := context.Background()
	bus := NewMemoryActivationTransport(1)
	writer, err := bus.OpenWriter(ctx, StreamID{ParentJobID: "job-parent", StageJobID: "job-stage"}, "node-b")
	if err != nil {
		t.Fatal(err)
	}
	err = writer.Send(ctx, ActivationFrame{
		Header: cdip.ActivationChunk{
			Envelope:     cdip.NewEnvelope(cdip.MessageActivationChunk),
			ParentJobID:  "job-parent",
			StageJobID:   "job-stage",
			Sequence:     1,
			ContentType:  "application/vnd.cmesh.activation+binary",
			PayloadBytes: 4,
		},
		Payload: []byte{1},
	})
	if err == nil {
		t.Fatal("expected invalid frame error")
	}
}

func TestHTTPActivationTransportSendsAndReceivesFrames(t *testing.T) {
	stream := StreamID{ParentJobID: "job-parent", StageJobID: "job-stage"}
	sent := make(chan ActivationFrame, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-CMesh-Operator-Token") != "operator-token" {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("X-CMesh-Node-ID") != "node-a" {
			http.Error(w, "missing node id", http.StatusForbidden)
			return
		}
		if r.URL.Path != "/v1/cdip/activations/"+stream.ParentJobID+"/"+stream.StageJobID+"/frames" {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodPost:
			var frame ActivationFrame
			if err := json.NewDecoder(r.Body).Decode(&frame); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			sent <- frame
			w.WriteHeader(http.StatusAccepted)
		case http.MethodGet:
			select {
			case frame := <-sent:
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(frame)
			default:
				w.WriteHeader(http.StatusNoContent)
			}
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	client := NewHTTPActivationTransport(server.URL, "operator-token").WithClient(server.Client()).WithNodeID("node-a").WithPollTimeout(time.Millisecond)
	writer, err := client.OpenWriter(context.Background(), stream, "node-b")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := client.OpenReader(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
	frame := ActivationFrame{
		Header: cdip.ActivationChunk{
			Envelope:     cdip.NewEnvelope(cdip.MessageActivationChunk),
			ParentJobID:  stream.ParentJobID,
			StageJobID:   stream.StageJobID,
			Sequence:     11,
			ContentType:  "application/vnd.cmesh.activation+binary",
			Encoding:     "raw",
			Shape:        []int{1, 1, 4},
			DType:        "f16",
			PayloadBytes: 4,
		},
		Payload: []byte{1, 3, 5, 7},
	}
	if err := writer.Send(context.Background(), frame); err != nil {
		t.Fatal(err)
	}
	got, err := reader.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Header.Sequence != frame.Header.Sequence || string(got.Payload) != string(frame.Payload) {
		t.Fatalf("unexpected relayed frame: %#v", got)
	}
	if _, err := reader.Receive(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF when relay has no frame, got %v", err)
	}
}

func TestHTTPActivationTransportRejectsMismatchedFrame(t *testing.T) {
	stream := StreamID{ParentJobID: "job-parent", StageJobID: "job-stage"}
	client := NewHTTPActivationTransport("http://127.0.0.1", "")
	writer, err := client.OpenWriter(context.Background(), stream, "")
	if err != nil {
		t.Fatal(err)
	}
	err = writer.Send(context.Background(), ActivationFrame{
		Header: cdip.ActivationChunk{
			Envelope:     cdip.NewEnvelope(cdip.MessageActivationChunk),
			ParentJobID:  "other-parent",
			StageJobID:   stream.StageJobID,
			Sequence:     1,
			ContentType:  "application/vnd.cmesh.activation+binary",
			PayloadBytes: 1,
		},
		Payload: []byte{1},
	})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected mismatched stream error, got %v", err)
	}
}
