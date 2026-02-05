# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository Info

- **GitHub Organization**: `vfarcic`
- **Repository**: `vfarcic/dot-ai-controller`

## Project Overview

DevOps AI Toolkit Controller - A Kubernetes controller built with Kubebuilder v4.7.1. The controller provides five main capabilities:

1. **Solution CRD** (`dot-ai.devopstoolkit.live/v1alpha1`): Resource tracking and lifecycle management
   - Groups related Kubernetes resources as logical solutions
   - Manages ownerReferences for automatic cleanup
   - Aggregates health status across tracked resources
   - Standalone feature - no external dependencies

2. **RemediationPolicy CRD** (`dot-ai.devopstoolkit.live/v1alpha1`): Event-driven remediation
   - Watches Kubernetes events with configurable filtering
   - Integrates with DevOps AI Toolkit MCP for AI-powered remediation
   - Supports automatic and manual modes
   - Includes rate limiting and Slack notifications
   - Requires external MCP endpoint

3. **ResourceSyncConfig CRD** (`dot-ai.devopstoolkit.live/v1alpha1`): Resource visibility and semantic search
   - Discovers all resource types in cluster via Discovery API
   - Watches for resource changes (create, update, delete)
   - Syncs resource metadata to MCP for semantic search
   - Debounces changes to reduce API calls
   - Periodic full resync catches missed events
   - Requires external MCP endpoint

4. **CapabilityScanConfig CRD** (`dot-ai.devopstoolkit.live/v1alpha1`): Autonomous capability discovery
   - Detects CRD changes (create, update, delete) automatically
   - Triggers capability scans when new CRDs are installed
   - Syncs cluster state with MCP on controller restart
   - Supports resource filtering with include/exclude patterns
   - Requires external MCP endpoint

5. **GitKnowledgeSource CRD** (`dot-ai.devopstoolkit.live/v1alpha1`): Documentation ingestion from Git
   - Clones Git repositories and syncs documents to MCP knowledge base
   - Supports glob patterns for file selection (e.g., `docs/**/*.md`)
   - Change detection syncs only modified files since last commit
   - Scheduled sync with cron or interval expressions
   - Automatic cleanup removes documents when CR is deleted
   - Requires external MCP endpoint

## Development Commands

### Build and Test
```bash
# Build the controller binary
make build

# Run locally (requires kubeconfig)
make run

# Run all tests (unit + integration using envtest)
make test

# Run unit tests only
go test $(go list ./... | grep -v /e2e)

# Run e2e tests (creates Kind cluster named 'controller-init-test-e2e')
# Uses isolated kubeconfig 'e2e-kubeconfig' in current directory
make test-e2e

# Cleanup e2e cluster when done
make cleanup-test-e2e
```

### Code Generation (Required after API changes)
```bash
# Generate manifests (CRDs, RBAC, etc.)
make manifests

# Generate deepcopy methods
make generate

# Format and validate code
make fmt vet

# Lint code
make lint
make lint-fix
```

### Cluster Operations
```bash
# Install CRDs into cluster
make install

# Deploy controller to cluster
make deploy IMG=<registry>/controller:tag

# Apply sample resources
kubectl apply -k config/samples/

# Remove controller and CRDs
make undeploy
make uninstall
```

### Container Operations
```bash
# Build Docker image
make docker-build IMG=<registry>/controller:tag

# Push Docker image
make docker-push IMG=<registry>/controller:tag

# Multi-arch build and push
make docker-buildx IMG=<registry>/controller:tag
```

### Development Environment Setup
```bash
# Full development environment using Nushell scripts
# Sets up Kind cluster with all dependencies
./dot.nu setup \
  --dot-ai-tag "0.144.0" \
  --kubernetes-provider "kind" \
  --crossplane-enabled true \
  --kyverno-enabled true

# Teardown development environment
./dot.nu destroy
```

## Architecture

### Kubebuilder Structure
- **API Definitions**: `api/v1alpha1/`
  - `solution_types.go` - Solution CRD schema for resource tracking
  - `remediationpolicy_types.go` - RemediationPolicy CRD schema for event remediation
  - `resourcesyncconfig_types.go` - ResourceSyncConfig CRD schema for resource visibility
  - `capabilityscanconfig_types.go` - CapabilityScanConfig CRD schema for capability discovery
  - `gitknowledgesource_types.go` - GitKnowledgeSource CRD schema for documentation ingestion
- **Controllers**: `internal/controller/`
  - `solution_controller.go` - Manages Solution CRs and ownerReferences
  - `remediationpolicy_controller.go` - Watches events and triggers remediation
  - `resourcesync_controller.go` - Syncs resource metadata to MCP for semantic search
  - `resourcesync_mcp.go` - MCP client for resource sync
  - `resourcesync_debounce.go` - Debounce buffer for batching changes
  - `capabilityscan_controller.go` - Watches CRDs and triggers capability scans
  - `gitknowledgesource_controller.go` - Syncs Git repository documents to MCP
  - `gitknowledgesource_git.go` - Git client for cloning and change detection
  - `gitknowledgesource_mcp.go` - MCP client for document ingestion
- **Main Entry**: `cmd/main.go` - Sets up manager and registers all five controllers
- **Configuration**: `config/` directory contains Kustomize manifests
  - `config/crd/bases/` - Generated CRD definitions
  - `config/manager/` - Controller deployment manifests
  - `config/samples/` - Example custom resources

