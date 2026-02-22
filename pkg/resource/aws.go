package scaler

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"time"

	"flux/pkg/config"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// AWSProvider implements CloudProvider for Amazon Web Services.
// It is responsible only for EC2-specific operations: launching and terminating
// instances. SSH bootstrap and user-data generation are handled by shared,
// provider-agnostic helpers.
type AWSProvider struct {
	ec2Client    *ec2.Client
	bootstrapper *SSHBootstrapper // nil when ssh_key_path is not configured
	seqNum       int
}

// NewAWSProvider creates an AWS cloud provider with a configured EC2 client.
func NewAWSProvider() (*AWSProvider, error) {
	cfg := config.Get().Providers.AWS

	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
	}
	if cfg.AccessKeyID != "" && cfg.SecretAccessKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		))
	}

	sdkCfg, err := awsconfig.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	fluxCfg := config.Get()

	// Build the SSH bootstrapper only when a key path is provided.
	// If absent, provisioning relies entirely on user-data.
	bootstrapper := NewSSHBootstrapper(BootstrapConfig{
		SSHKeyPath:   cfg.SSHKeyPath,
		SSHUser:      cfg.SSHUser,
		AgentPort:    fluxCfg.AgentPort,
		RedisAddr:    fluxCfg.RedisAddr,
		AgentVersion: cfg.AgentVersion,
		AgentGRPC:    fluxCfg.GRPC.Agent,
	})

	return &AWSProvider{
		ec2Client:    ec2.NewFromConfig(sdkCfg),
		bootstrapper: bootstrapper,
	}, nil
}

func (a *AWSProvider) Name() string { return "aws" }

// SpawnNode launches an EC2 instance whose type satisfies the requested
// resources. The instance type is resolved from the operator-configured
// node_types list — no hardcoded catalog.
func (a *AWSProvider) SpawnNode(ctx context.Context, resources NodeResources) (*ProvisionedNode, error) {
	cfg := config.Get().Providers.AWS
	fluxCfg := config.Get()

	if cfg.Autoscaling == nil || len(cfg.Autoscaling.NodeTypes) == 0 {
		return nil, fmt.Errorf("no node_types configured")
	}
	instanceType, err := selectInstanceType(cfg.Autoscaling.NodeTypes, resources)
	if err != nil {
		return nil, fmt.Errorf("instance selection failed: %w", err)
	}

	a.seqNum++
	agentID := fmt.Sprintf("auto-agent-%d-%d", time.Now().Unix(), a.seqNum)

	log.Printf("[aws] Selected %s for vcpus=%d memory_gb=%.1f (agent: %s)",
		instanceType, resources.VCPUs, resources.MemoryGB, agentID)

	userData := buildAgentUserData(agentID, fluxCfg.AgentPort, fluxCfg.RedisAddr, cfg.AgentVersion, fluxCfg.GRPC.Agent)

	input := &ec2.RunInstancesInput{
		ImageId:      aws.String(cfg.AMI),
		InstanceType: types.InstanceType(instanceType),
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
		KeyName:      aws.String(cfg.KeyName),
		UserData:     aws.String(base64.StdEncoding.EncodeToString([]byte(userData))),

		NetworkInterfaces: []types.InstanceNetworkInterfaceSpecification{
			{
				DeviceIndex:              aws.Int32(0),
				SubnetId:                 aws.String(cfg.SubnetID),
				Groups:                   []string{cfg.SecurityGroupID},
				AssociatePublicIpAddress: aws.Bool(true),
			},
		},

		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeInstance,
				Tags: []types.Tag{
					{Key: aws.String("Name"), Value: aws.String("flux-agent-" + agentID)},
					{Key: aws.String("flux:role"), Value: aws.String("agent")},
					{Key: aws.String("flux:agent-id"), Value: aws.String(agentID)},
				},
			},
		},

		// Enforce IMDSv2 — token-based metadata only, no credential leakage.
		MetadataOptions: &types.InstanceMetadataOptionsRequest{
			HttpTokens:   types.HttpTokensStateRequired,
			HttpEndpoint: types.InstanceMetadataEndpointStateEnabled,
		},
	}

	log.Printf("[aws] Launching %s — ami=%s subnet=%s sg=%s",
		instanceType, cfg.AMI, cfg.SubnetID, cfg.SecurityGroupID)

	result, err := a.ec2Client.RunInstances(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("RunInstances: %w", err)
	}
	if len(result.Instances) == 0 {
		return nil, fmt.Errorf("RunInstances returned no instances")
	}

	instanceID := aws.ToString(result.Instances[0].InstanceId)
	log.Printf("[aws] Instance %s launched, waiting for IPs...", instanceID)

	publicIP, privateIP, err := a.waitForIPs(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("instance %s launched but IP wait failed: %w", instanceID, err)
	}

	log.Printf("[aws] Instance %s ready — public=%s private=%s", instanceID, publicIP, privateIP)

	return &ProvisionedNode{
		ProviderID:   instanceID,
		AgentID:      agentID,
		InstanceType: instanceType,
		PublicIP:     publicIP,
		PrivateIP:    privateIP,
	}, nil
}

// Bootstrap delegates to the shared SSHBootstrapper. If no SSH key is
// configured, it is a no-op and provisioning relies on user-data alone.
func (a *AWSProvider) Bootstrap(ctx context.Context, node *ProvisionedNode) error {
	if a.bootstrapper == nil {
		log.Printf("[aws] No ssh_key_path — skipping SSH bootstrap for %s (user-data only)", node.AgentID)
		return nil
	}
	return a.bootstrapper.Bootstrap(ctx, node)
}

// TerminateNode terminates the EC2 instance with the given instance ID.
func (a *AWSProvider) TerminateNode(ctx context.Context, providerID string) error {
	log.Printf("[aws] Terminating instance %s", providerID)
	_, err := a.ec2Client.TerminateInstances(ctx, &ec2.TerminateInstancesInput{
		InstanceIds: []string{providerID},
	})
	if err != nil {
		return fmt.Errorf("TerminateInstances %s: %w", providerID, err)
	}
	log.Printf("[aws] Instance %s terminated", providerID)
	return nil
}

// waitForIPs polls EC2 until the instance has both a public and private IP.
func (a *AWSProvider) waitForIPs(ctx context.Context, instanceID string) (publicIP, privateIP string, err error) {
	timeout := time.After(3 * time.Minute)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			return "", "", fmt.Errorf("timed out waiting for IPs on %s", instanceID)
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-ticker.C:
			out, err := a.ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
				InstanceIds: []string{instanceID},
			})
			if err != nil {
				log.Printf("[aws] DescribeInstances error: %v", err)
				continue
			}
			for _, r := range out.Reservations {
				for _, inst := range r.Instances {
					pub := aws.ToString(inst.PublicIpAddress)
					priv := aws.ToString(inst.PrivateIpAddress)
					if pub != "" && priv != "" {
						return pub, priv, nil
					}
				}
			}
		}
	}
}
