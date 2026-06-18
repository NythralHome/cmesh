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
