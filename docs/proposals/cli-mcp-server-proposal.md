# CLI MCP Server — Proposal

A new MCP server that gives TARSy agents generic, read-only CLI access for cluster investigation.

**Initial scope:** `oc` and `kubectl`. Extensible to additional CLIs later without code changes.

---

## Why

TARSy's investigation agents are limited to pre-built MCP tools. When an investigation requires a command not covered by existing tools — `oc adm top nodes`, `oc get clusteroperators`, a `kubectl get` with custom `-o jsonpath` — the LLM is stuck. Adding each new command means Go code, PR, review, build, deploy.

Today we have six MCP servers, all structured (purpose-built tools with fixed parameters):

| Server | Example tools |
|---|---|
| `kubernetes-mcp-server` | get, list, logs, events (via K8s API) |
| `mcp-server-devsandbox` (standard) | user-pods, grep-files-in-pod, guards |
| `mcp-server-devsandbox` (virt) | vm-ssh, vm-status, vm-ls |
| `mcp-server-devsandbox` (actions) | vm-start, vm-stop |
| `argocd-mcp-server` | unhealthy-apps, app-resources |
| `observability-mcp-server` | Loki/Prometheus queries |

The long tail of investigation commands makes purpose-built tools unsustainable. This server covers that long tail.

---

## What

A single MCP server that registers `execute_oc` and `execute_kubectl` tools. The LLM writes CLI commands as plain strings. The server validates them, resolves the target cluster, injects kubeconfig, executes the command, and returns the output.

### Example interaction

```
LLM → execute_oc(command="get clusteroperators", cluster="rm1")

Server:
  1. Validate: "get" is in the allowlist ✓
  2. Resolve cluster "rm1" → /config/kubeconfigs/rm1.kubeconfig, context "default"
  3. Execute: oc --kubeconfig=/config/kubeconfigs/rm1.kubeconfig --context=default get clusteroperators
  4. Return output to LLM
```

### Tool schema

Each CLI gets one tool:

| Parameter | Type | Required | Description |
|---|---|---|---|
| `command` | string | yes | CLI arguments (e.g., `get pods -n foo -o json`) |
| `cluster` | string | yes | Target cluster name (e.g., `rm1`) |
| `timeout` | int | no | Max seconds (default 60, max 300) |

### How it differs from existing servers

| | mcp-server-devsandbox | cli-mcp-server (this) |
|---|---|---|
| **What LLM controls** | Only parameters (namespace, pod name) | The full CLI command |
| **Adding a command** | Write Go code, PR, build, deploy | Config entry + Dockerfile |
| **Security model** | Code controls what's possible | RBAC + allowlist/blocklist + no shell |

This server is **additive** — it doesn't replace existing structured tools. Structured tools are better for well-understood, high-frequency operations. This covers the edge cases.

---

## How it's secured

Security is layered. RBAC is the hard boundary; everything else is defense-in-depth.

### Layer 1: Kubernetes RBAC (hard boundary)

The server uses the existing `sandbox-mcp-sa` ServiceAccount with read-only permissions:

- `view` ClusterRole — standard K8s read-only (excludes Secrets)
- `list-nodes` — node/machine listing
- `kube-investigation-readonly` — cluster-scoped reads (namespaces, PVs, CRDs, operators)

Even if the LLM sends `delete pod foo`, the API server rejects it with 403. RBAC is the safety net that can't be bypassed.

> **Note:** `sandbox-mcp-sa` also has `pods/exec` (needed by `mcp-server-devsandbox`). This is mitigated by the application-level filter blocking `exec`. A dedicated SA can be introduced later if needed — only the kubeconfig mount changes.

### Layer 2: Application-level command filtering

Before any command runs, the server checks:

1. **Allowlist** — the first word (verb) must be in the allowed list
2. **Blocklist** — the full command must not match any blocked pattern

Example for `oc`:
```
Allowed verbs:   get, describe, logs, status, adm, explain, api-resources, whoami, version
Blocked patterns: "adm drain", "adm cordon", "adm uncordon", "adm taint", "adm migrate"
```

