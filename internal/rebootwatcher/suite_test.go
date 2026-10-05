package rebootwatcher_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestRebootWatcher(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Reboot Watcher Suite")
}
