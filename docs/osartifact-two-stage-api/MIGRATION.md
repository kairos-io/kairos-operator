# Migration: OSArtifact to Two-Stage API

**⚠️ Breaking change.** If you are upgrading from an operator version that used the previous OSArtifact spec, you must update your `OSArtifact` resources **before** upgrading to a release that only supports the new two-stage structure.

## What changed

The OSArtifact API was restructured into a **two-stage model**:

- **Stage 1 (`spec.image`)** — How to obtain the OCI image: pre-built ref, build with options (default OCI build definition), or build with your OCI spec.
- **Stage 2 (`spec.artifacts`)** — Which artifacts to produce (ISO, cloud image, etc.) and options (arch, overlays, cloud-config).

Legacy top-level fields were removed. There is no backward compatibility layer.

## Field mapping (old → new)

| Old (removed) | New |
|---------------|-----|
| `spec.imageName` | `spec.image.ref` |
| `spec.baseImageName` | `spec.image.buildOptions.baseImage` (when using buildOptions) |
| `spec.baseImageDockerfile` | `spec.image.ociSpec.ref` |
| `spec.dockerfileTemplateValuesFrom` | `spec.image.ociSpec.templateValuesFrom` |
| `spec.dockerfileTemplateValues` | `spec.image.ociSpec.templateValues` |
| `spec.iso` | `spec.artifacts.iso` |
| `spec.cloudImage` | `spec.artifacts.cloudImage` |
| `spec.azureImage` | `spec.artifacts.azureImage` |
| `spec.gceImage` | `spec.artifacts.gceImage` |
| `spec.netboot` | `spec.artifacts.netboot` |
| `spec.netbootURL` | `spec.artifacts.netbootURL` |
| `spec.diskSize` | `spec.artifacts.diskSize` |
| `spec.arch` | `spec.artifacts.arch` |
| `spec.cloudConfigRef` | `spec.artifacts.cloudConfigRef` |
| `spec.grubConfig` | `spec.artifacts.grubConfig` |
| `spec.bundles` | `spec.artifacts.bundles` |
| `spec.osRelease` | `spec.artifacts.osRelease` |
| `spec.kairosRelease` | `spec.artifacts.kairosRelease` |
| `spec.volumeBindings.buildContext` | `spec.image.ociSpec.buildContextVolume` or `spec.image.buildOptions.buildContextVolume` |
| `spec.volumeBindings.overlayISO` | `spec.artifacts.overlayISOVolume` |
| `spec.volumeBindings.overlayRootfs` | `spec.artifacts.overlayRootfsVolume` |

Unchanged at `spec`: `volumes`, `importers`, `exporters`, `volume`, `imagePullSecrets`.

## Example: before and after

**Before (legacy):**

```yaml
apiVersion: build.kairos.io/v1alpha2
kind: OSArtifact
metadata:
  name: my-artifact
spec:
  imageName: quay.io/kairos/opensuse:leap-15.6-core-amd64-generic-v3.6.0
  iso: true
  cloudImage: true
  arch: amd64
  cloudConfigRef:
    name: cloud-config
    key: userdata
```

**After (two-stage):**

```yaml
apiVersion: build.kairos.io/v1alpha2
kind: OSArtifact
metadata:
  name: my-artifact
spec:
  image:
    ref: quay.io/kairos/opensuse:leap-15.6-core-amd64-generic-v3.6.0
  artifacts:
    arch: amd64
    iso: true
    cloudImage: true
    cloudConfigRef:
      name: cloud-config
      key: userdata
```

## Steps to migrate

1. **Before upgrading the operator:** Edit each `OSArtifact` in the cluster (or in Git) and move fields as in the table above. Ensure `spec.image` has exactly one of: `ref`, `buildOptions`, or `ociSpec`. Add `spec.artifacts` with any artifact types and options you used before.
2. Apply the updated manifests (or commit and deploy).
3. Upgrade the operator to the version that only supports the two-stage API.

See the example YAMLs in this directory (`01-prebuilt-image.yaml` through `08-ocispec-build-context-volume.yaml`) and the [README](README.md) for the full API description.
