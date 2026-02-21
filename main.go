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

	agentClient := client.NewAgentClient(fluxConfig.GRPC)
	if fluxConfig.GRPC.Insecure {
		log.Printf("gRPC client: plaintext (insecure mode)")
	} else {
		log.Printf("gRPC client: mTLS (ca=%s cert=%s)", fluxConfig.GRPC.CACert, fluxConfig.GRPC.CertFile)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go startHealthPolling(ctx, reg, agentClient)

	// Start autoscaler if configured.
	var scaleHint chan<- struct{}
	if fluxConfig.Autoscaling != nil && fluxConfig.Autoscaling.Enabled {
		autoscaler, err := scaler.NewAutoscaler(
			reg,
			agentClient,
			fluxConfig.Autoscaling,
			fluxConfig.AgentPort,
			redisAddr,
			fluxConfig.GRPC.Agent,
		)
		if err != nil {
			log.Fatalf("Failed to initialize autoscaler: %v", err)
		}
		if autoscaler != nil {
			autoscaler.Start(ctx)
			scaleHint = autoscaler.ScaleHintCh()
		}
	} else {
		log.Printf("Autoscaling is disabled")
	}

	apiServer := api.NewAPIServer(reg, fluxConfig.APIKey, agentClient, scaleHint)
	log.Printf("HTTP API server listening on port %d", httpPort)

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
		if agent.Status != models.AgentOffline {
			log.Printf("Agent %s health check failed: %v", agent.ID, err)
		}
		return
	}

	// If this agent was offline, a successful health check means it is
	// now reachable — fetch status to promote it to online.
	if agent.Status == models.AgentOffline {
		status, err := agentClient.GetNodeStatus(agent)
		if err == nil {
			reg.UpdateNodeStatus(agent.ID, status)
		}
	}
}
