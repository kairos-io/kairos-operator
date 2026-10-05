package bootid_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestBootID(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Boot ID Suite")
}
