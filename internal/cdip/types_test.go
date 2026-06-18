package cdip

import (
	"strings"
	"testing"
)

func TestEnvelopeValidation(t *testing.T) {
	msg := NewEnvelope(MessageStagePrepare)
	if err := msg.Validate(); err != nil {
		t.Fatal(err)
	}
	msg.Version = "9.9"
	if err := msg.Validate(); err == nil || !strings.Contains(err.Error(), "unsupported cdip version") {
		t.Fatalf("expected version error, got %v", err)
	}
	msg = NewEnvelope("unknown")
	if err := msg.Validate(); err == nil || !strings.Contains(err.Error(), "unknown cdip message type") {
		t.Fatalf("expected message type error, got %v", err)
	}
}

func TestNodeHelloValidation(t *testing.T) {
	msg := NodeHello{
		Envelope: NewEnvelope(MessageNodeHello),
		NodeID:   "node-a",
		Resources: ResourceCapability{
			CPUCores:    8,
			MemoryBytes: 16,
		},
	}
	if err := msg.Validate(); err != nil {
		t.Fatal(err)
	}
	msg.NodeID = ""
	if err := msg.Validate(); err == nil || !strings.Contains(err.Error(), "node_id") {
		t.Fatalf("expected node_id error, got %v", err)
	}
}

func TestPlanProposalValidatesContiguousStageChain(t *testing.T) {
	msg := PlanProposal{
		Envelope: NewEnvelope(MessagePlanProposal),
		ModelID:  "qwen",
		Mode:     "pipeline_layers",
		Runtime:  "llama.cpp",
		Stages: []Stage{
			{Index: 0, NodeID: "node-a", LayerStart: 0, LayerEnd: 10},
			{Index: 1, NodeID: "node-b", LayerStart: 11, LayerEnd: 20},
		},
	}
	if err := msg.Validate(); err != nil {
		t.Fatal(err)
	}
	msg.Stages[1].LayerStart = 12
	if err := msg.Validate(); err == nil || !strings.Contains(err.Error(), "expected 11") {
		t.Fatalf("expected contiguous range error, got %v", err)
	}
}

func TestStagePrepareValidation(t *testing.T) {
	msg := StagePrepare{
		Envelope:    NewEnvelope(MessageStagePrepare),
		ParentJobID: "job-parent",
		StageJobID:  "job-stage",
		ModelID:     "qwen",
		Stage: Stage{
			Index:      0,
			NodeID:     "node-a",
			LayerStart: 0,
			LayerEnd:   12,
		},
	}
	if err := msg.Validate(); err != nil {
		t.Fatal(err)
	}
	msg.Stage.LayerEnd = -1
	if err := msg.Validate(); err == nil || !strings.Contains(err.Error(), "layer range") {
		t.Fatalf("expected layer range error, got %v", err)
	}
}

func TestStageLifecycleTransitions(t *testing.T) {
	valid := []struct {
		from StageState
		to   StageState
	}{
		{StagePlanned, StagePreparing},
		{StagePreparing, StageReady},
		{StageReady, StagePrefill},
		{StagePrefill, StageDecode},
		{StageDecode, StageCompleted},
		{StageReady, StageAborted},
		{StageDecode, StageFailed},
	}
	for _, tt := range valid {
		if !CanTransition(tt.from, tt.to) {
			t.Fatalf("expected transition %s -> %s to be valid", tt.from, tt.to)
		}
	}
	invalid := []struct {
		from StageState
		to   StageState
	}{
		{StagePlanned, StageDecode},
		{StageCompleted, StageDecode},
		{StageFailed, StageReady},
	}
	for _, tt := range invalid {
		if CanTransition(tt.from, tt.to) {
			t.Fatalf("expected transition %s -> %s to be invalid", tt.from, tt.to)
		}
	}
}

func TestActivationChunkValidation(t *testing.T) {
	msg := ActivationChunk{
		Envelope:     NewEnvelope(MessageActivationChunk),
		ParentJobID:  "job-parent",
		StageJobID:   "job-stage",
		Sequence:     1,
		ContentType:  "application/vnd.cmesh.activation+binary",
		Encoding:     "raw",
		Shape:        []int{1, 128, 4096},
		DType:        "f16",
		PayloadBytes: 1024,
		Checksum:     "sha256:test",
	}
	if err := msg.Validate(); err != nil {
		t.Fatal(err)
	}
	msg.PayloadBytes = 0
	if err := msg.Validate(); err == nil || !strings.Contains(err.Error(), "payload_bytes") {
		t.Fatalf("expected payload_bytes error, got %v", err)
	}
}

func TestProtocolErrorValidation(t *testing.T) {
	index := 2
	msg := ProtocolError{
		Envelope:   NewEnvelope(MessageError),
		Code:       ErrorActivationTimeout,
		Message:    "activation stream timed out",
		Retryable:  true,
		NodeID:     "node-b",
		StageIndex: &index,
	}
	if err := msg.Validate(); err != nil {
		t.Fatal(err)
	}
	msg.Code = ""
	if err := msg.Validate(); err == nil || !strings.Contains(err.Error(), "code") {
		t.Fatalf("expected code error, got %v", err)
	}
}
