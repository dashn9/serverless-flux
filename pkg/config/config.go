package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type FluxConfig struct {
	APIKey      string           `yaml:"api_key"`
	RedisAddr   string           `yaml:"redis_addr"`
	AgentPort   int              `yaml:"agent_port"`
	GRPC        *GRPCConfig      `yaml:"grpc"`
	Autoscaling *AutoscaleConfig `yaml:"autoscaling,omitempty"`
}

// GRPCConfig controls how Flux dials agents over gRPC.
// TLS is required unless Insecure is explicitly set to true.
type GRPCConfig struct {
	Insecure bool             `yaml:"insecure"`
	CACert   string           `yaml:"ca_cert,omitempty"`
	CertFile string           `yaml:"cert,omitempty"`
	KeyFile  string           `yaml:"key,omitempty"`
	Agent    *AgentGRPCConfig `yaml:"agent,omitempty"`
}

// AgentGRPCConfig holds the LOCAL paths (on the Flux host) to the agent's
// TLS cert files. During SSH bootstrap these are uploaded to each node
// alongside agent.yaml. The agent.yaml references the standard on-node paths.
type AgentGRPCConfig struct {
	CACert   string `yaml:"ca_cert"`
	CertFile string `yaml:"cert"`
	KeyFile  string `yaml:"key"`
}

// AutoscaleConfig controls the autoscaling behaviour.
type AutoscaleConfig struct {
	Enabled bool `yaml:"enabled"`

	// Name is a unique identifier for this provider configuration.
	// Required when autoscaling is enabled; must be unique across all configs.
	Name string `yaml:"name"`

	// Provider is the cloud provider to use for scaling ("aws").
	Provider string `yaml:"provider"`

	// CPUUpperThreshold triggers scale-up when CPU is sustained above this (default: 80).
	CPUUpperThreshold float64 `yaml:"cpu_upper_threshold"`

	// CPULowerThreshold triggers scale-down when CPU is sustained below this (default: 20).
	CPULowerThreshold float64 `yaml:"cpu_lower_threshold"`

	// MemUpperThreshold triggers scale-up when memory is sustained above this (default: 80).
	MemUpperThreshold float64 `yaml:"mem_upper_threshold"`

	// MemLowerThreshold triggers scale-down when memory is sustained below this (default: 20).
	MemLowerThreshold float64 `yaml:"mem_lower_threshold"`

	// EvaluationWindowSec is how many seconds metrics must stay past threshold (default: 60).
	EvaluationWindowSec int `yaml:"evaluation_window_sec"`

	// PollIntervalSec is how often (seconds) node metrics are collected (default: 10).
	PollIntervalSec int `yaml:"poll_interval_sec"`

	// CooldownSec prevents another scale event within this period (default: 300).
	CooldownSec int `yaml:"cooldown_sec"`

	// MaxNodes is the upper limit of total nodes (default: 10).
	MaxNodes int `yaml:"max_nodes"`

	// MinNodes is the lower limit — autoscaler will never scale below this (default: 1).
	MinNodes int `yaml:"min_nodes"`

	// NodeTypes lists the instance types this autoscaler is allowed to launch,
	// each annotated with its vCPU and memory equivalent. The autoscaler picks
	// the tightest-fit entry when spawning a new node.
	// At least one entry is required when autoscaling is enabled.
	NodeTypes []NodeTypeConfig `yaml:"node_types"`

	// AWS-specific configuration.
	AWS *AWSConfig `yaml:"aws,omitempty"`
}

// NodeTypeConfig maps a cloud provider instance type to its resource equivalents.
type NodeTypeConfig struct {
	InstanceType string  `yaml:"instance_type"`
	VCPUs        int     `yaml:"vcpus"`
	MemoryGB     float64 `yaml:"memory_gb"`
}

// AWSConfig holds AWS-specific settings for the autoscaler.
type AWSConfig struct {
	Region          string `yaml:"region"`
	AMI             string `yaml:"ami"`
	KeyName         string `yaml:"key_name"`
	SubnetID        string `yaml:"subnet_id"`
	SecurityGroupID string `yaml:"security_group_id"`

	AccessKeyID     string `yaml:"access_key_id,omitempty"`
	SecretAccessKey string `yaml:"secret_access_key,omitempty"`

	// SSHKeyPath is the local path to the private key (.pem) used to SSH into
	// newly provisioned instances for the bootstrap step.
	// If empty, provisioning relies entirely on user-data.
	SSHKeyPath string `yaml:"ssh_key_path,omitempty"`

	// SSHUser is the OS user to log in as during bootstrap (default: "ec2-user").
	SSHUser string `yaml:"ssh_user,omitempty"`

	// AgentVersion is the flux-agent release version to fetch from GitHub
	// Releases and install as a .deb on newly provisioned nodes (e.g. "0.1.0").
	AgentVersion string `yaml:"agent_version,omitempty"`
}

func LoadFluxConfig(path string) (*FluxConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var config FluxConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	if config.APIKey == "" {
		panic("api_key is required in flux.yaml")
	}

	if config.GRPC == nil {
		panic("grpc section is required in flux.yaml — set insecure: true to disable TLS")
	}
	if !config.GRPC.Insecure && (config.GRPC.CACert == "" || config.GRPC.CertFile == "" || config.GRPC.KeyFile == "") {
		panic("grpc requires ca_cert, cert, and key — or set insecure: true")
	}

	// Defaults
	if config.AgentPort == 0 {
		config.AgentPort = 50052
	}

	if config.Autoscaling != nil {
		a := config.Autoscaling

		if a.Enabled && a.Name == "" {
			return nil, fmt.Errorf("autoscaling.name is required when autoscaling is enabled")
		}

		if a.CPUUpperThreshold == 0 {
			a.CPUUpperThreshold = 80.0
		}
		if a.CPULowerThreshold == 0 {
			a.CPULowerThreshold = 20.0
		}
		if a.MemUpperThreshold == 0 {
			a.MemUpperThreshold = 80.0
		}
		if a.MemLowerThreshold == 0 {
			a.MemLowerThreshold = 20.0
		}
		if a.EvaluationWindowSec == 0 {
			a.EvaluationWindowSec = 60
		}
		if a.PollIntervalSec == 0 {
			a.PollIntervalSec = 10
		}
		if a.CooldownSec == 0 {
			a.CooldownSec = 300
		}
		if a.MaxNodes == 0 {
			a.MaxNodes = 10
		}
		if a.MinNodes == 0 {
			a.MinNodes = 1
		}
		if a.Enabled && len(a.NodeTypes) == 0 {
			return nil, fmt.Errorf("autoscaling.node_types must have at least one entry when autoscaling is enabled")
		}
		if a.AWS != nil && a.AWS.SSHUser == "" {
			a.AWS.SSHUser = "ec2-user"
		}
	}

	return &config, nil
}
