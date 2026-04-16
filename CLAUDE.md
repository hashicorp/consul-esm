# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Consul ESM (External Service Monitor) is a daemon that monitors health checks for external nodes registered in Consul's catalog. It runs alongside Consul to perform health checks for nodes that don't run Consul agents, and can optionally update network coordinates for these nodes.

**Language:** Go
**Requires:** Consul 1.4.1+

## Common Development Commands

### Building
- `make dev` - Build and install binary locally to `$GOPATH/bin` and `bin/consul-esm`
- `make build` - Build Linux AMD64 binary to `dist/linux/amd64/`
- `make docker` - Build Docker image

### Testing
- `make test` - Run all tests with 60s timeout
- `make test-race` - Run tests with race detector enabled
- `go test ./... -run TestFunctionName` - Run specific test by name
- `go test -v ./...` - Run tests with verbose output

### Running Locally
```bash
consul-esm -config-file=/path/to/config.hcl
# or
consul-esm -config-dir=/path/to/config/dir
```

## Architecture

### Core Components

**Agent (`agent.go`)**
- Entry point for ESM functionality
- Handles registration with Consul (either via agent API or catalog API in agentless mode)
- Manages leader election and coordination
- Spawns goroutines for registration checks, TTL/session management, node watching, and leader loop
- Two operating modes:
  - **Agent mode** (default): Registers via Consul agent API, uses TTL checks
  - **Agentless mode** (`enable_agentless=true`): Registers via catalog API, uses session-based health checks

**Leader Election (`leader.go`)**
- Uses Consul KV lock at `consul-esm/leader` key
- Leader watches external nodes and distributes them across healthy ESM instances
- Node assignment stored in KV at `consul-esm/agents/<service-id>`
- Balances nodes round-robin across all healthy ESM instances with same service name/tag
- Nodes with `"external-probe": "true"` metadata are pinged for coordinates

**CheckRunner (`check.go`)**
- Executes HTTP and TCP health checks for assigned external nodes
- Supports threshold-based anti-flapping (`passing_threshold`, `critical_threshold`)
- Uses Consul's `CheckHTTP` and `CheckTCP` implementations
- Updates check status via Consul transactions (CAS operations)
- **Agentless mode optimization**: Batches check updates to reduce HTTP connections

**Batcher (`batcher.go`)**
- Used in agentless mode to batch check updates
- Collects updates and flushes at regular intervals or when batch size limit reached
- Reduces server load by combining multiple check updates into single transactions
- Handles retry logic for stale index errors

**Coordinate Updates (`coordinate.go`)**
- Pings external nodes marked with `"external-probe": "true"`
- Updates network coordinates in Consul for RTT calculations
- Supports UDP (default) or ICMP socket pings
- Also manages `externalNodeHealth` check based on ping success/failure

### Data Flow

1. **Leader Election**: One ESM instance acquires leader lock
2. **Node Distribution**: Leader watches external nodes and assigns them to ESM instances via KV
3. **Node Watching**: Each ESM instance watches its assigned node list at `consul-esm/agents/<service-id>`
4. **Health Checks**: ESM fetches health checks for assigned nodes and runs HTTP/TCP checks
5. **Status Updates**: Check results update Consul catalog (batched in agentless mode)
6. **Coordinate Updates**: ESM pings nodes and updates coordinates (if enabled)

### Key Files

- `main.go` - CLI parsing, logging setup, agent initialization
- `agent.go` - Agent lifecycle, registration, goroutine coordination
- `leader.go` - Leader election, node assignment distribution
- `check.go` - Health check execution and status updates
- `coordinate.go` - Network coordinate pinging and updates
- `config.go` - Configuration parsing (HCL/JSON)
- `batcher.go` - Check update batching for agentless mode

## Important Concepts

### External Nodes
External nodes are identified by node metadata `"external-node": "true"`. These are nodes registered directly in the Consul catalog (not running Consul agents) that ESM monitors.

### Node Probing
Nodes with `"external-probe": "true"` metadata are actively pinged by ESM to:
- Update network coordinates for RTT calculations
- Maintain an `externalNodeHealth` check (similar to Consul's `serfHealth`)

### Agentless Mode
When `enable_agentless=true`:
- ESM registers as a catalog service instead of agent service
- Uses session-based health checks instead of TTL checks
- Batches check updates to reduce HTTP connections to Consul servers
- More efficient for large-scale deployments

### Leader vs Follower Behavior
- **Leader**: Distributes external nodes across all healthy ESM instances
- **Follower**: Monitors assigned nodes and runs health checks

### Check Hash
Checks are uniquely identified by hash: `{node}/{service-id}/{check-id}` or `{node}/{check-id}` for node checks.

### Transaction Limits
Consul supports max 64 operations per transaction (`maxTxnOps`). Batching logic handles splitting larger batches.

## Configuration Notes

- Config files can be HCL or JSON format
- Multiple config files/directories can be specified
- Environment variables supported: `CONSUL_HTTP_ADDR`, `CONSUL_HTTP_TOKEN`, `CONSUL_ENABLEAGENTLESS`, `CONSUL_PARTITION`
- Default service name: `consul-esm`
- Default KV path: `consul-esm/`

## Testing Patterns

- Tests use `testutil.TestServer` from Consul for test Consul servers
- `agent_test.go` - Agent lifecycle, registration, leader election tests
- `check_test.go` - Health check runner tests
- `leader_test.go` - Node distribution and balancing tests
- `agentless_optimization_test.go` - Agentless mode batching tests

## Metrics

ESM exposes Prometheus metrics at `/metrics` endpoint (when `client_address` is configured):
- `esm_agent_isLeader` - Leader status (1 or 0)
- `esm_nodes_monitored` - Count of monitored external nodes
- `esm_services_monitored` - Count of monitored external services
- `esm_checks_*` - Check counts and health
- `esm_agents_healthy` - Count of healthy ESM instances (leader only)
