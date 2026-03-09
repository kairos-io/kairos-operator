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

import (
	"strings"
	"testing"

	buildv1alpha2 "github.com/kairos-io/kairos-operator/api/v1alpha2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestBuildISOCommandCloudConfigOrder verifies that --cloud-config flag appears BEFORE
// the positional dir:/rootfs argument. urfave/cli ignores flags that appear after
// positional arguments, so the order matters.
// See: https://github.com/kairos-io/kairos-operator/pull/73
func TestBuildISOCommandCloudConfigOrder(t *testing.T) {
	tests := []struct {
		name     string
		artifact *buildv1alpha2.OSArtifact
		wantFlag bool // whether --cloud-config should be present
	}{
		{
			name: "with CloudConfigRef",
			artifact: &buildv1alpha2.OSArtifact{
				ObjectMeta: metav1.ObjectMeta{Name: "test-artifact"},
				Spec: buildv1alpha2.OSArtifactSpec{
					Artifacts: &buildv1alpha2.ArtifactSpec{
						CloudConfigRef: &buildv1alpha2.SecretKeySelector{Name: "cloud-config", Key: "config.yaml"},
					},
				},
			},
			wantFlag: true,
		},
		{
			name: "with GRUBConfig",
			artifact: &buildv1alpha2.OSArtifact{
				ObjectMeta: metav1.ObjectMeta{Name: "test-artifact"},
				Spec: buildv1alpha2.OSArtifactSpec{
					Artifacts: &buildv1alpha2.ArtifactSpec{
						GRUBConfig: "set timeout=10",
					},
				},
			},
			wantFlag: true,
		},
		{
			name: "without CloudConfigRef or GRUBConfig",
			artifact: &buildv1alpha2.OSArtifact{
				ObjectMeta: metav1.ObjectMeta{Name: "test-artifact"},
				Spec: buildv1alpha2.OSArtifactSpec{
					Artifacts: &buildv1alpha2.ArtifactSpec{},
				},
			},
			wantFlag: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := buildISOCommand(tt.artifact, "amd64", "", "")

			cloudConfigIdx := strings.Index(cmd, "--cloud-config")
			dirRootfsIdx := strings.Index(cmd, "dir:/rootfs")

			if dirRootfsIdx == -1 {
				t.Errorf("command should contain dir:/rootfs, got: %s", cmd)
			}

			if tt.wantFlag {
				if cloudConfigIdx == -1 {
					t.Errorf("command should contain --cloud-config, got: %s", cmd)
				}
				if cloudConfigIdx >= dirRootfsIdx {
					t.Errorf("--cloud-config (%d) must appear before dir:/rootfs (%d) in command: %s",
						cloudConfigIdx, dirRootfsIdx, cmd)
				}
			} else {
				if cloudConfigIdx != -1 {
					t.Errorf("command should NOT contain --cloud-config, got: %s", cmd)
				}
			}
		})
	}
}

// TestBuildUKICommandCloudConfigOrder verifies that --cloud-config flag appears BEFORE
// the positional dir:/rootfs argument in UKI build commands.
// See: https://github.com/kairos-io/kairos-operator/pull/73
func TestBuildUKICommandCloudConfigOrder(t *testing.T) {
	tests := []struct {
		name     string
		artifact *buildv1alpha2.OSArtifact
		wantFlag bool // whether --cloud-config should be present
	}{
		{
			name: "with CloudConfigRef",
			artifact: &buildv1alpha2.OSArtifact{
				ObjectMeta: metav1.ObjectMeta{Name: "test-artifact"},
				Spec: buildv1alpha2.OSArtifactSpec{
					Artifacts: &buildv1alpha2.ArtifactSpec{
						CloudConfigRef: &buildv1alpha2.SecretKeySelector{Name: "cloud-config", Key: "config.yaml"},
					},
				},
			},
			wantFlag: true,
		},
		{
			name: "with GRUBConfig",
			artifact: &buildv1alpha2.OSArtifact{
				ObjectMeta: metav1.ObjectMeta{Name: "test-artifact"},
				Spec: buildv1alpha2.OSArtifactSpec{
					Artifacts: &buildv1alpha2.ArtifactSpec{
						GRUBConfig: "set timeout=10",
					},
				},
			},
			wantFlag: true,
		},
		{
			name: "without CloudConfigRef or GRUBConfig",
			artifact: &buildv1alpha2.OSArtifact{
				ObjectMeta: metav1.ObjectMeta{Name: "test-artifact"},
				Spec: buildv1alpha2.OSArtifactSpec{
					Artifacts: &buildv1alpha2.ArtifactSpec{},
				},
			},
			wantFlag: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := buildUKICommand(tt.artifact, "iso")

			cloudConfigIdx := strings.Index(cmd, "--cloud-config")
			dirRootfsIdx := strings.Index(cmd, "dir:/rootfs")

			if dirRootfsIdx == -1 {
				t.Errorf("command should contain dir:/rootfs, got: %s", cmd)
			}

			if tt.wantFlag {
				if cloudConfigIdx == -1 {
					t.Errorf("command should contain --cloud-config, got: %s", cmd)
				}
				if cloudConfigIdx >= dirRootfsIdx {
					t.Errorf("--cloud-config (%d) must appear before dir:/rootfs (%d) in command: %s",
						cloudConfigIdx, dirRootfsIdx, cmd)
				}
			} else {
				if cloudConfigIdx != -1 {
					t.Errorf("command should NOT contain --cloud-config, got: %s", cmd)
				}
			}
		})
	}
}
