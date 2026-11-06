package rebootwatcher_test

import (
	"fmt"
	"os"
	"strconv"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kairos-io/kairos-operator/internal/rebootwatcher"
)

// nsHelperPIDEnv makes the test binary run RunInNamespacesOf against the
// process it names instead of the suite. Entering a mount namespace moves the
// root of the whole process, so the hostns tests do it in a child process
// rather than in the suite itself.
const nsHelperPIDEnv = "REBOOTWATCHER_NS_HELPER_PID"

func TestMain(m *testing.M) {
	if pid := os.Getenv(nsHelperPIDEnv); pid != "" {
		os.Exit(runNamespaceHelper(pid, os.Args[len(os.Args)-1]))
	}
	os.Exit(m.Run())
}

// runNamespaceHelper prints the namespaces the helper itself is in, then runs
// a shell script in the mount and IPC namespaces of pid.
func runNamespaceHelper(pid, script string) int {
	target, err := strconv.Atoi(pid)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	for _, ns := range []string{"mnt", "ipc"} {
		own, err := os.Readlink("/proc/self/ns/" + ns)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		fmt.Println("helper " + own)
	}
	if err := rebootwatcher.RunInNamespacesOf(target, "sh", "-c", script); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func TestRebootWatcher(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Reboot Watcher Suite")
}
