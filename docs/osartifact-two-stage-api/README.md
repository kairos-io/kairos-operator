# OSArtifact: Two-Stage Build API

Single reference for the **current state**, **desired API**, **user scenarios with examples**, and **implementation plan**. Related: [kairos-io/kairos#3892](https://github.com/kairos-io/kairos/issues/3892).

---

## 1. Current state

- **Image source:** Either a pre-built image (`imageName` / ref) or a user-supplied Dockerfile (Secret, `BaseImageDockerfile`). No “options-only” build.
- **Build:** No first-class build args; user must bake everything in the Dockerfile or template. AuroraBoot gets arch, overlay, cloud-config, disk size only.
- **Push:** Operator does not push the built image to a registry.
- **Volume bindings:** Single top-level `volumeBindings` (buildContext, overlayISO, overlayRootfs).

Gap: no parity with factory/docker flows for “I want ubuntu + kairos vX + k3s without writing a Dockerfile.”

---

## 2. Desired state: two-stage model

The flow has **two stages**. The API groups options by stage.

### Stage 1: OCI image (`spec.image`)

Exactly one of:

| Option | Spec | Behavior |
|--------|------|----------|
| Pre-built | `image.ref` | Use existing Kairos image (no build). |
| Build with options | `image.buildOptions` | Operator’s default Dockerfile + build-args (version, baseImage, model, k8s, etc.). |
| Build with Dockerfile | `image.dockerfile` | User Dockerfile (ref, optional templateValuesFrom/templateValues). |

When building (options or dockerfile), optional **`image.push`** (imageName, credentialsSecretRef).

- **image.ref** (string), **image.buildOptions** (object), **image.dockerfile** (object) are mutually exclusive.
- **image.push** only when building; ignored when `image.ref` is set.

### Stage 2: Artifacts (`spec.artifacts`), optional

From the Stage 1 image: ISO, cloud image, netboot, VHD, GCE, etc. (auroraboot). If omitted or no artifact type enabled, only Stage 1 runs (OCI-only: build + optional push).

- **artifacts.***: iso, cloudImage, diskSize, cloudConfigRef, grubConfig, arch, etc.
- **artifacts.overlayISOVolume**, **artifacts.overlayRootfsVolume** (strings): volume names from `spec.volumes` for --overlay-iso and --overlay-rootfs (replaces top-level overlay volumeBindings).

### Volume bindings (scoped)

- **Stage 1:** **image.buildOptions.buildContextVolume**, **image.dockerfile.buildContextVolume** (optional): volume name for Docker build context at /workspace (kaniko).
- **Stage 2:** **artifacts.overlayISOVolume**, **artifacts.overlayRootfsVolume** (optional).
- **spec.volumes** and **spec.importers** remain at spec level (feed both stages as needed).

### Summary

| Path | Spec | Dockerfile | Build args |
|------|------|------------|------------|
| No build | `image.ref` | — | — |
| Build with options | `image.buildOptions` | Embedded default (+ optional preKairosInitSteps) | buildOptions → kaniko --build-arg |
| Build with custom Dockerfile | `image.dockerfile` | User Secret (ref) | User-defined |

- **Conflict rule:** Exactly one of `image.ref`, `image.buildOptions`, `image.dockerfile`. When buildOptions is set, custom Dockerfile group (ref, templateValuesFrom, templateValues) must be unset. baseImage + buildOptions allowed (“kairosify this image”).
- **Push:** When building, optional `image.push`. Not used when only `image.ref` is set.

### Decision tree

1. **Already have a Kairos image?** → `image.ref`. Don’t set buildOptions or dockerfile.
2. **Factory-like build (options only)?** → `image.buildOptions` (at least version). Optionally baseImage to kairosify that image.
3. **Default + extra steps before kairos-init?** → `image.buildOptions` + preKairosInitSteps (or full Dockerfile if steps between install and init).
4. **Full control (custom Dockerfile)?** → `image.dockerfile`. Don’t set buildOptions.
5. **Push built image?** → When building, set `image.push`.

---

## 3. User scenarios and examples

Each example is a target-API manifest (desired state). Secrets: `cloud-config` (key `userdata`) for cloudConfigRef; `registry-credentials` (`.dockerconfigjson`) for push.

| Scenario | Example file | Stage 1 | Stage 2 |
|----------|--------------|---------|---------|
| Use a pre-built image, generate ISO and cloud image | [01-prebuilt-image.yaml](01-prebuilt-image.yaml) | `image.ref` | ISO, cloud image |
| Build with default Dockerfile + options, push, then ISO/cloud | [02-build-options-only.yaml](02-build-options-only.yaml) | `image.buildOptions` + push | ISO, cloud image |
| Same, but kairosify a custom base image | [03-build-options-custom-base.yaml](03-build-options-custom-base.yaml) | `image.buildOptions` (baseImage) + push | ISO |
| Same, plus extra RUN steps before kairos-init | [04-build-options-extra-steps.yaml](04-build-options-extra-steps.yaml) | `image.buildOptions` + preKairosInitSteps + push | ISO |
| Build with your own Dockerfile, push, then ISO | [05-build-dockerfile.yaml](05-build-dockerfile.yaml) | `image.dockerfile` + push | ISO |
| Build with options and push only (no artifacts) | [06-oci-only.yaml](06-oci-only.yaml) | `image.buildOptions` + push | — |
| Pre-built image + importers + overlay volumes (scoped under artifacts) | [07-importers-and-scoped-bindings.yaml](07-importers-and-scoped-bindings.yaml) | `image.ref` | ISO + overlayISOVolume, overlayRootfsVolume + importers |
| Custom Dockerfile + build-context volume (scoped under image.dockerfile) | [08-dockerfile-build-context-volume.yaml](08-dockerfile-build-context-volume.yaml) | `image.dockerfile` + buildContextVolume + importers | ISO |

---

## 4. Implementation plan

### Phase 1: Default Dockerfile + build options (build-iso)

- **CRD:** Add `BuildOptions` (BaseImage, KairosInit, Model, TrustedBoot, KubernetesDistro, KubernetesVersion, Version, FIPS; PreKairosInitSteps optional). Add `ImagePush` (Push, ImageName, CredentialsSecretRef). Group image under `spec.image` (ref | buildOptions | dockerfile, push) and artifacts under `spec.artifacts`; move overlay bindings to artifacts.overlayISOVolume / overlayRootfsVolume; add buildContextVolume under image.buildOptions / image.dockerfile.
- **Validation:** Exactly one of image.ref, image.buildOptions, image.dockerfile. When buildOptions set: version required; custom Dockerfile group unset. buildOptions + BaseImageName allowed; buildOptions + ImageName invalid. Push only when building.
- **Default Dockerfile:** Embed in operator (match kairos/images/Dockerfile ARGs). Single insertion point for PreKairosInitSteps. No network fetch.
- **Reconciler:** If buildOptions set (no custom Dockerfile): render default Dockerfile (with PreKairosInitSteps if set), create Secret, mount for kaniko; pass build-args and --customPlatform=linux/<arch> when spec.arch set. If custom Dockerfile: keep current behavior (no build-args from buildOptions).
- **Kaniko:** Pass --build-arg from BuildOptions; when spec.arch set pass --customPlatform=linux/<arch> (default and custom Dockerfile modes).
- **Push:** When image.push set and we built: after build, push to registry (e.g. container that loads tarball and pushes with credentials from Secret).
- **Tests:** Validation, reconciler mode selection, kaniko args, PreKairosInitSteps injection; E2E with BuildOptions, BaseImageName+BuildOptions, PreKairosInitSteps.

### Phase 2 (optional): Trusted boot (build-uki)

- CRD fields for UKI keys (Secret or volume). When buildOptions.trustedBoot and keys provided: use `auroraboot build-uki` with --public-keys, --sb-key, etc.

### Design decisions (short)

- **Two modes only:** (1) No Dockerfile → build options + default Dockerfile. (2) User supplies full Dockerfile. No “partial Dockerfile” or operator-injected kairos-init block.
- **User Dockerfile = final image:** Operator does not run kairos-init on top of the user’s build. User’s Dockerfile must produce a Kairos-ready image (e.g. run kairos-init inside it).
- **Volume bindings:** Scoped by stage (build context under image; overlays under artifacts) so each binding lives where it is used.
