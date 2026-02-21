package scaler

import (
	"fmt"

	"flux/pkg/config"
)

const agentDebURLTemplate = "https://github.com/wraithbytes/serverless-fabric/releases/download/v%s/flux-agent_%s_amd64.deb"

// configDirFor returns the directory where agent.yaml and tls/ live.
// .deb installs use /etc/flux-agent; manual binary installs use /opt/flux-agent.
func configDirFor(agentVersion string) string {
	if agentVersion != "" {
		return "/etc/flux-agent"
	}
	return "/opt/flux-agent"
}

// buildAgentYAML returns the agent.yaml content for the given node.
// If agentGRPC is set, a TLS block is included referencing the standard
// on-node cert paths (uploaded separately during SSH bootstrap).
func buildAgentYAML(agentID string, port int, redisAddr string, configDir string, agentGRPC *config.AgentGRPCConfig) string {
	tlsBlock := ""
	if agentGRPC != nil {
		tlsBlock = fmt.Sprintf("\ntls:\n  enabled: true\n  ca_cert: %s/tls/ca.pem\n  cert: %s/tls/agent.pem\n  key: %s/tls/agent-key.pem\n",
			configDir, configDir, configDir)
	}
	return fmt.Sprintf("agent_id: %s\nport: \"%d\"\nredis_addr: \"%s\"\n%s",
		agentID, port, redisAddr, tlsBlock)
}

// buildAgentUserData returns a cloud-init bash script that optionally
// downloads and installs the flux-agent .deb, writes agent.yaml, and starts
// the systemd service.
func buildAgentUserData(agentID string, port int, redisAddr string, agentVersion string, agentGRPC *config.AgentGRPCConfig) string {
	configDir := configDirFor(agentVersion)

	installBlock := ""
	if agentVersion != "" {
		debURL := fmt.Sprintf(agentDebURLTemplate, agentVersion, agentVersion)
		installBlock = fmt.Sprintf(`
# Download and install flux-agent .deb from GitHub Releases
wget -q -O /tmp/flux-agent.deb %q
dpkg -i /tmp/flux-agent.deb
rm -f /tmp/flux-agent.deb
`, debURL)
	}

	return fmt.Sprintf(`#!/bin/bash
set -euo pipefail

mkdir -p %s
%s
cat > %s/agent.yaml <<'AGENTCFG'
%sAGENTCFG

systemctl enable flux-agent
systemctl start flux-agent
`, configDir, installBlock, configDir, buildAgentYAML(agentID, port, redisAddr, configDir, agentGRPC))
}
