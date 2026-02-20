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

- **When `image.ref` is set:** the image is pre-built (no build). BuildOptions and OCISpec are ignored.
- **When `image.ref` is empty:** we build. At least one of **image.buildOptions** or **image.ociSpec** must be set; **both may be set**.

Final build definition is constructed as:

| Mode | Spec | Behavior |
|------|------|----------|
| Pre-built | `image.ref` | Use existing Kairos image (no build). |
| OCISpec only | `image.ociSpec` | User’s OCI spec is the full definition (must include base image FROM and kairos-init; “advanced” mode). |
| BuildOptions only | `image.buildOptions` | Operator injects `FROM buildOptions.baseImage` at the top (when set), then default kairos-init flow (version required). |
| OCISpec + BuildOptions | both | Operator injects `FROM buildOptions.baseImage` at the top (when set), then user’s spec (dashboard + user parts, no FROM), then kairos-init at the bottom; BuildOptions supply args to kairos-init. |

When building, optional **`image.push`** (imageName, credentialsSecretRef). Ignored when `image.ref` is set.

### Stage 2: Artifacts (`spec.artifacts`), optional

From the Stage 1 image: ISO, cloud image, netboot, VHD, GCE, etc. (auroraboot). If omitted or no artifact type enabled, only Stage 1 runs (OCI-only: build + optional push).

- **artifacts.***: iso, cloudImage, diskSize, cloudConfigRef, grubConfig, arch, etc.
- **artifacts.overlayISOVolume**, **artifacts.overlayRootfsVolume** (strings): volume names from `spec.volumes` for --overlay-iso and --overlay-rootfs (replaces top-level overlay volumeBindings).

### Volume bindings (scoped)

- **Stage 1:** **image.ociSpec.buildContextVolume** (optional): volume name for OCI build context at /workspace (kaniko). Only used when building from OCISpec (BuildOptions-only has no user COPY).
- **Stage 2:** **artifacts.overlayISOVolume**, **artifacts.overlayRootfsVolume** (optional).
- **spec.volumes** and **spec.importers** remain at spec level (feed both stages as needed).

### Summary

| Path | Spec | Build definition | Build args |
|------|------|------------------|------------|
| No build | `image.ref` | — | — |
| BuildOptions only | `image.buildOptions` | Operator: FROM baseImage (if set) + default kairos-init | buildOptions → kaniko --build-arg |
| OCISpec only | `image.ociSpec` | User Secret (ref), full definition (FROM + kairos-init) | User-defined |
| OCISpec + BuildOptions | `image.ociSpec` + `image.buildOptions` | Operator: FROM baseImage (if set) + user spec + kairos-init at bottom | BuildOptions → kairos-init args |

- **Rules:** When `image.ref` is empty, at least one of buildOptions or ociSpec. Both allowed (operator then injects FROM + kairos-init). When ref is set, buildOptions/ociSpec are ignored.
- **Push:** When building, optional `image.push`. Ignored when `image.ref` is set.

### Decision tree

1. **Already have a Kairos image?** → `image.ref`.
2. **Factory-like build (options only)?** → `image.buildOptions` (version required). Set baseImage to choose the base image; operator injects FROM and kairos-init.
3. **Full control (your Dockerfile, you include FROM and kairos-init)?** → `image.ociSpec` only.
4. **Your fragment but operator adds base image and kairos-init?** → `image.ociSpec` + `image.buildOptions`. Set buildOptions.baseImage for the injected FROM; operator injects it at the top and kairos-init at the bottom. Your template must not add its own FROM.
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
| Build with your own OCI build definition, push, then ISO | [05-build-dockerfile.yaml](05-build-dockerfile.yaml) | `image.ociSpec` + push | ISO |
| Build with options and push only (no artifacts) | [06-oci-only.yaml](06-oci-only.yaml) | `image.buildOptions` + push | — |
| Pre-built image + importers + overlay volumes (scoped under artifacts) | [07-importers-and-scoped-bindings.yaml](07-importers-and-scoped-bindings.yaml) | `image.ref` | ISO + overlayISOVolume, overlayRootfsVolume + importers |
| Custom OCI spec + build-context volume (scoped under image.ociSpec) | [08-ocispec-build-context-volume.yaml](08-ocispec-build-context-volume.yaml) | `image.ociSpec` + buildContextVolume + importers | ISO |
| Templated Dockerfile (dashboard + user parts) + BuildOptions (baseImage) | [09-ocispec-buildoptions-kairosify.yaml](09-ocispec-buildoptions-kairosify.yaml) | `image.ociSpec` (template, no FROM) + `image.buildOptions` (baseImage + version, etc.) + push | ISO |

---

## 4. Implementation plan

### Phase 1: Default Dockerfile + build options (build-iso)

- **CRD:** Add `BuildOptions` (BaseImage, KairosInit, Model, TrustedBoot, KubernetesDistro, KubernetesVersion, Version, FIPS; PreKairosInitSteps optional). Add `ImagePush` (Push, ImageName, CredentialsSecretRef). Group image under `spec.image` (ref | buildOptions | ociSpec, push) and artifacts under `spec.artifacts`; move overlay bindings to artifacts.overlayISOVolume / overlayRootfsVolume; add buildContextVolume under image.buildOptions / image.ociSpec.
- **Validation:** When `image.ref` is set: `buildOptions` and `ociSpec` are ignored. When `image.ref` is empty: at least one of `image.buildOptions` or `image.ociSpec` must be set; both may be set. When `buildOptions` is set: version required; custom OCI spec group unset. `buildOptions` + `BaseImageName` allowed; `buildOptions` + `ImageName` invalid. Push only when building.
- **Default OCI build definition:** Embed in operator (match kairos/images Dockerfile ARGs). Single insertion point for PreKairosInitSteps. No network fetch.
- **Reconciler:** If buildOptions set (no custom OCI spec): render default build definition (with PreKairosInitSteps if set), create Secret, mount for kaniko; pass build-args and --customPlatform=linux/<arch> when spec.arch set. If custom OCI spec: keep current behavior (no build-args from buildOptions).
- **Kaniko:** Pass --build-arg from BuildOptions; when spec.arch set pass --customPlatform=linux/<arch> (default and custom Dockerfile modes).
- **Push:** When image.push set and we built: after build, push to registry (e.g. container that loads tarball and pushes with credentials from Secret).
- **Tests:** Validation, reconciler mode selection, kaniko args, PreKairosInitSteps injection; E2E with BuildOptions, BaseImageName+BuildOptions, PreKairosInitSteps.

### Phase 2 (optional): Trusted boot (build-uki)

- CRD fields for UKI keys (Secret or volume). When buildOptions.trustedBoot and keys provided: use `auroraboot build-uki` with --public-keys, --sb-key, etc.

### Design decisions (short)

- **Two modes only:** (1) No custom OCI spec → build options + default build definition. (2) User supplies full OCI build definition. No “partial” definition or operator-injected kairos-init block.
- **User OCI build definition = final image:** Operator does not run kairos-init on top of the user’s build. User’s build definition must produce a Kairos-ready image (e.g. run kairos-init inside it).
- **Volume bindings:** Scoped by stage (build context under image; overlays under artifacts) so each binding lives where it is used.
