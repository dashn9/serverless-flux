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
	functions map[string]*models.Function
	memory    memory.Memory
}

func NewRegistry(mem memory.Memory) *Registry {
	r := &Registry{
		agents:    make(map[string]*models.Agent),
		functions: make(map[string]*models.Function),
		memory:    mem,
	}

	// Restore state from memory on startup
	if err := r.restore(); err != nil {
		log.Printf("Warning: failed to restore state: %v", err)
	}

	return r
}

func (r *Registry) restore() error {
	// Restore functions
	functions, err := r.memory.GetAllFunctions()
	if err != nil {
		return err
	}
	for _, fn := range functions {
		r.functions[fn.Name] = fn
	}
	log.Printf("Restored %d functions from memory", len(functions))

	// Restore agents
	agents, err := r.memory.GetAllAgents()
	if err != nil {
		return err
	}
	for _, agent := range agents {
		r.agents[agent.ID] = agent
	}
	log.Printf("Restored %d agents from memory", len(agents))

	return nil
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
	r.mu.Lock()
	defer r.mu.Unlock()

	r.functions[fn.Name] = fn

	// Persist to memory
	if err := r.memory.SaveFunction(fn); err != nil {
		log.Printf("Failed to persist function %s: %v", fn.Name, err)
	}
}

func (r *Registry) GetFunction(name string) (*models.Function, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	fn, ok := r.functions[name]
	return fn, ok
}
