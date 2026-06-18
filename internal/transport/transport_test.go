package transport

import (
	"context"
	"errors"
	"io"
	"testing"

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
