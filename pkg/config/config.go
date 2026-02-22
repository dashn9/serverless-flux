package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type FluxConfig struct {
	APIKey    string           `yaml:"api_key"`
	RedisAddr string           `yaml:"redis_addr"`
	AgentPort int              `yaml:"agent_port"`
	GRPC      *GRPCConfig      `yaml:"grpc"`
	Providers *ProvidersConfig `yaml:"providers,omitempty"`
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

// ProvidersConfig holds configuration for each supported cloud provider.
// Only providers with a non-nil entry are active.
type ProvidersConfig struct {
	AWS *AWSProviderConfig `yaml:"aws,omitempty"`
}

// AWSProviderConfig holds all AWS-specific settings plus the autoscaling
// configuration that applies to this provider's node fleet.
type AWSProviderConfig struct {
	Region          string `yaml:"region"`
	AMI             string `yaml:"ami"`
	SecurityGroupID string `yaml:"security_group_id"`

	AccessKeyID     string `yaml:"access_key_id,omitempty"`
	SecretAccessKey string `yaml:"secret_access_key,omitempty"`

	// SSHKeyName is the AWS EC2 key pair name injected into spawned nodes at launch.
	// Anyone holding the matching private key (SSHKeyPath) can SSH into the node.
	SSHKeyName string `yaml:"ssh_keyname,omitempty"`

	// SSHKeyPath is the local path to the private key (.pem) used to SSH into
	// newly provisioned instances for the bootstrap step.
	// If empty, provisioning relies entirely on user-data.
	SSHKeyPath string `yaml:"ssh_key_path,omitempty"`

	// SSHUser is the OS user to log in as during bootstrap (default: "ec2-user").
	SSHUser string `yaml:"ssh_user,omitempty"`

	// AgentVersion is the flux-agent release version to fetch from GitHub
	// Releases and install as a .deb on newly provisioned nodes (e.g. "0.1.0").
	AgentVersion string `yaml:"agent_version,omitempty"`

	Tags map[string]string `yaml:"tags,omitempty"`

	Autoscaling *AutoscaleConfig `yaml:"autoscaling,omitempty"`
}

// AutoscaleConfig controls autoscaling behaviour for a provider's node fleet.
type AutoscaleConfig struct {
	Enabled bool `yaml:"enabled"`

	// Name is a unique identifier for this autoscaler (required when enabled).
	Name string `yaml:"name"`

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

	// NodeTypes lists the instance types this autoscaler is allowed to launch.
	// At least one entry is required when autoscaling is enabled.
	NodeTypes []NodeTypeConfig `yaml:"node_types"`
}

// NodeTypeConfig maps a cloud provider instance type to its resource equivalents.
type NodeTypeConfig struct {
	InstanceType string  `yaml:"instance_type"`
	VCPUs        int     `yaml:"vcpus"`
	MemoryGB     float64 `yaml:"memory_gb"`
}

var store *FluxConfig

// Load reads and parses the config file, storing it in the global read-only store.
// Must be called once at startup before any Get calls.
func Load(path string) error {
	cfg, err := parse(path)
	if err != nil {
		return err
	}
	store = cfg
	return nil
}

// Get returns the global config. Panics if Load has not been called.
func Get() *FluxConfig {
	if store == nil {
		panic("config.Load must be called before config.Get")
	}
	return store
}

func parse(path string) (*FluxConfig, error) {
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

	if config.RedisAddr == "" {
		config.RedisAddr = "localhost:6379"
	}
	if config.AgentPort == 0 {
		config.AgentPort = 50052
	}

	if config.Providers != nil {
		if p := config.Providers.AWS; p != nil {
			if p.SSHUser == "" {
				p.SSHUser = "ec2-user"
			}
			if p.Autoscaling != nil {
				a := p.Autoscaling
				applyAutoscaleDefaults(a)
				if a.Enabled && a.Name == "" {
					return nil, fmt.Errorf("providers.aws.autoscaling.name is required when autoscaling is enabled")
				}
				if a.Enabled && len(a.NodeTypes) == 0 {
					return nil, fmt.Errorf("providers.aws.autoscaling.node_types must have at least one entry when autoscaling is enabled")
				}
			}
		}
	}

	return &config, nil
}

func applyAutoscaleDefaults(a *AutoscaleConfig) {
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
}
