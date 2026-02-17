package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type FluxConfig struct {
	RedisAddr   string           `yaml:"redis_addr"`
	AgentPort   int              `yaml:"agent_port"`
	Agents      []AgentConfig    `yaml:"agents"`
	TLS         *TLSConfig       `yaml:"tls,omitempty"`
	Autoscaling *AutoscaleConfig `yaml:"autoscaling,omitempty"`
}

type AgentConfig struct {
	ID             string `yaml:"id"`
	Address        string `yaml:"address"`
	MaxConcurrency int32  `yaml:"max_concurrency"`
	// PreRegistered marks this agent as declared-but-not-yet-online.
	// Flux will not route work to it until health checks succeed.
	PreRegistered bool `yaml:"pre_registered,omitempty"`
}

// TLSConfig enables mTLS between Flux and Agents.
type TLSConfig struct {
	Enabled  bool   `yaml:"enabled"`
	CACert   string `yaml:"ca_cert"` // Path to CA certificate
	CertFile string `yaml:"cert"`    // Path to Flux certificate
	KeyFile  string `yaml:"key"`     // Path to Flux private key

	// Agent certificates — deployed to new nodes during provisioning.
	AgentCert string `yaml:"agent_cert"` // Path to agent certificate
	AgentKey  string `yaml:"agent_key"`  // Path to agent private key
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

	// MaxConcurrency is the max_concurrency to assign to dynamically spawned agents.
	MaxConcurrency int32 `yaml:"max_concurrency"`

	// AWS-specific configuration.
	AWS *AWSConfig `yaml:"aws,omitempty"`
}

// NodeTypeConfig maps a cloud provider instance type to its resource equivalents.
// The operator lists every instance type the autoscaler is allowed to launch,
// annotated with how many vCPUs and how much memory it provides.
// When scaling up, the autoscaler picks the tightest-fit entry from this list.
type NodeTypeConfig struct {
	// InstanceType is the cloud-provider identifier (e.g. "c5.xlarge" on AWS).
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

	// IAM instance profile for the agent nodes.
	IAMInstanceProfile string `yaml:"iam_instance_profile"`

	// SSHKeyPath is the local path to the private key (.pem) used to SSH into
	// newly provisioned instances for the bootstrap step.
	// If empty, bootstrap falls back to user-data only.
	SSHKeyPath string `yaml:"ssh_key_path,omitempty"`

	// SSHUser is the OS user to log in as during bootstrap (default: "ec2-user").
	SSHUser string `yaml:"ssh_user,omitempty"`

	// AgentBinaryPath is the local path to the compiled agent binary to upload
	// during SSH bootstrap. If empty, bootstrap assumes the binary is already
	// present on the AMI (e.g. pre-baked) or installed via user-data.
	AgentBinaryPath string `yaml:"agent_binary_path,omitempty"`
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
		if a.MaxConcurrency == 0 {
			a.MaxConcurrency = 10
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
