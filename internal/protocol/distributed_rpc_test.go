package protocol

import "testing"

func TestDistributedRPCProtocolContract(t *testing.T) {
	if DistributedRPCProtocol != "cmesh.distributed-rpc" {
		t.Fatalf("unexpected distributed rpc protocol: %q", DistributedRPCProtocol)
	}
	if DistributedRPCProtocolVersion != 1 {
		t.Fatalf("unexpected distributed rpc protocol version: %d", DistributedRPCProtocolVersion)
	}
	if DistributedRPCPlanSchemaVersion != 1 {
		t.Fatalf("unexpected distributed rpc plan schema version: %d", DistributedRPCPlanSchemaVersion)
	}
}

func TestValidateDistributedRPCExecutionPlan(t *testing.T) {
	plan := DistributedRPCExecutionPlan{
		Protocol:          DistributedRPCProtocol,
		ProtocolVersion:   DistributedRPCProtocolVersion,
		PlanSchemaVersion: DistributedRPCPlanSchemaVersion,
		Mode:              "llama.cpp-rpc",
		ModelID:           "model-a",
		CoordinatorNodeID: "node-a",
		RPCEndpoints:      []string{"127.0.0.1:50052"},
		Backends: []DistributedRPCBackend{{
			NodeID:   "node-b",
			Endpoint: "127.0.0.1:50052",
		}},
	}
	if err := ValidateDistributedRPCExecutionPlan(plan, "model-a", "node-a"); err != nil {
		t.Fatalf("expected valid plan, got %v", err)
	}
}

func TestValidateDistributedRPCExecutionPlanRejectsUnsupportedVersion(t *testing.T) {
	plan := DistributedRPCExecutionPlan{
		Protocol:          DistributedRPCProtocol,
		ProtocolVersion:   DistributedRPCProtocolVersion + 1,
		PlanSchemaVersion: DistributedRPCPlanSchemaVersion,
		Mode:              "llama.cpp-rpc",
		ModelID:           "model-a",
		CoordinatorNodeID: "node-a",
		RPCEndpoints:      []string{"127.0.0.1:50052"},
		Backends: []DistributedRPCBackend{{
			NodeID:   "node-b",
			Endpoint: "127.0.0.1:50052",
		}},
	}
	if err := ValidateDistributedRPCExecutionPlan(plan, "model-a", "node-a"); err == nil {
		t.Fatal("expected unsupported protocol version error")
	}
}

func TestValidateDistributedRPCExecutionPlanRejectsBackendOutsideEndpoints(t *testing.T) {
	plan := DistributedRPCExecutionPlan{
		Protocol:          DistributedRPCProtocol,
		ProtocolVersion:   DistributedRPCProtocolVersion,
		PlanSchemaVersion: DistributedRPCPlanSchemaVersion,
		Mode:              "llama.cpp-rpc",
		ModelID:           "model-a",
		CoordinatorNodeID: "node-a",
		RPCEndpoints:      []string{"127.0.0.1:50052"},
		Backends: []DistributedRPCBackend{{
			NodeID:   "node-b",
			Endpoint: "127.0.0.1:50053",
		}},
	}
	if err := ValidateDistributedRPCExecutionPlan(plan, "model-a", "node-a"); err == nil {
		t.Fatal("expected backend endpoint validation error")
	}
}