Result: `get pods` works. `adm top nodes` works. `adm drain node1` is blocked. `delete pod foo` is blocked (verb not allowed). `create -f bad.yaml` is blocked.

> **Team decision needed** — see [Open Decision 1](#open-decision-1-security-filtering-approach) below.

### Layer 3: No shell interpretation

Commands run via Go's `exec.Command(binary, args...)`, never through `sh -c`. This prevents:
- Pipe injection (`;`, `&&`, `||`)
- Command chaining
- Backtick/subshell substitution
- Glob expansion

The LLM uses built-in CLI flags (`-o jsonpath`, `-o custom-columns`, `--field-selector`) for output filtering instead of pipes.

### Layer 4: Container isolation

Standard pod security: non-root, read-only filesystem, no capabilities, no privilege escalation.

### Layer 5: Output controls

- **Timeout** — commands killed after 60s (configurable, max 300s)
- **Truncation** — output capped at 100KB
- **Data masking** — TARSy scrubs tokens, certs, emails (already in place)
- **Summarization** — TARSy compresses large responses (already in place)

---

## Multi-cluster support

The server targets our multi-cluster environment. At startup, it reads the shared kubeconfig (same one used by `mcp-server-devsandbox`), splits it into per-cluster kubeconfigs, and builds a cluster registry. The LLM specifies which cluster to target in each tool call.

```
execute_oc(command="get pods -n foo", cluster="rm1")   → runs against rm1
execute_oc(command="get nodes", cluster="rm2")          → runs against rm2
execute_oc(command="get pods", cluster="unknown")       → error: unknown cluster "unknown"; available: rm1, rm2, rm3
```

The LLM never manages kubeconfig paths or contexts — it only knows cluster names.

---

## Architecture

```
TARSy
  ↓ (bearer token)
kube-rbac-proxy (TLS :8443) ── TokenReview auth
  ↓
cli-mcp-server (:8080)
  ├── /mcp       → MCP tool handlers
  ├── /metrics   → Prometheus
  ├── /live      → liveness probe
  └── /health    → readiness (runs "oc version" against a cluster)
        ↓
  ┌─────────────────┐
  │ Security Filter  │ → allowlist + blocklist check
  └────────┬────────┘
           ↓
  exec.Command("oc", "--kubeconfig=...", "--context=...", "get", "pods", "-n", "foo")
           ↓
  Target K8s clusters (via read-only kubeconfig mount)
```

Same deployment pattern as all existing MCP servers: kube-rbac-proxy sidecar, ServiceAccount, NetworkPolicy, TLS cert.

---

## CLI configuration

CLIs are defined in a YAML config baked into the image (overridable via ConfigMap):

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
      Use for: OpenShift-specific resources, oc adm commands, and standard K8s
      operations on OpenShift clusters. Read-only only.
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
      Prefer execute_oc on OpenShift clusters.
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

**Adding a future CLI** (e.g., `virtctl`, `helm`): install the binary in the Dockerfile, add a config entry. No server code changes.

---

## TARSy integration

New entry in `tarsy.yaml`:

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
      Available clusters: rm1, rm2, rm3.
      Use when existing structured tools don't cover your investigation needs.
      Prefer structured tools (kubernetes-server, devsandbox-mcp-server) for standard operations.
      All access is read-only.
    data_masking:
      enabled: true
      pattern_groups: ["kubernetes", "security"]
      patterns: ["certificate", "token", "email"]
    summarization:
      enabled: true
      summary_max_token_limit: 1200
```

Which agents get access is configured per-agent — see [Open Decision 2](#open-decision-2-which-agents-get-access) below.

---

## Implementation plan

### Phase 1: Build the server (oc + kubectl)

Go server using `go-sdk v1.4.0`, `mcp-common` middleware, Cobra flags. Packages:

| Package | Responsibility |
|---|---|
| `pkg/config` | YAML config loading, validation, defaults |
| `pkg/cluster` | Kubeconfig splitting, cluster registry |
| `pkg/security` | Allowlist + blocklist validation |
| `pkg/executor` | `CommandExecutor` interface + OS implementation + test fake |
| `pkg/tools` | `CLITool` — tool registration, handler, cluster resolution |
| `pkg/server` | MCP server setup, middleware, HTTP endpoints |

Container image: multi-stage build, `quay.io/codeready-toolchain/oc-client-base-minimal` base (includes `oc`; `kubectl` is symlinked).

### Phase 2: Deploy to staging

Kustomize manifests in `sandbox-sre/components/cli-mcp-server/`. Same pattern as existing MCP servers: kube-rbac-proxy sidecar, ServiceAccount/RBAC, NetworkPolicy, serving cert, kubeconfig secret mount (shared with `mcp-server-devsandbox`).

### Phase 3: Production rollout

Production overlay. Monitor LLM behavior, token usage, command patterns. Iterate on security rules.

### Future

Add CLIs (`virtctl`, `helm`, `sandboxctl`, `argocd`) as needed. Evaluate whether this can subsume `kubernetes-mcp-server` for investigation agents.

---

## Open decisions for the team

### Open Decision 1: Security filtering approach

How should the application-level command filter work? (RBAC is the hard boundary regardless.)

| Option | How it works | Safety | Flexibility |
|---|---|---|---|
| **A: Allowlist only** | Only listed verbs can run. Unknown verbs denied. | Highest — nothing slips through | Lower — new useful subcommands blocked until allowlist updated |
| **B: Blocklist only** | Everything runs unless explicitly blocked. | Lower — new destructive commands could slip through | Highest — new subcommands work immediately |
| **C: Allowlist + blocklist** | Listed verbs allowed, but specific patterns within them blocked (e.g., allow `adm` but block `adm drain`) | High — two layers of filtering | High — broad verbs like `adm` can be allowed without opening everything |

**Recommendation:** Option C — allowlist for verbs, blocklist for specific dangerous sub-patterns. This lets `adm top nodes`, `adm top pods`, `adm inspect` all work while blocking `adm drain`, `adm cordon`, etc.

Option A is equally valid if the team prefers maximum restrictiveness.

### Open Decision 2: Which agents get access?

Which agents should have `cli-mcp-server` in their `mcp_servers` list? This is a one-line config change per agent, reversible at any time.

| Option | Description |
|---|---|
| **A: All investigation agents** | Maximum flexibility, but larger tool surface may confuse the LLM |
| **B: Only orchestrator agents** | Sub-agents stay focused, but orchestrators don't do direct investigation |
| **C: Specific sub-agents** | Targeted — only agents with broad investigation needs |

**Recommendation:** Option C — start with these agents:

| Agent | Add? | Why |
|---|---|---|
| SecurityInvestigationAgent | **Yes** | Broadest investigation scope, frequently hits edge cases |
| UnidledPodsSubAgent | **Yes** | Needs Idler CRs, owner chains, operator status |
| KubernetesAgent | **Yes** | General K8s troubleshooting |
| SecurityInvestigationOrchestrator | No | Orchestrator — dispatches, doesn't investigate |
| UnidledPodsOrchestrator | No | Orchestrator |
| VirtualMachineInvestigationAgent | Maybe | Has vm-ssh already; CLI access adds cluster-level checks |
| ArgoCD | No | Uses Argo CD API, not CLI |
| VMRemediationAgent | No | Destructive actions — CLI MCP is read-only |

Start with `SecurityInvestigationAgent`, evaluate behavior and token usage, then expand.

---

## What's out of scope

- **Replacing existing servers** — this is additive
- **Write/mutate operations** — read-only by design
- **Shell/pipes** — no `sh -c`, no pipes
- **Interactive commands** — no `oc exec -it`, `oc edit`
- **Per-user credentials** — shared kubeconfig (per-user auth is a separate proposal)

---

## Detailed design

For Go types, handler code, test patterns, and full implementation details, see the [detailed design document](cli-mcp-server-design.md).
