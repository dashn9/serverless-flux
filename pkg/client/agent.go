package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"flux/pkg/config"
	"flux/pkg/models"
	pb "flux/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

type AgentClient struct {
	mu      sync.RWMutex
	clients map[string]pb.AgentServiceClient
}

func NewAgentClient() *AgentClient {
	grpcCfg := config.Get().GRPC
	if grpcCfg.Insecure {
		log.Printf("[grpc] client: plaintext (insecure mode)")
	} else {
		log.Printf("[grpc] client: mTLS (ca=%s cert=%s)", grpcCfg.CACert, grpcCfg.CertFile)
	}
	return &AgentClient{clients: make(map[string]pb.AgentServiceClient)}
}

func (c *AgentClient) get(address string) (pb.AgentServiceClient, error) {
	c.mu.RLock()
	cl, exists := c.clients[address]
	c.mu.RUnlock()
	if exists {
		return cl, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if cl, exists := c.clients[address]; exists {
		return cl, nil
	}

	grpcCfg := config.Get().GRPC
	var creds credentials.TransportCredentials
	if !grpcCfg.Insecure {
		var err error
		creds, err = loadFluxTLSCredentials(grpcCfg)
		if err != nil {
			return nil, fmt.Errorf("load mTLS credentials: %w", err)
		}
	} else {
		creds = insecure.NewCredentials()
	}

	conn, err := grpc.NewClient(address,
		grpc.WithTransportCredentials(creds),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(50*1024*1024),
			grpc.MaxCallSendMsgSize(50*1024*1024),
		))
	if err != nil {
		return nil, err
	}

	cl = pb.NewAgentServiceClient(conn)
	c.clients[address] = cl
	return cl, nil
}

// loadFluxTLSCredentials builds mTLS client credentials for Flux connecting to agents.
func loadFluxTLSCredentials(grpcCfg *config.GRPCConfig) (credentials.TransportCredentials, error) {
	cert, err := tls.LoadX509KeyPair(grpcCfg.CertFile, grpcCfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load flux cert/key: %w", err)
	}

	caData, err := os.ReadFile(grpcCfg.CACert)
	if err != nil {
		return nil, fmt.Errorf("read CA cert: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caData) {
		return nil, fmt.Errorf("failed to parse CA certificate")
	}

	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   "flux-agent",
	}
	return credentials.NewTLS(cfg), nil
}

func (c *AgentClient) RegisterFunction(agent *models.Agent, function *models.Function) error {
	cl, err := c.get(agent.Address)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := cl.RegisterFunction(ctx, &pb.FunctionConfig{
		Name:                   function.Name,
		Handler:                function.Handler,
		CpuMillicores:          function.CPUMillicores,
		MemoryMb:               function.MemoryMB,
		TimeoutSeconds:         function.TimeoutSec,
		Env:                    function.Env,
		MaxConcurrency:         function.MaxConcurrency,
		MaxConcurrencyBehavior: string(function.MaxConcurrencyBehavior),
	})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("registration failed: %s", resp.Message)
	}
	return nil
}

func (c *AgentClient) DeployFunction(agent *models.Agent, functionName string, zipData []byte) error {
	cl, err := c.get(agent.Address)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := cl.DeployFunction(ctx, &pb.DeploymentPackage{
		FunctionName: functionName,
		CodeArchive:  zipData,
	})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("deployment failed: %s", resp.Message)
	}
	return nil
}

func (c *AgentClient) ExecuteFunction(agent *models.Agent, functionName string, args []string) (*pb.ExecutionResponse, error) {
	cl, err := c.get(agent.Address)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	return cl.ExecuteFunction(ctx, &pb.ExecutionRequest{
		FunctionName: functionName,
		Args:         args,
	})
}

func (c *AgentClient) HealthCheck(agent *models.Agent) error {
	cl, err := c.get(agent.Address)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err = cl.HealthCheck(ctx, &pb.HealthCheckRequest{})
	return err
}

func (c *AgentClient) GetNodeStatus(agent *models.Agent) (*models.NodeStatus, error) {
	cl, err := c.get(agent.Address)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := cl.ReportNodeStatus(ctx, &pb.NodeStatusRequest{})
	if err != nil {
		return nil, err
	}

	return &models.NodeStatus{
		AgentID:     resp.AgentId,
		CPUPercent:  resp.CpuPercent,
		MemPercent:  resp.MemoryPercent,
		MemTotalMB:  resp.MemoryTotalMb,
		MemUsedMB:   resp.MemoryUsedMb,
		ActiveTasks: resp.ActiveTasks,
		UptimeSec:   resp.UptimeSeconds,
		CollectedAt: time.Now(),
	}, nil
}
