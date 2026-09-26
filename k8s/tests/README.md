# Kubemoot Operator Tests

End-to-end tests for verifying Kubemoot Operator CRD functionality.

## Test Structure

```
k8s/tests/
├── lib.sh                     # Shared test utilities
├── test-all.sh                # Full test suite runner
├── test-modelprovider.sh      # ModelProvider resource test
├── test-model.sh              # Model resource test (includes pull/delete)
├── test-mcpserver.sh          # MCPServer resource test
├── test-embeddingmodel.sh     # EmbeddingModel resource test
├── test-ragsource.sh          # RAGSource resource test (Job-based indexing)
├── test-ragsource-schedule.sh # RAGSource scheduled indexing (CronJob)
├── test-rag-query.sh          # RAGSource query service test
└── test-agent.sh              # Agent resource test (orchestration)
```

## Running Tests

### Full Test Suite

```bash
# Run all tests
./test-all.sh

# Skip slow tests (Model and EmbeddingModel pull)
./test-all.sh --fast

# Keep resources after tests (for debugging)
./test-all.sh --no-cleanup

# Clean up all test resources
./test-all.sh --cleanup
```

### Individual Resource Tests

```bash
# Test ModelProvider only
./test-modelprovider.sh

# Test Model (includes ModelProvider setup)
./test-model.sh

# Test MCPServer
./test-mcpserver.sh

# Test EmbeddingModel
./test-embeddingmodel.sh

# Test RAGSource (includes EmbeddingModel setup)
./test-ragsource.sh

# Test RAGSource scheduled re-indexing
./test-ragsource-schedule.sh

# Test RAGSource query service
./test-rag-query.sh

# Test Agent (orchestration)
./test-agent.sh
```

## Prerequisites

- Kubernetes cluster accessible via `kubectl`
- Kubemoot operator deployed in `kubemoot` namespace
- Ollama deployed in `ollama` namespace (for Model tests)
- PostgreSQL with pgvector extension (for RAGSource tests)

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `NAMESPACE` | `kubemoot` | Namespace for Kubemoot resources |
| `OLLAMA_NAMESPACE` | `ollama` | Namespace for Ollama deployment |
| `TIMEOUT` | `120` | Default timeout in seconds |

## Test Coverage

| CRD | Tests |
|-----|-------|
| **ModelProvider** | Create, Ready status, Version detection, Conditions, Delete |
| **Model** | Create, Pull state, Available state, Ollama verification, Metadata, Finalizer cleanup |
| **MCPServer** | Create, Deployment, Service, Ready status, Connectivity, Owner reference cleanup |
| **EmbeddingModel** | Create, Available state, Status fields, Ollama verification, Delete |
| **RAGSource** | Create, Job creation, Status tracking, Job labels, Environment variables, Owner reference cleanup |
| **RAGSource-Schedule** | CronJob creation, Schedule validation, Suspend/Resume, CronJob labels, Cleanup |
| **RAG-Query** | Query service deployment, Service creation, Endpoint resolution, HTTP connectivity |
| **Agent** | Create, Model status tracking, Deployment creation, Service creation, Environment injection, Owner reference cleanup |

## Test Results

When running `test-all.sh`, you'll see a summary like:

```
==============================================
  Full Test Suite Results
==============================================

  ModelProvider: PASSED
  Model: PASSED
  MCPServer: PASSED
  EmbeddingModel: PASSED
  RAGSource: PASSED
  RAGSource-Schedule: PASSED
  RAG-Query: PASSED
  Agent: PASSED

----------------------------------------------
  Passed:  8
  Failed:  0
  Skipped: 0
==============================================
```

## Adding New Tests

1. Create `test-<resource>.sh` following the pattern of existing tests
2. Source `lib.sh` for shared utilities
3. Use standard helpers:
   - `log_ok` / `log_fail` / `log_warn` / `log_info` - Logging
   - `wait_for` - Wait for condition
   - `check_cluster` / `check_namespace` / `check_operator` - Pre-flight
   - `check_crd` / `check_ollama` / `check_pgvector` - Dependency checks
   - `cleanup_resource` - Resource cleanup
   - `exit_with_results` - Test summary
4. Add the test to `test-all.sh` runner

## Debugging Failed Tests

```bash
# Run with no cleanup to inspect resources
./test-ragsource.sh --no-cleanup

# Check resource status
kubectl get ragsource -n kubemoot -o yaml

# Check operator logs
kubectl logs -n kubemoot deploy/kubemoot-operator-controller-manager -c manager

# Check job logs
kubectl logs -n kubemoot -l kubemoot.ai/ragsource=test-ragsource

# Check query service logs
kubectl logs -n kubemoot -l kubemoot.ai/ragsource=test-rag-query -c query-service
```

## Query Service Tests

The `test-rag-query.sh` specifically tests the auto-deployed query service:

1. Creates RAGSource with `queryService.enabled: true`
2. Verifies Deployment created with correct labels
3. Verifies Service created with correct port
4. Checks `status.queryEndpoint` is populated
5. Optionally tests HTTP connectivity to query endpoint
6. Verifies cleanup via owner references

## Agent Tests

The `test-agent.sh` tests the Agent CRD orchestration:

1. Creates prerequisite Model resource
2. Creates Agent with model reference
3. Verifies Deployment with injected environment variables
4. Verifies Service for agent endpoint
5. Checks `status.modelStatus` tracking
6. Verifies cleanup via owner references
