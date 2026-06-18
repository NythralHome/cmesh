package cdip

import (
	"errors"
	"fmt"
	"strings"
)

const (
	Protocol = "cdip"
	Version  = "0.1"
)

type MessageType string

const (
	MessageNodeHello       MessageType = "node.hello"
	MessagePlanProposal    MessageType = "plan.proposal"
	MessageStagePrepare    MessageType = "stage.prepare"
	MessageStageReady      MessageType = "stage.ready"
	MessageStagePrefill    MessageType = "stage.prefill"
	MessageStageDecode     MessageType = "stage.decode"
	MessageStageComplete   MessageType = "stage.complete"
	MessageStageAbort      MessageType = "stage.abort"
	MessageActivationChunk MessageType = "activation.chunk"
	MessageError           MessageType = "error"
)

type Envelope struct {
	Protocol string      `json:"protocol"`
	Version  string      `json:"version"`
	Type     MessageType `json:"type"`
}

func NewEnvelope(messageType MessageType) Envelope {
	return Envelope{
		Protocol: Protocol,
		Version:  Version,
		Type:     messageType,
	}
}

func (e Envelope) Validate() error {
	if e.Protocol != Protocol {
		return fmt.Errorf("unsupported protocol %q", e.Protocol)
	}
	if e.Version != Version {
		return fmt.Errorf("unsupported cdip version %q", e.Version)
	}
	if !KnownMessageType(e.Type) {
		return fmt.Errorf("unknown cdip message type %q", e.Type)
	}
	return nil
}

func KnownMessageType(messageType MessageType) bool {
	switch messageType {
	case MessageNodeHello, MessagePlanProposal, MessageStagePrepare, MessageStageReady, MessageStagePrefill, MessageStageDecode, MessageStageComplete, MessageStageAbort, MessageActivationChunk, MessageError:
		return true
	default:
		return false
	}
}

type RuntimeCapability struct {
	Name     string   `json:"name"`
	Ready    bool     `json:"ready"`
	Features []string `json:"features,omitempty"`
}

type ResourceCapability struct {
	CPUCores    int    `json:"cpu_cores,omitempty"`
	MemoryBytes uint64 `json:"memory_bytes,omitempty"`
	DiskBytes   uint64 `json:"disk_bytes,omitempty"`
	VRAMBytes   uint64 `json:"vram_bytes,omitempty"`
}

type NetworkCapability struct {
	ListenEndpoint string `json:"listen_endpoint,omitempty"`
	EstimatedRTTMS int    `json:"estimated_rtt_ms,omitempty"`
}

type NodeHello struct {
	Envelope
	NodeID    string              `json:"node_id"`
	Runtimes  []RuntimeCapability `json:"runtimes,omitempty"`
	Resources ResourceCapability  `json:"resources"`
	Network   NetworkCapability   `json:"network,omitempty"`
}

func (m NodeHello) Validate() error {
	if err := m.Envelope.Validate(); err != nil {
		return err
	}
	if m.Type != MessageNodeHello {
		return fmt.Errorf("expected %s message, got %s", MessageNodeHello, m.Type)
	}
	if strings.TrimSpace(m.NodeID) == "" {
		return errors.New("node_id is required")
	}
	if m.Resources.CPUCores <= 0 && m.Resources.MemoryBytes == 0 && m.Resources.DiskBytes == 0 && m.Resources.VRAMBytes == 0 {
		return errors.New("at least one resource capability is required")
	}
	return nil
}

type Stage struct {
	Index      int    `json:"index"`
	NodeID     string `json:"node_id"`
	NodeName   string `json:"node_name,omitempty"`
	LayerStart int    `json:"layer_start"`
	LayerEnd   int    `json:"layer_end"`
}

func (s Stage) Layers() int {
	if s.LayerEnd < s.LayerStart {
		return 0
	}
	return s.LayerEnd - s.LayerStart + 1
}

type PlanProposal struct {
	Envelope
	ModelID       string   `json:"model_id"`
	Mode          string   `json:"mode"`
	Runtime       string   `json:"runtime"`
	ExecutableNow bool     `json:"executable_now"`
	Stages        []Stage  `json:"stages,omitempty"`
	Blockers      []string `json:"blockers,omitempty"`
}

func (m PlanProposal) Validate() error {
	if err := m.Envelope.Validate(); err != nil {
		return err
	}
	if m.Type != MessagePlanProposal {
		return fmt.Errorf("expected %s message, got %s", MessagePlanProposal, m.Type)
	}
	if strings.TrimSpace(m.ModelID) == "" {
		return errors.New("model_id is required")
	}
	if strings.TrimSpace(m.Mode) == "" {
		return errors.New("mode is required")
	}
	return ValidateStageChain(m.Stages)
}

type StagePrepare struct {
	Envelope
	ParentJobID      string `json:"parent_job_id"`
	StageJobID       string `json:"stage_job_id"`
	ModelID          string `json:"model_id"`
	Stage            Stage  `json:"stage"`
	UpstreamNodeID   string `json:"upstream_node_id,omitempty"`
	DownstreamNodeID string `json:"downstream_node_id,omitempty"`
}

