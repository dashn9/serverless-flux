package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"flux/pkg/api"
	"flux/pkg/client"
	"flux/pkg/config"
	"flux/pkg/memory"
	"flux/pkg/models"
	"flux/pkg/registry"
	scaler "flux/pkg/resource"
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

	fluxConfig, err := config.LoadFluxConfig(configPath)
	if err != nil {
		log.Fatalf("Failed to load flux config: %v", err)
	}

	redisAddr := fluxConfig.RedisAddr
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}
	mem := memory.NewRedisMemory(redisAddr)
	defer mem.Close()
	log.Printf("Connected to Redis at %s", redisAddr)

	reg := registry.NewRegistry(mem)

	// Register agents from config. Agents marked pre_registered are declared
	// but not yet online — they won't receive work until health checks succeed.
	for _, agentConfig := range fluxConfig.Agents {
		if agentConfig.PreRegistered {
			reg.PreRegisterAgent(agentConfig.ID, agentConfig.Address, agentConfig.MaxConcurrency, "")
		} else {
			reg.RegisterAgent(agentConfig.ID, agentConfig.Address, agentConfig.MaxConcurrency)
		}
	}

	// Build the gRPC agent client. Use mTLS when TLS is configured.
	var agentClient *client.AgentClient
	if fluxConfig.TLS != nil && fluxConfig.TLS.Enabled {
		agentClient = client.NewAgentClientTLS(fluxConfig.TLS)
		log.Printf("gRPC client: mTLS enabled (ca=%s cert=%s)", fluxConfig.TLS.CACert, fluxConfig.TLS.CertFile)
	} else {
		agentClient = client.NewAgentClient()
		log.Printf("gRPC client: plaintext (TLS not configured)")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go startHealthPolling(ctx, reg, agentClient)

	// Start autoscaler if configured. Pass all parameters needed to construct
	// the cloud provider — keeps the provider resource-aware (vCPUs + memory)
	// rather than requiring the caller to specify an instance type.
	if fluxConfig.Autoscaling != nil && fluxConfig.Autoscaling.Enabled {
		autoscaler, err := scaler.NewAutoscaler(
			reg,
			agentClient,
			fluxConfig.Autoscaling,
			fluxConfig.AgentPort,
			redisAddr,
			fluxConfig.TLS,
		)
		if err != nil {
			log.Fatalf("Failed to initialize autoscaler: %v", err)
		}
		if autoscaler != nil {
			autoscaler.Start(ctx)
		}
	} else {
		log.Printf("Autoscaling is disabled")
	}

	apiServer := api.NewAPIServer(reg, apiKey, agentClient)
	log.Printf("HTTP API server listening on port %d", httpPort)
	log.Printf("API Key: %s", apiKey)
	log.Printf("Monitoring %d agents", len(fluxConfig.Agents))

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down...", sig)
		cancel()
		os.Exit(0)
	}()

	if err := http.ListenAndServe(fmt.Sprintf(":%d", httpPort), apiServer); err != nil {
		log.Fatalf("Failed to serve HTTP: %v", err)
	}
}

func startHealthPolling(ctx context.Context, reg *registry.Registry, agentClient *client.AgentClient) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Poll all agents including pre-registered ones — a successful health
			// check on a pre-registered agent promotes it to online.
			agents := reg.GetAllAgents()
			for _, agent := range agents {
				go checkAgentHealth(agent, reg, agentClient)
			}
		}
	}
}

func checkAgentHealth(agent *models.Agent, reg *registry.Registry, agentClient *client.AgentClient) {
	if err := agentClient.HealthCheck(agent); err != nil {
		if agent.Status != models.AgentPreRegistered {
			log.Printf("Agent %s health check failed: %v", agent.ID, err)
		}
		return
	}

	// If this agent was pre-registered, a successful health check means it is
	// now online. Trigger a status fetch which will promote it.
	if agent.Status == models.AgentPreRegistered {
		status, err := agentClient.GetNodeStatus(agent)
		if err == nil {
			reg.UpdateNodeStatus(agent.ID, status)
			log.Printf("Agent %s is now online (promoted from pre-registered)", agent.ID)
		}
	}
}
