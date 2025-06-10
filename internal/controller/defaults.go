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
