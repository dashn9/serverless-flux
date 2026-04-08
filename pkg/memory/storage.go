package memory

import "flux/pkg/models"

// Memory is the interface for persisting and routing Flux state.
type Memory interface {
	// Function operations
	SaveFunction(function *models.Function) error
	GetFunction(name string) (*models.Function, error)
	GetAllFunctions() ([]*models.Function, error)

	// Code archive operations
	SaveCodeArchive(functionName string, data []byte) error
	GetCodeArchive(functionName string) ([]byte, error)

	// Agent operations
	SaveAgent(agent *models.Agent) error
	GetAgent(id string) (*models.Agent, error)
	GetAllAgents() ([]*models.Agent, error)
	DeleteAgent(id string) error

	// ExecutionToAgentMap — maps executionID to the agent that owns it.
	// Written by Flux at async dispatch; used to route GetExecution and
	// CancelExecution to the correct agent without touching the agent's Redis.
	SaveExecutionToAgentMap(executionID, agentID string) error
	GetExecutionToAgentMap(executionID string) (agentID string, err error)

	// Close the storage connection
	Close() error
}
