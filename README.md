# Kairos Operator

[![Tests](https://github.com/kairos-io/kairos-operator/actions/workflows/test.yml/badge.svg)](https://github.com/kairos-io/kairos-operator/actions/workflows/test.yml)

This is the Kubernetes operator of [Kairos](https://kairos.io), for day-2 operations of Kairos clusters. It provides custom resources for running operations on nodes, upgrading Kairos, and building OS artifacts.

For user documentation (installation, usage, examples), see the [Kairos Operator docs](https://kairos.io/docs/operator/).

For one-shot semantics and reusing manifests (`generateName`, `kubectl create`), see the **One-off** sections in the [OSArtifact](https://kairos.io/operator-docs/osartifact/), [NodeOp](https://kairos.io/operator-docs/nodeop/), and [NodeOpUpgrade](https://kairos.io/operator-docs/nodeop-upgrade/) pages under the operator documentation.

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

Note: OSArtifact controller tests require `USE_EXISTING_CLUSTER=true` and will be skipped in the unit test suite. Use `make controller-tests` to run them: it sets up a Kind cluster and installs CRDs but does not deploy the operator, so the test (using a direct client) is the only actor.

## Contributing

Contributions are welcome! This repo follows the [main Kairos contributing guide](https://github.com/kairos-io/kairos/blob/master/CONTRIBUTING.md).

### Quick start

1. Fork this repository and add it as `upstream`.
2. Create a feature branch: `git checkout -b feat/rename-to-chronos`
3. Make your changes (see **Operator-specific patterns** below).
4. Run `make test` and `make lint` before submitting.
5. Open a PR against `main`.

### Operator-specific patterns

- **API types** live in `api/v1alpha2/`. Add new fields there.
- **Controllers** live in `internal/controller/`.
- After changing API types or controllers, regenerate:

  ```bash
  make generate   # DeepCopy
  make manifests  # CRDs
  ```

- **Tests** - unit tests use [envtest](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/envtest); controller/e2e tests spin up a [Kind](https://kind.sigs.k8s.io/) cluster.
