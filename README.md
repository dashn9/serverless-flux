# Flux

Flux is the control plane for Serverless Fabric, a lightweight serverless platform that executes functions natively on Linux using cgroups v2 for memory isolation — no Docker required. Flux routes execution requests to worker [agents](https://github.com/dashn9/serverless-agent), manages the agent registry, and coordinates autoscaling across AWS EC2 and GCP Compute Engine. When provisioning new nodes, Flux installs the matching agent version from its own GitHub Releases.

## Architecture

```
Client
  │  HTTP :7227
  ▼
Flux (control plane)
  │  gRPC (mTLS)
  ├─► Agent #1
  ├─► Agent #2
  └─► Agent #3
```

- **Flux** routes requests, manages the agent registry, and coordinates autoscaling.
- **Agents** execute functions inside isolated processes with cgroup memory limits.
- **Redis** persists agent and function state across restarts.
- **Config** is a global read-only store — loaded once at startup from `flux.yaml`, never passed through function parameters.

## Install from .deb

Download the latest release from [GitHub Releases](https://github.com/dashn9/serverless-flux/releases):

```bash
wget https://github.com/dashn9/serverless-flux/releases/download/v0.1.0/flux_0.1.0_amd64.deb
sudo dpkg -i flux_0.1.0_amd64.deb
```

The package installs a systemd service and places an example config at `/etc/flux/flux.yaml.example`.

## Configuration

Copy `example.flux.yaml` to `flux.yaml` and edit:

```yaml
api_key: your-secret-key

redis_addr: localhost:6379
agent_port: 50052

providers:
  aws:
    region: us-east-1
    ami: ami-...
    security_group_id: sg-...
    agent_version: "0.1.0"   # installs .deb from GitHub Releases

    agent_setup_commands:    # run on each new node before agent install
      - sudo apt-get update
      - sudo apt-get install -y curl

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

### Auto-generated PKI

Flux automatically generates and manages all TLS and SSH keys on first startup. No manual certificate or key management is required.

Generated material is stored under `certs_dir` (configurable):

```
<certs_dir>/
  ca/
    ca.pem            # CA certificate
    ca-key.pem        # CA private key (never leaves Flux host)
  flux/
    flux.pem          # Flux client certificate
    flux-key.pem      # Flux private key
  ssh/
    flux-agent.pem    # SSH private key (shared across all agents)
    flux-agent.pub    # SSH public key (auto-imported to cloud providers)
  agents/
    <agent-id>/
      agent.pem       # Per-agent server certificate (minted at provision time)
      agent-key.pem   # Per-agent private key
```

Default location:
- **Linux/macOS**: `/etc/flux/certs`
- **Windows**: `%PROGRAMDATA%\flux\certs`

Override with:
```yaml
certs_dir: /custom/path/to/certs
```

On each node provision, Flux mints a unique agent TLS certificate signed by its CA and uploads it during SSH bootstrap. The SSH key pair is auto-imported as an EC2 key pair on AWS, and injected via instance metadata on GCP.

### Cloud Credentials

**AWS** — uses the standard credential chain by default (env vars, `~/.aws/credentials`, instance profile). Optional static credentials:
```yaml
providers:
  aws:
    access_key_id: AKIA...
    secret_access_key: wJal...
```

**GCP** — uses Application Default Credentials by default (`GOOGLE_APPLICATION_CREDENTIALS` env, `gcloud auth`, or GCE metadata). Optional service account key file:
```yaml
providers:
  gcp:
    credentials_file: /path/to/service-account.json
```

## Quick Start

```bash
# 1. Start Redis
redis-server

# 2. Start an agent (see https://github.com/dashn9/serverless-agent)
AGENT_CONFIG=agent.yaml go run main.go

# 3. Start Flux
go run main.go
```

## API

All protected endpoints require an `X-API-Key` header (or `Authorization: Bearer <key>`).

### Public
| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/health` | Health check |
| `GET` | `/agents` | List all agents with node status |

### Protected
| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/initialize` | Spawn min nodes for each configured provider |
| `POST` | `/nodes/register` | Self-register an agent (called by agent on boot) |
| `PUT` | `/functions` | Register a function (YAML body) |
| `PUT` | `/deploy/{name}` | Deploy function code (zip body) |
| `POST` | `/execute/{name}` | Execute a function (synchronous) |
| `POST` | `/execute/{name}/async` | Execute a function (fire-and-forget) |
| `GET` | `/executions/{id}` | Fetch status and output for an async execution |
| `DELETE` | `/executions/{id}` | Cancel a running async execution |
| `DELETE` | `/nodes` | Terminate and deregister all managed nodes |
| `GET` | `/resources` | Flux and agent CPU/memory/uptime stats |

### Function YAML

```yaml
name: hello-world
handler: main
resources:
  cpu: 100        # millicores
  memory: 128     # MB
timeout: 30       # seconds — omit or set to 0 for no timeout
max_concurrency: 5
max_concurrency_behavior: exit        # or "wait" — when agent hits concurrency limit
resource_pressure_behavior: exit      # or "wait" — when no agent has enough CPU/memory
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

Response behaviour when agents are constrained:

- **Resource pressure** (no agent has enough CPU/memory): controlled by `resource_pressure_behavior` — `exit` returns `429` immediately, `wait` retries until an agent frees up.
- **Concurrency limit** (agent at max concurrent executions): controlled by `max_concurrency_behavior` — `exit` returns `503` immediately, `wait` retries on another agent.

Every execution response includes an `execution_id` (e.g. `exc-550e8400-...`) which is also injected into the function process as the `FLUX_EXECUTION_ID` environment variable.

### Async Execute

Fire-and-forget execution — Flux submits the job to the agent and returns immediately. The agent owns the full execution lifecycle including logging, status tracking, and cancellation.

```bash
curl -X POST http://localhost:7227/execute/hello-world/async \
  -H "X-API-Key: your-secret-key" \
  -H "Content-Type: application/json" \
  -d '{"args": ["arg1"]}'
```

```json
{"status": "accepted", "execution_id": "exc-550e8400-..."}
```

### Fetch Execution

Poll status and output for a running or completed async execution.

```bash
curl http://localhost:7227/executions/exc-550e8400-... \
  -H "X-API-Key: your-secret-key"
```

While running:
```json
{"execution_id": "exc-...", "agent_id": "agent-1", "function_name": "hello-world", "status": "running", "output": "partial output so far...", "started_at": "..."}
```

Once complete:
```json
{"execution_id": "exc-...", "agent_id": "agent-1", "function_name": "hello-world", "status": "success", "output": "full output", "duration_ms": 42, "started_at": "...", "status_at": "..."}
```

The `output` field is updated live as the function writes to stdout/stderr — poll repeatedly to stream progress. Execution records expire after **1 hour**.

### Cancel Execution

```bash
curl -X DELETE http://localhost:7227/executions/exc-550e8400-... \
  -H "X-API-Key: your-secret-key"
```

Flux routes the cancel to the agent running the execution. The process is killed immediately and the status is updated to `cancelled`.

## Autoscaling

The autoscaler runs autonomously — it is started by `ProvidersManager` and holds no external references after that.

**Scale-up:** triggered when every online agent has sustained CPU >= `cpu_upper_threshold` (or mem >= `mem_upper_threshold`) for the full `evaluation_window_sec`. Spawns the smallest configured `node_type` first; at `max_nodes`, upgrades an idle node to the largest type instead.

**Scale-down:** triggered when every agent has sustained CPU <= `cpu_lower_threshold` and mem <= `mem_lower_threshold`. Terminates the least-loaded idle managed node.

**Cooldown** (`cooldown_sec`) prevents back-to-back events. Operator can tune thresholds and cooldown to control responsiveness vs. cost.

## Node Provisioning Flow

1. `POST /initialize` spawns `min_nodes - current` nodes concurrently per provider.
2. Each spawn: `CloudProvider.SpawnNode` (EC2 RunInstances / GCP Insert) → waits for IPs → `RegisterOfflineAgent`.
3. SSH bootstrap connects using the auto-generated SSH key, then:
   1. Runs `agent_setup_commands` (if configured) — package installs, OS updates, etc.
   2. Downloads and installs the flux-agent `.deb`.
   3. Mints a per-agent TLS cert and uploads it alongside `agent.yaml`.
   4. Starts the `flux-agent` systemd service.
4. Autoscaler probes offline agents on every poll tick; first successful contact promotes them to online and syncs all registered functions and deployed code.

## Agent Node Monitoring

Flux collects node-level metrics (CPU, memory, active tasks, uptime) from agents via the `ReportNodeStatus` gRPC call. These metrics drive autoscaling decisions and are surfaced through the `GET /resources` endpoint.

## Agent Self-Registration

Agents call `POST /nodes/register` on startup with their ID and gRPC address. This is how statically configured agents come online — Flux does not push registration.
