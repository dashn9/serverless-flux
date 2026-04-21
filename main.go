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
	"flux/pkg/pki"
	"flux/pkg/registry"
	scaler "flux/pkg/resource"
)

func main() {
	httpPort := 7227

	configPath := os.Getenv("FLUX_CONFIG")
	if configPath == "" {
		configPath = "flux.yaml"
	}

	if err := config.Load(configPath); err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	var pkiMgr *pki.PKI
	if !config.Get().DisableGRPCTLS {
		p, err := pki.New(config.Get().CertsDir)
		if err != nil {
			log.Fatalf("Failed to initialize PKI: %v", err)
		}
		pkiMgr = p
		log.Printf("PKI initialized (certs_dir=%s)", config.Get().CertsDir)
	} else {
		log.Printf("[grpc] TLS disabled — agents will be dialed without mTLS")
	}

	mem := memory.NewRedisMemory()
	defer mem.Close()

	reg := registry.NewRegistry(mem)
	agentClient := client.NewAgentClient(pkiMgr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	provMgr, err := scaler.NewProvidersManager(reg, agentClient, pkiMgr)
	if err != nil {
		log.Fatalf("Failed to initialize providers: %v", err)
	}
	provMgr.Start()

	apiServer := api.NewAPIServer(reg, agentClient, provMgr)

	go startHealthPolling(ctx, reg, agentClient, apiServer)

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

func startHealthPolling(ctx context.Context, reg *registry.Registry, agentClient *client.AgentClient, apiServer *api.APIServer) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			agents := reg.GetAllAgents()
			for _, agent := range agents {
				go checkAgentHealth(agent, reg, agentClient, apiServer)
			}
		}
	}
}

func checkAgentHealth(agent *models.Agent, reg *registry.Registry, agentClient *client.AgentClient, apiServer *api.APIServer) {
	if err := agentClient.HealthCheck(agent); err != nil {
		if agent.Status != models.AgentOffline {
			log.Printf("Agent %s health check failed: %v", agent.ID, err)
		}
		return
	}

	if agent.Status == models.AgentOffline {
		status, err := agentClient.GetNodeStatus(agent)
		if err == nil {
			reg.UpdateNodeStatus(agent.ID, status)
			// Agent just came online — sync all functions and code.
			go apiServer.SyncFunctionsToAgent(agent.ID, agent.Address)
		}
	}
}
