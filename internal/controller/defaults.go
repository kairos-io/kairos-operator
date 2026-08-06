package controller

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

// upgradeAlwaysExcludePaths are the paths the operator always passes to
// `kairos-agent upgrade` as `--exclude-path` in the NodeOpUpgrade flow. This
// flow uses `--source dir:/` which rsyncs the pod's rootfs onto the host, and
// Kubernetes injects a pod-specific /etc/hostname and /etc/hosts into every
// pod. Copying those over the real host files breaks the node's identity
// after the post-upgrade reboot.
var upgradeAlwaysExcludePaths = []string{"/etc/hostname", "/etc/hosts"}

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
