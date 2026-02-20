package scaler

import (
	"fmt"

	"flux/pkg/config"
)

// buildAgentYAML returns only the agent.yaml content for the given parameters.
// Used by SSHBootstrapper to write configuration directly to a remote node.
func buildAgentYAML(agentID string, port int, redisAddr string, tlsCfg *config.TLSConfig) string {
	tlsBlock := ""
	if tlsCfg != nil && tlsCfg.Enabled {
		tlsBlock = `
tls:
  enabled: true
  ca_cert: /opt/flux-agent/tls/ca.pem
  cert: /opt/flux-agent/tls/agent.pem
  key: /opt/flux-agent/tls/agent-key.pem`
	}
	return fmt.Sprintf("agent_id: %s\nport: \"%d\"\nredis_addr: \"%s\"%s\n",
		agentID, port, redisAddr, tlsBlock)
}

// buildAgentUserData returns a cloud-init bash script that writes agent.yaml
// and starts the flux-agent systemd service.
func buildAgentUserData(agentID string, port int, redisAddr string, tlsCfg *config.TLSConfig) string {
	return fmt.Sprintf(`#!/bin/bash
set -euo pipefail

mkdir -p /opt/flux-agent/tls

cat > /opt/flux-agent/agent.yaml <<'AGENTCFG'
%sAGENTCFG

systemctl enable flux-agent
systemctl start flux-agent
`, buildAgentYAML(agentID, port, redisAddr, tlsCfg))
}
