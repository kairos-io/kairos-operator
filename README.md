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

### Workflow

1. Fork the repository, then clone your fork locally. Configure your fork as the
`origin` remote and the original repository as `upstream`.
2. Create a feature branch: `git checkout -b feat/my-change`
3. Make your changes (see **Repository layout** and **Operator-specific patterns** below).
4. Verify locally before submitting:
   - `make lint` - static analysis
   - `make test` - unit tests (envtest); `make build` compiles the manager binary
   - `make controller-tests` - OSArtifact controller tests against a real Kind cluster
   - `make test-e2e` (or `ginkgo test/e2e`) - end-to-end tests
5. Open a PR against `main`. CI runs the same suites.

### Repository layout

- `api/v1alpha1/` - `NodeOp` and `NodeOpUpgrade` types (node operations and upgrades).
- `api/v1alpha2/` - `OSArtifact` types (OS image building: iso, cloud, azure, gce, netboot, uki).
- `internal/controller/` - reconcilers: `nodeop_controller.go`, `nodeopupgrade_controller.go`, `osartifact_controller.go`, plus internal `nodelabeler_controller.go` / `nodelabeler_daemonset_controller.go` (no CRD, manages the labeler DaemonSet).
- `config/` - kustomize manifests: CRDs, RBAC, deployment, dev overrides.
- `charts/` - Helm chart for deploying the operator.
- `test/e2e/` - ginkgo end-to-end suites.
- `examples/` - sample CRs.

### Operator-specific patterns

- **API types** live in `api/` (see layout above). Add new fields to the appropriate version's types; new CRDs go into a new version package.
- **Controllers** live in `internal/controller/`, one file per reconciler.
- After changing API types or controllers, regenerate:

  ```bash
  make generate   # DeepCopy
  make manifests  # CRDs
  ```

- **Tests** - unit tests use [envtest](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/envtest); controller tests (`make controller-tests`) and e2e tests spin up a [Kind](https://kind.sigs.k8s.io/) cluster.
- **Deploy for local development**: `make run` runs the manager against your local cluster; `make deploy-dev` deploys the dev config; `make install`/`make uninstall` manage CRDs.
