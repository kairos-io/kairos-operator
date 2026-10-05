// Package bootid reads and checks boot IDs. Linux picks a new random boot ID
// every time it starts, so a node that reports a different boot ID has
// rebooted. When an upgrade finishes, the upgrade Job reports the node's boot
// ID in its termination message. The operator and the reboot Pod compare that
// boot ID with the node's current one to tell whether the node rebooted.
package bootid

import (
	"os"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// ReporterContainerName is the name of the upgrade Job's last container. It
// writes the node's boot ID to its termination message.
const ReporterContainerName = "boot-id-reporter"

const hostBootIDPath = "/proc/sys/kernel/random/boot_id"

// pattern matches the canonical 8-4-4-4-12 hex form the kernel uses for
// /proc/sys/kernel/random/boot_id. A strict pattern is used instead of
// uuid.Parse, which also accepts braces, URN prefixes and unhyphenated forms
// that the kernel never produces.
var pattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Valid reports whether s is a boot ID in the canonical 8-4-4-4-12 hex form.
func Valid(s string) bool {
	return pattern.MatchString(s)
}

// FromJobPods returns the boot ID the upgrade Job reported, given the upgrade
// Job's Pods. It takes the termination message of the ReporterContainerName
// container in the first succeeded Pod whose message is a valid boot ID, and
// returns "" when no Pod reports one. Pods of failed attempts are ignored.
func FromJobPods(pods []corev1.Pod) string {
	for _, pod := range pods {
		if pod.Status.Phase != corev1.PodSucceeded {
			continue
		}
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.Name != ReporterContainerName || cs.State.Terminated == nil {
				continue
			}
			if id := strings.TrimSpace(cs.State.Terminated.Message); Valid(id) {
				return id
			}
		}
	}
	return ""
}

// ReadHost returns the boot ID of the node the calling container runs on.
// Every container on a node sees the node's own
// /proc/sys/kernel/random/boot_id, so the container can read it directly. It
// returns "" if the file cannot be read or does not hold a valid boot ID.
func ReadHost() string {
	b, err := os.ReadFile(hostBootIDPath)
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(string(b))
	if !Valid(id) {
		return ""
	}
	return id
}
