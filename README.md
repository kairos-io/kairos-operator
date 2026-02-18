# Kairos Operator

[![Tests](https://github.com/kairos-io/kairos-operator/actions/workflows/test.yml/badge.svg)](https://github.com/kairos-io/kairos-operator/actions/workflows/test.yml)

This is the Kubernetes operator of [Kairos](https://kairos.io), for day-2 operations of Kairos clusters. It provides custom resources for running operations on nodes, upgrading Kairos, and building OS artifacts.

For user documentation (installation, usage, examples), see the [Kairos Operator docs](https://kairos.io/docs/operator/).

## Development notes

This project is managed with [kubebuilder](https://book.kubebuilder.io).

### Running tests

There are multiple test suites in this project:

**Unit tests** (using envtest):
```bash
make test
```

**Controller tests** (OSArtifact tests requiring a real cluster):
```bash
make controller-tests
```
This will set up a kind cluster, deploy the operator, and run OSArtifact controller tests.

**End-to-end tests**:
```bash
make test-e2e
```
Or using ginkgo directly:
```bash
ginkgo test/e2e
```

**All controller tests** (including NodeOp, NodeOpUpgrade, and OSArtifact):
```bash
ginkgo internal/controller
```

Note: OSArtifact controller tests require `USE_EXISTING_CLUSTER=true` and will be skipped in the unit test suite. Use `make controller-tests` to run them with a real cluster.

## Contributing

TODO
