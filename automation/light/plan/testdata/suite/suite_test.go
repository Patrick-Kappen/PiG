// SPDX-License-Identifier: MIT

// Package suite is the Ginkgo suite the tests of automation/light/plan run: a spec that passes, one that fails its
// first attempt when LIGHT_SUITE_TEST_MARKER names a directory, one that is skipped, and one with a slash in its text.
package suite

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSuite(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "the plan's own suite")
}

var _ = Describe("a spec", Label("demo"), func() {
	It("passes", func() {
		Expect(1 + 1).To(Equal(2))
	})

	It("fails its first attempt", func() {
		dir := os.Getenv("LIGHT_SUITE_TEST_MARKER")
		if dir == "" {
			return
		}
		marker := filepath.Join(dir, "ran")
		if _, err := os.Stat(marker); err != nil {
			Expect(os.WriteFile(marker, nil, 0o600)).To(Succeed())
			Fail("the first attempt fails")
		}
	})

	It("is skipped", func() {
		Skip("not here")
	})

	It("reads a/b", func() {
		Expect("a/b").To(ContainSubstring("/"))
	})
})
