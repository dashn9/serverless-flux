package scaler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"flux/pkg/config"
	"flux/pkg/pki"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

const ec2KeyPairName = "flux-agent"

// AWSProvider implements CloudProvider for Amazon Web Services.
// It is responsible only for EC2-specific operations: launching and terminating
// instances. SSH bootstrap and user-data generation are handled by shared,
// provider-agnostic helpers.
type AWSProvider struct {
	ec2Client    *ec2.Client
	bootstrapper *SSHBootstrapper
	seqNum       int
}

// NewAWSProvider creates an AWS cloud provider with a configured EC2 client.
// The PKI-managed SSH public key is auto-imported as an EC2 key pair.
func NewAWSProvider(pkiMgr *pki.PKI) (*AWSProvider, error) {
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

	ec2Client := ec2.NewFromConfig(sdkCfg)

	// Import the PKI-managed SSH public key to EC2.
	if err := importSSHKey(context.Background(), ec2Client, pkiMgr); err != nil {
		return nil, fmt.Errorf("import SSH key pair: %w", err)
	}

	fluxCfg := config.Get()

	bootstrapper := NewSSHBootstrapper(BootstrapConfig{
		PKI:                pkiMgr,
		SSHUser:            cfg.SSHUser,
		AgentPort:          fluxCfg.AgentPort,
		RedisAddr:          fluxCfg.RedisAddr,
		AgentVersion:       cfg.AgentVersion,
		AgentSetupCommands: cfg.AgentSetupCommands,
	})

	return &AWSProvider{
		ec2Client:    ec2Client,
		bootstrapper: bootstrapper,
	}, nil
}

func (a *AWSProvider) Name() string { return "aws" }

// SpawnNode launches an EC2 instance whose type satisfies the requested
// resources. The instance type is resolved from the operator-configured
// node_types list — no hardcoded catalog.
func (a *AWSProvider) SpawnNode(ctx context.Context, resources NodeResources) (*ProvisionedNode, error) {
	cfg := config.Get().Providers.AWS

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

	input := &ec2.RunInstancesInput{
		ImageId:      aws.String(cfg.AMI),
		InstanceType: types.InstanceType(instanceType),
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
		KeyName:      aws.String(ec2KeyPairName),
		// TODO: support explicit subnet/VPC targeting via config (subnet_id, vpc_id)
		// for environments that don't use the default VPC.
		SecurityGroupIds: []string{cfg.SecurityGroupID},

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

	log.Printf("[aws] Launching %s — ami=%s sg=%s (default VPC)",
		instanceType, cfg.AMI, cfg.SecurityGroupID)

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

// Bootstrap delegates to the shared SSHBootstrapper.
func (a *AWSProvider) Bootstrap(ctx context.Context, node *ProvisionedNode) error {
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

// importSSHKey imports the PKI-managed SSH public key as an EC2 key pair.
// If the key pair already exists, it is left as-is (idempotent).
func importSSHKey(ctx context.Context, client *ec2.Client, pkiMgr *pki.PKI) error {
	_, err := client.ImportKeyPair(ctx, &ec2.ImportKeyPairInput{
		KeyName:           aws.String(ec2KeyPairName),
		PublicKeyMaterial: pkiMgr.SSHPublicKey(),
	})
	if err != nil {
		// If the key pair already exists, that's fine.
		var apiErr interface{ ErrorCode() string }
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "InvalidKeyPair.Duplicate" {
			log.Printf("[aws] SSH key pair %q already exists", ec2KeyPairName)
			return nil
		}
		return err
	}
	log.Printf("[aws] SSH key pair %q imported", ec2KeyPairName)
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
