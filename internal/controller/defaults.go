package controller

import "time"

// Default values for boolean fields
const (
	// NodeOp defaults
	RebootOnSuccessDefault   = false
	CordonDefault            = false
	UncordonOnFailureDefault = false
	StopOnFailureDefault     = false

	// DrainOptions defaults
	DrainEnabledDefault            = false
	DrainForceDefault              = false
	DrainIgnoreDaemonSetsDefault   = true
	DrainDeleteEmptyDirDataDefault = false

	// Upgrade defaults
	UpgradeActiveDefault   = true
	UpgradeRecoveryDefault = false
	UpgradeForceDefault    = false
	UpgradeDebugDefault    = false
)

const (
	defaultHostMountPath = "/host"
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

// Drain wait tuning. DrainTimeoutDefault is how long a drain waits for the
// pods it evicted when drainOptions.timeoutSeconds is unset; it matches the
// value the shipped NodeOp sample sets. drainPollInterval is how often the
// wait re-reads those pods.
const (
	DrainTimeoutDefault = 5 * time.Minute
	drainPollInterval   = 2 * time.Second
)
