package protocol

import (
	"fmt"
	"strings"
)

const (
	DistributedRPCProtocol          = "cmesh.distributed-rpc"
	DistributedRPCProtocolVersion   = 1
	DistributedRPCPlanSchemaVersion = 1
)

type DistributedRPCExecutionPlan struct {
	ID                  string                  `json:"id,omitempty"`
	Protocol            string                  `json:"protocol"`
	ProtocolVersion     int                     `json:"protocol_version"`
	PlanSchemaVersion   int                     `json:"plan_schema_version"`
	Mode                string                  `json:"mode"`
	ModelID             string                  `json:"model_id"`
	CoordinatorNodeID   string                  `json:"coordinator_node_id,omitempty"`
	CoordinatorNodeName string                  `json:"coordinator_node_name,omitempty"`
	RPCEndpoints        []string                `json:"rpc_endpoints"`
	Backends            []DistributedRPCBackend `json:"backends,omitempty"`
	HealthChecked       bool                    `json:"health_checked"`
	PlannedAt           string                  `json:"planned_at,omitempty"`
}

type DistributedRPCBackend struct {
	NodeID       string `json:"node_id"`
	NodeName     string `json:"node_name"`
	Runtime      string `json:"runtime"`
	Endpoint     string `json:"endpoint"`
	HealthStatus string `json:"health_status,omitempty"`
	LatencyMS    int64  `json:"latency_ms,omitempty"`
	Error        string `json:"error,omitempty"`
}

type DistributedRPCExecutionResult struct {
	Protocol          string   `json:"protocol"`
	ProtocolVersion   int      `json:"protocol_version"`
	PlanSchemaVersion int      `json:"plan_schema_version"`
	PlanID            string   `json:"plan_id,omitempty"`
	Kind              string   `json:"kind"`
	ModelID           string   `json:"model_id"`
	Output            string   `json:"output"`
	Runtime           string   `json:"runtime"`
	RuntimeVersion    string   `json:"runtime_version,omitempty"`
	WorkerRuntime     string   `json:"worker_runtime"`
	RPCEndpoints      []string `json:"rpc_endpoints"`
	RPCEndpointCount  int      `json:"rpc_endpoint_count"`
	DurationMS        int64    `json:"duration_ms"`
	CompletedAt       string   `json:"completed_at,omitempty"`
}

func ValidateDistributedRPCExecutionPlan(plan DistributedRPCExecutionPlan, modelID string, coordinatorNodeID string) error {
	if strings.TrimSpace(plan.Protocol) == "" {
		return fmt.Errorf("execution_plan.protocol is required")
	}
	if plan.Protocol != DistributedRPCProtocol {
		return fmt.Errorf("unsupported distributed rpc protocol %q", plan.Protocol)
	}
	if plan.ProtocolVersion != DistributedRPCProtocolVersion {
		return fmt.Errorf("unsupported distributed rpc protocol_version %d", plan.ProtocolVersion)
	}
	if plan.PlanSchemaVersion != DistributedRPCPlanSchemaVersion {
		return fmt.Errorf("unsupported distributed rpc plan_schema_version %d", plan.PlanSchemaVersion)
	}
	if strings.TrimSpace(plan.Mode) == "" {
		return fmt.Errorf("execution_plan.mode is required")
	}
	if strings.TrimSpace(plan.ModelID) == "" {
		return fmt.Errorf("execution_plan.model_id is required")
	}
	if modelID != "" && plan.ModelID != modelID {
		return fmt.Errorf("execution_plan.model_id %q does not match request model_id %q", plan.ModelID, modelID)
	}
	if strings.TrimSpace(plan.CoordinatorNodeID) == "" {
		return fmt.Errorf("execution_plan.coordinator_node_id is required")
	}
	if coordinatorNodeID != "" && plan.CoordinatorNodeID != coordinatorNodeID {
		return fmt.Errorf("execution_plan.coordinator_node_id %q does not match assigned worker %q", plan.CoordinatorNodeID, coordinatorNodeID)
	}
	if len(cleanRPCEndpoints(plan.RPCEndpoints)) == 0 {
		return fmt.Errorf("execution_plan.rpc_endpoints is required")
	}
	if len(plan.Backends) == 0 {
		return fmt.Errorf("execution_plan.backends is required")
	}
	endpointSet := map[string]bool{}
	for _, endpoint := range cleanRPCEndpoints(plan.RPCEndpoints) {
		endpointSet[endpoint] = true
	}
	for _, backend := range plan.Backends {
		endpoint := strings.TrimSpace(backend.Endpoint)
		if endpoint == "" {
			return fmt.Errorf("execution_plan.backends contains empty endpoint")
		}
		if !endpointSet[endpoint] {
			return fmt.Errorf("execution_plan backend endpoint %q is not listed in rpc_endpoints", endpoint)
		}
		if strings.TrimSpace(backend.NodeID) == "" {
			return fmt.Errorf("execution_plan backend %q has empty node_id", endpoint)
		}
	}
	return nil
}

func ValidateDistributedRPCExecutionResult(result DistributedRPCExecutionResult, plan DistributedRPCExecutionPlan) error {
	if result.Protocol != DistributedRPCProtocol {
		return fmt.Errorf("unsupported distributed rpc result protocol %q", result.Protocol)
	}
	if result.ProtocolVersion != DistributedRPCProtocolVersion {
		return fmt.Errorf("unsupported distributed rpc result protocol_version %d", result.ProtocolVersion)
	}
	if result.PlanSchemaVersion != DistributedRPCPlanSchemaVersion {
		return fmt.Errorf("unsupported distributed rpc result plan_schema_version %d", result.PlanSchemaVersion)
	}
	if strings.TrimSpace(result.Kind) == "" {
		return fmt.Errorf("execution_result.kind is required")
	}
	if strings.TrimSpace(result.ModelID) == "" {
		return fmt.Errorf("execution_result.model_id is required")
	}
	if strings.TrimSpace(result.Output) == "" {
		return fmt.Errorf("execution_result.output is required")
	}
	if len(cleanRPCEndpoints(result.RPCEndpoints)) == 0 {
		return fmt.Errorf("execution_result.rpc_endpoints is required")
	}
	if plan.ID != "" && result.PlanID != "" && result.PlanID != plan.ID {
		return fmt.Errorf("execution_result.plan_id %q does not match plan id %q", result.PlanID, plan.ID)
	}
	if plan.ModelID != "" && result.ModelID != plan.ModelID {
		return fmt.Errorf("execution_result.model_id %q does not match plan model_id %q", result.ModelID, plan.ModelID)
	}
	planEndpoints := map[string]bool{}
	for _, endpoint := range cleanRPCEndpoints(plan.RPCEndpoints) {
		planEndpoints[endpoint] = true
	}
	for _, endpoint := range cleanRPCEndpoints(result.RPCEndpoints) {
		if !planEndpoints[endpoint] {
			return fmt.Errorf("execution_result endpoint %q is not listed in execution plan", endpoint)
		}
	}
	return nil
}

func cleanRPCEndpoints(endpoints []string) []string {
	out := make([]string, 0, len(endpoints))
	seen := map[string]bool{}
	for _, endpoint := range endpoints {
		endpoint = strings.TrimSpace(endpoint)
		if endpoint == "" || seen[endpoint] {
			continue
		}
		seen[endpoint] = true
		out = append(out, endpoint)
	}
	return out
}