func (m StagePrepare) Validate() error {
	if err := m.Envelope.Validate(); err != nil {
		return err
	}
	if m.Type != MessageStagePrepare {
		return fmt.Errorf("expected %s message, got %s", MessageStagePrepare, m.Type)
	}
	if strings.TrimSpace(m.ParentJobID) == "" {
		return errors.New("parent_job_id is required")
	}
	if strings.TrimSpace(m.StageJobID) == "" {
		return errors.New("stage_job_id is required")
	}
	if strings.TrimSpace(m.ModelID) == "" {
		return errors.New("model_id is required")
	}
	if strings.TrimSpace(m.Stage.NodeID) == "" {
		return errors.New("stage node_id is required")
	}
	if m.Stage.Layers() <= 0 {
		return errors.New("stage layer range is invalid")
	}
	return nil
}

type StageState string

const (
	StagePlanned   StageState = "planned"
	StagePreparing StageState = "preparing"
	StageReady     StageState = "ready"
	StagePrefill   StageState = "prefill"
	StageDecode    StageState = "decode"
	StageCompleted StageState = "completed"
	StageFailed    StageState = "failed"
	StageAborted   StageState = "aborted"
)

func CanTransition(from StageState, to StageState) bool {
	if from == to {
		return true
	}
	switch from {
	case StagePlanned:
		return to == StagePreparing || to == StageAborted || to == StageFailed
	case StagePreparing:
		return to == StageReady || to == StageAborted || to == StageFailed
	case StageReady:
		return to == StagePrefill || to == StageAborted || to == StageFailed
	case StagePrefill:
		return to == StageDecode || to == StageAborted || to == StageFailed
	case StageDecode:
		return to == StageCompleted || to == StageAborted || to == StageFailed
	case StageCompleted, StageFailed, StageAborted:
		return false
	default:
		return false
	}
}

type ActivationChunk struct {
	Envelope
	ParentJobID  string `json:"parent_job_id"`
	StageJobID   string `json:"stage_job_id"`
	Sequence     uint64 `json:"sequence"`
	ContentType  string `json:"content_type"`
	Encoding     string `json:"encoding,omitempty"`
	Shape        []int  `json:"shape,omitempty"`
	DType        string `json:"dtype,omitempty"`
	PayloadBytes uint64 `json:"payload_bytes"`
	Checksum     string `json:"checksum,omitempty"`
}

func (m ActivationChunk) Validate() error {
	if err := m.Envelope.Validate(); err != nil {
		return err
	}
	if m.Type != MessageActivationChunk {
		return fmt.Errorf("expected %s message, got %s", MessageActivationChunk, m.Type)
	}
	if strings.TrimSpace(m.ParentJobID) == "" {
		return errors.New("parent_job_id is required")
	}
	if strings.TrimSpace(m.StageJobID) == "" {
		return errors.New("stage_job_id is required")
	}
	if strings.TrimSpace(m.ContentType) == "" {
		return errors.New("content_type is required")
	}
	if m.PayloadBytes == 0 {
		return errors.New("payload_bytes is required")
	}
	return nil
}

type ErrorCode string

const (
	ErrorRuntimeMissing             ErrorCode = "runtime_missing"
	ErrorModelShardMissing          ErrorCode = "model_shard_missing"
	ErrorActivationTimeout          ErrorCode = "activation_timeout"
	ErrorActivationChecksumFailed   ErrorCode = "activation_checksum_failed"
	ErrorWorkerOffline              ErrorCode = "worker_offline"
	ErrorProtocolVersionUnsupported ErrorCode = "protocol_version_unsupported"
	ErrorStageOrderInvalid          ErrorCode = "stage_order_invalid"
)

type ProtocolError struct {
	Envelope
	Code       ErrorCode `json:"code"`
	Message    string    `json:"message"`
	Retryable  bool      `json:"retryable"`
	NodeID     string    `json:"node_id,omitempty"`
	StageIndex *int      `json:"stage_index,omitempty"`
}

func (m ProtocolError) Validate() error {
	if err := m.Envelope.Validate(); err != nil {
		return err
	}
	if m.Type != MessageError {
		return fmt.Errorf("expected %s message, got %s", MessageError, m.Type)
	}
	if strings.TrimSpace(string(m.Code)) == "" {
		return errors.New("code is required")
	}
	if strings.TrimSpace(m.Message) == "" {
		return errors.New("message is required")
	}
	return nil
}

func ValidateStageChain(stages []Stage) error {
	if len(stages) == 0 {
		return nil
	}
	expectedStart := stages[0].LayerStart
	for i, stage := range stages {
		if stage.Index != i {
			return fmt.Errorf("stage index mismatch: got %d at position %d", stage.Index, i)
		}
		if strings.TrimSpace(stage.NodeID) == "" {
			return fmt.Errorf("stage %d node_id is required", i)
		}
		if stage.LayerStart != expectedStart {
			return fmt.Errorf("stage %d starts at %d, expected %d", i, stage.LayerStart, expectedStart)
		}
		if stage.LayerEnd < stage.LayerStart {
			return fmt.Errorf("stage %d layer range is invalid", i)
		}
		expectedStart = stage.LayerEnd + 1
	}
	return nil
}
