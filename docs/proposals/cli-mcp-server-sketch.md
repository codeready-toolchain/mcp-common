# CLI MCP Server — Generic CLI Passthrough for LLM Investigation

**Status:** Sketch complete — ready for detailed design.

## Problem

TARSy's alert investigation agents are limited to pre-built, purpose-specific MCP tools. When an investigation requires a command not covered by existing tools (e.g., `oc adm top nodes`, `oc get clusteroperators`, `helm list`, or a multi-flag `kubectl get` with custom output formatting), the LLM is stuck — it cannot proceed without a human adding a new tool, rebuilding, and redeploying.

Today, six MCP servers provide structured tools:

| Server | Tools | Style |
|---|---|---|
| `kubernetes-mcp-server` (upstream) | K8s CRUD (get, list, logs, events) | Structured — native API calls, no kubectl binary |
| `mcp-server-devsandbox` (standard) | Pod inspection, file/grep in containers, guards | Structured — purpose-built per use case |
| `mcp-server-devsandbox` (virt) | VM SSH, VM lifecycle, VM inspection | Structured |
| `mcp-server-devsandbox` (actions) | VM start/stop (destructive) | Structured |
| `argocd-mcp-server` | Argo CD application analysis | Structured |
| `observability-mcp-server` | Loki/Prometheus queries | Structured |

Each new investigation pattern requires Go code → PR → review → build → deploy. The long tail of edge cases makes this unsustainable.

## Goal

A new, standalone MCP server that gives TARSy agents generic, read-only CLI access to bundled command-line tools. **Initial scope: `oc` and `kubectl`.** The architecture is config-driven and extensible to additional CLIs (e.g., `virtctl`, `sandboxctl`, `helm`, `argocd`) in the future without code changes.

The server targets a **multi-cluster** environment — the LLM specifies the target cluster by name, and the server resolves the corresponding kubeconfig and context (same shared kubeconfig as `mcp-server-devsandbox`). The LLM constructs and executes CLI commands directly, with security enforced at multiple layers.

This server **complements** existing structured MCP servers — it doesn't replace them. Structured tools remain better for well-understood, high-frequency operations. This server covers the long tail.

## Approach Options

Three architectural approaches were evaluated. This sketch proceeds with **Option 1**, chosen for its balance of LLM usability, security granularity, and extensibility.

### Option 1: Per-CLI Tools with Rich Descriptions (selected)

Register a separate `execute_<cli>` tool for each bundled CLI. The LLM picks the right tool based on MCP tool descriptions — the same mechanism that drives tool selection across all existing MCP servers.

- **Pro:** LLMs perform best when selecting from distinct tools with clear descriptions — this is the core function-calling capability
- **Pro:** Per-CLI security rules — each CLI gets its own allowlist/blocklist
- **Pro:** Adding a new CLI = install binary in Dockerfile + add config entry. No MCP server code changes.
- **Pro:** Each tool's description guides the LLM on when to use it
- **Con:** More tools registered = slightly larger tool list for the LLM to process (mitigated by clear descriptions)

### Option 2: Single Generic Tool with CLI Parameter (rejected)

One `cli_execute(cli: string, args: string)` tool. The LLM passes which CLI to use as a parameter.

- **Pro:** Simpler server code — one tool handler
- **Con:** Tool description must cram guidance for all CLIs into one block — worse LLM tool selection
- **Con:** No per-CLI security configuration — all CLIs share one allowlist/blocklist or need complex conditional logic
- **Con:** Harder for agents with `custom_instructions` to reference specific tools

### Option 3: Auto-Discovery + `describe_<cli>` Help Tools (rejected for now)

Same as Option 1, but also register `describe_<cli>` tools that return `--help` output so the LLM can learn subcommands dynamically.

- **Pro:** LLM can discover unknown subcommands (e.g., `describe_oc("adm")` to learn `oc adm` options)
- **Pro:** Self-documenting — no need to maintain usage docs
- **Con:** Extra LLM iterations spent on help lookups before actual commands
- **Con:** Token cost — help output can be verbose
- **Con:** Can be added later as an enhancement without architectural changes

## How It Relates to the Existing System

### Relationship to existing MCP servers

