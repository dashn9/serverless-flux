package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"flux/pkg/models"
	"flux/pkg/pki"
	pb "flux/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

type AgentClient struct {
	mu      sync.RWMutex
	clients map[string]pb.AgentServiceClient
	pki     *pki.PKI
}

func NewAgentClient(p *pki.PKI) *AgentClient {
	if p != nil {
		log.Printf("[grpc] client: mTLS (ca=%s cert=%s)", p.CACertPath(), p.FluxCertPath())
	} else {
		log.Printf("[grpc] client: TLS disabled")
	}
	return &AgentClient{
		clients: make(map[string]pb.AgentServiceClient),
		pki:     p,
	}
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

	var creds credentials.TransportCredentials
	if c.pki != nil {
		var err error
		creds, err = c.loadTLSCredentials()
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

func (c *AgentClient) loadTLSCredentials() (credentials.TransportCredentials, error) {
	cert, err := tls.LoadX509KeyPair(c.pki.FluxCertPath(), c.pki.FluxKeyPath())
	if err != nil {
		return nil, fmt.Errorf("load flux cert/key: %w", err)
	}

	caData, err := os.ReadFile(c.pki.CACertPath())
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

func (c *AgentClient) ExecuteFunction(ctx context.Context, agent *models.Agent, functionName string, args []string, executionID string, async bool) (*pb.ExecutionResponse, error) {
	cl, err := c.get(agent.Address)
	if err != nil {
		return nil, err
	}

	return cl.ExecuteFunction(ctx, &pb.ExecutionRequest{
		FunctionName: functionName,
		Args:         args,
		ExecutionId:  executionID,
		Async:        async,
	})
}

func (c *AgentClient) CancelExecution(agent *models.Agent, executionID string) error {
	cl, err := c.get(agent.Address)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := cl.CancelExecution(ctx, &pb.CancelExecutionRequest{ExecutionId: executionID})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("cancel failed: %s", resp.Message)
	}
	return nil
}

func (c *AgentClient) GetExecution(agent *models.Agent, executionID string) (*models.ExecutionRecord, error) {
	cl, err := c.get(agent.Address)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := cl.GetExecution(ctx, &pb.GetExecutionRequest{ExecutionId: executionID})
	if err != nil {
		return nil, err
	}
	if !resp.Found {
		return nil, nil
	}

	var record models.ExecutionRecord
	if err := json.Unmarshal(resp.Data, &record); err != nil {
		return nil, fmt.Errorf("decode execution record: %w", err)
	}
	return &record, nil
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
