package registry

import (
	"log"
	"sync"
	"time"

	"flux/pkg/memory"
	"flux/pkg/models"
)

type Registry struct {
	mu     sync.RWMutex
	agents map[string]*models.Agent
	memory memory.Memory
}

func NewRegistry(mem memory.Memory) *Registry {
	return &Registry{
		agents: make(map[string]*models.Agent),
		memory: mem,
	}
}

// RegisterAgent adds or updates an agent as fully online.
func (r *Registry) RegisterAgent(id, address string, maxConcurrent int32) {
	r.mu.Lock()
	defer r.mu.Unlock()

	agent := &models.Agent{
		ID:            id,
		Address:       address,
		MaxConcurrent: maxConcurrent,
		ActiveCount:   0,
		LastHeartbeat: time.Now(),
		Status:        models.AgentOnline,
	}
	r.agents[id] = agent

	if err := r.memory.SaveAgent(agent); err != nil {
		log.Printf("Failed to persist agent %s: %v", id, err)
	}
}

// PreRegisterAgent records a node that has been provisioned but is not yet
// running the agent. It will not receive work until it transitions to
// AgentOnline (via a successful health check or explicit re-registration).
// providerID is the cloud-instance ID (e.g. EC2 instance ID).
func (r *Registry) PreRegisterAgent(id, address string, maxConcurrent int32, providerID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	agent := &models.Agent{
		ID:            id,
		Address:       address,
		MaxConcurrent: maxConcurrent,
		ActiveCount:   0,
		LastHeartbeat: time.Now(),
		Status:        models.AgentPreRegistered,
		ProviderID:    providerID,
	}
	r.agents[id] = agent

	log.Printf("[registry] Agent %s pre-registered at %s (provider: %s)", id, address, providerID)

	if err := r.memory.SaveAgent(agent); err != nil {
		log.Printf("Failed to persist pre-registered agent %s: %v", id, err)
	}
}

func (r *Registry) UpdateHeartbeat(id string, activeCount int32) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if agent, ok := r.agents[id]; ok {
		agent.LastHeartbeat = time.Now()
		agent.ActiveCount = activeCount
		agent.Status = models.AgentOnline

		if err := r.memory.SaveAgent(agent); err != nil {
			log.Printf("Failed to persist agent heartbeat %s: %v", id, err)
		}
	}
}

// UpdateNodeStatus stores the latest node telemetry and promotes a
// pre-registered agent to AgentOnline on first successful contact.
func (r *Registry) UpdateNodeStatus(id string, status *models.NodeStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if agent, ok := r.agents[id]; ok {
		if agent.Status == models.AgentPreRegistered {
			log.Printf("[registry] Agent %s promoted from pre-registered to online", id)
		}
		agent.NodeStatus = status
		agent.ActiveCount = status.ActiveTasks
		agent.LastHeartbeat = status.CollectedAt
		agent.Status = models.AgentOnline
	}
}

func (r *Registry) GetAgent(id string) (*models.Agent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	agent, ok := r.agents[id]
	return agent, ok
}

// GetAvailableAgents returns online agents that have capacity for more work.
// Pre-registered agents are excluded.
func (r *Registry) GetAvailableAgents() []*models.Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var available []*models.Agent
	for _, agent := range r.agents {
		if agent.Status == models.AgentOnline && agent.ActiveCount < agent.MaxConcurrent {
			available = append(available, agent)
		}
	}
	return available
}

// GetOnlineAgents returns all agents with AgentOnline status.
// Used by the autoscaler for metrics polling (excludes pre-registered).
func (r *Registry) GetOnlineAgents() []*models.Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var online []*models.Agent
	for _, agent := range r.agents {
		if agent.Status == models.AgentOnline {
			online = append(online, agent)
		}
	}
	return online
}

// GetAllAgents returns every registered agent regardless of status.
func (r *Registry) GetAllAgents() []*models.Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()

	agents := make([]*models.Agent, 0, len(r.agents))
	for _, agent := range r.agents {
		agents = append(agents, agent)
	}
	return agents
}

func (r *Registry) RegisterFunction(fn *models.Function) {
	if err := r.memory.SaveFunction(fn); err != nil {
		log.Printf("Failed to save function %s: %v", fn.Name, err)
	}
}

func (r *Registry) GetFunction(name string) (*models.Function, bool) {
	fn, err := r.memory.GetFunction(name)
	if err != nil || fn == nil {
		return nil, false
	}
	return fn, true
}
