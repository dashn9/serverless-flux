package api

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"flux/pkg/client"
	"flux/pkg/models"
	"flux/pkg/registry"

	"gopkg.in/yaml.v3"
)

// TODO(multi-flux): Support multiple Flux nodes forming a cluster that spans
// itself automatically from a single seed node based on config. Each Flux node
// would manage a shard of the agent fleet. Attaching a load balancer in front
// of the Flux cluster is the responsibility of the operator.

type APIServer struct {
	registry    *registry.Registry
	apiKey      string
	agentClient *client.AgentClient
}

type FunctionYAML struct {
	Name                   string            `yaml:"name"`
	Handler                string            `yaml:"handler"`
	Resources              ResourceLimits    `yaml:"resources"`
	Timeout                int32             `yaml:"timeout"`
	MaxConcurrency         int32             `yaml:"max_concurrency,omitempty"`
	MaxConcurrencyBehavior string            `yaml:"max_concurrency_behavior,omitempty"`
	Env                    map[string]string `yaml:"env,omitempty"`
}

type ResourceLimits struct {
	CPU    int32 `yaml:"cpu" json:"cpu"`
	Memory int64 `yaml:"memory" json:"memory"`
}

type ExecuteRequest struct {
	Args []string `json:"args"`
}

type ExecuteResponse struct {
	Status     string `json:"status"`
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"duration_ms"`
	AgentID    string `json:"agent_id"`
}

type AgentInfo struct {
	ID            string          `json:"id"`
	Address       string          `json:"address"`
	ActiveCount   int32           `json:"active_count"`
	Status        string          `json:"status"`
	LastHeartbeat string          `json:"last_heartbeat"`
	ProviderID    string          `json:"provider_id,omitempty"`
	InstanceType  string          `json:"instance_type,omitempty"`
	NodeStatus    *NodeStatusInfo `json:"node_status,omitempty"`
}

type NodeStatusInfo struct {
	CPUPercent  float64 `json:"cpu_percent"`
	MemPercent  float64 `json:"memory_percent"`
	MemTotalMB  uint64  `json:"memory_total_mb"`
	MemUsedMB   uint64  `json:"memory_used_mb"`
	ActiveTasks int32   `json:"active_tasks"`
	UptimeSec   int64   `json:"uptime_seconds"`
	CollectedAt string  `json:"collected_at"`
}

type AgentOperationStatus struct {
	AgentID string `json:"agent_id"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// RegisterNodeRequest is the body for POST /nodes/register.
// An agent calls this endpoint when it has booted and is ready to accept work.
type RegisterNodeRequest struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	NodeId  string `json:"node_id"`
}

func NewAPIServer(registry *registry.Registry, apiKey string, agentClient *client.AgentClient) *APIServer {
	return &APIServer{
		registry:    registry,
		apiKey:      apiKey,
		agentClient: agentClient,
	}
}

func (s *APIServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/health" && r.Method == "GET" {
		s.handleHealth(w, r)
		return
	}

	if r.URL.Path == "/agents" && r.Method == "GET" {
		s.handleGetAgents(w, r)
		return
	}

	// POST /nodes/register — an agent calls this to register itself with Flux.
	// The agent provides its own ID, reachable address, and concurrency limit.
	// Use this for any node that isn't statically declared in flux.yaml,
	// including nodes co-located on the same host (use address: localhost:<port>).
	if r.URL.Path == "/nodes/register" && r.Method == "POST" {
		if !s.authenticate(r) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		s.handleRegisterNode(w, r)
		return
	}

	if r.URL.Path == "/functions" && r.Method == "PUT" {
		if !s.authenticate(r) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		s.handleRegisterFunction(w, r)
		return
	}

	if strings.HasPrefix(r.URL.Path, "/deploy/") && r.Method == "PUT" {
		if !s.authenticate(r) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		functionName := strings.TrimPrefix(r.URL.Path, "/deploy/")
		s.handleDeploy(w, r, functionName)
		return
	}

	if strings.HasPrefix(r.URL.Path, "/execute/") && r.Method == "POST" {
		if !s.authenticate(r) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		functionName := strings.TrimPrefix(r.URL.Path, "/execute/")
		s.handleExecute(w, r, functionName)
		return
	}

	http.NotFound(w, r)
}

