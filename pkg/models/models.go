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

type ConcurrencyBehavior string

const (
	ConcurrencyBehaviorWait ConcurrencyBehavior = "wait"
	ConcurrencyBehaviorExit ConcurrencyBehavior = "exit"
)

type Function struct {
	Name                     string
	Handler                  string
	CPUMillicores            int32
	MemoryMB                 int64
	TimeoutSec               int32
	MaxConcurrency           int32
	MaxConcurrencyBehavior   ConcurrencyBehavior
	CodePath                 string
	Env                      map[string]string
}
