package registry

import (
	"log"
	"time"

	"flux/pkg/memory"
	"flux/pkg/models"
)

type Registry struct {
	memory memory.Memory
}

func NewRegistry(mem memory.Memory) *Registry {
	return &Registry{memory: mem}
}

func (r *Registry) RegisterAgent(id, address, providerID, instanceType string) {
	agent := &models.Agent{
		ID:            id,
		Address:       address,
		LastHeartbeat: time.Now(),
		Status:        models.AgentOnline,
		ProviderID:    providerID,
		InstanceType:  instanceType,
	}
	if err := r.memory.SaveAgent(agent); err != nil {
		log.Printf("[registry] Failed to persist agent %s: %v", id, err)
	}
}

func (r *Registry) RegisterOfflineAgent(id, address, providerID, instanceType string) {
	agent := &models.Agent{
		ID:            id,
		Address:       address,
		LastHeartbeat: time.Now(),
		Status:        models.AgentOffline,
		ProviderID:    providerID,
		InstanceType:  instanceType,
	}
	if err := r.memory.SaveAgent(agent); err != nil {
		log.Printf("[registry] Failed to persist agent %s: %v", id, err)
	}
}

func (r *Registry) DeregisterAgent(id string) {
	if err := r.memory.DeleteAgent(id); err != nil {
		log.Printf("[registry] Failed to delete agent %s: %v", id, err)
	}
}

func (r *Registry) SetOffline(id string) {
	agent, err := r.memory.GetAgent(id)
	if err != nil || agent == nil {
		return
	}
	if agent.Status == models.AgentDraining {
		return
	}
	agent.Status = models.AgentOffline
	if err := r.memory.SaveAgent(agent); err != nil {
		log.Printf("[registry] Failed to mark agent %s offline: %v", id, err)
	}
	log.Printf("[registry] Agent %s marked offline", id)
}

func (r *Registry) SetDraining(id string) {
	agent, err := r.memory.GetAgent(id)
	if err != nil || agent == nil {
		return
	}
	agent.Status = models.AgentDraining
	if err := r.memory.SaveAgent(agent); err != nil {
		log.Printf("[registry] Failed to mark agent %s draining: %v", id, err)
	}
	log.Printf("[registry] Agent %s set to draining", id)
}

func (r *Registry) UpdateHeartbeat(id string, activeCount int32) {
	agent, err := r.memory.GetAgent(id)
	if err != nil || agent == nil {
		return
	}
	agent.LastHeartbeat = time.Now()
	agent.ActiveCount = activeCount
	if agent.Status == models.AgentOffline {
		agent.Status = models.AgentOnline
	}
	if err := r.memory.SaveAgent(agent); err != nil {
		log.Printf("[registry] Failed to persist heartbeat for %s: %v", id, err)
	}
}

func (r *Registry) UpdateNodeStatus(id string, status *models.NodeStatus) {
	agent, err := r.memory.GetAgent(id)
	if err != nil || agent == nil {
		return
	}
	agent.NodeStatus = status
	agent.ActiveCount = status.ActiveTasks
	agent.LastHeartbeat = status.CollectedAt
	if agent.Status == models.AgentOffline {
		log.Printf("[registry] Agent %s came online", id)
		agent.Status = models.AgentOnline
	}
	if err := r.memory.SaveAgent(agent); err != nil {
		log.Printf("[registry] Failed to persist node status for %s: %v", id, err)
	}
}

func (r *Registry) GetAgent(id string) (*models.Agent, bool) {
	agent, err := r.memory.GetAgent(id)
	if err != nil || agent == nil {
		return nil, false
	}
	return agent, true
}

func (r *Registry) GetAllAgents() []*models.Agent {
	agents, err := r.memory.GetAllAgents()
	if err != nil {
		log.Printf("[registry] Failed to fetch agents: %v", err)
		return nil
	}
	return agents
}

func (r *Registry) GetAvailableAgents() []*models.Agent {
	all, err := r.memory.GetAllAgents()
	if err != nil {
		log.Printf("[registry] Failed to fetch agents: %v", err)
		return nil
	}
	var available []*models.Agent
	for _, a := range all {
		if a.Status == models.AgentOnline {
			available = append(available, a)
		}
	}
	return available
}

func (r *Registry) GetOnlineAgents() []*models.Agent {
	all, err := r.memory.GetAllAgents()
	if err != nil {
		log.Printf("[registry] Failed to fetch agents: %v", err)
		return nil
	}
	var online []*models.Agent
	for _, a := range all {
		if a.Status == models.AgentOnline {
			online = append(online, a)
		}
	}
	return online
}

func (r *Registry) RegisterFunction(fn *models.Function) {
	if err := r.memory.SaveFunction(fn); err != nil {
		log.Printf("[registry] Failed to save function %s: %v", fn.Name, err)
	}
}

func (r *Registry) GetFunction(name string) (*models.Function, bool) {
	fn, err := r.memory.GetFunction(name)
	if err != nil || fn == nil {
		return nil, false
	}
	return fn, true
}
