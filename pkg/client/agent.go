package client

import (
	"context"
	"fmt"
	"sync"
	"time"

	pb "flux/proto"
	"flux/pkg/models"

	"google.golang.org/grpc"
)

type AgentClient struct{
	mu    sync.RWMutex
	conns map[string]*grpc.ClientConn
}

func NewAgentClient() *AgentClient {
	return &AgentClient{
		conns: make(map[string]*grpc.ClientConn),
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

	conn, err := grpc.Dial(address,
		grpc.WithInsecure(),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(50*1024*1024), // 50MB
			grpc.MaxCallSendMsgSize(50*1024*1024), // 50MB
		))
	if err != nil {
		return nil, err
	}

	c.conns[address] = conn
	return conn, nil
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
		Name:           function.Name,
		Handler:        function.Handler,
		CpuMillicores:  function.CPUMillicores,
		MemoryMb:       function.MemoryMB,
		TimeoutSeconds: function.TimeoutSec,
		Env:            function.Env,
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
