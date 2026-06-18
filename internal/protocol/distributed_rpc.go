package protocol

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