```
TARSy
 ├── kubernetes-mcp-server          (structured K8s API tools — keep as-is)
 ├── mcp-server-devsandbox          (structured sandbox tools — keep as-is)
 ├── mcp-server-devsandbox-virt     (structured VM tools — keep as-is)
 ├── mcp-server-devsandbox-actions  (structured VM lifecycle — keep as-is)
 ├── argocd-mcp-server              (structured Argo CD tools — keep as-is)
 ├── observability-mcp-server       (structured Loki/Prom tools — keep as-is)
 └── cli-mcp-server (NEW)           (generic CLI passthrough)
```

The new server is additive. Agents that benefit from it (e.g., `SecurityInvestigationAgent`, `KubernetesAgent`) gain it as an additional `mcp_servers` entry in `tarsy.yaml`. Existing structured tools remain the preferred path for their specific use cases.

### Relationship to `mcp-server-devsandbox`

The `mcp-server-devsandbox` already bundles and executes `oc`, `virtctl`, and `sandboxctl` via `exec.Command()`. The difference:

| | mcp-server-devsandbox | cli-mcp-server |
|---|---|---|
| **Tool granularity** | One tool per use case (`user-pods`, `grep-files-in-pod`) | One tool per CLI (`execute_oc`, `execute_kubectl`) |
| **What the LLM controls** | Only tool parameters (namespace, pod name) | The full CLI command string |
| **Adding capability** | Write Go code, add a tool, rebuild | Install binary in Dockerfile, add config |

