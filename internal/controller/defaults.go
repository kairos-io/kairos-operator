/*
Copyright 2025.

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

// Default values for boolean fields
const (
	// NodeOp defaults
	RebootOnSuccessDefault = false
	CordonDefault          = false
	StopOnFailureDefault   = false

	// DrainOptions defaults
	DrainEnabledDefault            = false
	DrainForceDefault              = false
	DrainIgnoreDaemonSetsDefault   = true
	DrainDeleteEmptyDirDataDefault = false

	// Upgrade defaults
	UpgradeActiveDefault   = true
	UpgradeRecoveryDefault = false
	UpgradeForceDefault    = false
)

// Helper functions for handling pointer to bool fields

// getBool returns the value with default fallback for boolean fields
func getBool(ptr *bool, defaultValue bool) bool {
	if ptr == nil {
		return defaultValue
	}
	return *ptr
}

// asBool creates a pointer to a boolean value
func asBool(value bool) *bool {
	return &[]bool{value}[0]
}