func (s *APIServer) authenticate(r *http.Request) bool {
	apiKey := r.Header.Get("X-API-Key")
	if apiKey == "" {
		apiKey = r.Header.Get("Authorization")
		if len(apiKey) > 7 && apiKey[:7] == "Bearer " {
			apiKey = apiKey[7:]
		}
	}

	return apiKey == s.apiKey
}

func (s *APIServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *APIServer) handleGetAgents(w http.ResponseWriter, r *http.Request) {
	agents := s.registry.GetAllAgents()

	response := make([]AgentInfo, len(agents))
	for i, agent := range agents {
		status := "offline"
		switch agent.Status {
		case models.AgentOnline:
			status = "online"
		case models.AgentBusy:
			status = "busy"
		case models.AgentDraining:
			status = "draining"
		}

		info := AgentInfo{
			ID:            agent.ID,
			Address:       agent.Address,
			ActiveCount:   agent.ActiveCount,
			Status:        status,
			LastHeartbeat: agent.LastHeartbeat.Format(time.RFC3339),
			ProviderID:    agent.ProviderID,
			InstanceType:  agent.InstanceType,
		}

		if agent.NodeStatus != nil {
			info.NodeStatus = &NodeStatusInfo{
				CPUPercent:  agent.NodeStatus.CPUPercent,
				MemPercent:  agent.NodeStatus.MemPercent,
				MemTotalMB:  agent.NodeStatus.MemTotalMB,
				MemUsedMB:   agent.NodeStatus.MemUsedMB,
				ActiveTasks: agent.NodeStatus.ActiveTasks,
				UptimeSec:   agent.NodeStatus.UptimeSec,
				CollectedAt: agent.NodeStatus.CollectedAt.Format(time.RFC3339),
			}
		}

		response[i] = info
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// handleRegisterNode handles POST /nodes/register.
// An agent (or its bootstrap script) calls this to join the Flux fleet.
func (s *APIServer) handleRegisterNode(w http.ResponseWriter, r *http.Request) {
	var req RegisterNodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.ID == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	if req.Address == "" {
		http.Error(w, "address is required", http.StatusBadRequest)
		return
	}

	s.registry.RegisterAgent(req.ID, req.Address, req.NodeId, "")
	log.Printf("[api] Node registered: id=%s address=%s node_id=%s", req.ID, req.Address, req.NodeId)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "registered",
		"id":     req.ID,
	})
}

func (s *APIServer) handleRegisterFunction(w http.ResponseWriter, r *http.Request) {
	yamlData, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read YAML file", http.StatusBadRequest)
		return
	}

	if len(yamlData) == 0 {
		http.Error(w, "empty YAML file", http.StatusBadRequest)
		return
	}

	var funcConfig FunctionYAML
	if err := yaml.Unmarshal(yamlData, &funcConfig); err != nil {
		http.Error(w, "invalid YAML format: "+err.Error(), http.StatusBadRequest)
		return
	}

	if funcConfig.Name == "" || funcConfig.Handler == "" {
		http.Error(w, "name and handler are required", http.StatusBadRequest)
		return
	}

	maxConcurrency := funcConfig.MaxConcurrency
	if maxConcurrency == 0 {
		maxConcurrency = 5
	}

	maxConcurrencyBehavior := models.ConcurrencyBehavior(funcConfig.MaxConcurrencyBehavior)
	if maxConcurrencyBehavior == "" {
		maxConcurrencyBehavior = models.ConcurrencyBehaviorExit
	}

	if maxConcurrencyBehavior != models.ConcurrencyBehaviorWait && maxConcurrencyBehavior != models.ConcurrencyBehaviorExit {
		http.Error(w, "max_concurrency_behavior must be 'wait' or 'exit'", http.StatusBadRequest)
		return
	}

	function := &models.Function{
		Name:                   funcConfig.Name,
		Handler:                funcConfig.Handler,
		CPUMillicores:          funcConfig.Resources.CPU,
		MemoryMB:               funcConfig.Resources.Memory,
		TimeoutSec:             funcConfig.Timeout,
		MaxConcurrency:         maxConcurrency,
		MaxConcurrencyBehavior: maxConcurrencyBehavior,
		Env:                    funcConfig.Env,
	}
	s.registry.RegisterFunction(function)

	log.Printf("Registered function: %s", funcConfig.Name)

	agents := s.registry.GetAllAgents()
	statuses := make([]AgentOperationStatus, 0, len(agents))
	registered := 0

	for _, agent := range agents {
		status := AgentOperationStatus{AgentID: agent.ID}

		if err := s.agentClient.RegisterFunction(agent, function); err != nil {
			log.Printf("Failed to register function on agent %s: %v", agent.ID, err)
			status.Status = "failed"
			status.Message = err.Error()
		} else {
			status.Status = "success"
			registered++
		}

		statuses = append(statuses, status)
	}

	if registered == 0 {
		http.Error(w, "failed to register function on any agent", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":           "success",
		"registered_count": registered,
		"total_agents":     len(agents),
		"agents":           statuses,
	})
}

