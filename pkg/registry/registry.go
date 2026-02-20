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

// RegisterAgent registers an agent as online (self-registration via HTTP).
func (r *Registry) RegisterAgent(id, address, providerID, instanceType string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	agent := &models.Agent{
		ID:            id,
		Address:       address,
		LastHeartbeat: time.Now(),
		Status:        models.AgentOnline,
		ProviderID:    providerID,
		InstanceType:  instanceType,
	}
	r.agents[id] = agent

	if err := r.memory.SaveAgent(agent); err != nil {
		log.Printf("Failed to persist agent %s: %v", id, err)
	}
}

// RegisterOfflineAgent registers an autoscaler-spawned node as offline.
// The node transitions to online once first successfully contacted.
func (r *Registry) RegisterOfflineAgent(id, address, providerID, instanceType string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	agent := &models.Agent{
		ID:            id,
		Address:       address,
		LastHeartbeat: time.Now(),
		Status:        models.AgentOffline,
		ProviderID:    providerID,
		InstanceType:  instanceType,
	}
	r.agents[id] = agent

	if err := r.memory.SaveAgent(agent); err != nil {
		log.Printf("Failed to persist agent %s: %v", id, err)
	}
}

// DeregisterAgent removes an agent from the registry and persistent storage.
func (r *Registry) DeregisterAgent(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.agents, id)
	if err := r.memory.DeleteAgent(id); err != nil {
		log.Printf("Failed to delete agent %s from storage: %v", id, err)
	}
}

// SetDraining marks an agent as draining so no new work is routed to it.
func (r *Registry) SetDraining(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if agent, ok := r.agents[id]; ok {
		agent.Status = models.AgentDraining
		log.Printf("[registry] Agent %s set to draining", id)
	}
}

func (r *Registry) UpdateHeartbeat(id string, activeCount int32) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if agent, ok := r.agents[id]; ok {
		agent.LastHeartbeat = time.Now()
		agent.ActiveCount = activeCount
		// Don't change draining agents via heartbeat.
		if agent.Status == models.AgentOffline {
			agent.Status = models.AgentOnline
		}

		if err := r.memory.SaveAgent(agent); err != nil {
			log.Printf("Failed to persist agent heartbeat %s: %v", id, err)
		}
	}
}

// UpdateNodeStatus stores the latest node telemetry.
// Promotes an offline agent to online on first successful contact.
func (r *Registry) UpdateNodeStatus(id string, status *models.NodeStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if agent, ok := r.agents[id]; ok {
		agent.NodeStatus = status
		agent.ActiveCount = status.ActiveTasks
		agent.LastHeartbeat = status.CollectedAt
		// Promote offline → online on first contact. Draining stays draining.
		if agent.Status == models.AgentOffline {
			log.Printf("[registry] Agent %s came online", id)
			agent.Status = models.AgentOnline
		}
	}
}

func (r *Registry) GetAgent(id string) (*models.Agent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	agent, ok := r.agents[id]
	return agent, ok
}

// GetAvailableAgents returns agents that are fully online and accepting work.
// Offline, draining, and busy agents are excluded.
func (r *Registry) GetAvailableAgents() []*models.Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var available []*models.Agent
	for _, agent := range r.agents {
		if agent.Status == models.AgentOnline {
			available = append(available, agent)
		}
	}
	return available
}

// GetOnlineAgents returns all agents with AgentOnline status.
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

// GetOfflineAgents returns managed (autoscaler-spawned) agents that are offline.
// Used by the autoscaler to probe newly booted nodes for promotion.
func (r *Registry) GetOfflineAgents() []*models.Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var offline []*models.Agent
	for _, agent := range r.agents {
		if agent.Status == models.AgentOffline && agent.ProviderID != "" {
			offline = append(offline, agent)
		}
	}
	return offline
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
