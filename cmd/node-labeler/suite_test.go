package main

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestNodeLabeler(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Node Labeler Suite")
}