func (s *APIServer) handleDeploy(w http.ResponseWriter, r *http.Request, functionName string) {
	_, exists := s.registry.GetFunction(functionName)
	if !exists {
		http.Error(w, "function not registered", http.StatusNotFound)
		return
	}

	zipData, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read zip file", http.StatusBadRequest)
		return
	}

	if len(zipData) == 0 {
		http.Error(w, "empty zip file", http.StatusBadRequest)
		return
	}

	agents := s.registry.GetAvailableAgents()
	if len(agents) == 0 {
		http.Error(w, "no agents available", http.StatusServiceUnavailable)
		return
	}

	statuses := make([]AgentOperationStatus, 0, len(agents))
	deployed := 0

	for _, agent := range agents {
		status := AgentOperationStatus{AgentID: agent.ID}

		if err := s.agentClient.DeployFunction(agent, functionName, zipData); err != nil {
			log.Printf("Failed to deploy to agent %s: %v", agent.ID, err)
			status.Status = "failed"
			status.Message = err.Error()
		} else {
			status.Status = "success"
			deployed++
		}

		statuses = append(statuses, status)
	}

	if deployed == 0 {
		http.Error(w, "failed to deploy to any agent", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":         "success",
		"deployed_count": deployed,
		"total_agents":   len(agents),
		"agents":         statuses,
	})
}

func (s *APIServer) handleExecute(w http.ResponseWriter, r *http.Request, functionName string) {
	var req ExecuteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	fn, exists := s.registry.GetFunction(functionName)
	if !exists {
		http.Error(w, "function not found", http.StatusNotFound)
		return
	}

	for {
		agents := s.registry.GetAvailableAgents()

		if len(agents) > 0 {
			// Route to the agent with the lowest combined CPU+mem pressure.
			agent := agents[0]
			for _, a := range agents[1:] {
				if a.Pressure() < agent.Pressure() {
					agent = a
				}
			}

			log.Printf("[execute] %s → agent=%s (pressure=%.0f%%)", functionName, agent.ID, agent.Pressure())

			start := time.Now()
			result, err := s.agentClient.ExecuteFunction(agent, functionName, req.Args)
			elapsed := time.Since(start).Milliseconds()
			if err != nil {
				log.Printf("[execute] %s failed on agent=%s in %dms: %v", functionName, agent.ID, elapsed, err)
				http.Error(w, "agent communication error: "+err.Error(), http.StatusInternalServerError)
				return
			}

			if result.Error != "" && strings.Contains(result.Error, "at capacity") {
				if fn.MaxConcurrencyBehavior == models.ConcurrencyBehaviorExit {
					http.Error(w, result.Error, http.StatusServiceUnavailable)
					return
				}
				log.Printf("[execute] %s at capacity on agent=%s, retrying...", functionName, agent.ID)
			} else {
				status := "success"
				statusCode := http.StatusOK
				if result.Error != "" {
					status = "failed"
					statusCode = http.StatusInternalServerError
					log.Printf("[execute] %s failed on agent=%s in %dms: %s", functionName, agent.ID, elapsed, result.Error)
				} else {
					log.Printf("[execute] %s completed on agent=%s in %dms", functionName, agent.ID, elapsed)
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(statusCode)
				json.NewEncoder(w).Encode(ExecuteResponse{
					Status:     status,
					Output:     string(result.Output),
					Error:      result.Error,
					DurationMs: result.DurationMs,
					AgentID:    agent.ID,
				})
				return
			}
		} else {
			if fn.MaxConcurrencyBehavior == models.ConcurrencyBehaviorExit {
				http.Error(w, "no agents available", http.StatusServiceUnavailable)
				return
			}
			log.Printf("[execute] %s waiting — no agents available", functionName)
		}

		select {
		case <-r.Context().Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}
