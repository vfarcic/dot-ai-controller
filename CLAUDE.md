# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This is a Kubernetes controller built with Kubebuilder framework. The controller manages `RemediationPolicy` custom resources (CRDs) to watch and respond to Kubernetes events.

## Development Commands

### Build and Test
```bash
# Build the controller binary
make build

# Run locally (requires kubeconfig)
make run

# Run all tests
make test

# Run unit tests only
go test $(go list ./... | grep -v /e2e)

# Run e2e tests (creates Kind cluster)
make test-e2e
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

## Architecture

### Kubebuilder Structure
- **API Definition**: `api/v1alpha1/remediationpolicy_types.go` - Defines the RemediationPolicy CRD schema
- **Controller**: `internal/controller/remediationpolicy_controller.go` - Reconciliation logic
- **Main Entry**: `cmd/main.go` - Sets up manager and starts controller
- **Configuration**: `config/` directory contains Kustomize manifests for deployment

### Key Patterns
- **Reconciliation Loop**: Controller implements the Reconcile method to handle RemediationPolicy changes
- **RBAC Markers**: Use `+kubebuilder:rbac` comments to generate RBAC manifests
- **Status Updates**: CRD includes Status subresource for tracking operational state
- **Generated Code**: DeepCopy methods and CRD manifests are auto-generated - never edit manually

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
- Always run the full test suite (`go test ./...`) before declaring work finished
- Fix all test failures before updating PRDs, documentation, or moving to next tasks
- Test failures are as important as functionality - they ensure maintainability and regression prevention

## Dependencies

- **Go**: 1.24.0+
- **Kubebuilder**: Project scaffolded with Kubebuilder v3.x
- **controller-runtime**: v0.21.0 - Main framework for building controllers
- **Kubernetes APIs**: v0.33.0

## Important Notes

- Always run `make generate manifests` after modifying API types
- RBAC permissions are generated from controller comments - update them as needed
- The controller uses a shared informer cache - avoid direct API calls when possible
- Follow Kubebuilder conventions for controller implementation