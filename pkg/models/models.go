package models

import "time"

type Agent struct {
	ID            string
	Address       string
	MaxConcurrent int32
	ActiveCount   int32
	LastHeartbeat time.Time
	Status        AgentStatus

	// ProviderID is the cloud-provider instance identifier (e.g. EC2 instance ID).
	// Populated for dynamically provisioned nodes; empty for statically configured ones.
	ProviderID string

	NodeStatus *NodeStatus
}

type AgentStatus int

const (
	AgentOnline AgentStatus = iota
	AgentOffline
	AgentBusy
	// AgentPreRegistered means the node has been declared but is not yet online.
	// Flux knows it is coming (e.g. an instance is still booting) but will not
	// route work to it until it transitions to AgentOnline.
	AgentPreRegistered
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

// NodeStatus holds the latest resource metrics reported by an agent node.
type NodeStatus struct {
	AgentID     string
	CPUPercent  float64
	MemPercent  float64
	MemTotalMB  uint64
	MemUsedMB   uint64
	ActiveTasks int32
	MaxTasks    int32
	UptimeSec   int64
	CollectedAt time.Time
}
