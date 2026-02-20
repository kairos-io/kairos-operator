/*
Copyright 2022.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

// Final Dockerfile is built by concatenating sections when buildOptions is set:
//   baseImageSection + userOciSpecContent (optional) + kairosInitSection
// When only ociSpec (no buildOptions), the final Dockerfile is just the user's ociSpec.
// See https://github.com/kairos-io/kairos/blob/master/images/Dockerfile

// DefaultDockerfileBaseImageSection is injected at the top when buildOptions is set.
// BASE_IMAGE has no default; validation requires buildOptions.baseImage when buildOptions is set.
const DefaultDockerfileBaseImageSection = `# base image section
ARG BASE_IMAGE
FROM ${BASE_IMAGE} AS base-kairos
ARG MODEL=generic
ARG TRUSTED_BOOT=false
ARG KUBERNETES_DISTRO
ARG KUBERNETES_VERSION
ARG VERSION
ARG FIPS=no-fips
`

// DefaultDockerfileKairosInitSection is injected at the end when buildOptions is set.
const DefaultDockerfileKairosInitSection = `# kairos init section
ARG KAIROS_INIT=v0.7.0
FROM quay.io/kairos/kairos-init:${KAIROS_INIT} AS kairos-init
RUN --mount=type=bind,from=kairos-init,src=/kairos-init,dst=/kairos-init \
 if [ -n "${KUBERNETES_DISTRO}" ]; then \
 K8S_FLAG="-p ${KUBERNETES_DISTRO}"; \
 if [ "${KUBERNETES_DISTRO}" = "k0s" ] && [ -n "${KUBERNETES_VERSION}" ]; then \
 K8S_VERSION_FLAG="--provider-k0s-version \"${KUBERNETES_VERSION}\""; \
 elif [ "${KUBERNETES_DISTRO}" = "k3s" ] && [ -n "${KUBERNETES_VERSION}" ]; then \
 K8S_VERSION_FLAG="--provider-k3s-version \"${KUBERNETES_VERSION}\""; \
 else \
 K8S_VERSION_FLAG=""; \
 fi; \
 else \
 K8S_FLAG=""; \
 K8S_VERSION_FLAG=""; \
 fi; \
 if [ "$FIPS" == "fips" ]; then FIPS_FLAG="--fips"; else FIPS_FLAG=""; fi; \
 eval /kairos-init -l debug -s install -m \"${MODEL}\" -t \"${TRUSTED_BOOT}\" ${K8S_FLAG} ${K8S_VERSION_FLAG} --version \"${VERSION}\" \"${FIPS_FLAG}\" && \
 eval /kairos-init -l debug -s init -m \"${MODEL}\" -t \"${TRUSTED_BOOT}\" ${K8S_FLAG} ${K8S_VERSION_FLAG} --version \"${VERSION}\" \"${FIPS_FLAG}\"
`

// AssembleFinalDockerfile returns the final OCI build definition.
// When buildOptions is set: baseImageSection + middle + kairosInitSection.
// When only ociSpec: middle is the sole content (no wrapping).
func AssembleFinalDockerfile(buildOptionsSet bool, middleContent string) string {
	if !buildOptionsSet {
		return middleContent
	}
	if middleContent != "" {
		return DefaultDockerfileBaseImageSection + "\n" + middleContent + "\n" + DefaultDockerfileKairosInitSection
	}
	return DefaultDockerfileBaseImageSection + "\n" + DefaultDockerfileKairosInitSection
}
