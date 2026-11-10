/*
Copyright 2026.

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
	"fmt"
	"strconv"
	"strings"

	buildv1alpha2 "github.com/kairos-io/kairos-operator/api/v1alpha2"
)

// Experimental raw disk options, passed to auroraboot as the matching disk.* keys (see kairos-io/kairos#5544).
const (
	// diskAnnotationNamespace is reserved for raw disk options, unknown keys under it are rejected.
	diskAnnotationNamespace = buildAnnotationPrefix + "disk."

	diskKeyBootActive = "disk.boot_active"
	diskKeyStateSlots = "disk.state_slots"
	diskKeyNoRecovery = "disk.no_recovery"

	annotationDiskBootActive = buildAnnotationPrefix + diskKeyBootActive
	annotationDiskStateSlots = buildAnnotationPrefix + diskKeyStateSlots
	annotationDiskNoRecovery = buildAnnotationPrefix + diskKeyNoRecovery

	minStateSlots = 1
	maxStateSlots = 3
)

// diskOptions holds the parsed raw disk annotations; a zero StateSlots means the auroraboot default.
type diskOptions struct {
	BootActive bool
	StateSlots int
	NoRecovery bool
}

// diskOptionsFor parses and validates the raw disk annotations of an OSArtifact.
func diskOptionsFor(artifact *buildv1alpha2.OSArtifact) (diskOptions, error) {
	var opts diskOptions
	for key, value := range artifact.GetAnnotations() {
		if !strings.HasPrefix(key, diskAnnotationNamespace) {
			continue
		}
		if err := opts.set(key, value); err != nil {
			return diskOptions{}, err
		}
	}
	if err := opts.validate(artifact.Spec.Artifacts); err != nil {
		return diskOptions{}, err
	}
	return opts, nil
}

func (o *diskOptions) set(key, value string) error {
	var err error
	switch key {
	case annotationDiskBootActive:
		o.BootActive, err = strconv.ParseBool(value)
	case annotationDiskNoRecovery:
		o.NoRecovery, err = strconv.ParseBool(value)
	case annotationDiskStateSlots:
		o.StateSlots, err = strconv.Atoi(value)
		if err == nil && (o.StateSlots < minStateSlots || o.StateSlots > maxStateSlots) {
			err = fmt.Errorf("must be between %d and %d", minStateSlots, maxStateSlots)
		}
	default:
		return fmt.Errorf("unknown annotation %q, supported: %s, %s, %s",
			key, annotationDiskBootActive, annotationDiskStateSlots, annotationDiskNoRecovery)
	}
	if err != nil {
		return fmt.Errorf("invalid annotation %s=%q: %w", key, value, err)
	}
	return nil
}

func (o diskOptions) validate(artifacts *buildv1alpha2.ArtifactSpec) error {
	if !o.BootActive {
		if o.StateSlots != 0 {
			return fmt.Errorf("annotation %s requires %s=true", annotationDiskStateSlots, annotationDiskBootActive)
		}
		if o.NoRecovery {
			return fmt.Errorf("annotation %s requires %s=true", annotationDiskNoRecovery, annotationDiskBootActive)
		}
		return nil
	}
	if o.NoRecovery && o.StateSlots != minStateSlots {
		return fmt.Errorf("annotation %s requires %s=%d", annotationDiskNoRecovery, annotationDiskStateSlots, minStateSlots)
	}
	if artifacts == nil || (!artifacts.CloudImage && !artifacts.AzureImage && !artifacts.GCEImage) {
		return fmt.Errorf("annotation %s requires at least one of spec.artifacts.cloudImage, azureImage or gceImage", annotationDiskBootActive)
	}
	return nil
}

// args renders the options as auroraboot --set flags from parsed values only, never raw annotation strings.
func (o diskOptions) args() string {
	var c strings.Builder
	if o.BootActive {
		fmt.Fprintf(&c, " --set '%s=true'", diskKeyBootActive)
	}
	if o.StateSlots != 0 {
		fmt.Fprintf(&c, " --set '%s=%d'", diskKeyStateSlots, o.StateSlots)
	}
	if o.NoRecovery {
		fmt.Fprintf(&c, " --set '%s=true'", diskKeyNoRecovery)
	}
	return c.String()
}

// writeDiskArgs appends the raw disk flags; invalid annotations are rejected in startBuild before the pod exists.
func writeDiskArgs(c *strings.Builder, artifact *buildv1alpha2.OSArtifact) {
	opts, err := diskOptionsFor(artifact)
	if err != nil {
		return
	}
	c.WriteString(opts.args())
}
