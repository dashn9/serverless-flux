package scaler

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"time"

	"flux/pkg/pki"

	"golang.org/x/crypto/ssh"
)

const agentDebURLTemplate = "https://github.com/dashn9/serverless-agent/releases/download/v%s/flux-agent_%s_amd64.deb"

// buildAgentYAML returns the agent.yaml content for the given node.
func buildAgentYAML(agentID string, port int, redisAddr string, configDir string) string {
	tlsBlock := fmt.Sprintf("\ntls:\n  enabled: true\n  ca_cert: %s/tls/ca.pem\n  cert_file: %s/tls/agent.pem\n  key_file: %s/tls/agent-key.pem\n",
		configDir, configDir, configDir)
	return fmt.Sprintf("agent_id: %s\nport: \"%d\"\nredis_addr: \"%s\"\n%s", agentID, port, redisAddr, tlsBlock)
}

// BootstrapConfig holds everything needed to configure and start an agent on a
// freshly provisioned node via SSH. It is cloud-provider-agnostic.
type BootstrapConfig struct {
	// PKI provides the SSH private key for dialling and mints per-agent TLS certs.
	PKI *pki.PKI

	// SSH credentials
	SSHUser string // remote OS user (e.g. "ec2-user", "ubuntu")

	// Agent runtime configuration written to the remote node.
	AgentPort int
	RedisAddr string

	// AgentVersion is used to download the .deb from GitHub Releases.
	AgentVersion string

	// AgentSetupCommands is a list of shell commands to run before agent install.
	AgentSetupCommands []string
}

// SSHBootstrapper installs and starts the flux-agent on a remote node over SSH.
// It is intentionally provider-agnostic: the same bootstrapper works for any
// cloud or bare-metal node reachable via SSH.
type SSHBootstrapper struct {
	cfg BootstrapConfig
}

// NewSSHBootstrapper returns a bootstrapper for the given config.
func NewSSHBootstrapper(cfg BootstrapConfig) *SSHBootstrapper {
	return &SSHBootstrapper{cfg: cfg}
}

// Bootstrap connects to node.PublicIP and:
//  1. Downloads and installs the flux-agent .deb (if AgentVersion is set).
//  2. Mints and uploads mTLS certificates for the agent.
//  3. Writes agent.yaml with the runtime config.
//  4. Enables and starts the flux-agent systemd service.
func (b *SSHBootstrapper) Bootstrap(ctx context.Context, node *ProvisionedNode) error {
	log.Printf("[bootstrap] SSH bootstrap starting for %s at %s", node.AgentID, node.PublicIP)

	client, err := b.dial(ctx, node.PublicIP)
	if err != nil {
		return fmt.Errorf("SSH dial %s: %w", node.PublicIP, err)
	}
	defer client.Close()

	// Run setup commands before agent install.
	for i, cmd := range b.cfg.AgentSetupCommands {
		log.Printf("[bootstrap] Running setup command %d/%d on %s", i+1, len(b.cfg.AgentSetupCommands), node.PublicIP)
		if err := b.run(client, cmd); err != nil {
			return fmt.Errorf("setup command %d: %w", i+1, err)
		}
	}

	// Download and install the agent .deb if a version is configured.
	if b.cfg.AgentVersion != "" {
		debURL := fmt.Sprintf(agentDebURLTemplate, b.cfg.AgentVersion, b.cfg.AgentVersion)
		installCmd := fmt.Sprintf(
			`curl --connect-timeout 10 --max-time 30 -fLo /tmp/flux-agent.deb %q && sudo dpkg -i /tmp/flux-agent.deb && rm -f /tmp/flux-agent.deb`, debURL)
		log.Printf("[bootstrap] Installing flux-agent v%s on %s", b.cfg.AgentVersion, node.PublicIP)
		if err := b.run(client, installCmd); err != nil {
			return fmt.Errorf("install agent: %w", err)
		}
	}

	configDir := "/home/" + b.cfg.SSHUser + "/flux-agent"

	if err := b.run(client, fmt.Sprintf("sudo mkdir -p %s/tls", configDir)); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	// Mint and upload agent TLS certs.
	certPEM, keyPEM, caPEM, err := b.cfg.PKI.MintAgentCert(node.AgentID)
	if err != nil {
		return fmt.Errorf("mint agent cert: %w", err)
	}
	for _, t := range []struct {
		data   []byte
		remote string
	}{
		{caPEM, configDir + "/tls/ca.pem"},
		{certPEM, configDir + "/tls/agent.pem"},
		{keyPEM, configDir + "/tls/agent-key.pem"},
	} {
		if err := b.uploadBytes(client, t.data, t.remote); err != nil {
			return fmt.Errorf("upload TLS %s: %w", t.remote, err)
		}
	}
	if err := b.run(client, fmt.Sprintf("sudo chmod 600 %s/tls/*", configDir)); err != nil {
		return fmt.Errorf("chmod tls: %w", err)
	}
	log.Printf("[bootstrap] Agent TLS certs deployed to %s", node.PublicIP)

	// Write agent.yaml.
	agentYAML := buildAgentYAML(node.AgentID, b.cfg.AgentPort, b.cfg.RedisAddr, configDir)
	writeCmd := fmt.Sprintf("sudo tee %s/agent.yaml > /dev/null <<'EOF'\n%sEOF", configDir, agentYAML)
	if err := b.run(client, writeCmd); err != nil {
		return fmt.Errorf("write agent.yaml: %w", err)
	}

	// Set AGENT_CONFIG for the systemd service via a drop-in override.
	dropInCmd := fmt.Sprintf(
		`sudo mkdir -p /etc/systemd/system/flux-agent.service.d && printf '[Service]\nEnvironment="AGENT_CONFIG=%s/agent.yaml"\n' | sudo tee /etc/systemd/system/flux-agent.service.d/override.conf > /dev/null`,
		configDir,
	)
	if err := b.run(client, dropInCmd); err != nil {
		return fmt.Errorf("write systemd env: %w", err)
	}

	// Restart the agent service with the new config.
	if err := b.run(client, "sudo systemctl daemon-reload && sudo systemctl enable flux-agent && sudo systemctl restart flux-agent"); err != nil {
		return fmt.Errorf("start service: %w", err)
	}

	log.Printf("[bootstrap] Bootstrap complete for %s (%s)", node.AgentID, node.PublicIP)
	return nil
}

// dial opens an SSH connection using the PKI-managed SSH key, retrying until
// the host is reachable or the context is cancelled.
func (b *SSHBootstrapper) dial(ctx context.Context, host string) (*ssh.Client, error) {
	keyData, err := os.ReadFile(b.cfg.PKI.SSHPrivateKeyPath())
	if err != nil {
		return nil, fmt.Errorf("read SSH key %s: %w", b.cfg.PKI.SSHPrivateKeyPath(), err)
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

// uploadBytes writes raw bytes to a remote path over SSH.
func (b *SSHBootstrapper) uploadBytes(client *ssh.Client, data []byte, remotePath string) error {
	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()

	stdin, err := sess.StdinPipe()
	if err != nil {
		return err
	}

	if err := sess.Start(fmt.Sprintf("sudo tee %s > /dev/null", remotePath)); err != nil {
		return err
	}
	if _, err := stdin.Write(data); err != nil {
		return err
	}
	stdin.Close()
	return sess.Wait()
}
