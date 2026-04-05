package memory

import "flux/pkg/models"

// Memory is the interface for persisting state
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

	// Execution log operations
	SaveExecution(record *models.ExecutionRecord) error
	GetExecution(executionID string) (*models.ExecutionRecord, error)
	GetExecutionLogs(executionID string) (string, error)

	// Close the storage connection
	Close() error
}
