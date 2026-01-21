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

type APIServer struct {
	registry    *registry.Registry
	apiKey      string
	agentClient *client.AgentClient
}

type FunctionYAML struct {
	Name      string            `yaml:"name"`
	Handler   string            `yaml:"handler"`
	Resources ResourceLimits    `yaml:"resources"`
	Timeout   int32             `yaml:"timeout"`
	Env       map[string]string `yaml:"env,omitempty"`
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
	ID            string `json:"id"`
	Address       string `json:"address"`
	MaxConcurrent int32  `json:"max_concurrent"`
	ActiveCount   int32  `json:"active_count"`
	Status        string `json:"status"`
	LastHeartbeat string `json:"last_heartbeat"`
}

type AgentOperationStatus struct {
	AgentID string `json:"agent_id"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

func NewAPIServer(registry *registry.Registry, apiKey string) *APIServer {
	return &APIServer{
		registry:    registry,
		apiKey:      apiKey,
		agentClient: client.NewAgentClient(),
	}
}

func (s *APIServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Route handling
	if r.URL.Path == "/health" && r.Method == "GET" {
		s.handleHealth(w, r)
		return
	}

	if r.URL.Path == "/agents" && r.Method == "GET" {
		s.handleGetAgents(w, r)
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
		if agent.Status == models.AgentOnline {
			status = "online"
		} else if agent.Status == models.AgentBusy {
			status = "busy"
		}

		response[i] = AgentInfo{
			ID:            agent.ID,
			Address:       agent.Address,
			MaxConcurrent: agent.MaxConcurrent,
			ActiveCount:   agent.ActiveCount,
			Status:        status,
			LastHeartbeat: agent.LastHeartbeat.Format(time.RFC3339),
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func (s *APIServer) handleRegisterFunction(w http.ResponseWriter, r *http.Request) {
	// Read YAML file from request body
	yamlData, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read YAML file", http.StatusBadRequest)
		return
	}

	if len(yamlData) == 0 {
		http.Error(w, "empty YAML file", http.StatusBadRequest)
		return
	}

	// Parse YAML
	var funcConfig FunctionYAML
	if err := yaml.Unmarshal(yamlData, &funcConfig); err != nil {
		http.Error(w, "invalid YAML format: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Validate required fields
	if funcConfig.Name == "" || funcConfig.Handler == "" {
		http.Error(w, "name and handler are required", http.StatusBadRequest)
		return
	}

	// Register function in registry
	function := &models.Function{
		Name:          funcConfig.Name,
		Handler:       funcConfig.Handler,
		CPUMillicores: funcConfig.Resources.CPU,
		MemoryMB:      funcConfig.Resources.Memory,
		TimeoutSec:    funcConfig.Timeout,
		Env:           funcConfig.Env,
	}
	s.registry.RegisterFunction(function)

	log.Printf("Registered function: %s", funcConfig.Name)

	// Register with all agents
	agents := s.registry.GetAllAgents()
	statuses := make([]AgentOperationStatus, 0, len(agents))
	registered := 0

	for _, agent := range agents {
		status := AgentOperationStatus{
			AgentID: agent.ID,
		}

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
	// Check if function exists
	_, exists := s.registry.GetFunction(functionName)
	if !exists {
		http.Error(w, "function not registered", http.StatusNotFound)
		return
	}

	// Read zip file from request body
	zipData, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read zip file", http.StatusBadRequest)
		return
	}

	if len(zipData) == 0 {
		http.Error(w, "empty zip file", http.StatusBadRequest)
		return
	}

	// Deploy to all available agents
	agents := s.registry.GetAvailableAgents()
	if len(agents) == 0 {
		http.Error(w, "no agents available", http.StatusServiceUnavailable)
		return
	}

	statuses := make([]AgentOperationStatus, 0, len(agents))
	deployed := 0

	for _, agent := range agents {
		status := AgentOperationStatus{
			AgentID: agent.ID,
		}

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

	// Check if function exists
	_, exists := s.registry.GetFunction(functionName)
	if !exists {
		http.Error(w, "function not found", http.StatusNotFound)
		return
	}

	// Find available agent
	agents := s.registry.GetAvailableAgents()
	if len(agents) == 0 {
		http.Error(w, "no agents available", http.StatusServiceUnavailable)
		return
	}

	// Use first available agent
	agent := agents[0]

	// Execute on agent
	result, err := s.agentClient.ExecuteFunction(agent, functionName, req.Args)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ExecuteResponse{
			Status:     "error",
			Error:      err.Error(),
			DurationMs: 0,
			AgentID:    agent.ID,
		})
		return
	}

	status := "success"
	statusCode := http.StatusOK
	if result.Error != "" {
		status = "failed"
		statusCode = http.StatusInternalServerError
	}

	response := ExecuteResponse{
		Status:     status,
		Output:     string(result.Output),
		Error:      result.Error,
		DurationMs: result.DurationMs,
		AgentID:    agent.ID,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(response)
}
