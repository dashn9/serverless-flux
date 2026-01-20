package client

import (
	"context"
	"fmt"
	"time"

	pb "flux/proto"
	"flux/pkg/models"

	"google.golang.org/grpc"
)

type AgentClient struct{}

func NewAgentClient() *AgentClient {
	return &AgentClient{}
}

func (c *AgentClient) RegisterFunction(agent *models.Agent, function *models.Function) error {
	conn, err := grpc.NewClient(agent.Address, grpc.WithInsecure())
	if err != nil {
		return err
	}
	defer conn.Close()

	client := pb.NewAgentServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.RegisterFunction(ctx, &pb.FunctionConfig{
		Name:           function.Name,
		Handler:        function.Handler,
		CpuMillicores:  function.CPUMillicores,
		MemoryMb:       function.MemoryMB,
		TimeoutSeconds: function.TimeoutSec,
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
	conn, err := grpc.NewClient(agent.Address, grpc.WithInsecure())
	if err != nil {
		return err
	}
	defer conn.Close()

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

func (c *AgentClient) ExecuteFunction(agent *models.Agent, functionName string, input []byte) (*pb.ExecutionResponse, error) {
	conn, err := grpc.NewClient(agent.Address, grpc.WithInsecure())
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	client := pb.NewAgentServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	return client.ExecuteFunction(ctx, &pb.ExecutionRequest{
		FunctionName: functionName,
		Input:        input,
	})
}

func (c *AgentClient) HealthCheck(agent *models.Agent) error {
	conn, err := grpc.NewClient(agent.Address, grpc.WithInsecure())
	if err != nil {
		return err
	}
	defer conn.Close()

	client := pb.NewAgentServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err = client.HealthCheck(ctx, &pb.HealthCheckRequest{})
	return err
}
