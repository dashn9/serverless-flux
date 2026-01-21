package models

import "time"

type Agent struct {
	ID            string
	Address       string
	MaxConcurrent int32
	ActiveCount   int32
	LastHeartbeat time.Time
	Status        AgentStatus
}

type AgentStatus int

const (
	AgentOnline AgentStatus = iota
	AgentOffline
	AgentBusy
)

type Function struct {
	Name          string
	Handler       string
	CPUMillicores int32
	MemoryMB      int64
	TimeoutSec    int32
	CodePath      string
	Env           map[string]string
}
