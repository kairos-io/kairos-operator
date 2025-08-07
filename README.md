# Kairos Operator

[![Tests](https://github.com/kairos-io/kairos-operator/actions/workflows/test.yml/badge.svg)](https://github.com/kairos-io/kairos-operator/actions/workflows/test.yml)

## Table of Contents
- [Overview](#overview)
- [Deploying the operator](#deploying-the-operator)
- [Removing the operator](#removing-the-operator)
- [Running an operation on the Nodes](#running-an-operation-on-the-nodes)
- [Upgrading Kairos](#upgrading-kairos)
- [Using Private Registries](#using-private-registries)
- [Getting Started](#getting-started)
- [Development notes](#development-notes)
- [Contributing](#contributing)
- [License](#license)

## Overview

This is the Kubernetes operator of [kairos](https://kairos.io). It's for day-2 operations of a Kairos kubernetes cluster. It provides 2 custom resources:

- NodeOp: Use for generic operations on the Kubernetes nodes (kairos or not). It allows mounting the host's root filesystem using `hostMountPath` to allow user's scripts to manipulate it or use it (e.g. mount `/dev` to perform upgrades).

- NodeOpUpgrade: A Kairos specific custom resource. Under the hood it creates a NodeOp with a specific script suitable to upgrade Kairos nodes.

## Deploying the operator

To deploy the operator you can use kubectl (provided that the `git` command is available):

``` bash
# Using local directory
kubectl apply -k config/default

# Or using GitHub URL
kubectl apply -k https://github.com/kairos-io/kairos-operator/config/default
```

When the operator starts it will detect which Nodes are running Kairos OS and still label them with the label `kairos.io/managed: true`. This label can be used to target Kairosnodes if you are on a hybrid cluster (both Kairos and non-Kairos nodes).

## Removing the operator

```bash
# Using local directory
kubectl delete -k config/default

# Or using GitHub URL
kubectl delete -k https://github.com/kairos-io/kairos-operator/config/default
```

## Running an operation on the Nodes

This is an example of a basic NodeOp resource:

```yaml
apiVersion: operator.kairos.io/v1alpha1
kind: NodeOp
metadata:
  name: example-nodeop
  namespace: default
spec:
  # NodeSelector to target specific nodes (optional)
  nodeSelector:
    matchLabels:
      kairos.io/managed: "true"
    matchExpressions:
    - key: node-role.kubernetes.io/control-plane
      operator: In
      values: ["true"]

  # The container image to run on each node
  image: busybox:latest

  # ImagePullSecrets for private registries (optional)
  imagePullSecrets:
  - name: private-registry-secret

  # The command to execute in the container
  command: 
    - sh
    - -c
    - |
      echo "Running on node $(hostname)"
      ls -la /host/etc/kairos-release
      cat /host/etc/kairos-release

  # Path where the node's root filesystem will be mounted (defaults to /host)
  hostMountPath: /host

  # Whether to cordon the node before running the operation
  cordon: true

  # Drain options for pod eviction
  drainOptions:
    # Enable draining
    enabled: true
    # Force eviction of pods without a controller
    force: false
    # Grace period for pod termination (in seconds)
    gracePeriodSeconds: 30
    # Ignore DaemonSet pods
    ignoreDaemonSets: true
    # Delete data in emptyDir volumes
    deleteEmptyDirData: false
    # Timeout for drain operation (in seconds)
    timeoutSeconds: 300

  # Whether to reboot the node after successful operation
  rebootOnSuccess: true

  # Number of retries before marking the job failed
  backoffLimit: 3

  # Maximum number of nodes that can run the operation simultaneously
  # 0 means run on all nodes at once
  concurrency: 1

  # Whether to stop creating new jobs when a job fails
  # Useful for canary deployments
  stopOnFailure: true
```

The above example is only trying to demonstrate the various available fields. Not all of them are required, especially for a script like the one in the example.

## Upgrading Kairos

Although a NodeOp can be used to upgrade Kairos, it makes it a lot easier to use NodeOpUpgrade which exposes only the necessary fields for a Kairos upgrade. The actual script and the rest of the NodeOp fields are atomatically taken care of.

The following is an example of a "canary upgrade", which upgrades Kairos node one-by-one (master nodes first). It will stop upgrading if one of the Nodes doesn't complete the upgrade and reboot successfully.

```yaml
apiVersion: operator.kairos.io/v1alpha1
kind: NodeOpUpgrade
metadata:
  name: kairos-upgrade
  namespace: default
spec:
  # The container image containing the new Kairos version
  image: quay.io/kairos/opensuse:leap-15.6-standard-amd64-generic-v3.4.2-k3sv1.30.11-k3s1

  # ImagePullSecrets for private registries (optional)
  imagePullSecrets:
  - name: private-registry-secret

  # NodeSelector to target specific nodes (optional)
  nodeSelector:
    matchLabels:
      kairos.io/managed: "true"

  # Maximum number of nodes that can run the upgrade simultaneously
  # 0 means run on all nodes at once
  concurrency: 1

  # Whether to stop creating new jobs when a job fails
  # Useful for canary deployments
  stopOnFailure: true

  # Whether to upgrade the active partition (defaults to true)
  # upgradeActive: true

  # Whether to upgrade the recovery partition (defaults to false)
  # upgradeRecovery: false

  # Whether to force the upgrade without version checks
  # force: false
```

Some options where left as comments to show what else is possible, but 4 keys is all it takes to upgrade the whole cluster in a safe way.

### How upgrade is performed

Before you attempt an upgrade, it's good to know what to expect. Here is how the process works.

- The operator is notified about the NodeOpUpgrade resource and creates a NodeOp with the appropriate script and options.
- The operator creates a list of matching Nodes using the provided label. If no label is provided, all Nodes will match.
- The list is sorted with master nodes first, and based on the `concurrency` value, the first batch of Nodes will be upgraded (could be just 1 Node).
- Before the upgrade Job is created, the operator creates a Pod that will perform the reboot when the Job completes. Then a `Job` is created that performs the upgrade.
- The Job has one InitContainer, which performs the upgrade and a container which runs only if the upgrade script completes successfully. When the InitContainer exits, the container creates a sentinel file on the host's filesystem which is what the "reboot" Pod waits for, in order to perform the reboot. This way the Job completes successfully before the Node is completed. This is important because it prevents the Job from re-creating its Pod after reboot (which would be the case if the Job performed the reboot before it exited gracefully).
- After the reboot of the Node, the "reboot Pod" will be restarted but it will detect that reboot has already happened (using an annotation on itself that works as a sentinel) and will exit with `0`.
- If everything worked successfully, the operator will create another Job to replace the one that finished, resulting in `concurrency` number of Nodes being upgraded in parallel.

The result of the above process it that each upgrade Job finishes successfully, with no uneccessary restarts. The iupgrade logs can be found in the Job's Pod logs.

The NodeOpUpgrade stores the statuses of the various Jobs it creates so it can be used to monitor the summary of the operation.

## Using Private Registries

The Kairos Operator supports `imagePullSecrets` for both `NodeOp` and `NodeOpUpgrade` resources, allowing you to pull images from private container registries.

### Creating Image Pull Secrets

Before using `imagePullSecrets`, you need to create a Kubernetes secret containing your registry credentials. Here are examples for different registry types ([See also here](https://kubernetes.io/docs/concepts/containers/images/#creating-a-secret-with-a-docker-config)):

#### Docker Hub

```bash
kubectl create secret docker-registry private-registry-secret \
  --docker-server=https://index.docker.io/v1/ \
  --docker-username=your-username \
  --docker-password=your-password \
  --docker-email=your-email@example.com
```

#### Private Registry

```bash
kubectl create secret docker-registry private-registry-secret \
  --docker-server=private-registry.example.com \
  --docker-username=your-username \
  --docker-password=your-password \
  --docker-email=your-email@example.com
```

#### Using a .docker/config.json file

```bash
kubectl create secret generic private-registry-secret \
  --from-file=.dockerconfigjson=/path/to/.docker/config.json \
  --type=kubernetes.io/dockerconfigjson
```

### Example with Private Registry

```yaml
apiVersion: operator.kairos.io/v1alpha1
kind: NodeOp
metadata:
  name: operation-with-private-image
  namespace: default
spec:
  # The container image from private registry
  image: private-registry.example.com/kairos/opensuse:leap-15.6-standard-amd64-generic-v3.4.2-k3sv1.30.11-k3s1

  # ImagePullSecrets to authenticate with the private registry
  imagePullSecrets:
  - name: private-registry-secret

  # Target specific nodes
  nodeSelector:
    matchLabels:
      kairos.io/managed: "true"

  # Run one node at a time
  concurrency: 1

  # Stop if any job fails
  stopOnFailure: true

  # Reboot after successful operation
  rebootOnSuccess: true

  # Cordon and drain nodes before operation
  cordon: true
  drainOptions:
    enabled: true
    force: false
    gracePeriodSeconds: 30
    ignoreDaemonSets: true
    deleteEmptyDirData: false
    timeoutSeconds: 300

  # The command to run on each node
  command:
    - /bin/sh
    - -c
    - |
      #!/bin/bash
      echo "Running on node $(hostname)"
      ls -la /host/etc/kairos-release
      cat /host/etc/kairos-release
```

### How It Works

1. When you specify `imagePullSecrets` in a `NodeOp` or `NodeOpUpgrade` resource, the operator will include these secrets in the Pod spec of the jobs it creates.

2. For `NodeOpUpgrade` resources, the `imagePullSecrets` are automatically passed to the underlying `NodeOp` resource that gets created.

3. The Kubernetes kubelet on each node will use these secrets to authenticate with the container registry when pulling the specified images.

### Notes

- The secrets must exist in the same namespace as the NodeOp or NodeOpUpgrade resource
- Multiple secrets can be specified if needed
- The secrets are only used for pulling the main operation image, not for any additional images that might be used internally by the operator

## Development notes

This project is managed with [kubebuilder](https://book.kubebuilder.io).

### Running tests

There are 2 tests suites in this project. The end-to-end suite can be run with:

```bash
ginkgo test/e2e
```

the controllers test suite can be run with:

```bash
ginkgo internal/controller
```

## Contributing

TODO
