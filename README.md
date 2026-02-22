# Flux

Lightweight serverless platform. Flux is the control plane; agents are the workers. Agents run functions natively (no Docker) using cgroups v2 for memory isolation.

## Architecture

```
Client
  │  HTTP :7227
  ▼
Flux (control plane)
  │  gRPC (mTLS optional)
  ├─► Agent #1
  ├─► Agent #2
  └─► Agent #3
```

- **Flux** routes requests, manages the agent registry, and coordinates autoscaling.
- **Agents** execute functions inside isolated processes with cgroup memory limits.
- **Redis** persists agent and function state across restarts.
- **Config** is a global read-only store — loaded once at startup from `flux.yaml`, never passed through function parameters.

## Configuration

Copy `example.flux.yaml` to `flux.yaml` and edit:

```yaml
api_key: your-secret-key

redis_addr: localhost:6379
agent_port: 50052

grpc:
  insecure: true          # set false + provide certs for production

providers:
  aws:
    region: us-east-1
    ami: ami-...
    ssh_keyname: my-key
    security_group_id: sg-...
    ssh_key_path: ~/.ssh/key.pem
    agent_version: "0.1.0"   # installs .deb from GitHub Releases

    autoscaling:
      enabled: true
      name: aws-prod
      min_nodes: 1
      max_nodes: 10
      node_types:
        - instance_type: c5.large
          vcpus: 2
          memory_gb: 4
        - instance_type: c5.xlarge
          vcpus: 4
          memory_gb: 8
      cpu_upper_threshold: 80
      cpu_lower_threshold: 20
      cooldown_sec: 300
```

### mTLS (production)

```yaml
grpc:
  insecure: false
  ca_cert: certs/ca.pem
  cert:    certs/flux.pem
  key:     certs/flux.key
  agent:                    # uploaded to each node during SSH bootstrap
    ca_cert: certs/ca.pem
    cert:    certs/agent.pem
    key:     certs/agent.key
```

Generate certs:
```bash
# CA
openssl genrsa -out certs/ca.key 4096
openssl req -new -x509 -days 3650 -key certs/ca.key -out certs/ca.pem -subj "/CN=flux-ca"

# Flux cert
openssl genrsa -out certs/flux.key 2048
openssl req -new -key certs/flux.key -out flux.csr -subj "/CN=flux"
openssl x509 -req -in flux.csr -CA certs/ca.pem -CAkey certs/ca.key -CAcreateserial -out certs/flux.pem -days 365

# Agent cert (CN must be "flux-agent")
openssl genrsa -out certs/agent.key 2048
openssl req -new -key certs/agent.key -out agent.csr -subj "/CN=flux-agent"
openssl x509 -req -in agent.csr -CA certs/ca.pem -CAkey certs/ca.key -CAcreateserial -out certs/agent.pem -days 365
```

## Quick Start

```bash
# 1. Start Redis
redis-server

# 2. Start an agent (see ../agent)
AGENT_CONFIG=agent.yaml go run main.go

# 3. Start Flux
go run main.go
```

## API

### Public
| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/health` | Health check |
| `GET` | `/agents` | List all agents with node status |

### Protected (require `X-API-Key` header)
| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/initialize` | Spawn min nodes for each configured provider |
| `POST` | `/nodes/register` | Self-register an agent (called by agent on boot) |
| `PUT` | `/functions` | Register a function (YAML body) |
| `PUT` | `/deploy/{name}` | Deploy function code (zip body) |
| `POST` | `/execute/{name}` | Execute a function |

### Function YAML

```yaml
name: hello-world
handler: main
resources:
  cpu: 100        # millicores
  memory: 128     # MB
timeout: 30       # seconds
max_concurrency: 5
max_concurrency_behavior: exit   # or "wait"
env:
  FOO: bar
```

### Execute

```bash
curl -X POST http://localhost:7227/execute/hello-world \
  -H "X-API-Key: your-secret-key" \
  -H "Content-Type: application/json" \
  -d '{"args": ["arg1"]}'
```

Returns `429` when no agent has sufficient resources to run the function.

## Autoscaling

The autoscaler runs autonomously — it is started by `ProvidersManager` and holds no external references after that.

**Scale-up:** triggered when every online agent has sustained CPU ≥ `cpu_upper_threshold` (or mem ≥ `mem_upper_threshold`) for the full `evaluation_window_sec`. Spawns the smallest configured `node_type` first; at `max_nodes`, upgrades an idle node to the largest type instead.

**Scale-down:** triggered when every agent has sustained CPU ≤ `cpu_lower_threshold` and mem ≤ `mem_lower_threshold`. Terminates the least-loaded idle managed node.

**Cooldown** (`cooldown_sec`) prevents back-to-back events. Operator can tune thresholds and cooldown to control responsiveness vs. cost.

## Node Provisioning Flow

1. `POST /initialize` → `ProvidersManager.InitializeNodes` → spawns `min_nodes - current` nodes concurrently.
2. Each spawn: `CloudProvider.SpawnNode` (EC2 RunInstances) → waits for IPs → `RegisterOfflineAgent`.
3. If `ssh_key_path` is set: SSH bootstrap uploads `agent.yaml` + TLS certs → restarts `flux-agent` service.
4. If only `agent_version` is set: user-data script downloads the `.deb` from GitHub Releases, writes `agent.yaml`, starts service.
5. Autoscaler probes offline agents on every poll tick; first successful contact promotes them to online.

## Agent Self-Registration

Agents call `POST /nodes/register` on startup with their ID and gRPC address. This is how statically configured agents come online — Flux does not push registration.
