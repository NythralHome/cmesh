package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

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

type HTTPActivationTransport struct {
	managerURL    string
	operatorToken string
	nodeID        string
	client        *http.Client
	pollTimeout   time.Duration
}

func NewHTTPActivationTransport(managerURL string, operatorToken string) *HTTPActivationTransport {
	return &HTTPActivationTransport{
		managerURL:    strings.TrimRight(managerURL, "/"),
		operatorToken: operatorToken,
		client:        http.DefaultClient,
		pollTimeout:   250 * time.Millisecond,
	}
}

func (t *HTTPActivationTransport) WithClient(client *http.Client) *HTTPActivationTransport {
	if client != nil {
		t.client = client
	}
	return t
}

func (t *HTTPActivationTransport) WithPollTimeout(timeout time.Duration) *HTTPActivationTransport {
	if timeout > 0 {
		t.pollTimeout = timeout
	}
	return t
}

func (t *HTTPActivationTransport) WithNodeID(nodeID string) *HTTPActivationTransport {
	t.nodeID = strings.TrimSpace(nodeID)
	return t
}

func (t *HTTPActivationTransport) OpenWriter(ctx context.Context, stream StreamID, _ string) (ActivationWriter, error) {
	if err := validateStreamID(stream); err != nil {
		return nil, err
	}
	if strings.TrimSpace(t.managerURL) == "" {
		return nil, fmt.Errorf("manager url is required")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return &httpActivationWriter{transport: t, stream: stream}, nil
}

func (t *HTTPActivationTransport) OpenReader(ctx context.Context, stream StreamID) (ActivationReader, error) {
	if err := validateStreamID(stream); err != nil {
		return nil, err
	}
	if strings.TrimSpace(t.managerURL) == "" {
		return nil, fmt.Errorf("manager url is required")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return &httpActivationReader{transport: t, stream: stream}, nil
}

func validateStreamID(stream StreamID) error {
	if stream.ParentJobID == "" {
		return fmt.Errorf("parent job id is required")
	}
	if stream.StageJobID == "" {
		return fmt.Errorf("stage job id is required")
	}
	return nil
}

func (t *HTTPActivationTransport) streamURL(stream StreamID) string {
	return t.managerURL + "/v1/cdip/activations/" + stream.ParentJobID + "/" + stream.StageJobID + "/frames"
}

func (t *HTTPActivationTransport) authorize(req *http.Request) {
	if strings.TrimSpace(t.operatorToken) != "" {
		req.Header.Set("X-CMesh-Operator-Token", t.operatorToken)
	}
	if strings.TrimSpace(t.nodeID) != "" {
		req.Header.Set("X-CMesh-Node-ID", t.nodeID)
	}
}

type httpActivationWriter struct {
	transport *HTTPActivationTransport
	stream    StreamID
}

func (w *httpActivationWriter) Send(ctx context.Context, frame ActivationFrame) error {
	if err := frame.Validate(); err != nil {
		return err
	}
	if frame.Header.ParentJobID != w.stream.ParentJobID || frame.Header.StageJobID != w.stream.StageJobID {
		return fmt.Errorf("activation frame stream does not match writer stream")
	}
	body, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.transport.streamURL(w.stream), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	w.transport.authorize(req)
	resp, err := w.transport.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("activation relay send failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

func (w *httpActivationWriter) Close() error {
	return nil
}

type httpActivationReader struct {
	transport *HTTPActivationTransport
	stream    StreamID
}

func (r *httpActivationReader) Receive(ctx context.Context) (ActivationFrame, error) {
	url := r.transport.streamURL(r.stream) + fmt.Sprintf("?timeout_ms=%d", maxInt64(1, r.transport.pollTimeout.Milliseconds()))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ActivationFrame{}, err
	}
	r.transport.authorize(req)
	resp, err := r.transport.client.Do(req)
	if err != nil {
		return ActivationFrame{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return ActivationFrame{}, io.EOF
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return ActivationFrame{}, fmt.Errorf("activation relay receive failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var frame ActivationFrame
	if err := json.NewDecoder(resp.Body).Decode(&frame); err != nil {
		return ActivationFrame{}, err
	}
	if err := frame.Validate(); err != nil {
		return ActivationFrame{}, err
	}
	if frame.Header.ParentJobID != r.stream.ParentJobID || frame.Header.StageJobID != r.stream.StageJobID {
		return ActivationFrame{}, fmt.Errorf("activation frame stream does not match reader stream")
	}
	return frame, nil
}

func maxInt64(a int64, b int64) int64 {
	if a > b {
		return a
	}
	return b
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
	if err := validateStreamID(stream); err != nil {
		return nil, err
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
