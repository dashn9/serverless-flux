# Flux - Serverless Function Execution Platform

A lightweight, Kubernetes-style serverless platform that runs functions natively without Docker/containerd. Uses gRPC for communication between master (Flux) and workers (Agents).

## Architecture

```
                HTTP REST API (Port 7227)
                     │
                     │  - Deploy Functions (Authenticated)
                     │  - Execute Functions (Authenticated)
                     │  - List Agents
                     │
           ┌─────────▼──────────┐
           │  Flux (Master)     │
           │  HTTP: 7227        │
           │                    │
           │  - Agent Registry  │
           │  - Function Registry
           │  - API Key Auth    │
           │  - Health Polling  │
           │                    │
           │  Reads: flux.yaml  │
           └────────┬───────────┘
                    │ gRPC (Flux → Agents)
                    │ - Deploy functions
                    │ - Execute functions
                    │ - Health checks
                    │
        ┌───────────┼───────────┐
        │           │           │
    ┌───▼──┐    ┌──▼───┐   ┌───▼──┐
    │Agent │    │Agent │   │Agent │
    │  #1  │    │  #2  │   │  #3  │
    └──────┘    └──────┘   └──────┘

Communication:
- External → Flux: HTTP/REST (with API key)
- Flux → Agents: gRPC (Flux initiates all communication)
```

## ⚠️ SECURITY WARNING

**THIS SYSTEM IS CURRENTLY INSECURE AND NOT PRODUCTION-READY**

### Critical Security Issues (PRIORITY TO FIX):

1. **HTTP API has no encryption**
   - All traffic is sent in plaintext (no HTTPS/TLS)
   - API keys are transmitted unencrypted in HTTP headers
   - Anyone on the network can intercept credentials and data

2. **gRPC communication is unencrypted**
   - Flux ↔ Agent communication uses insecure gRPC
   - No TLS/mTLS for agent connections
   - Function code and execution results sent in plaintext

3. **No authentication between Flux and Agents**
   - Any server can connect to agents
   - No verification of Flux identity

4. **Weak API key authentication**
   - Single static API key for all users
   - No user management or role-based access control
   - API key stored in environment variable

### Before Production Use:
- [ ] Add HTTPS/TLS for HTTP API
- [ ] Implement mTLS for Flux-Agent gRPC communication
- [ ] Add proper authentication/authorization system
- [ ] Implement secret management for credentials
- [ ] Add audit logging for all operations

### Components

**Flux (Master)**
- **HTTP API Server** (Port 7227): External REST endpoints for deployments and execution
- **Agent Configuration** (flux.yaml): Defines agents and their capacities
- **Health Polling**: Flux initiates health checks to all registered agents
- Function deployment coordination via gRPC to agents
- API key authentication for protected endpoints
- Located in `/cmd/flux`

**Agent (Worker)**
- **gRPC Server**: Receives commands from Flux
- Function execution with cgroup-based memory limits
- Deployment reception and extraction
- Capacity management (max concurrent executions)
- Health check endpoint
- Located in `../agent`

**Executor**
- Lightweight execution environment
- cgroups v2 memory limiting (Linux only)
- Timeout enforcement
- Process isolation

## Function Configuration

Functions are defined using YAML:

```yaml
name: hello-world
handler: main           # Execution entry point (binary/script)
resources:
  cpu: 100             # Millicores (100 = 0.1 core)
  memory: 128          # MB
timeout: 30            # Seconds
env:                   # Optional environment variables
  API_KEY: your-key
  DATABASE_URL: postgres://localhost/db
```

## Quick Start

### 1. Generate Protobuf Code

```bash
# In flux directory
protoc --go_out=. --go_opt=paths=source_relative \
       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
       proto/flux.proto

# In agent directory
cd ../agent
protoc --go_out=. --go_opt=paths=source_relative \
       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
       proto/flux.proto
```

### 2. Create Flux Configuration

```bash
cd ../flux
cp example.flux.yaml flux.yaml
# Edit flux.yaml to configure your agents
```

Example `flux.yaml`:
```yaml
redis_addr: localhost:6379

agents:
  - id: agent-1
    address: localhost:50052
    max_concurrency: 10
  - id: agent-2
    address: localhost:50053
    max_concurrency: 10
```

### 3. Create Agent Configuration

```bash
cd ../agent
cp example.agent.yaml agent.yaml
# Edit agent.yaml to configure agent settings
```

Example `agent.yaml`:
```yaml
agent_id: agent-1
port: 50052
redis_addr: localhost:6379
max_concurrency: 10
```

### 4. Start Redis (for persistence)

```bash
redis-server
# Or using Docker: docker run -d -p 6379:6379 redis
```

### 5. Start Agent(s)

```bash
cd ../agent
go run main.go

# Start additional agents (in separate terminals with different agent.yaml configs)
AGENT_CONFIG=agent2.yaml go run main.go
```

### 6. Start Flux Master

```bash
cd ../flux
API_KEY=my-secret-key go run main.go
# HTTP API: http://localhost:7227
# Flux will connect to agents defined in flux.yaml
# State persisted to Redis - survives crashes/restarts
```

### 7. Register a Function

This registers the function configuration with Flux and all agents:

First, create a function YAML file (e.g., `function.yaml`):
```yaml
name: hello-world
handler: main
resources:
  cpu: 100
  memory: 128
timeout: 30
env:
  API_KEY: your-api-key
  DATABASE_URL: postgres://localhost:5432/mydb
```

