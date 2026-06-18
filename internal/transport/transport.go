package transport

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/cmesh/cmesh/internal/cdip"
)

type Server interface {
	Start(ctx context.Context) error
}

type ActivationFrame struct {
	Header  cdip.ActivationChunk `json:"header"`
	Payload []byte               `json:"payload,omitempty"`
}

func (f ActivationFrame) Validate() error {
	if err := f.Header.Validate(); err != nil {
		return err
	}
	if uint64(len(f.Payload)) != f.Header.PayloadBytes {
		return fmt.Errorf("activation payload size mismatch: header=%d actual=%d", f.Header.PayloadBytes, len(f.Payload))
	}
	return nil
}

type StreamID struct {
	ParentJobID string
	StageJobID  string
}

type ActivationWriter interface {
	Send(ctx context.Context, frame ActivationFrame) error
	Close() error
}

type ActivationReader interface {
	Receive(ctx context.Context) (ActivationFrame, error)
}

type ActivationTransport interface {
	OpenWriter(ctx context.Context, stream StreamID, destinationNodeID string) (ActivationWriter, error)
	OpenReader(ctx context.Context, stream StreamID) (ActivationReader, error)
}

type MemoryActivationTransport struct {
	mu      sync.Mutex
	buffer  int
	streams map[StreamID]chan ActivationFrame
}

func NewMemoryActivationTransport(buffer int) *MemoryActivationTransport {
	if buffer < 1 {
		buffer = 1
	}
	return &MemoryActivationTransport{
		buffer:  buffer,
		streams: make(map[StreamID]chan ActivationFrame),
	}
}

func (t *MemoryActivationTransport) OpenWriter(ctx context.Context, stream StreamID, _ string) (ActivationWriter, error) {
	ch, err := t.stream(ctx, stream)
	if err != nil {
		return nil, err
	}
	return &memoryActivationWriter{ch: ch}, nil
}

func (t *MemoryActivationTransport) OpenReader(ctx context.Context, stream StreamID) (ActivationReader, error) {
	ch, err := t.stream(ctx, stream)
	if err != nil {
		return nil, err
	}
	return &memoryActivationReader{ch: ch}, nil
}

func (t *MemoryActivationTransport) stream(ctx context.Context, stream StreamID) (chan ActivationFrame, error) {
	if stream.ParentJobID == "" {
		return nil, fmt.Errorf("parent job id is required")
	}
	if stream.StageJobID == "" {
		return nil, fmt.Errorf("stage job id is required")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	ch, ok := t.streams[stream]
	if !ok {
		ch = make(chan ActivationFrame, t.buffer)
		t.streams[stream] = ch
	}
	return ch, nil
}

type memoryActivationWriter struct {
	once sync.Once
	ch   chan ActivationFrame
}

func (w *memoryActivationWriter) Send(ctx context.Context, frame ActivationFrame) error {
	if err := frame.Validate(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case w.ch <- frame:
		return nil
	}
}

func (w *memoryActivationWriter) Close() error {
	w.once.Do(func() {
		close(w.ch)
	})
	return nil
}

type memoryActivationReader struct {
	ch <-chan ActivationFrame
}

func (r *memoryActivationReader) Receive(ctx context.Context) (ActivationFrame, error) {
	select {
	case <-ctx.Done():
		return ActivationFrame{}, ctx.Err()
	case frame, ok := <-r.ch:
		if !ok {
			return ActivationFrame{}, io.EOF
		}
		return frame, nil
	}
}
