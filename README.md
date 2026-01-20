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

```bash
curl -X POST http://localhost:7227/functions \
  -H "X-API-Key: my-secret-key" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "hello-world",
    "handler": "main",
    "resources": {"cpu": 100, "memory": 128},
    "timeout": 30
  }'
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
  -d '{"input": {"message": "test"}}'
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
- `POST /functions` - Register a new function
- `PUT /deploy/{function_name}` - Deploy code to a registered function (zip file)
- `POST /execute/{function_name}` - Execute a function