This is a **standalone repo** (`cli-mcp-server`), separate from `mcp-server-devsandbox`. The generic passthrough model has a fundamentally different security model (LLM controls the command vs LLM controls only parameters), different container image composition (bundles CLIs that structured servers don't need), and benefits from an independent release cycle.

**Future direction:** As LLMs improve at CLI command construction, this server could potentially subsume `kubernetes-mcp-server` and the read-only investigation parts of `mcp-server-devsandbox` — reducing the number of MCP servers to maintain for investigation workflows. API-based servers (observability, Argo CD) and destructive-action servers would remain purpose-built.

### Deployment pattern

Follows the established pattern used by all six existing MCP servers:

```
TARSy → (bearer token) → kube-rbac-proxy (TLS :8443) → cli-mcp-server (:8080)
                                                              ↓
                                                    resolve cluster → kubeconfig + context
                                                              ↓
                                                    exec.Command("oc", "--kubeconfig=...", args...)
                                                              ↓
                                                    shared kubeconfig (read-only mount) → target clusters
```

Components:
- **kube-rbac-proxy sidecar** — TLS termination, bearer token authentication via TokenReview
- **ServiceAccount** for the server pod — bound to `system:auth-delegator` for token review
- **Client ServiceAccount** for TARSy — with a token secret and `ClusterRoleBinding` to the server's `nonResourceURL` access role
- **NetworkPolicy** — restricts ingress to the pod
- **Serving cert secret** — TLS cert for kube-rbac-proxy

## Key Concepts

### CLI registry and auto-discovery

CLIs are configured via a **static YAML config file** baked into the container image. The file can be overridden via ConfigMap mount for different environments (staging, production). Server-level settings (address, transport, kubeconfig path, stateless mode) remain CLI flags.

At startup, the server reads the config, checks which binaries exist, and registers MCP tools for available CLIs. Each CLI entry specifies:

- **Name** — used in tool naming (`execute_<name>`)
- **Binary path** — filesystem path to the executable
- **Description** — MCP tool description that guides the LLM on when to use this CLI
- **Environment variables** — optional per-CLI env vars (for future CLIs that need them)
- **Security rules** — allowed verbs/subcommands and blocked patterns (see Security Model below)

The server also discovers available clusters from a shared kubeconfig at startup. Each tool call requires a `cluster` parameter — the server resolves the cluster to a kubeconfig path and context, and injects `--kubeconfig` and `--context` flags automatically.

Only CLIs whose binary is found on the filesystem at startup are registered. This means the Dockerfile controls what's available — remove a binary and the tool disappears.

### Tool interface

Each registered CLI exposes one MCP tool with a simple schema:

```
Tool: execute_<cli_name>
Parameters:
  - command (string, required): The CLI command arguments (e.g., "get pods -n foo -o json")
  - cluster (string, required): Target cluster name (e.g., "rm1")
  - timeout (int, optional): Max execution time in seconds (default from config, max from config)
```

The `command` parameter is a single string — the LLM writes CLI commands naturally. The `cluster` parameter specifies the target cluster; the server resolves it to a kubeconfig path and context, then auto-injects `--kubeconfig` and `--context` flags (same pattern as `mcp-server-devsandbox`'s `RunOcOnBytes`). The LLM never manages kubeconfig details. This matches the `alexei-led/k8s-mcp-server` pattern. The command string is parsed via `strings.Fields()` for security validation and passed to `exec.Command()`.

### How the LLM decides which CLI to use

Three layers of guidance, all part of the existing TARSy architecture:

1. **MCP tool descriptions** — each `execute_<cli>` tool has a detailed description explaining when to use it:
   - `execute_oc`: "Use for OpenShift-specific resources, `oc adm` commands, and any standard K8s operations on OpenShift clusters"
   - `execute_kubectl`: "Use for standard Kubernetes operations. Prefer `execute_oc` on OpenShift clusters."

2. **Server instructions** — the `instructions` field in `tarsy.yaml`'s `mcp_servers` config provides overall guidance (including available cluster names)

3. **Agent instructions** — each agent's `custom_instructions` can specify preferences ("use `oc` for cluster investigation")

> **Future CLIs:** Adding e.g. `execute_virtctl` or `execute_helm` follows the same pattern — install binary, add config entry, update tool descriptions.

## Security Model

Security is enforced at **five layers**, matching and extending the patterns used by existing MCP servers.

### Layer 1: Kubernetes RBAC (primary boundary)

The kubeconfig mounted into the container uses a ServiceAccount with strictly scoped permissions. This is the **hard security boundary** — even if the LLM constructs a destructive command, the API server rejects it with 403.

Existing RBAC for investigation (already deployed):
- `view` ClusterRole — standard K8s read-only for namespaced resources (explicitly excludes Secrets)
- `list-nodes` ClusterRole — node/machine listing
- `kube-investigation-readonly` ClusterRole — cluster-scoped reads (namespaces, PVs, CRDs, metrics, OpenShift operators, machine configs)

The server reuses the existing `sandbox-mcp-sa` ServiceAccount and kubeconfig. This SA is already bound to `view` + `kube-investigation-readonly`, providing the right read-only boundary.

**Over-privilege note:** The current `sandbox-mcp-sa` also has `pods/exec` and KubeVirt lifecycle permissions (needed by `mcp-server-devsandbox`, not by this server). These are mitigated by application-level command blocking. If audit separation or compliance requires it, a dedicated SA with a trimmed ClusterRole can be introduced later — only the kubeconfig secret mount changes, no architectural impact.

### Layer 2: Application-level command filtering

Per-CLI allowlists and blocklists enforced before command execution.

Filtering uses an **allowlist + blocklist** model (belt and suspenders). The allowlist defines which top-level verbs are permitted. The blocklist catches specific dangerous patterns within those allowed verbs. Unknown verbs are denied by default.

Example configuration:

```yaml
clis:
  - name: oc
    security:
      allowed_verbs:
        - get
        - describe
        - logs
        - status
        - adm
        - explain
        - api-resources
        - whoami
        - version
      blocked_patterns:
        - "adm drain"
        - "adm cordon"
        - "adm uncordon"
        - "adm taint"
        - "adm migrate"
```

In this example, `get`, `describe`, `logs`, etc. are straightforward read verbs. `adm` is broadly allowed (so `adm top nodes`, `adm top pods`, `adm inspect` all work), but specific dangerous `adm` subcommands are blocked. Any verb not in the allowlist (e.g., `delete`, `apply`, `create`, `exec`) is rejected before it reaches the API server.

**Note:** The team may choose to simplify to allowlist-only if the flexibility of broad verbs isn't needed.

### Layer 3: No shell interpretation

All commands are executed via Go's `exec.Command(binary, args...)` — never through `sh -c`. This eliminates:
- Pipe injection (`; rm -rf /`)
- Command chaining (`&& curl evil.com`)
- Backtick/subshell substitution (`` `whoami` ``)
- Glob expansion in unintended contexts

This matches the existing pattern in `mcp-server-devsandbox`'s `OsCommandExecutor`. Piped commands are not supported — the LLM uses built-in CLI output format flags (`-o jsonpath`, `-o go-template`, `--sort-by`, `--field-selector`, `-o custom-columns`) for output filtering, and makes multiple sequential tool calls for cross-resource correlation. Pipe support can be added later (allowlisted pipe targets) if needed without architectural changes.

### Layer 4: Container-level isolation

Standard pod security (same as all existing MCP servers):
- `runAsNonRoot: true`
- `readOnlyRootFilesystem: true` (where feasible)
- `capabilities: drop: [ALL]`
- `allowPrivilegeEscalation: false`

### Layer 5: Output controls

- **Timeout** — kill commands after a configurable max (default 60s) to prevent hangs (e.g., `oc logs -f`)
- **Output truncation** — cap response size (e.g., 100KB) to prevent token explosion
- **Data masking** — TARSy's `data_masking` config scrubs tokens, certs, emails from tool responses (already in place for all MCP servers)
- **Summarization** — TARSy's `summarization` config compresses large responses (already in place)

## TARSy Integration

### tarsy.yaml configuration

```yaml
mcp_servers:
  cli-mcp-server:
    transport:
      type: "http"
      url: "https://cli-mcp-server:8443/mcp"
      bearer_token: "{{.CLI_MCP_BEARER_TOKEN}}"
      timeout: 90
      verify_ssl: false
    instructions: |
      This server provides generic CLI access for cluster investigation.
      Available CLIs: oc, kubectl.
      Available clusters: rm1, rm2, rm3 (specify cluster name in each tool call).
      Use this when existing structured tools don't cover your investigation needs.
      Prefer structured tools (kubernetes-server, devsandbox-mcp-server) for standard operations.
      All access is read-only — destructive commands will be rejected.
    data_masking:
      enabled: true
      pattern_groups:
        - "kubernetes"
        - "security"
      patterns:
        - "certificate"
        - "token"
        - "email"
    summarization:
      enabled: true
      summary_max_token_limit: 1200
```

### Agent wiring

Added to investigation agents that benefit from flexible CLI access:

```yaml
agents:
  SecurityInvestigationAgent:
    mcp_servers:
      - "kubernetes-server"
      - "devsandbox-mcp-server"
      - "observability-mcp-server"
      - "cli-mcp-server"              # NEW
```

Which agents get access is a TARSy configuration decision (the `mcp_servers` list per agent in `tarsy.yaml`), not an MCP server design decision. The server itself has no knowledge of which agent is calling it. This can be decided at deployment time.

## Technology and Implementation

### Language and frameworks

- **Go** — consistent with all existing MCP servers (`mcp-server-devsandbox`, `argocd-mcp`, `devsandbox-observability-mcp`)
- **`github.com/modelcontextprotocol/go-sdk/mcp`** — same MCP SDK
- **`github.com/codeready-toolchain/mcp-common`** — shared metrics/logging middleware
- **`github.com/spf13/cobra`** — CLI flag parsing

### Container image

Multi-stage Go build, runtime image bundles all CLI binaries:

```dockerfile
FROM golang:1.24-alpine AS builder
# ... build the Go binary ...

FROM quay.io/codeready-toolchain/oc-client-base-minimal
# oc is already in the base image; kubectl is symlinked to oc
COPY --from=builder /workspace/bin/cli-mcp-server /usr/bin/
```

Adding a future CLI = add its install step to the Dockerfile + a config entry. No server code changes.

### Shared infrastructure with mcp-server-devsandbox

The server imports `mcp-common` for metrics and logging middleware. Command execution is implemented locally — the executor has CLI-specific concerns (security validation, timeout, output truncation) that don't belong in a shared library.

Reusable components from the existing ecosystem:
- `mcp-common` — Prometheus metrics middleware, logging middleware
- Deployment manifests — kube-rbac-proxy sidecar, ServiceAccount/RBAC, NetworkPolicy, TLS secrets
- `OsCommandExecutor` pattern — `exec.Command()` with env var support

## What Is Out of Scope

- **Replacing existing structured MCP servers** — this is additive, not a replacement
- **Write/mutate operations** — this server is read-only by design; destructive use cases belong in purpose-built tools with explicit safeguards
- **Shell interpretation or piped commands** — no `sh -c` execution
- **Interactive commands** — no `oc exec -it`, `oc edit`, or anything requiring stdin
- **Custom output parsing** — the server returns raw CLI output; TARSy's summarization handles compression
- **Multi-tenancy / per-user credentials** — uses shared kubeconfig (per-user auth is handled by the token exchange proposal when implemented)
