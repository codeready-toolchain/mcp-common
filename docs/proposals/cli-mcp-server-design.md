# CLI MCP Server — Detailed Design

**Status:** Final — all decisions made.

**Prerequisite:** [Sketch document](cli-mcp-server-sketch.md) — contains problem statement, approach selection, and high-level decisions.

## Overview

A standalone Go MCP server that exposes bundled CLI tools as per-CLI `execute_<name>` MCP tools. **Initial scope: `oc` and `kubectl` only.** The architecture is config-driven and extensible to additional CLIs (e.g., `virtctl`, `helm`) in the future without code changes.

The server targets a **multi-cluster** environment. The LLM specifies the target cluster by name; the server resolves the corresponding kubeconfig and context, validates the command against per-CLI security rules, executes via `exec.Command`, and returns the output.

## Design Principles

1. **Config-driven, code-stable** — adding a new CLI requires a Dockerfile change and a config entry, never a code change to the MCP server itself.
2. **Defense-in-depth** — RBAC is the hard boundary; application-level filtering, no-shell execution, container isolation, and output controls are layered on top.
3. **Consistent with the ecosystem** — follows `mcp-server-devsandbox` patterns for middleware, deployment, and testing. Entry point convention is `cmd/main.go` (vs `sandbox/main.go` in devsandbox — functionally equivalent, `cmd/` is standard Go).
4. **Simple tool schema** — `command` string + `cluster` name + optional `timeout` per CLI. LLMs write commands naturally.
5. **Stateless HTTP** — multi-replica deployment behind kube-rbac-proxy, same as all existing MCP servers.
6. **Multi-cluster native** — discovers available clusters from a shared kubeconfig at startup, same kubeconfig as `mcp-server-devsandbox`.

## Architecture

### Component diagram

```
┌──────────────────────────────────────────────────────────────────────┐
│  Pod: cli-mcp-server                                                 │
│                                                                      │
│  ┌─────────────────┐     ┌─────────────────────────────────────────┐ │
│  │ kube-rbac-proxy  │     │ cli-mcp-server (main container)        │ │
│  │                  │     │                                         │ │
│  │  TLS :8443 ──────┼────▶│  :8080                                 │ │
│  │  TokenReview     │     │                                         │ │
│  │  Bearer auth     │     │  ┌──────────┐  ┌────────────────────┐  │ │
│  │                  │     │  │ MCP SDK  │  │ CLI Registry       │  │ │
│  │                  │     │  │ Server   │  │                    │  │ │
│  └─────────────────┘     │  │          │  │ oc ──▶ executor    │  │ │
│                           │  │ /mcp     │──▶ kubectl ──▶ exec  │  │ │
│                           │  │ /metrics │  │                    │  │ │
│                           │  │ /live    │  │ Cluster Registry   │  │ │
│                           │  │ /health  │  │ rm1 ──▶ kubeconfig │  │ │
│                           │  │          │  │ rm2 ──▶ kubeconfig │  │ │
│                           │  └──────────┘  └────────────────────┘  │ │
│                           │                        │                │ │
│                           │                        ▼                │ │
│                           │              ┌──────────────────┐      │ │
│                           │              │ Security Filter  │      │ │
│                           │              │ (allowlist +     │      │ │
│                           │              │  blocklist)      │      │ │
│                           │              └────────┬─────────┘      │ │
│                           │                       ▼                 │ │
│                           │              exec.Command(binary, args) │ │
│                           │                       │                 │ │
│                           └───────────────────────┼─────────────────┘ │
│                                                   ▼                   │
│                                          kubeconfig (ro mount)        │
│                                                   │                   │
└───────────────────────────────────────────────────┼───────────────────┘
                                                    ▼
                                           Target K8s clusters
```

### Request flow

1. TARSy sends `tools/call` with tool name `execute_oc` and params `{"command": "get pods -n foo -o json", "cluster": "rm1"}`
2. kube-rbac-proxy validates the bearer token via TokenReview, forwards to `:8080`
3. MCP SDK dispatches to the registered handler for `execute_oc`
4. Handler parses the command string via `strings.Fields()`
5. **Security filter** checks: first token against CLI's `allowed_verbs`, full command against `blocked_patterns`
6. Handler resolves `cluster` → kubeconfig path + context from the cluster registry, injects `--kubeconfig` and `--context` flags
7. Handler calls `CommandExecutor.Execute()` with timeout context
8. Output is captured (stdout + stderr), truncated if needed
9. Result returned as `mcp.CallToolResult` with the CLI output as text content

