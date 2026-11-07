package rebootwatcher_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kairos-io/kairos-operator/internal/rebootwatcher"
)

var _ = Describe("RunInNamespacesOf", func() {
	It("runs the command in the target's mount and IPC namespaces", func() {
		if err := exec.Command("unshare", "-Urmi", "true").Run(); err != nil {
			Skip("unprivileged user namespaces are not available: " + err.Error())
		}
		self, err := os.Executable()
		Expect(err).NotTo(HaveOccurred())
		dir := GinkgoT().TempDir()
		marker := filepath.Join(dir, "marker")

		// The target is a shell in its own user, mount and IPC namespaces. It
		// mounts a tmpfs that only its mount namespace sees and records its
		// namespaces in it. The helper starts in further mount and IPC
		// namespaces of its own, unmounts the tmpfs there, and enters the
		// target's namespaces through RunInNamespacesOf, as the reboot Pod
		// enters the node's.
		target := `set -e
mount -t tmpfs none "$DIR"
readlink /proc/$$/ns/mnt /proc/$$/ns/ipc > "$DIR/marker"
REBOOTWATCHER_NS_HELPER_PID=$$ unshare -mi sh -c 'umount "$DIR" && exec "$SELF" "$SCRIPT"'`
		cmd := exec.Command("unshare", "-Urmi", "sh", "-c", target)
		cmd.Env = append(os.Environ(),
			"DIR="+dir,
			"SELF="+self,
			"SCRIPT=test ! -e "+marker+" && echo missing; cat "+marker+"; readlink /proc/self/ns/mnt /proc/self/ns/ipc",
		)
		out, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))

		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		Expect(lines).To(HaveLen(6), string(out))
		helperMnt := strings.TrimPrefix(lines[0], "helper ")
		helperIPC := strings.TrimPrefix(lines[1], "helper ")
		targetMnt, targetIPC := lines[2], lines[3]
		Expect(targetMnt).To(HavePrefix("mnt:"))
		Expect(targetIPC).To(HavePrefix("ipc:"))

		// The helper really started elsewhere, and the command saw the
		// target's tmpfs and ran in exactly the target's namespaces.
		Expect(helperMnt).NotTo(Equal(targetMnt))
		Expect(helperIPC).NotTo(Equal(targetIPC))
		Expect(lines[4:]).To(Equal([]string{targetMnt, targetIPC}))
		Expect(string(out)).NotTo(ContainSubstring("missing"))
	})

	It("fails without running anything when the target process does not exist", func() {
		gone := exec.Command("true")
		Expect(gone.Run()).To(Succeed())

		err := rebootwatcher.RunInNamespacesOf(gone.Process.Pid, "sh", "-c", "exit 0")
		Expect(err).To(MatchError(ContainSubstring("open pidfd of process")))
	})
})
