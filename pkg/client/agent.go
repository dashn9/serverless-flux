package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
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
	mu     sync.RWMutex
	conns  map[string]*grpc.ClientConn
	tlsCfg *config.TLSConfig
}

// NewAgentClient creates a client that uses insecure (plaintext) gRPC.
func NewAgentClient() *AgentClient {
	return &AgentClient{
		conns: make(map[string]*grpc.ClientConn),
	}
}

// NewAgentClientTLS creates a client that authenticates agents via mTLS.
// Flux presents its own cert/key and verifies agents against the CA.
func NewAgentClientTLS(tlsCfg *config.TLSConfig) *AgentClient {
	return &AgentClient{
		conns:  make(map[string]*grpc.ClientConn),
		tlsCfg: tlsCfg,
	}
}

func (c *AgentClient) getConn(address string) (*grpc.ClientConn, error) {
	c.mu.RLock()
	conn, exists := c.conns[address]
	c.mu.RUnlock()

	if exists {
		return conn, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock
	if conn, exists := c.conns[address]; exists {
		return conn, nil
	}

	var creds credentials.TransportCredentials

	if c.tlsCfg != nil && c.tlsCfg.Enabled {
		var err error
		creds, err = loadFluxTLSCredentials(c.tlsCfg)
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

	c.conns[address] = conn
	return conn, nil
}

// loadFluxTLSCredentials builds mTLS client credentials for Flux connecting to agents.
// Flux presents its cert/key and verifies the agent's cert against the CA.
func loadFluxTLSCredentials(tlsCfg *config.TLSConfig) (credentials.TransportCredentials, error) {
	cert, err := tls.LoadX509KeyPair(tlsCfg.CertFile, tlsCfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load flux cert/key: %w", err)
	}

	caData, err := os.ReadFile(tlsCfg.CACert)
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
	}
	return credentials.NewTLS(cfg), nil
}

func (c *AgentClient) RegisterFunction(agent *models.Agent, function *models.Function) error {
	conn, err := c.getConn(agent.Address)
	if err != nil {
		return err
	}

	client := pb.NewAgentServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.RegisterFunction(ctx, &pb.FunctionConfig{
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
	conn, err := c.getConn(agent.Address)
	if err != nil {
		return err
	}

	client := pb.NewAgentServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := client.DeployFunction(ctx, &pb.DeploymentPackage{
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
	conn, err := c.getConn(agent.Address)
	if err != nil {
		return nil, err
	}

	client := pb.NewAgentServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	return client.ExecuteFunction(ctx, &pb.ExecutionRequest{
		FunctionName: functionName,
		Args:         args,
	})
}

func (c *AgentClient) HealthCheck(agent *models.Agent) error {
	conn, err := c.getConn(agent.Address)
	if err != nil {
		return err
	}

	client := pb.NewAgentServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err = client.HealthCheck(ctx, &pb.HealthCheckRequest{})
	return err
}

// GetNodeStatus fetches live CPU/memory metrics from the given agent.
func (c *AgentClient) GetNodeStatus(agent *models.Agent) (*models.NodeStatus, error) {
	conn, err := c.getConn(agent.Address)
	if err != nil {
		return nil, err
	}

	client := pb.NewAgentServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := client.ReportNodeStatus(ctx, &pb.NodeStatusRequest{})
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
		MaxTasks:    resp.MaxTasks,
		UptimeSec:   resp.UptimeSeconds,
		CollectedAt: time.Now(),
	}, nil
}
