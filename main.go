package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"flux/pkg/api"
	"flux/pkg/client"
	"flux/pkg/config"
	"flux/pkg/memory"
	"flux/pkg/models"
	"flux/pkg/registry"
)

func main() {
	httpPort := 7227

	apiKey := os.Getenv("API_KEY")
	if apiKey == "" {
		apiKey = "default-secret-key"
		log.Printf("Warning: Using default API key. Set API_KEY environment variable.")
	}

	configPath := os.Getenv("FLUX_CONFIG")
	if configPath == "" {
		configPath = "flux.yaml"
	}

	// Load flux configuration
	fluxConfig, err := config.LoadFluxConfig(configPath)
	if err != nil {
		log.Fatalf("Failed to load flux config: %v", err)
	}

	// Initialize Redis memory
	redisAddr := fluxConfig.RedisAddr
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}
	mem := memory.NewRedisMemory(redisAddr)
	defer mem.Close()
	log.Printf("Connected to Redis at %s", redisAddr)

	// Shared registry with persistent memory
	reg := registry.NewRegistry(mem)

	// Register agents from config
	for _, agentConfig := range fluxConfig.Agents {
		log.Printf("Registering agent from config: %s at %s", agentConfig.ID, agentConfig.Address)
		reg.RegisterAgent(agentConfig.ID, agentConfig.Address, agentConfig.MaxConcurrency)
	}

	// Start health polling for agents
	agentClient := client.NewAgentClient()
	go startHealthPolling(reg, agentClient)

	// Start HTTP server for external API
	apiServer := api.NewAPIServer(reg, apiKey)
	log.Printf("HTTP API server listening on port %d", httpPort)
	log.Printf("API Key: %s", apiKey)
	log.Printf("Monitoring %d agents", len(fluxConfig.Agents))

	if err := http.ListenAndServe(fmt.Sprintf(":%d", httpPort), apiServer); err != nil {
		log.Fatalf("Failed to serve HTTP: %v", err)
	}
}

func startHealthPolling(reg *registry.Registry, agentClient *client.AgentClient) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		agents := reg.GetAllAgents()
		for _, agent := range agents {
			go checkAgentHealth(agent, agentClient)
		}
	}
}

func checkAgentHealth(agent *models.Agent, agentClient *client.AgentClient) {
	if err := agentClient.HealthCheck(agent); err != nil {
		log.Printf("Agent %s health check failed: %v", agent.ID, err)
	}
}
