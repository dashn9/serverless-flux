package scaler

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"flux/pkg/config"
	"flux/pkg/pki"

	"google.golang.org/api/compute/v1"
	"google.golang.org/api/option"
)

// GCPProvider implements CloudProvider for Google Cloud Platform.
// It is responsible only for GCE-specific operations: launching and terminating
// instances. SSH bootstrap is handled by the shared, provider-agnostic helper.
type GCPProvider struct {
	instances    *compute.InstancesService
	zoneOps      *compute.ZoneOperationsService
	bootstrapper *SSHBootstrapper
	pkiMgr       *pki.PKI
	seqNum       int
}

// NewGCPProvider creates a GCP cloud provider with a configured Compute Engine client.
func NewGCPProvider(pkiMgr *pki.PKI) (*GCPProvider, error) {
	cfg := config.Get().Providers.GCP

	opts := []option.ClientOption{option.WithScopes(compute.ComputeScope)}
	if cfg.CredentialsFile != "" {
		opts = append(opts, option.WithCredentialsFile(cfg.CredentialsFile))
	}

	svc, err := compute.NewService(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCE service: %w", err)
	}

	fluxCfg := config.Get()

	bootstrapper := NewSSHBootstrapper(BootstrapConfig{
		PKI:                pkiMgr,
		SSHUser:            cfg.SSHUser,
		AgentPort:          fluxCfg.AgentPort,
		RedisAddr:          fluxCfg.AgentRedisAddr(),
		AgentVersion:       cfg.AgentVersion,
		AgentSetupCommands: cfg.AgentSetupCommands,
	})

	return &GCPProvider{
		instances:    svc.Instances,
		zoneOps:      svc.ZoneOperations,
		bootstrapper: bootstrapper,
		pkiMgr:       pkiMgr,
	}, nil
}

func (g *GCPProvider) Name() string { return "gcp" }

// SpawnNode launches a GCE instance whose machine type satisfies the requested
// resources. The machine type is resolved from the operator-configured
// node_types list — no hardcoded catalog.
func (g *GCPProvider) SpawnNode(ctx context.Context, resources NodeResources) (*ProvisionedNode, error) {
	cfg := config.Get().Providers.GCP

	if cfg.Autoscaling == nil || len(cfg.Autoscaling.NodeTypes) == 0 {
		return nil, fmt.Errorf("no node_types configured")
	}
	machineType, err := selectInstanceType(cfg.Autoscaling.NodeTypes, resources)
	if err != nil {
		return nil, fmt.Errorf("instance selection failed: %w", err)
	}

	g.seqNum++
	agentID := fmt.Sprintf("auto-agent-%d-%d", time.Now().Unix(), g.seqNum)
	instanceName := "flux-agent-" + agentID

	log.Printf("[gcp] Selected %s for vcpus=%d memory_gb=%.1f (agent: %s)",
		machineType, resources.VCPUs, resources.MemoryGB, agentID)

	labels := map[string]string{
		"flux-role":     "agent",
		"flux-agent-id": agentID,
	}
	for k, v := range cfg.Labels {
		labels[k] = v
	}

	// Build the ssh-keys metadata value: "<user>:<public_key>"
	sshKeyEntry := fmt.Sprintf("%s:%s", cfg.SSHUser, strings.TrimSpace(string(g.pkiMgr.SSHPublicKey())))

	instance := &compute.Instance{
		Name:        instanceName,
		MachineType: fmt.Sprintf("zones/%s/machineTypes/%s", cfg.Zone, machineType),
		Disks: []*compute.AttachedDisk{
			{
				AutoDelete:       true,
				Boot:             true,
				InitializeParams: &compute.AttachedDiskInitializeParams{SourceImage: cfg.Image},
			},
		},
		NetworkInterfaces: []*compute.NetworkInterface{
			{
				AccessConfigs: []*compute.AccessConfig{{
					Name: "External NAT",
					Type: "ONE_TO_ONE_NAT",
				}},
			},
		},
		Labels: labels,
		Metadata: &compute.Metadata{
			Items: []*compute.MetadataItems{
				{Key: "ssh-keys", Value: &sshKeyEntry},
			},
		},
	}

	if cfg.ServiceAccountEmail != "" {
		instance.ServiceAccounts = []*compute.ServiceAccount{
			{
				Email:  cfg.ServiceAccountEmail,
				Scopes: []string{"https://www.googleapis.com/auth/cloud-platform"},
			},
		}
	}

	log.Printf("[gcp] Launching %s — image=%s zone=%s", machineType, cfg.Image, cfg.Zone)

	op, err := g.instances.Insert(cfg.ProjectID, cfg.Zone, instance).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("Insert: %w", err)
	}
	if err := g.waitForOp(ctx, op.Name); err != nil {
		return nil, fmt.Errorf("insert %s: %w", instanceName, err)
	}

	log.Printf("[gcp] Instance %s launched, waiting for IPs...", instanceName)

	publicIP, privateIP, err := g.waitForIPs(ctx, instanceName)
	if err != nil {
		return nil, fmt.Errorf("instance %s launched but IP wait failed: %w", instanceName, err)
	}

	log.Printf("[gcp] Instance %s ready — public=%s private=%s", instanceName, publicIP, privateIP)

	return &ProvisionedNode{
		ProviderID:   instanceName,
		AgentID:      agentID,
		InstanceType: machineType,
		PublicIP:     publicIP,
		PrivateIP:    privateIP,
	}, nil
}

