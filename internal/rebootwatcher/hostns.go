package rebootwatcher

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"golang.org/x/sys/unix"
)

// HostReboot reboots the node by running the node's own `reboot` in the mount
// and IPC namespaces of the node's init process, which is what
// `nsenter -i -m -t 1 -- reboot` does. The reboot stays the host's: on a
// systemd node it is still `systemctl reboot`, which stops units, unmounts and
// syncs, rather than a reboot(2) call that resets the node with unsynced
// writes. It is the Reboot function used in production; tests use a stub.
func HostReboot() error {
	return RunInNamespacesOf(1, "reboot")
}

// RunInNamespacesOf runs name with args in the mount and IPC namespaces of the
// process pid, like `nsenter -i -m -t <pid> -- name args...`, and waits for it.
// name is looked up in pid's mount namespace, not in the caller's.
//
// Entering the mount namespace moves the root and working directory of the
// whole calling process to those of pid, so call it only from a process that
// has nothing left to do but wait for the command. It needs Linux 5.8 or
// newer, and CAP_SYS_ADMIN and CAP_SYS_CHROOT over pid's namespaces.
func RunInNamespacesOf(pid int, name string, args ...string) error {
	errc := make(chan error, 1)
	go func() {
		// A namespace belongs to a thread, not to the process, so the setns
		// and the fork of the command must happen on the same thread. The
		// thread is never unlocked: the runtime ends it with this goroutine
		// instead of handing a thread in pid's namespaces to other code.
		runtime.LockOSThread()
		errc <- runInNamespacesOf(pid, name, args...)
	}()
	return <-errc
}

func runInNamespacesOf(pid int, name string, args ...string) error {
	// setns(2) on a /proc/<pid>/ns/mnt file refuses a caller that shares its
	// filesystem information with other threads, and every thread of a Go
	// process does. A pidfd with more than one namespace flag gives the call
	// a private copy instead, so it is the only form that works from Go.
	pidfd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return fmt.Errorf("open pidfd of process %d: %w", pid, err)
	}
	defer unix.Close(pidfd) //nolint:errcheck

	if err := unix.Setns(pidfd, unix.CLONE_NEWNS|unix.CLONE_NEWIPC); err != nil {
		return fmt.Errorf("enter the mount and IPC namespaces of process %d: %w", pid, err)
	}

	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
