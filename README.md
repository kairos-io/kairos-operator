# Kairos Operator

[![Tests](https://github.com/kairos-io/kairos-operator/actions/workflows/test.yml/badge.svg)](https://github.com/kairos-io/kairos-operator/actions/workflows/test.yml)

This is the Kubernetes operator of [Kairos](https://kairos.io), for day-2 operations of Kairos clusters. It provides custom resources for running operations on nodes, upgrading Kairos, and building OS artifacts.

For user documentation (installation, usage, examples), see the [Kairos Operator docs](https://kairos.io/docs/operator/).

For one-shot semantics and reusing manifests (`generateName`, `kubectl create`), see the **One-off** sections in the [OSArtifact](https://kairos.io/operator-docs/osartifact/), [NodeOp](https://kairos.io/operator-docs/nodeop/), and [NodeOpUpgrade](https://kairos.io/operator-docs/nodeop-upgrade/) pages under the operator documentation.

> **Found a bug, or want to request a feature?** Open it on
> [kairos-io/kairos](https://github.com/kairos-io/kairos/issues), including
> issues about this repository. Every Kairos issue lives in one place, so you
> never have to work out which repository to file against.

## Development notes

This project is managed with [kubebuilder](https://book.kubebuilder.io).

### Repository layout

- `api/v1alpha1/` - `NodeOp` and `NodeOpUpgrade` types (node operations and upgrades).
- `api/v1alpha2/` - `OSArtifact` types (OS image building: iso, cloud, azure, gce, netboot, uki).
- `internal/controller/` - reconcilers: `nodeop_controller.go`, `nodeopupgrade_controller.go`, `osartifact_controller.go`, plus internal `nodelabeler_controller.go` / `nodelabeler_daemonset_controller.go` (no CRD, manages the labeler DaemonSet).
- `config/` - kustomize manifests: CRDs, RBAC, deployment, dev overrides.
- `charts/` - Helm chart for deploying the operator.
- `test/e2e/` - ginkgo end-to-end suites.
- `examples/` - sample CRs.

### API changes & CRD sync

API types live in `api/` (see layout above). Add new fields to the appropriate version's types; new CRDs go into a new version package.

When you change API types, regenerate the generated code and CRDs, then sync the CRDs into the Helm chart:

```bash
make update-crds
```

This runs `make generate` (DeepCopy), `make manifests` (CRDs), and `make sync-crds` (copies `config/crd/bases/` into `charts/kairos-operator/crds/`). You can also run the pieces individually:

```bash
make generate   # DeepCopy
make manifests  # CRDs
make sync-crds  # copy config/crd/bases/ -> charts/kairos-operator/crds/
```

Commit the regenerated files **and** the synced chart CRDs together. Use `make diff-crds` to check sync locally.

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

### Local development

- `make run` runs the operator against your local cluster.
- `make deploy-dev` deploys the dev config.
- `make install` / `make uninstall` manage CRDs.
- Unit tests use [envtest](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/envtest); controller tests (`make controller-tests`) and e2e tests spin up a [Kind](https://kind.sigs.k8s.io/) cluster.

## Contributing

Contributions are welcome! This repo follows the [main Kairos contributing guide](https://github.com/kairos-io/kairos/blob/master/CONTRIBUTING.md).

### Workflow

1. Fork the repository, then clone your fork locally. Configure your fork as the
`origin` remote and the original repository as `upstream`.
2. Create a feature branch: `git checkout -b feat/my-change`
3. Make your changes (see **Repository layout** above). For API type changes, follow **API changes & CRD sync**.
4. Verify locally before submitting: `make lint` (static analysis), plus the test suites in **Running tests**.
5. Open a PR against `main`. CI runs the same suites, including the `CRDs sync` check.
