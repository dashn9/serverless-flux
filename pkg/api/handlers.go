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
)

type APIServer struct {
	registry    *registry.Registry
	apiKey      string
	agentClient *client.AgentClient
}

type RegisterFunctionRequest struct {
	Name      string         `json:"name"`
	Handler   string         `json:"handler"`
	Resources ResourceLimits `json:"resources"`
	Timeout   int32          `json:"timeout"`
}

type ResourceLimits struct {
	CPU    int32 `json:"cpu"`
	Memory int64 `json:"memory"`
}

type ExecuteRequest struct {
	Input map[string]interface{} `json:"input"`
}

type ExecuteResponse struct {
	Output     string `json:"output"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"duration_ms"`
	AgentID    string `json:"agent_id"`
}

type AgentStatus struct {
	ID            string `json:"id"`
	Address       string `json:"address"`
	MaxConcurrent int32  `json:"max_concurrent"`
	ActiveCount   int32  `json:"active_count"`
	Status        string `json:"status"`
	LastHeartbeat string `json:"last_heartbeat"`
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

	if r.URL.Path == "/functions" && r.Method == "POST" {
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

	response := make([]AgentStatus, len(agents))
	for i, agent := range agents {
		status := "offline"
		if agent.Status == models.AgentOnline {
			status = "online"
		} else if agent.Status == models.AgentBusy {
			status = "busy"
		}

		response[i] = AgentStatus{
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
	var req RegisterFunctionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Register function in registry
	function := &models.Function{
		Name:          req.Name,
		Handler:       req.Handler,
		CPUMillicores: req.Resources.CPU,
		MemoryMB:      req.Resources.Memory,
		TimeoutSec:    req.Timeout,
	}
	s.registry.RegisterFunction(function)

	log.Printf("Registered function: %s", req.Name)

	// Register with all agents
	agents := s.registry.GetAllAgents()
	registered := 0
	for _, agent := range agents {
		if err := s.agentClient.RegisterFunction(agent, function); err != nil {
			log.Printf("Failed to register function on agent %s: %v", agent.ID, err)
			continue
		}
		registered++
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":          true,
		"message":          "function registered",
		"registered_count": registered,
		"total_agents":     len(agents),
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

	deployed := 0
	for _, agent := range agents {
		if err := s.agentClient.DeployFunction(agent, functionName, zipData); err != nil {
			log.Printf("Failed to deploy to agent %s: %v", agent.ID, err)
			continue
		}
		deployed++
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":        true,
		"deployed_count": deployed,
		"total_agents":   len(agents),
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
	inputBytes, _ := json.Marshal(req.Input)
	result, err := s.agentClient.ExecuteFunction(agent, functionName, inputBytes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	response := ExecuteResponse{
		Output:     string(result.Output),
		Error:      result.Error,
		DurationMs: result.DurationMs,
		AgentID:    agent.ID,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