// Bootstrap delegates to the shared SSHBootstrapper.
func (g *GCPProvider) Bootstrap(ctx context.Context, node *ProvisionedNode) error {
	return g.bootstrapper.Bootstrap(ctx, node)
}

// TerminateNode deletes the GCE instance with the given name.
func (g *GCPProvider) TerminateNode(ctx context.Context, providerID string) error {
	cfg := config.Get().Providers.GCP

	log.Printf("[gcp] Terminating instance %s", providerID)
	op, err := g.instances.Delete(cfg.ProjectID, cfg.Zone, providerID).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("Delete %s: %w", providerID, err)
	}
	if err := g.waitForOp(ctx, op.Name); err != nil {
		return fmt.Errorf("delete %s: %w", providerID, err)
	}
	log.Printf("[gcp] Instance %s terminated", providerID)
	return nil
}

// waitForOp polls a zone operation until it completes or the context is cancelled.
func (g *GCPProvider) waitForOp(ctx context.Context, opName string) error {
	cfg := config.Get().Providers.GCP

	for {
		op, err := g.zoneOps.Wait(cfg.ProjectID, cfg.Zone, opName).Context(ctx).Do()
		if err != nil {
			return fmt.Errorf("operation %s: %w", opName, err)
		}
		if op.Status == "DONE" {
			if op.Error != nil {
				return fmt.Errorf("operation %s failed: %v", opName, op.Error.Errors[0].Message)
			}
			return nil
		}
	}
}

// waitForIPs polls GCE until the instance has both a public and private IP.
func (g *GCPProvider) waitForIPs(ctx context.Context, instanceName string) (publicIP, privateIP string, err error) {
	cfg := config.Get().Providers.GCP

	timeout := time.After(3 * time.Minute)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			return "", "", fmt.Errorf("timed out waiting for IPs on %s", instanceName)
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-ticker.C:
			inst, err := g.instances.Get(cfg.ProjectID, cfg.Zone, instanceName).Context(ctx).Do()
			if err != nil {
				log.Printf("[gcp] Get instance error: %v", err)
				continue
			}
			for _, iface := range inst.NetworkInterfaces {
				priv := iface.NetworkIP
				for _, ac := range iface.AccessConfigs {
					if ac.NatIP != "" && priv != "" {
						return ac.NatIP, priv, nil
					}
				}
			}
		}
	}
}
