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
	buildv1alpha2 "github.com/kairos-io/kairos-operator/api/v1alpha2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (r *OSArtifactReconciler) genConfigMap(artifact *buildv1alpha2.OSArtifact) *corev1.ConfigMap {
	grubCfg, osRelease := "", ""
	if artifact.Spec.Artifacts != nil {
		grubCfg = artifact.Spec.Artifacts.GRUBConfig
		osRelease = artifact.Spec.Artifacts.OSRelease
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      artifact.Name,
			Namespace: artifact.Namespace,
		},
		Data: map[string]string{
			"grub.cfg":   grubCfg,
			"os-release": osRelease,
		}}
}