### Key Patterns

**Solution Controller:**
- Reconciles Solution CRs by setting ownerReferences on tracked resources
- Aggregates health status from all child resources
- Uses exponential backoff with jitter for retries
- Updates status with detailed resource conditions

**RemediationPolicy Controller:**
- Watches Kubernetes Events cluster-wide using MapFunc
- Implements deduplication to prevent processing same event multiple times
- Rate limiting per policy+object+reason with configurable cooldowns
- HTTP client calls external MCP endpoint for remediation analysis
- Supports per-selector overrides for mode, confidence, and risk level

**ResourceSync Controller:**
- Discovers all resource types via Kubernetes Discovery API
- Creates dynamic informers for each resource type
- Detects create/update/delete events on all resources
- Debounces changes in configurable time window before syncing
- Syncs resource metadata (not status/spec) to MCP for semantic search
- Periodic full resync to catch any missed events

**CapabilityScan Controller:**
- Watches CRD create/update/delete events cluster-wide
- Triggers capability scans to MCP when CRDs change
- Performs startup reconciliation to sync cluster state with MCP
- Supports include/exclude resource filtering patterns
- Uses debouncing to batch rapid CRD changes

**GitKnowledgeSource Controller:**
- Clones Git repositories using go-git library (HTTPS only)
- Matches files using glob patterns (doublestar library)
- Detects changes via git commit diff since last synced commit
- Syncs documents to MCP knowledge base with sourceIdentifier
- Schedules periodic syncs using cron/interval expressions
- Cleans up MCP documents on CR deletion (finalizer pattern)

**Common Patterns:**
- **RBAC Markers**: Use `+kubebuilder:rbac` comments to generate RBAC manifests
- **Status Updates**: All five CRDs include Status subresource for operational state
- **Generated Code**: DeepCopy methods and CRD manifests are auto-generated - never edit manually
- **Structured Logging**: Use `logf.FromContext(ctx)` for consistent logging

### Adding New Controllers or APIs
```bash
# Create new API/CRD
kubebuilder create api --group <group> --version <version> --kind <Kind>

# Create new controller only
kubebuilder create controller --group <group> --version <version> --kind <Kind>
```

## Testing

- **Unit Tests**: Test controller logic in isolation using fake clients
- **Integration Tests**: Use envtest to run against a real API server (no cluster needed)
- **E2E Tests**: Full cluster testing using Kind

Test files follow Go convention: `*_test.go` alongside source files.

### Test Quality Standards

**IMPORTANT**: All tests must pass before marking any task, milestone, or feature as complete. No exceptions.

- A task is NOT complete if any tests are failing, even if the core functionality works
- Failing tests indicate incomplete implementation, insufficient test isolation, or bugs that must be addressed
- Always run `make test` (unit + integration) before declaring work finished
- Run `make test-e2e` for e2e tests - this automatically creates the Kind cluster
- **Always run `make cleanup-test-e2e` after analyzing e2e test results** - the cluster is reused between runs, which can cause failures if left in a dirty state from previous runs
- **Do NOT use `go test ./...`** - this includes e2e tests but skips Kind cluster setup, causing failures
- Fix all test failures before updating PRDs, documentation, or moving to next tasks
- Test failures are as important as functionality - they ensure maintainability and regression prevention
- After tests pass, look for opportunities to reduce execution time

### Test Output

Save test output to `./tmp/test-output.txt` for review.

## Dependencies

- **Go**: 1.24.0+
- **Kubebuilder**: Project scaffolded with Kubebuilder v4.7.1
- **controller-runtime**: v0.21.0 - Main framework for building controllers
- **Kubernetes APIs**: v0.33.0
- **Testing**: Ginkgo v2.22.0 + Gomega v1.36.1

## Repository Structure

```
dot-ai-controller/
├── api/v1alpha1/           # CRD API definitions (Solution, RemediationPolicy, ResourceSyncConfig)
├── internal/controller/    # Controller implementations
├── cmd/main.go            # Application entry point
├── config/                # Kustomize manifests (CRDs, RBAC, deployment)
├── charts/                # Helm chart for installation
├── docs/                  # User documentation (setup, guides, troubleshooting)
├── examples/              # Example manifests
├── scripts/               # Nushell utility scripts for development setup
├── test/e2e/              # End-to-end tests using Kind
├── prds/                  # Product requirement documents
└── dot.nu                 # Main development environment script
```

## Important Notes

- **Code Generation**: Always run `make generate manifests` after modifying API types in `api/v1alpha1/*_types.go`
- **RBAC**: Permissions are generated from `+kubebuilder:rbac` comments in controller files - update them as needed
- **Informer Cache**: The controller uses a shared informer cache - avoid direct API calls when possible
- **Status Updates**: Use `r.Status().Update()` for status changes, not `r.Update()`
- **ownerReferences**: Solution controller sets these automatically - don't manually manage them
- **Event Deduplication**: RemediationPolicy controller tracks processed events in-memory - state is lost on restart
- **Rate Limiting**: Configured per policy+object+reason to prevent storms while allowing different objects to be processed
- **External Dependencies**: RemediationPolicy and ResourceSyncConfig require MCP endpoint; Solution CRD works standalone
- **Helm Chart**: Located in `charts/dot-ai-controller/` and published as OCI artifact to GHCR
- **Documentation**: User-facing docs in `docs/`, PRDs track feature development in `prds/`