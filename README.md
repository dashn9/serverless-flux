# Serverless Fabric — Flux

> Lightweight serverless execution platform for Linux. Run functions natively with cgroup memory isolation — no Docker, no Kubernetes.

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
                          │  • Async execution state
```

- **Functions** are any executable — Go binary, Python script, Rust binary, shell script
- **Agents** run on Linux, execute functions in isolated processes under cgroups v2 memory limits as an unprivileged `flux-runner` user
- **Flux** handles routing, autoscaling, PKI, and deployment — agents are stateless workers
- **Redis** persists all state so Flux and agents survive restarts

## Features

- **No containers** — functions run as native processes with cgroup memory limits
- **Sync and async execution** — block for a result or fire-and-forget with live output polling
- **Async cancellation** — cancel a running async execution via a single API call; agent kills the process immediately
- **Live execution output** — poll `GET /executions/{id}` while a function runs to stream partial output
- **Auto-provisioning** — `POST /initialize` spawns nodes on AWS/GCP, bootstraps agents over SSH, and syncs all functions automatically
- **Autoscaling** — CPU and memory pressure-based scale-up/down with configurable thresholds, evaluation windows, and cooldown
- **Auto PKI** — Flux generates its own CA and per-agent TLS certificates on first start; no manual cert management
- **Function lifecycle sync** — new or recovered agents automatically receive all registered functions and code

## Install

**From .deb:**
```bash
wget https://github.com/dashn9/serverless-flux/releases/download/v0.1.0/flux_0.1.0_amd64.deb
sudo dpkg -i flux_0.1.0_amd64.deb
```

**From source:**
```bash
go build -o flux .
```

The `.deb` installs a systemd service and places an example config at `/etc/flux/flux.yaml.example`.

## Configuration

See [`example.flux.yaml`](example.flux.yaml) for a fully annotated configuration reference covering:

- API key and Redis address
- AWS and GCP provider settings (AMI/image, region, SSH user, credentials)
- Autoscaling thresholds, node types, evaluation window, and cooldown
- PKI / certs directory override

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
| `POST` | `/nodes/register` | Self-register an agent (called by agent on boot) |
| `PUT` | `/functions` | Register a function (YAML body — see [Function YAML](#function-yaml)) |
| `PUT` | `/deploy/{name}` | Deploy function code (zip body) |
| `POST` | `/execute/{name}` | Execute a function and wait for the result |
| `POST` | `/execute/{name}/async` | Submit a fire-and-forget execution |
| `GET` | `/executions/{id}` | Fetch status and live output for an async execution |
| `DELETE` | `/executions/{id}` | Cancel a running async execution |
| `DELETE` | `/nodes` | Terminate and deregister all managed nodes |
| `GET` | `/resources` | CPU/memory/uptime for Flux and all agents |

## Function YAML

See [`linkedin-scraper-function.yaml`](../linkedin-scraper-function.yaml) for a working example. Fields:

| Field | Required | Description |
|-------|----------|-------------|
| `name` | yes | Unique function name |
| `handler` | yes | Executable name inside the deployed zip |
| `resources.cpu` | no | CPU limit in millicores |
| `resources.memory` | no | Memory limit in MB |
| `timeout` | no | Execution timeout in seconds — `0` or omit for no timeout |
| `max_concurrency` | no | Max concurrent executions per agent (default: 5) |
| `max_concurrency_behavior` | no | `exit` (503) or `wait` when at capacity (default: `exit`) |
| `resource_pressure_behavior` | no | `exit` (429) or `wait` when no agent fits (default: `exit`) |
| `env` | no | Environment variables injected into the process |

Every execution injects `FLUX_EXECUTION_ID` into the process environment.

## Async Executions

Async executions are fully owned by the agent — Flux submits the job and returns a `202` with an `execution_id` immediately. The agent runs the execution in the background, writing live output and status to Redis.

**Polling** `GET /executions/{id}` returns the current record. The `output` field updates live as the process writes to stdout/stderr. Poll until `status` leaves `running`.

Possible statuses: `running` · `success` · `failed` · `cancelled`

**Cancellation** `DELETE /executions/{id}` routes a cancel to the owning agent, which kills the process group immediately.

Execution records expire after **1 hour**.

## Autoscaling

The autoscaler runs per provider, started automatically when providers are configured.

**Scale-up** — triggers when every online agent sustains CPU ≥ `cpu_upper_threshold` or memory ≥ `mem_upper_threshold` for the full `evaluation_window_sec`. Spawns the smallest configured `node_type` first; at `max_nodes`, upgrades an idle node to the largest type instead.

**Scale-down** — triggers when every agent sustains CPU ≤ `cpu_lower_threshold` and memory ≤ `mem_lower_threshold`. Terminates the least-loaded idle managed node. Never scales below `min_nodes`.

Cooldown (`cooldown_sec`) prevents back-to-back scale events.

## Node Provisioning

1. `POST /initialize` spawns `min_nodes − current` nodes concurrently per provider
2. Each node: cloud API spawn → wait for IP → register as offline
3. SSH bootstrap: run `agent_setup_commands` → install `flux-agent` `.deb` → upload minted TLS cert + config → start systemd service
4. Health poller promotes offline agents to online on first contact and syncs all functions and code

Statically configured agents self-register by calling `POST /nodes/register` on boot.

## Related

- [serverless-agent](https://github.com/dashn9/serverless-agent) — the worker component