## Core Concepts

### CLI configuration (`config.yaml`)

The config uses a **list** of CLIs (preserves registration order, consistent with `mcp-server-devsandbox` toolsets).

```yaml
defaults:
  timeout: 60
  max_timeout: 300
  max_output_bytes: 102400  # 100KB

clis:
  - name: oc
    path: /usr/bin/oc
    description: |
      Execute OpenShift CLI (oc) commands for cluster investigation.
      Use for: OpenShift-specific resources (Routes, DeploymentConfigs, ClusterOperators,
      MachineConfigs), oc adm commands, and any standard K8s operations on OpenShift clusters.
      Read-only: only investigation commands are allowed.
    security:
      allowed_verbs:
        - get
        - describe
        - logs
        - status
        - adm
        - explain
        - api-resources
        - api-versions
        - whoami
        - version
      blocked_patterns:
        - "adm drain"
        - "adm cordon"
        - "adm uncordon"
        - "adm taint"
        - "adm migrate"
        - "adm must-gather"

  - name: kubectl
    path: /usr/bin/kubectl
    description: |
      Execute kubectl commands for Kubernetes resource investigation.
      Use for: standard Kubernetes resources. Prefer execute_oc on OpenShift clusters.
    security:
      allowed_verbs:
        - get
        - describe
        - logs
        - top
        - explain
        - api-resources
        - api-versions
        - version
      blocked_patterns: []
```

> **Extensibility:** Adding a future CLI (e.g., `virtctl`, `helm`) requires only a new entry here and a binary in the Dockerfile. The `CLIConfig` struct also supports optional `env` (per-CLI environment variables) for CLIs that need them. No code changes.

### Go types

```go
type Config struct {
    Defaults DefaultsConfig `yaml:"defaults"`
    CLIs     []CLIConfig    `yaml:"clis"`
}

type DefaultsConfig struct {
    Timeout        int `yaml:"timeout"`
    MaxTimeout     int `yaml:"max_timeout"`
    MaxOutputBytes int `yaml:"max_output_bytes"`
}

type CLIConfig struct {
    Name     string            `yaml:"name"`
    Path     string            `yaml:"path"`
    Description string         `yaml:"description"`
    Env      map[string]string `yaml:"env,omitempty"`
    Security SecurityConfig    `yaml:"security"`
}

type SecurityConfig struct {
    AllowedVerbs    []string `yaml:"allowed_verbs"`
    BlockedPatterns []string `yaml:"blocked_patterns"`
}
```

### Multi-cluster support

The server discovers available clusters from a shared kubeconfig file at startup, following the same multi-cluster pattern as `mcp-server-devsandbox`. Each cluster maps to a kubeconfig path and context name.

```go
type ClusterInfo struct {
    Name           string
    KubeconfigPath string
    Context        string
}

type ClusterRegistry struct {
    clusters map[string]ClusterInfo
}

func (r *ClusterRegistry) Resolve(clusterName string) (ClusterInfo, error) {
    info, ok := r.clusters[clusterName]
    if !ok {
        return ClusterInfo{}, fmt.Errorf("unknown cluster %q; available: %s",
            clusterName, strings.Join(r.ClusterNames(), ", "))
    }
    return info, nil
}

func (r *ClusterRegistry) ClusterNames() []string { /* sorted keys */ }
```

