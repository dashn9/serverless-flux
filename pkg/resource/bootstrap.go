package scaler

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"time"

	"flux/pkg/config"
	"golang.org/x/crypto/ssh"
)

// BootstrapConfig holds everything needed to install and start an agent on a
// freshly provisioned node via SSH. It is cloud-provider-agnostic — any
// provider can embed or instantiate an SSHBootstrapper with this config.
type BootstrapConfig struct {
	// SSH credentials
	SSHKeyPath string // local path to the private key (.pem / openssh)
	SSHUser    string // remote OS user (e.g. "ec2-user", "ubuntu")

	// Agent binary to upload. Empty means the binary is already present on
	// the node (pre-baked AMI or installed via user-data).
	AgentBinaryPath string

	// Agent runtime configuration written to the remote node.
	// AgentID is not stored here — it is taken from ProvisionedNode at bootstrap time.
	AgentPort      int
	RedisAddr      string
	MaxConcurrency int32

	// TLS — when non-nil and Enabled, CA + agent cert/key are deployed before
	// the agent service is started.
	TLS *config.TLSConfig
}

// SSHBootstrapper installs and starts the flux-agent on a remote node over SSH.
// It is intentionally provider-agnostic: the same bootstrapper works for any
// cloud or bare-metal node reachable via SSH.
type SSHBootstrapper struct {
	cfg BootstrapConfig
}

// NewSSHBootstrapper returns a bootstrapper for the given config.
// Returns nil if SSHKeyPath is empty (no SSH bootstrap configured).
func NewSSHBootstrapper(cfg BootstrapConfig) *SSHBootstrapper {
	if cfg.SSHKeyPath == "" {
		return nil
	}
	return &SSHBootstrapper{cfg: cfg}
}

// Bootstrap connects to node.PublicIP and:
//  1. Uploads the agent binary (if AgentBinaryPath is set).
//  2. Deploys mTLS certificates (if TLS is enabled).
//  3. Writes agent.yaml with the runtime config.
//  4. Starts (or restarts) the flux-agent systemd service.
func (b *SSHBootstrapper) Bootstrap(ctx context.Context, node *ProvisionedNode) error {
	log.Printf("[bootstrap] SSH bootstrap starting for %s at %s", node.AgentID, node.PublicIP)

	client, err := b.dial(ctx, node.PublicIP)
	if err != nil {
		return fmt.Errorf("SSH dial %s: %w", node.PublicIP, err)
	}
	defer client.Close()

	if err := b.run(client, "mkdir -p /opt/flux-agent/tls"); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	// Upload agent binary if provided.
	if b.cfg.AgentBinaryPath != "" {
		if err := b.upload(client, b.cfg.AgentBinaryPath, "/opt/flux-agent/flux-agent"); err != nil {
			return fmt.Errorf("upload binary: %w", err)
		}
		if err := b.run(client, "chmod +x /opt/flux-agent/flux-agent"); err != nil {
			return fmt.Errorf("chmod binary: %w", err)
		}
		log.Printf("[bootstrap] Agent binary uploaded to %s", node.PublicIP)
	}

	// Deploy mTLS certs when TLS is enabled.
	if b.cfg.TLS != nil && b.cfg.TLS.Enabled && b.cfg.TLS.AgentCert != "" {
		for _, transfer := range []struct{ local, remote string }{
			{b.cfg.TLS.CACert, "/opt/flux-agent/tls/ca.pem"},
			{b.cfg.TLS.AgentCert, "/opt/flux-agent/tls/agent.pem"},
			{b.cfg.TLS.AgentKey, "/opt/flux-agent/tls/agent-key.pem"},
		} {
			if err := b.upload(client, transfer.local, transfer.remote); err != nil {
				return fmt.Errorf("upload TLS file %s: %w", transfer.local, err)
			}
		}
		if err := b.run(client, "chmod 600 /opt/flux-agent/tls/*"); err != nil {
			return fmt.Errorf("chmod tls: %w", err)
		}
		log.Printf("[bootstrap] mTLS certs deployed to %s", node.PublicIP)
	}

	// Write agent.yaml on the remote node using node.AgentID from the provisioned instance.
	agentYAML := buildAgentYAML(node.AgentID, b.cfg.AgentPort, b.cfg.RedisAddr, b.cfg.MaxConcurrency, b.cfg.TLS)
	writeCmd := fmt.Sprintf("cat > /opt/flux-agent/agent.yaml <<'EOF'\n%sEOF", agentYAML)
	if err := b.run(client, writeCmd); err != nil {
		return fmt.Errorf("write agent.yaml: %w", err)
	}

	// Start the agent service.
	if err := b.run(client, "systemctl daemon-reload && systemctl enable flux-agent && systemctl restart flux-agent"); err != nil {
		return fmt.Errorf("start service: %w", err)
	}

	log.Printf("[bootstrap] Bootstrap complete for %s (%s)", node.AgentID, node.PublicIP)
	return nil
}

// dial opens an SSH connection, retrying until the host is reachable or the
// context is cancelled. Handles the common case of an instance still booting.
func (b *SSHBootstrapper) dial(ctx context.Context, host string) (*ssh.Client, error) {
	keyData, err := os.ReadFile(b.cfg.SSHKeyPath)
	if err != nil {
		return nil, fmt.Errorf("read SSH key %s: %w", b.cfg.SSHKeyPath, err)
	}
	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return nil, fmt.Errorf("parse SSH key: %w", err)
	}

	sshCfg := &ssh.ClientConfig{
		User:            b.cfg.SSHUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec — bootstrapping; no known_hosts
		Timeout:         10 * time.Second,
	}

	addr := net.JoinHostPort(host, "22")
	deadline := time.Now().Add(3 * time.Minute)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		client, err := ssh.Dial("tcp", addr, sshCfg)
		if err == nil {
			return client, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("SSH unreachable after 3 minutes: %w", err)
		}
		log.Printf("[bootstrap] SSH not ready at %s, retrying... (%v)", host, err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// run executes a shell command on the remote host and returns any error.
func (b *SSHBootstrapper) run(client *ssh.Client, cmd string) error {
	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	out, err := sess.CombinedOutput(cmd)
	if err != nil {
		return fmt.Errorf("command %q: %w\noutput: %s", cmd, err, string(out))
	}
	return nil
}

// upload copies a local file to a remote path by piping its contents over SSH.
func (b *SSHBootstrapper) upload(client *ssh.Client, localPath, remotePath string) error {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", localPath, err)
	}

	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()

	stdin, err := sess.StdinPipe()
	if err != nil {
		return err
	}

	if err := sess.Start(fmt.Sprintf("cat > %s", remotePath)); err != nil {
		return err
	}
	if _, err := stdin.Write(data); err != nil {
		return err
	}
	stdin.Close()
	return sess.Wait()
}
