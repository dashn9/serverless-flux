package registry

import (
	"flux/pkg/models"
	"flux/pkg/memory"
	"log"
	"sync"
	"time"
)

type Registry struct {
	mu        sync.RWMutex
	agents    map[string]*models.Agent
	memory    memory.Memory
}

func NewRegistry(mem memory.Memory) *Registry {
	return &Registry{
		agents: make(map[string]*models.Agent),
		memory: mem,
	}
}

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

	// Persist to memory
	if err := r.memory.SaveAgent(agent); err != nil {
		log.Printf("Failed to persist agent %s: %v", id, err)
	}
}

func (r *Registry) UpdateHeartbeat(id string, activeCount int32) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if agent, ok := r.agents[id]; ok {
		agent.LastHeartbeat = time.Now()
		agent.ActiveCount = activeCount
		agent.Status = models.AgentOnline

		// Persist to memory
		if err := r.memory.SaveAgent(agent); err != nil {
			log.Printf("Failed to persist agent heartbeat %s: %v", id, err)
		}
	}
}

func (r *Registry) GetAgent(id string) (*models.Agent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	agent, ok := r.agents[id]
	return agent, ok
}

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
