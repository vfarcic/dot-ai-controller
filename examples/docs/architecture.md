# Architecture

The controller follows standard Kubernetes controller patterns.

## Components

1. **Controller Manager** - Runs reconciliation loops
2. **CRDs** - Define custom resources (Solution, RemediationPolicy, ResourceSyncConfig, CapabilityScanConfig, GitKnowledgeSource)
3. **Webhooks** - Validate and mutate resources

## Data Flow

User creates CR -> Controller watches -> Reconciles state -> Updates status
