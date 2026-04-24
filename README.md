# Serverless Fabric — Flux

> Lightweight serverless execution platform. Run functions natively — no Docker, no Kubernetes.

Flux is the control plane. It accepts HTTP requests, routes executions to worker [agents](https://github.com/dashn9/serverless-agent) over mTLS gRPC, and autoscales node fleets across AWS EC2 and GCP Compute Engine.

## How It Works

```
Client
  │  HTTP :7227
  ▼
Flux  ──────────────────────────────────────────────
  │  gRPC + mTLS          │  Redis (shared state)
  ├─► Agent #1            │  • Agent registry
  ├─► Agent #2            │  • Function configs
  └─► Agent #3            │  • Code archives
                          │  • Execution→agent routing map
```

- **Functions** are any executable — Go binary, Python script, Rust binary, shell script
- **Agents** execute functions as isolated processes; Linux agents apply cgroup v2 memory limits under an unprivileged `flux-runner` user
- **Flux** handles routing, autoscaling, PKI, and deployment — agents are stateless workers
- **Redis** persists all state so Flux and agents survive restarts

## Features

- **No containers** — functions run as native processes; memory isolation via cgroups on Linux
- **Cross-platform agents** — agents run on Linux (with full isolation) and Windows (without cgroups)
- **Sync and async execution** — block for a result or fire-and-forget with live output polling
- **Async cancellation** — cancel a running async execution via a single API call; agent kills the process immediately
- **Live execution output** — poll `GET /executions/{id}` while a function runs to stream partial output; fetched directly from the owning agent
- **Auto-provisioning** — `POST /initialize` spawns nodes on AWS/GCP, bootstraps agents over SSH, and syncs all functions automatically
- **Autoscaling** — CPU and memory pressure-based scale-up/down with configurable thresholds, evaluation windows, and cooldown; manually registered agents are excluded from all scaling decisions
- **Auto PKI** — Flux generates its own CA and per-agent TLS certificates on first start; no manual cert management
- **Function lifecycle sync** — new or recovered agents automatically receive all registered functions and code

## Install

**Docker (GHCR):**
```bash
docker run -p 7227:7227 \
  -v /path/to/flux.yaml:/etc/flux/flux.yaml \
  ghcr.io/dashn9/serverless-flux:latest
```

**From .deb:**
```bash
wget https://github.com/dashn9/serverless-flux/releases/download/v0.1.0/flux_0.1.0_amd64.deb
sudo dpkg -i flux_0.1.0_amd64.deb
```

**From source:**
```bash
go build -o flux .
```

**Windows release artifact:**

Download `flux_<version>_windows_amd64.zip` from GitHub Releases, extract it, copy `flux.yaml.example` to `flux.yaml`, then run `flux.exe`.

The `.deb` installs a systemd service and places an example config at `/etc/flux/flux.yaml.example`.

## Configuration

See [`example.flux.yaml`](example.flux.yaml) for a fully annotated configuration reference covering:

- API key and Redis address
- Optional `agent_redis_url` — if agents connect to Redis via a different address than Flux (e.g. public vs. private endpoint), set this and bootstrapped agents will use it instead of `redis_addr`
- AWS and GCP provider settings (AMI/image, region, SSH user, credentials)
- Autoscaling thresholds, node types, evaluation window, and cooldown
- PKI / certs directory override
- `disable_grpc_tls` — disables mTLS on all agent connections (development / trusted networks only)

**Cloud credentials:**

- **AWS** — standard credential chain (env vars, `~/.aws/credentials`, instance profile) or static keys in config
- **GCP** — Application Default Credentials (`GOOGLE_APPLICATION_CREDENTIALS`, `gcloud auth`, GCE metadata) or a service account key file in config

**PKI** — auto-generated on first startup. Stored under `certs_dir` (default `/etc/flux/certs` on Linux, `%PROGRAMDATA%\flux\certs` on Windows):

```
<certs_dir>/
  ca/           ca.pem, ca-key.pem
  flux/         flux.pem, flux-key.pem
  ssh/          flux-agent.pem, flux-agent.pub
  agents/<id>/  agent.pem, agent-key.pem
```

Set `disable_grpc_tls: true` to skip PKI entirely and connect to agents without mTLS. Agents must also set `tls.enabled: false`. Recommended only for local development or fully private networks.

## Quick Start

```bash
redis-server
cp example.flux.yaml flux.yaml   # edit api_key and redis_addr
go run main.go
```

See the [agent repo](https://github.com/dashn9/serverless-agent) to set up a worker node.

## API

All protected endpoints require `X-API-Key: <key>` or `Authorization: Bearer <key>`.

### Public

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/health` | Health check |
| `GET` | `/agents` | List all agents with status and node metrics |

### Protected

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/initialize` | Spawn `min_nodes` for each configured provider |
| `POST` | `/agents/register` | Register an agent by address — Flux probes the agent to retrieve its ID and node stats |
| `PUT` | `/functions` | Register a function (YAML body — see [Function YAML](#function-yaml)) |
| `PUT` | `/deploy/{name}` | Deploy function code (zip body) |
| `POST` | `/execute/{name}` | Execute a function and wait for the result |
| `POST` | `/execute/{name}/async` | Submit a fire-and-forget execution |
| `GET` | `/executions/{id}` | Fetch status and live output for an async execution (fetched directly from the owning agent) |
| `DELETE` | `/executions/{id}` | Cancel a running async execution |
| `DELETE` | `/nodes` | Terminate and deregister all managed nodes |
| `GET` | `/resources` | CPU/memory/uptime for Flux and all agents |

### Register an Agent

```bash
curl -X POST http://localhost:7227/agents/register \
  -H "X-API-Key: your-key" \
  -H "Content-Type: application/json" \
  -d '{"address": "10.0.0.5:50052"}'
```

Flux dials the agent, retrieves its ID and node stats, checks uniqueness, then returns:

```json
{
  "status": "registered",
  "id": "agent-abc123",
  "address": "10.0.0.5:50052",
  "node": {
    "cpu_percent": 3.2,
    "memory_percent": 18.5,
    "memory_total_mb": 8192,
    "memory_used_mb": 1516,
    "active_tasks": 0,
    "uptime_seconds": 43201
  }
}
```

## Function YAML

See [`linkedin-scraper-function.yaml`](../linkedin-scraper-function.yaml) for a working example. Fields:

| Field | Required | Description |
|-------|----------|-------------|
| `name` | yes | Unique function name |
| `handler` | yes | Executable name inside the deployed zip |
| `resources.cpu` | no | CPU limit in millicores |
| `resources.memory` | no | Memory limit in MB (enforced via cgroups on Linux) |
| `timeout` | no | Execution timeout in seconds — `0` or omit for no timeout |
| `max_concurrency` | no | Max concurrent executions per agent (default: 5) |
| `max_concurrency_behavior` | no | `exit` (503) or `wait` when at capacity (default: `exit`) |
| `resource_pressure_behavior` | no | `exit` (429) or `wait` when no agent fits (default: `exit`) |
| `env` | no | Environment variables injected into the process |

Every execution injects `FLUX_EXECUTION_ID` into the process environment.

## Async Executions

Async executions are fully owned by the agent — Flux submits the job and returns a `202` with an `execution_id` immediately. The agent runs the execution in the background, writing live output and status to its Redis instance.

Flux stores an `executionID → agentID` mapping in its own Redis at dispatch time. This allows `GET /executions/{id}` and `DELETE /executions/{id}` to route correctly even if Flux restarts, and regardless of whether Flux and agents share the same Redis instance.

**Polling** `GET /executions/{id}` proxies the request to the owning agent and returns the current execution record. The `output` field updates live as the process writes to stdout/stderr. Poll until `status` leaves `running`.

Possible statuses: `running` · `success` · `failed` · `cancelled`

**Cancellation** `DELETE /executions/{id}` routes a cancel to the owning agent, which kills the process group immediately.

Execution records expire after **1 hour**.

## Autoscaling

The autoscaler runs per provider, started automatically when providers are configured. **Only provider-managed agents are considered** — manually registered agents are invisible to all scaling decisions.

**Scale-up** — triggers when every managed agent sustains CPU ≥ `cpu_upper_threshold` or memory ≥ `mem_upper_threshold` for the full `evaluation_window_sec`. Spawns the smallest configured `node_type` first; at `max_nodes`, upgrades an idle node to the largest type instead.

**Scale-down** — triggers when every managed agent sustains CPU ≤ `cpu_lower_threshold` and memory ≤ `mem_lower_threshold`. Terminates the least-loaded idle managed node. Never scales below `min_nodes`.

Cooldown (`cooldown_sec`) prevents back-to-back scale events.

## Node Provisioning

1. `POST /initialize` spawns `min_nodes − current` nodes concurrently per provider
2. Each node: cloud API spawn → wait for public IP → register as offline
3. SSH bootstrap: run `agent_setup_commands` → install `flux-agent` `.deb` → upload minted TLS cert + config → start systemd service
4. Health poller promotes offline agents to online on first contact and syncs all functions and code

Agents not provisioned by Flux (bare-metal, self-hosted VMs) register by calling `POST /agents/register` with their address.

## Related

- [serverless-agent](https://github.com/dashn9/serverless-agent) — the worker component