At startup, the server:
1. Reads the kubeconfig file (path from `--kubeconfig` flag)
2. Splits it into per-cluster kubeconfigs (same approach as `mcp-server-devsandbox`'s `SplitToKubeConfigs`)
3. Builds the `ClusterRegistry` mapping cluster names to kubeconfig paths and contexts
4. Makes available cluster names visible in tool descriptions and server instructions

### CLI registry and tool registration

At startup, the server:

1. Reads the config file (path from `--config` flag, default `/etc/cli-mcp-server/config.yaml`)
2. For each CLI entry, checks if the binary exists at `path` via `exec.LookPath` or `os.Stat`
3. For CLIs with binaries found, creates a `CLITool` and registers it with the MCP server
4. Logs which CLIs were registered and which were skipped (binary not found)

```go
type CLITool struct {
    config   CLIConfig
    defaults DefaultsConfig
    clusters *ClusterRegistry
    executor CommandExecutor
    logger   *slog.Logger
}

func (t *CLITool) Tool() *mcp.Tool {
    return &mcp.Tool{
        Name:        "execute_" + t.config.Name,
        Description: t.config.Description,
        InputSchema: executeInputSchema,
        Annotations: &mcp.ToolAnnotations{
            ReadOnlyHint: true,
        },
    }
}

func (t *CLITool) RegisterWith(s *mcp.Server) {
    mcp.AddTool(s, t.Tool(), t.handle)
}
```

Input schema uses `jsonschema.For[T]()` — consistent with all other MCP servers, enables the typed handler pattern, prevents drift.

### Tool input/output types

```go
type ExecuteInput struct {
    Command string `json:"command" jsonschema:"required,description=CLI command arguments (e.g. 'get pods -n foo -o json')"`
    Cluster string `json:"cluster" jsonschema:"required,description=Target cluster name (e.g. 'rm1')"`
    Timeout *int   `json:"timeout,omitempty" jsonschema:"description=Max execution time in seconds"`
}

type ExecuteOutput struct {
    CLI           string  `json:"cli"`
    Cluster       string  `json:"cluster"`
    Status        string  `json:"status"`
    Output        string  `json:"output"`
    ExitCode      int     `json:"exit_code,omitempty"`
    ExecutionTime float64 `json:"execution_time,omitempty"`
    Truncated     bool    `json:"truncated,omitempty"`
}
```

### Security filter

The security filter runs before command execution and before kubeconfig injection. It operates on the raw parsed command tokens from the LLM:

```go
func (t *CLITool) validate(args []string) error {
    if len(args) == 0 {
        return fmt.Errorf("empty command")
    }

    verb := args[0]

    // Check allowlist — verb must be in the list
    if !slices.Contains(t.config.Security.AllowedVerbs, verb) {
        return fmt.Errorf("verb %q is not allowed for %s; allowed: %s",
            verb, t.config.Name, strings.Join(t.config.Security.AllowedVerbs, ", "))
    }

    // Check blocklist — full command string must not match any blocked pattern
    fullCommand := strings.Join(args, " ")
    for _, pattern := range t.config.Security.BlockedPatterns {
        if strings.HasPrefix(fullCommand, pattern) {
            return fmt.Errorf("command %q is blocked for %s", pattern, t.config.Name)
        }
    }

    return nil
}
```

**`HasPrefix` limitation:** Blocked patterns are matched as command prefixes only (e.g., `"adm drain"` blocks `adm drain node1` but would not catch a hypothetical reordering like `adm --flag drain`). This is sufficient for the current blocked patterns which are all verb+subcommand prefixes. If future patterns need position-independent matching (e.g., blocking a flag like `--as=`), upgrade to `strings.Contains` or regex matching.

### Command execution

Execution goes through a `CommandExecutor` interface for testability. This extends the `mcp-server-devsandbox` `CommandExecutor` pattern by adding `context.Context` for timeout support via `exec.CommandContext`.

```go
type CommandExecutor interface {
    Execute(ctx context.Context, command string, args ...string) ([]byte, error)
    WithEnv(env ...string) CommandExecutor
}
```

The handler resolves the target cluster, validates the command, injects kubeconfig flags, and delegates to the executor:

```go
func (t *CLITool) handle(ctx context.Context, req *mcp.CallToolRequest, input ExecuteInput) (*mcp.CallToolResult, ExecuteOutput, error) {
    args := strings.Fields(input.Command)

    if err := t.validate(args); err != nil {
        return nil, ExecuteOutput{}, err
    }

    // Resolve cluster → kubeconfig path + context
    cluster, err := t.clusters.Resolve(input.Cluster)
    if err != nil {
        return nil, ExecuteOutput{}, err
    }

    args = append([]string{
        fmt.Sprintf("--kubeconfig=%s", cluster.KubeconfigPath),
        fmt.Sprintf("--context=%s", cluster.Context),
    }, args...)

    // Resolve timeout
    timeout := t.defaults.Timeout
    if input.Timeout != nil && *input.Timeout > 0 {
        timeout = min(*input.Timeout, t.defaults.MaxTimeout)
    }

    execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
    defer cancel()

    start := time.Now()
    executor := t.executor
    if len(t.config.Env) > 0 {
        envSlice := make([]string, 0, len(t.config.Env))
        for k, v := range t.config.Env {
            envSlice = append(envSlice, fmt.Sprintf("%s=%s", k, v))
        }
        executor = executor.WithEnv(envSlice...)
    }

    output, err := executor.Execute(execCtx, t.config.Path, args...)
    duration := time.Since(start)

    truncated := false
    if len(output) > t.defaults.MaxOutputBytes {
        output = output[:t.defaults.MaxOutputBytes]
        truncated = true
    }

    result := ExecuteOutput{
        CLI:           t.config.Name,
        Cluster:       input.Cluster,
        Status:        "success",
        Output:        string(output),
        ExitCode:      0,
        ExecutionTime: duration.Seconds(),
        Truncated:     truncated,
    }

    // Non-zero exit codes are NOT returned as MCP errors. A CLI returning
    // exit code 1 (e.g., "oc get pod nonexistent" → "NotFound") is still
    // useful output for the LLM. Only validation failures (bad verb, unknown
    // cluster) return MCP errors that prevent the tool result from reaching
    // the LLM.
    if err != nil {
        result.Status = "error"
        if exitErr, ok := err.(*exec.ExitError); ok {
            result.ExitCode = exitErr.ExitCode()
        }
        if execCtx.Err() == context.DeadlineExceeded {
            result.Output = fmt.Sprintf("Command timed out after %d seconds.\n%s", timeout, string(output))
        }
    }

    return nil, result, nil
}
```

### Server bootstrap

Follows the `mcp-server-devsandbox` pattern but simplified — no `Clients` struct, no toolsets, no shutdown tracker:

```go
// cmd/main.go
func main() {
    var (
        address    string
        transport  string
        configPath string
        kubeconfig string
        stateless  bool
    )

    rootCmd := &cobra.Command{
        Use:   "cli-mcp-server",
        Short: "MCP server for generic CLI passthrough",
        RunE: func(cmd *cobra.Command, args []string) error {
            cfg, err := loadConfig(configPath)
            if err != nil {
                return fmt.Errorf("failed to load config: %w", err)
            }
            return runServer(cfg, transport, address, kubeconfig, stateless)
        },
    }

    rootCmd.Flags().StringVarP(&address, "address", "a", "localhost:8080", "Server address")
    rootCmd.Flags().StringVarP(&transport, "transport", "t", "stdio", "Transport (stdio, http)")
    rootCmd.Flags().StringVarP(&configPath, "config", "c", "/etc/cli-mcp-server/config.yaml", "CLI config file path")
    rootCmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to combined kubeconfig file")
    rootCmd.Flags().BoolVar(&stateless, "stateless", false, "Enable stateless mode for multi-replica")

    if err := rootCmd.Execute(); err != nil {
        log.Fatalf("Error: %v", err)
    }
}
```

`runServer`:
1. Splits the kubeconfig into per-cluster kubeconfigs and builds the `ClusterRegistry` (same approach as `mcp-server-devsandbox`'s `SplitToKubeConfigs`)
2. Creates `mcp.Server` with `mcp-common` middleware
3. Creates `CommandExecutor` (OS implementation)
4. Registers tools for each available CLI, passing the shared `ClusterRegistry` and `CommandExecutor`
5. Sets up HTTP mux with `/mcp`, `/metrics`, `/live`, `/health`
6. Signal handling for graceful shutdown

Health check runs `oc version` (without `--client`) against one cluster to verify the full path: binary exists, kubeconfig works, cluster reachable. No additional K8s `client-go` dependency needed. Process-per-probe (~100–200ms every 10s) is negligible for a server designed around spawning CLI processes.

## Project Structure

```
cli-mcp-server/
├── cmd/
│   └── main.go                    # Cobra root, flag parsing, entry point
├── pkg/
│   ├── cluster/
│   │   ├── registry.go            # ClusterRegistry — kubeconfig splitting, cluster resolution
│   │   └── registry_test.go
│   ├── config/
│   │   ├── config.go              # Config types, YAML loading, validation
│   │   └── config_test.go
│   ├── executor/
│   │   ├── executor.go            # CommandExecutor interface + OS implementation
│   │   ├── executor_test.go
│   │   └── fake/
│   │       └── fake.go            # Test fake for CommandExecutor
│   ├── security/
│   │   ├── filter.go              # Allowlist + blocklist validation
│   │   └── filter_test.go
│   ├── server/
│   │   ├── server.go              # MCP server setup, middleware, endpoints
│   │   └── server_test.go
│   └── tools/
│       ├── cli_tool.go            # CLITool — tool definition, handler, registration
│       └── cli_tool_test.go
├── config/
│   └── default.yaml               # Default CLI config baked into the image
├── docs/
│   └── proposals/
│       ├── cli-mcp-server-sketch.md
│       ├── cli-mcp-server-proposal.md
│       └── cli-mcp-server-design.md
├── Dockerfile
├── Makefile
├── go.mod
├── go.sum
└── README.md
```

Project uses `cmd/` + `pkg/` layout. `mcp-server-devsandbox` uses `sandbox/` + `pkg/` (functionally equivalent; `cmd/` is the more standard Go convention). Allows potential library reuse.

## Dockerfile

```dockerfile
# ---------- Stage 1: Build ----------
FROM golang:1.24-alpine AS builder
RUN apk add --no-cache git make
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN make build GIT_COMMIT_ID=${VERSION}

# ---------- Stage 2: Runtime ----------
# Base image includes oc; kubectl is a symlink to oc on OpenShift
FROM quay.io/codeready-toolchain/oc-client-base-minimal

# Server binary
COPY --from=builder /workspace/bin/cli-mcp-server /usr/bin/

# Default config
COPY config/default.yaml /etc/cli-mcp-server/config.yaml

# Non-root user (same pattern as mcp-server-devsandbox)
RUN groupadd -g 1001 mcpuser \
    && useradd -u 1001 -g 1001 -r -s /bin/sh -d /home/mcpuser mcpuser \
    && mkdir -p /home/mcpuser && chown -R 1001:1001 /home/mcpuser

USER mcpuser

ENTRYPOINT ["/usr/bin/cli-mcp-server"]
CMD ["--transport", "http"]
```

> **Adding future CLIs:** Install the binary in a new `RUN` layer (same as `mcp-server-devsandbox` installs `virtctl`) and add a config entry. No server code changes.

## Kubernetes Deployment (sandbox-sre)

The deployment manifests follow the exact pattern of `kubernetes-mcp-server` and `mcp-server-devsandbox`. These live in `sandbox-sre/components/cli-mcp-server/`.

### Key manifests

| File | Purpose |
|---|---|
| `deployment.yaml` | kube-rbac-proxy sidecar + main container, kubeconfig mount, config mount |
| `service.yaml` | ClusterIP port 8443, serving cert annotation |
| `service-accounts.yaml` | Server SA (`cli-mcp-server`) + client SA (`cli-mcp-client`) + RBAC |
| `network-policy.yaml` | Ingress restrictions |
| `kustomization.yaml` | Ties everything together |

### Deployment args

```yaml
args:
  - --transport
  - http
  - --address
  - 127.0.0.1:8080
  - --stateless
  - --kubeconfig
  - /config/kubeconfig    # combined kubeconfig with contexts for all target clusters
  - --config
  - /etc/cli-mcp-server/config.yaml
```

### Config override via ConfigMap

For environment-specific CLI configs (e.g., different allowed verbs in staging vs production):

```yaml
volumes:
  - name: cli-config
    configMap:
      name: cli-mcp-server-config
      optional: true  # falls back to baked-in default
```

ConfigMap override uses **full replace** — simpler to reason about, avoids merge ambiguity, operator always sees exactly what's deployed.

## Testing Strategy

### Unit tests

| Package | What's tested | Approach |
|---|---|---|
| `pkg/security` | Allowlist/blocklist validation | Table-driven: valid commands, blocked verbs, blocked patterns, edge cases |
| `pkg/tools` | Tool handler — parsing, cluster resolution, kubeconfig injection, timeout, truncation, error handling | `CommandExecutor` fake that records calls and returns canned output |
| `pkg/cluster` | Cluster registry — kubeconfig splitting, resolution, unknown cluster error | In-memory kubeconfig data |
| `pkg/config` | YAML loading, validation, defaults | Load from string, check parsed values |
| `pkg/server` | Tool registration, CLI discovery (binary not found → skipped) | In-memory config with fake executor |

### Integration tests

Testing uses **unit tests with fakes only** — consistent with `mcp-server-devsandbox` (`fake.MemoizedCommandExecutor`). Integration tests with real binaries can be added later if needed.

### Test fake for command execution

Inspired by `mcp-server-devsandbox`'s `fake.MemoizedCommandExecutor` (which uses a builder pattern: `OnCommand().Return()`), adapted with `context.Context` support:

```go
type FakeExecutor struct {
    mu       sync.Mutex
    expected []ExpectedCall
    calls    []RecordedCall
}

type ExpectedCall struct {
    Command  string
    Args     []string
    Output   []byte
    Error    error
}

func (f *FakeExecutor) Execute(ctx context.Context, command string, args ...string) ([]byte, error) {
    f.mu.Lock()
    defer f.mu.Unlock()
    f.calls = append(f.calls, RecordedCall{Command: command, Args: args})
    for _, e := range f.expected {
        if e.Command == command && slicesEqual(e.Args, args) {
            return e.Output, e.Error
        }
    }
    return nil, fmt.Errorf("unexpected command: %s %v", command, args)
}

func (f *FakeExecutor) WithEnv(env ...string) CommandExecutor { return f }
```

## Implementation Plan

### Phase 1: Core server (oc + kubectl)

1. Initialize Go module, `go.mod` with `go-sdk v1.4.0`, `mcp-common`, Cobra
2. Implement `pkg/config` — YAML loading, validation, defaults
3. Implement `pkg/cluster` — kubeconfig splitting, `ClusterRegistry`
4. Implement `pkg/security` — allowlist + blocklist filter
5. Implement `pkg/executor` — `CommandExecutor` interface (with `context.Context`) + OS implementation + fake
6. Implement `pkg/tools` — `CLITool` with registration, handler, cluster resolution, kubeconfig injection
7. Implement `cmd/main.go` — Cobra root, server bootstrap
8. Implement `pkg/server` — MCP server setup with middleware, endpoints
9. Write `config/default.yaml` with `oc` and `kubectl` configs
10. Write Dockerfile (base image already includes `oc`; `kubectl` is symlinked)
11. Write Makefile (`build`, `test`, `docker-build`)
12. Unit tests for all packages

### Phase 2: Deployment

1. Create `sandbox-sre/components/cli-mcp-server/` kustomize manifests
2. ServiceAccount, ClusterRole/Binding for `/mcp` access
3. Service with serving cert annotation
4. NetworkPolicy
5. Staging overlay with kubeconfig secret reference (same multi-cluster kubeconfig as `mcp-server-devsandbox`)
6. Add `cli-mcp-server` entry to `tarsy.yaml`
7. Wire to selected agents (per team decision on Q7 from sketch)

### Phase 3: Production rollout

1. Production overlay
2. Monitor token usage, LLM behavior, command patterns
3. Iterate on security rules based on real usage
4. Evaluate: can this subsume `kubernetes-mcp-server` for investigation agents?

### Future: Additional CLIs

When needed, add CLIs by installing binaries in the Dockerfile and adding config entries. No server code changes. Candidates: `virtctl`, `helm`, `sandboxctl`, `argocd`.

## Design Decisions Summary

All decisions finalized.

| # | Question | Decision |
|---|---|---|
| Q1 | Config structure | Flat list of CLIs |
| Q2 | Input schema | `jsonschema.For[T]()` with typed handler |
| Q3 | Command execution | `CommandExecutor` interface (extends devsandbox pattern with `context.Context`) |
| Q4 | Health check | CLI command (`oc version` — validates binary + cluster) |
| Q5 | Project layout | `cmd/` + `pkg/` |
| Q6 | ConfigMap override | Full replace |
| Q7 | Integration tests | Unit tests with fakes only (for now) |
