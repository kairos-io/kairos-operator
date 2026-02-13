# Kairos Operator

[![Tests](https://github.com/kairos-io/kairos-operator/actions/workflows/test.yml/badge.svg)](https://github.com/kairos-io/kairos-operator/actions/workflows/test.yml)

## Table of Contents
- [Overview](#overview)
- [Deploying the operator](#deploying-the-operator)
- [Removing the operator](#removing-the-operator)
- [Running an operation on the Nodes](#running-an-operation-on-the-nodes)
- [Upgrading Kairos](#upgrading-kairos)
- [Building OS Artifacts](#building-os-artifacts)
- [Dockerfile Templating](#dockerfile-templating)
- [Serving Artifacts with Nginx](#serving-artifacts-with-nginx)
- [Using Private Registries](#using-private-registries)
- [Getting Started](#getting-started)
- [Development notes](#development-notes)
- [Contributing](#contributing)
- [License](#license)

## Overview

This is the Kubernetes operator of [kairos](https://kairos.io). It's for day-2 operations of a Kairos kubernetes cluster. It provides 3 custom resources:

- **NodeOp**: Use for generic operations on the Kubernetes nodes (kairos or not). It allows mounting the host's root filesystem using `hostMountPath` to allow user's scripts to manipulate it or use it (e.g. mount `/dev` to perform upgrades).

- **NodeOpUpgrade**: A Kairos specific custom resource. Under the hood it creates a NodeOp with a specific script suitable to upgrade Kairos nodes.

- **OSArtifact**: Build Linux distribution artifacts (ISO, cloud images, netboot artifacts, etc.) from container images directly in Kubernetes. This allows you to build Kairos OS images and other artifacts as Kubernetes-native resources.

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

## Building OS Artifacts

The operator includes the **OSArtifact** custom resource that allows you to build Linux distribution artifacts (ISO images, cloud images, netboot artifacts, etc.) from container images directly in Kubernetes. This is particularly useful for building Kairos OS images and other bootable artifacts.

### Basic Example

Here's a simple example that builds an ISO image from a Kairos container image:

```yaml
apiVersion: build.kairos.io/v1alpha2
kind: OSArtifact
metadata:
  name: my-kairos-iso
  namespace: default
spec:
  # The container image to build from (can be a Kairos image or vanilla Linux image)
  imageName: quay.io/kairos/opensuse:leap-15.6-core-amd64-generic-v3.6.0

  # Build an ISO image
  iso: true

  # Optional: Export the artifact using a custom job
  exporters:
  - template:
      spec:
        restartPolicy: Never
        containers:
        - name: copy
          image: debian:latest
          command: ["bash", "-c"]
          args:
          - |
            # Copy the built ISO to a location of your choice
            cp /artifacts/*.iso /output/ || true
          volumeMounts:
          - name: artifacts
            readOnly: true
            mountPath: /artifacts
          - name: output
            mountPath: /output
        volumes:
        - name: artifacts
          persistentVolumeClaim:
            claimName: artifact-pvc
        - name: output
          # Your output volume configuration
```

### Supported Artifact Formats

The OSArtifact resource supports building various artifact formats:

#### ISO Images
```yaml
spec:
  imageName: quay.io/kairos/opensuse:leap-15.6-core-amd64-generic-v3.6.0
  iso: true
```

#### Cloud Images (Raw Disk)
```yaml
spec:
  imageName: quay.io/kairos/opensuse:leap-15.6-core-amd64-generic-v3.6.0
  cloudImage: true
  diskSize: "10G"  # Optional: specify disk size
```

#### Azure VHD Images
```yaml
spec:
  imageName: quay.io/kairos/opensuse:leap-15.6-core-amd64-generic-v3.6.0
  azureImage: true
```

#### GCE Images
```yaml
spec:
  imageName: quay.io/kairos/opensuse:leap-15.6-core-amd64-generic-v3.6.0
  gceImage: true
```

#### Netboot Artifacts
```yaml
spec:
  imageName: quay.io/kairos/opensuse:leap-15.6-core-amd64-generic-v3.6.0
  iso: true
  netboot: true
  netbootURL: "http://example.com/netboot"  # URL where artifacts will be served
```

### Building from Dockerfiles

You can also build from a Dockerfile stored in a Kubernetes Secret:

```yaml
apiVersion: build.kairos.io/v1alpha2
kind: OSArtifact
metadata:
  name: custom-build
  namespace: default
spec:
  baseImageDockerfile:
    name: my-dockerfile-secret
    key: Dockerfile  # Optional: defaults to "Dockerfile"
  iso: true
```

### Dockerfile Templating

When building from a Dockerfile (via `baseImageDockerfile`), you can use Go template syntax in the Dockerfile. This lets you parameterize your builds — for example, changing the base image or injecting version strings — without maintaining multiple Dockerfiles.

Template variables use the standard Go template syntax `{{ .VariableName }}`. Values can be provided in two ways, and both can be used together:

1. **Inline values** (`dockerfileTemplateValues`) — a map of key-value pairs defined directly in the OSArtifact spec.
2. **Secret reference** (`dockerfileTemplateValuesFrom`) — a reference to a Kubernetes Secret whose data entries are used as template values.

When both are set, inline values take precedence on key conflicts.

#### Example Dockerfile with template variables

Create a Secret containing a Dockerfile that uses template variables:

```bash
kubectl create secret generic my-dockerfile --from-file=Dockerfile
```

Where `Dockerfile` contains:

```dockerfile
FROM {{ .BaseImage }}

RUN zypper install -y {{ .ExtraPackages }}
RUN echo "Version: {{ .Version }}" > /etc/build-info
```

#### Example: Inline template values

```yaml
apiVersion: build.kairos.io/v1alpha2
kind: OSArtifact
metadata:
  name: custom-build
  namespace: default
spec:
  baseImageDockerfile:
    name: my-dockerfile
    key: Dockerfile
  dockerfileTemplateValues:
    BaseImage: "opensuse/leap:15.6"
    ExtraPackages: "vim curl"
    Version: "1.0.0"
  iso: true
```

#### Example: Template values from a Secret

First, create a Secret with the template values:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: dockerfile-values
  namespace: default
stringData:
  BaseImage: "opensuse/leap:15.6"
  ExtraPackages: "vim curl"
  Version: "1.0.0"
```

Then reference it in the OSArtifact:

```yaml
apiVersion: build.kairos.io/v1alpha2
kind: OSArtifact
metadata:
  name: custom-build
  namespace: default
spec:
  baseImageDockerfile:
    name: my-dockerfile
    key: Dockerfile
  dockerfileTemplateValuesFrom:
    name: dockerfile-values
  iso: true
```

#### Combining both sources

You can use a Secret for base values and override specific ones inline:

```yaml
apiVersion: build.kairos.io/v1alpha2
kind: OSArtifact
metadata:
  name: custom-build
  namespace: default
spec:
  baseImageDockerfile:
    name: my-dockerfile
    key: Dockerfile
  dockerfileTemplateValuesFrom:
    name: dockerfile-values
  dockerfileTemplateValues:
    Version: "2.0.0"  # overrides the value from the Secret
  iso: true
```

#### Template restrictions

Only simple value substitution and basic control flow (`if`/`else`/`range`/`with`) are supported. The `define`, `template`, and `block` directives are explicitly forbidden and will cause the build to fail. Any template variable that is not provided will render as an empty string.

### Image Sources

The OSArtifact resource supports three ways to specify the source image:

1. **Pre-built Kairos image** (using `imageName`):
   ```yaml
   spec:
     imageName: quay.io/kairos/opensuse:leap-15.6-core-amd64-generic-v3.6.0
   ```

2. **Vanilla Linux image** (using `baseImageName`):
   ```yaml
   spec:
     baseImageName: opensuse/leap:15.6
     iso: true
   ```
   The operator will attempt to convert this to a Kairos image.

3. **Dockerfile in a Secret** (using `baseImageDockerfile`):
   ```yaml
   spec:
     baseImageDockerfile:
       name: dockerfile-secret
       key: Dockerfile
     iso: true
   ```

### Monitoring Build Status

The OSArtifact resource tracks the build status through the `status.phase` field:

- `Pending`: The artifact is queued for building
- `Building`: The build is in progress
- `Exporting`: The artifact is being exported (if exporters are configured)
- `Ready`: The artifact build completed successfully
- `Error`: The build failed

You can check the status with:

```bash
kubectl get osartifact my-kairos-iso
kubectl describe osartifact my-kairos-iso
```

### Accessing Built Artifacts

Built artifacts are stored in a PersistentVolumeClaim (PVC) that is automatically created. You can access them through:

1. **Export Jobs**: Configure `exporters` in the spec to run custom jobs that can copy, upload, or process the artifacts
2. **Direct PVC Access**: The PVC is labeled with `build.kairos.io/artifact=<artifact-name>` and can be mounted by other pods

### Serving Artifacts with Nginx

The project includes a ready-to-use nginx kustomization at `config/nginx` that deploys an nginx server with WebDAV upload support. This lets OSArtifact exporters upload built artifacts via HTTP PUT and then serve them for download.

Deploy it with:

```bash
# Deploy in the same namespace as your OSArtifact resources
kubectl apply -k config/nginx -n default

# Or using GitHub URL
kubectl apply -k https://github.com/kairos-io/kairos-operator/config/nginx -n default
```

This creates:
- An **nginx Deployment** with a PersistentVolumeClaim for artifact storage
- A **ConfigMap** with an nginx configuration that enables WebDAV PUT and directory listing
- A **NodePort Service** (`kairos-operator-nginx`) to expose the server
- A **Role** (`artifactCopier`) with permissions to list pods and exec into them

Once deployed, configure your OSArtifact exporter to upload artifacts to the nginx service:

```yaml
apiVersion: build.kairos.io/v1alpha2
kind: OSArtifact
metadata:
  name: my-kairos-iso
  namespace: default
spec:
  imageName: quay.io/kairos/opensuse:leap-15.6-core-amd64-generic-v3.6.0
  iso: true
  exporters:
  - template:
      spec:
        restartPolicy: Never
        containers:
        - name: upload-to-nginx
          image: curlimages/curl
          command: ["sh", "-ec"]
          args:
          - |
            NGINX_URL="${NGINX_URL:-http://kairos-operator-nginx}"
            for f in /artifacts/*; do
              [ -f "$f" ] || continue
              base=$(basename "$f")
              echo "Uploading $base to $NGINX_URL/$base"
              curl -fsSL -T "$f" "$NGINX_URL/$base" || exit 1
            done
            echo "Upload done"
          env:
          - name: NGINX_URL
            value: "http://kairos-operator-nginx"
          volumeMounts:
          - name: artifacts
            readOnly: true
            mountPath: /artifacts
```

If the nginx service is deployed in a different namespace than the OSArtifact, set the `NGINX_URL` environment variable to `http://kairos-operator-nginx.<namespace>.svc.cluster.local`.

After the exporter completes, artifacts are available for download through the nginx service (accessible via the NodePort or from within the cluster at `http://kairos-operator-nginx`).

### Advanced Configuration

```yaml
apiVersion: build.kairos.io/v1alpha2
kind: OSArtifact
metadata:
  name: advanced-build
  namespace: default
spec:
  imageName: quay.io/kairos/opensuse:leap-15.6-core-amd64-generic-v3.6.0

  # Build multiple formats
  iso: true
  cloudImage: true
  azureImage: true

  # Custom cloud config
  cloudConfigRef:
    name: cloud-config-secret
    key: cloud-config.yaml

  # Custom GRUB configuration
  grubConfig: |
    set timeout=5
    set default=0

  # OS release information
  osRelease: "opensuse-leap-15.6"
  kairosRelease: "v3.6.0"

  # Additional bundles to include
  bundles:
  - docker
  - k3s

  # Image pull secrets for private registries
  imagePullSecrets:
  - name: private-registry-secret

  # Custom volume configuration
  volume:
    accessModes:
    - ReadWriteOnce
    resources:
      requests:
        storage: 50Gi
```

For more information, see the [Kairos documentation on building artifacts](https://kairos.io/docs/advanced/build/).

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