Then register it:
```bash
curl -X PUT http://localhost:7227/functions \
  -H "X-API-Key: my-secret-key" \
  -H "Content-Type: application/x-yaml" \
  --data-binary @function.yaml
```

### 8. Deploy Function Code

This deploys only the code (zip file) to all agents:

```bash
# Create zip file
cd functions/example
zip -r ../../hello-world.zip .
cd ../..

# Deploy via PUT
curl -X PUT http://localhost:7227/deploy/hello-world \
  -H "X-API-Key: my-secret-key" \
  -H "Content-Type: application/zip" \
  --data-binary @hello-world.zip
```

Note: Function must be registered before deployment.

### 9. Execute a Function

```bash
curl -X POST http://localhost:7227/execute/hello-world \
  -H "X-API-Key: my-secret-key" \
  -H "Content-Type: application/json" \
  -d '{"args": ["arg1", "arg2", "arg3"]}'
```

Success response (HTTP 200):
```json
{
  "status": "success",
  "output": "execution output here",
  "duration_ms": 1234,
  "agent_id": "agent-1"
}
```

Failed execution (HTTP 500):
```json
{
  "status": "failed",
  "error": "error message here",
  "duration_ms": 100,
  "agent_id": "agent-1"
}
```

Example with no arguments:
```bash
curl -X POST http://localhost:7227/execute/hello-world \
  -H "X-API-Key: my-secret-key" \
  -H "Content-Type: application/json" \
  -d '{"args": []}'
```

### 10. List Agents

```bash
curl http://localhost:7227/agents
```

## API Reference

### Public Endpoints
- `GET /health` - Health check
- `GET /agents` - List all registered agents

### Protected Endpoints (require API key)
- `PUT /functions` - Register a new function (YAML file)
- `PUT /deploy/{function_name}` - Deploy code to a registered function (zip file)
- `POST /execute/{function_name}` - Execute a function

## Autoscaling

Flux includes an autoscaler that monitors node-level CPU pressure and automatically provisions new agent nodes when sustained load is detected.

### How It Works

1. **Agents report metrics** — Each agent exposes a `ReportNodeStatus` gRPC endpoint that returns live CPU%, memory%, active tasks, and uptime via [gopsutil](https://github.com/shirou/gopsutil).
2. **Flux polls metrics** — The autoscaler periodically calls every agent to collect node status.
3. **Sliding window evaluation** — CPU samples are tracked per agent. A scale-up is triggered only when **all** agents have sustained CPU above the configured threshold for the full evaluation window.
4. **Cooldown** — After a scale-up, no further scaling occurs for the configured cooldown period.
5. **History reset** — After a new node is added, the CPU history is cleared to prevent immediate re-triggers.

### Configuration

Add an `autoscaling` section to `flux.yaml`:

```yaml
autoscaling:
  enabled: true
  provider: aws                     # Cloud provider ("aws")
  cpu_threshold: 80                 # Scale when CPU stays above this % (default: 80)
  evaluation_window_sec: 60         # Seconds CPU must sustain above threshold (default: 60)
  poll_interval_sec: 10             # How often to collect metrics (default: 10)
  cooldown_sec: 300                 # Min seconds between scale-ups (default: 300)
  max_nodes: 10                     # Upper limit of total nodes (default: 10)

  aws:
    region: us-east-1
    instance_type: c5.xlarge        # C-class compute-optimised instances
    ami: ami-0abcdef1234567890      # Pre-baked AMI with flux-agent installed
    key_name: flux-agent-key
    subnet_id: subnet-0123456789abcdef0
    security_group_id: sg-0123456789abcdef0
    iam_instance_profile: flux-agent-profile
    agent_port: "50052"
    max_concurrency: 10
    tags:
      Environment: production
      Team: platform
```

### Configuration Reference

| Field | Default | Description |
|---|---|---|
| `cpu_threshold` | `80` | CPU percentage that triggers evaluation |
| `evaluation_window_sec` | `60` | How long CPU must stay above threshold |
| `poll_interval_sec` | `10` | Interval between metric collections |
| `cooldown_sec` | `300` | Minimum gap between scale-up events |
| `max_nodes` | `10` | Maximum number of agent nodes |

### AWS Provider

When using `provider: aws`, the autoscaler launches EC2 instances with these security measures:

- **IMDSv2 enforced** — Instance metadata requires token-based access (no IMDSv1)
- **No public IP** — Instances launch in private subnets only
- **Security group** — Configurable SG controls inbound/outbound traffic
- **IAM instance profile** — Least-privilege role attached to agent nodes
- **Resource tagging** — All instances tagged with `flux:managed=true` and custom tags

The launched instance runs a user-data script that writes `agent.yaml` and starts the `flux-agent` systemd service.

### Agent Node Status API

The `GET /agents` endpoint now includes node metrics when available:

```json
[
  {
    "id": "agent-1",
    "address": "10.0.1.50:50052",
    "max_concurrent": 10,
    "active_count": 3,
    "status": "online",
    "last_heartbeat": "2026-02-17T01:50:00Z",
    "node_status": {
      "cpu_percent": 45.2,
      "memory_percent": 62.1,
      "memory_total_mb": 16384,
      "memory_used_mb": 10178,
      "active_tasks": 3,
      "max_tasks": 10,
      "uptime_seconds": 86400,
      "collected_at": "2026-02-17T01:50:00Z"
    }
  }
]
```
