package scaler

import "context"

// ProvisionedNode represents a cloud node that was launched by a provider.
type ProvisionedNode struct {
	// ProviderID is the cloud-specific instance identifier (e.g. EC2 instance ID).
	ProviderID string

	// AgentID is the logical agent identifier registered in the flux registry.
	AgentID string

	// PublicIP is the publicly reachable IP address of the node.
	PublicIP string

	// PrivateIP is the internal IP address (may be identical to PublicIP).
	PrivateIP string
}

// NodeResources describes the desired compute resources for a node.
// The cloud provider resolves these into its own instance type internally —
// callers are resource-aware, not instance-type-aware.
type NodeResources struct {
	VCPUs    int     `yaml:"vcpus"`
	MemoryGB float64 `yaml:"memory_gb"`
}

// CloudProvider is the abstraction that cloud-specific implementations must
// satisfy. This keeps the autoscaler cloud-agnostic — only the concrete
// provider knows how to map resources to instance types, launch nodes,
// bootstrap them, and tear them down.
type CloudProvider interface {
	// Name returns the unique provider name (e.g. "aws", "gcp", "azure").
	Name() string

	// SpawnNode provisions a new compute node with at least the given resources.
	// The provider is responsible for mapping resources → instance type internally.
	SpawnNode(ctx context.Context, resources NodeResources) (*ProvisionedNode, error)

	// Bootstrap installs and starts the agent on a freshly provisioned node.
	// It is called after SpawnNode and may use SSH, cloud-init, or a
	// provider-specific mechanism. Implementations that rely solely on
	// user-data can return nil immediately.
	Bootstrap(ctx context.Context, node *ProvisionedNode) error

	// TerminateNode destroys the node identified by its provider-specific ID.
	TerminateNode(ctx context.Context, providerID string) error
}
