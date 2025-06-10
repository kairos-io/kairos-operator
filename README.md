# Kairos Operator

> ⚠️ **WARNING: This operator is currently a work in progress and not ready for production use.** Until it's ready for consumption, this README won't always reflect the current state of things.


# TODO:

- When we have label filtering for NodeOpUpgrade, we should explain to the user that the Label should always include a master node (unless they are already upgraded). The reason is because the cluster could end up in a deadlock situation if the operator is running on a worker node, the user does a "canary upgrade" (e.g. one-by-one Node) and the new k3s version is not compatible with the master nodes. This will result in the operator never becoming ready after its Node's restart, thus not creating the rest of the upgrade Jobs. This cluster won't recover unless the operator is scheduled on one of the still running Nodes.
  The solution to this is to always perform the upgrade on master nodes first. Optionally, we can refuse to upgrade is there is no master node in the target list or check that we have at least one master node already running the target k8s version, but this is harder to implement (we don't know what k8s version is in the upgrade image).
  Let's just document the situation. Most people will allow the ugprade to run on all nodes. So, by simply performing the upgrade on the master nodes first, we should be good.
  Even if the deadlock occurs, usually it's enough to kill the operator Pod and let it be restarted on a Node that is still usable.


[![Tests](https://github.com/kairos-io/kairos-operator/actions/workflows/test.yml/badge.svg)](https://github.com/kairos-io/kairos-operator/actions/workflows/test.yml)

## Implementation Plan (TODO list)


- Deploy a **DaemonSet (`KairosNodeStatus`)** on all nodes.
  - Mount `/etc/kairos-release` and `/etc/os-release` using `hostPath`.
  - Detect if the node is Kairos-based.
  - If so, create or update a `KairosNode` CR named after the node.
  - Only update the `status` field (e.g., `observedVersion`, `cloudConfigHash`, `upgradeState`).

- Define a **`KairosNode` CRD**:
  - `.spec`: desired version, active/recovery images, config hash, etc.
  - `.status`: current version, upgrade progress, timestamps.

- The **Operator**:
  - Watches `KairosNode` resources.
  - Compares `spec.version` vs `status.observedVersion`.
  - Triggers an **upgrade Job** when needed and `upgradeState != InProgress`.
  - The Job:
    - Drains the node (excluding upgrade and status Pods).
    - Performs upgrade and reboots the node.
    - Optionally uncordons the node.

- After reboot, the DaemonSet resumes and updates `KairosNode.status`.
- Operator detects successful upgrade and marks the state as `Completed`.

---

## 🔐 Security & Permissions

- DaemonSet Pods:
  - Run unprivileged.
  - Mount only required files (`hostPath: /etc/kairos-release`, etc).
  - Use a dedicated `ServiceAccount` with RBAC:
    ```yaml
    apiGroups: ["kairos.io"]
    resources: ["kairosnodes/status"]
    verbs: ["get", "patch", "update"]
    ```

- Use **projected service account tokens** with:
  - Short expiration (e.g., `600s`)
  - Custom audience (e.g., `kairos-operator`)

---

## 🧠 Behavioral Logic

- DaemonSet creates CRs only for **Kairos nodes** (detected via `/etc/os-release`).
- `KairosNode.status.upgradeState` acts as a state machine:
  - `Idle` → `InProgress` → `Completed`
- Node is uncordoned by:
  - Upgrade Job (preferred), or
  - Operator after detecting version match.

---

## 📌 Notes

- Do **not** create `KairosNode` for non-Kairos nodes.
- Operator does **not** update built-in `Node` resources.
- Optional: label Kairos nodes for visibility:
  ```yaml
  labels:
    kairos.io/managed: "true"


## Getting Started

### Prerequisites
- go version v1.23.0+
- docker version 17.03+.
- kubectl version v1.11.3+.
- Access to a Kubernetes v1.11.3+ cluster.

### To Deploy on the cluster
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/operator:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don't work.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/operator:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

>**NOTE**: Ensure that the samples has default values to test it out.

### To Uninstall
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Project Distribution

Following the options to release and provide this solution to the users.

### By providing a bundle with all YAML files

1. Build the installer for the image built and published in the registry:

```sh
make build-installer IMG=<some-registry>/operator:tag
```

**NOTE:** The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without its
dependencies.

2. Using the installer

Users can just run 'kubectl apply -f <URL for YAML BUNDLE>' to install
the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/operator/<tag or branch>/dist/install.yaml
```

### By providing a Helm Chart

1. Build the chart using the optional helm plugin

```sh
kubebuilder edit --plugins=helm/v1-alpha
```

2. See that a chart was generated under 'dist/chart', and users
can obtain this solution from there.

**NOTE:** If you change the project, you need to update the Helm Chart
using the same command above to sync the latest changes. Furthermore,
if you create webhooks, you need to use the above command with
the '--force' flag and manually ensure that any custom configuration
previously added to 'dist/chart/values.yaml' or 'dist/chart/manager/manager.yaml'
is manually re-applied afterwards.

## Contributing
// TODO(user): Add detailed information on how you would like others to contribute to this project

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

