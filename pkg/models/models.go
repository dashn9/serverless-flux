package models

import "time"

type Agent struct {
	ID            string
	Address       string
	ActiveCount   int32
	LastHeartbeat time.Time
	Status        AgentStatus

	// ProviderID is the cloud-provider instance identifier (e.g. EC2 instance ID).
	// Populated for dynamically provisioned nodes; empty for statically configured ones.
	ProviderID string

	// InstanceType is the cloud provider instance type (e.g. "c5.xlarge").
	// Populated for dynamically provisioned nodes; empty for statically configured ones.
	InstanceType string

	// Provider is the name of the cloud provider that owns this agent (e.g. "aws", "gcp").
	// Empty for statically configured agents.
	Provider string

	NodeStatus *NodeStatus
}

type AgentStatus int

const (
	AgentOnline AgentStatus = iota
	AgentOffline
	AgentBusy
	// AgentDraining means the node is being decommissioned. No new work is routed
	// to it; once ActiveCount reaches 0 it will be terminated.
	AgentDraining
)

type ConcurrencyBehavior string

const (
	ConcurrencyBehaviorWait ConcurrencyBehavior = "wait"
	ConcurrencyBehaviorExit ConcurrencyBehavior = "exit"
)

type PressureBehavior string

const (
	PressureBehaviorWait PressureBehavior = "wait"
	PressureBehaviorExit PressureBehavior = "exit"
)

type Function struct {
	Name                     string
	Handler                  string
	CPUMillicores            int32
	MemoryMB                 int64
	TimeoutSec               int32
	MaxConcurrency           int32
	MaxConcurrencyBehavior   ConcurrencyBehavior
	ResourcePressureBehavior PressureBehavior
	CodePath                 string
	Env                      map[string]string
}

// Pressure returns the combined CPU+memory load score (0–200).
// Agents with no status yet score 0 and are treated as lowest pressure.
func (a *Agent) Pressure() float64 {
	if a.NodeStatus == nil {
		return 0
	}
	return a.NodeStatus.CPUPercent + a.NodeStatus.MemPercent
}

// CanFit reports whether this agent has enough free resources to run fn.
// Agents with no status yet are assumed capable (optimistic — they may be booting).
func (a *Agent) CanFit(fn *Function) bool {
	if a.NodeStatus == nil {
		return true
	}
	if fn.MemoryMB > 0 && a.NodeStatus.MemTotalMB > 0 {
		availMB := a.NodeStatus.MemTotalMB - a.NodeStatus.MemUsedMB
		if uint64(fn.MemoryMB) > availMB {
			return false
		}
	}
	// Treat CPU >= 90% as saturated regardless of millicores requested.
	if fn.CPUMillicores > 0 && a.NodeStatus.CPUPercent >= 90 {
		return false
	}
	return true
}

// AvailableScore returns a score (higher = more headroom) used to pick the
// best agent when multiple agents can fit a function's requirements.
func (a *Agent) AvailableScore() float64 {
	if a.NodeStatus == nil {
		return 50 // unknown — moderate score
	}
	cpuAvail := 100 - a.NodeStatus.CPUPercent
	memAvail := float64(0)
	if a.NodeStatus.MemTotalMB > 0 {
		memAvail = float64(a.NodeStatus.MemTotalMB-a.NodeStatus.MemUsedMB) / float64(a.NodeStatus.MemTotalMB) * 100
	}
	return cpuAvail + memAvail
}

type ExecutionStatus string

const (
	ExecutionStatusRunning ExecutionStatus = "running"
	ExecutionStatusSuccess ExecutionStatus = "success"
	ExecutionStatusFailed  ExecutionStatus = "failed"
)

type ExecutionRecord struct {
	ExecutionID  string          `json:"execution_id"`
	FunctionName string          `json:"function_name"`
	Status       ExecutionStatus `json:"status"`
	Output       string          `json:"output,omitempty"`
	Error        string          `json:"error,omitempty"`
	DurationMs   int64           `json:"duration_ms,omitempty"`
	StartedAt    time.Time       `json:"started_at"`
	StatusAt     *time.Time      `json:"status_at,omitempty"`
}

// NodeStatus holds the latest resource metrics reported by an agent node.
type NodeStatus struct {
	AgentID     string
	CPUPercent  float64
	MemPercent  float64
	MemTotalMB  uint64
	MemUsedMB   uint64
	ActiveTasks int32
	UptimeSec   int64
	CollectedAt time.Time
}
