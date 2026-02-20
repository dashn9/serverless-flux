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

type Function struct {
	Name                   string
	Handler                string
	CPUMillicores          int32
	MemoryMB               int64
	TimeoutSec             int32
	MaxConcurrency         int32
	MaxConcurrencyBehavior ConcurrencyBehavior
	CodePath               string
	Env                    map[string]string
}

// Pressure returns the combined CPU+memory load score (0–200).
// Agents with no status yet score 0 and are treated as lowest pressure.
func (a *Agent) Pressure() float64 {
	if a.NodeStatus == nil {
		return 0
	}
	return a.NodeStatus.CPUPercent + a.NodeStatus.MemPercent
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
