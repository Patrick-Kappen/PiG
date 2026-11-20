// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The targets this command builds into are found by name. Every name here must be one a test package gives
// testenv.SharedCargoTargetDir, or the build lands in a directory no test reads; and every such name must be here, or
// that package's tests start from an empty target.
func TestTheTargetNamesAreTheOnesTheTestPackagesUse(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	call := regexp.MustCompile(`testenv\.SharedCargoTargetDir\("([^"]+)"`)
	var used []string
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".upstream", "node_modules", "tmp", "testenv", "extension-conformance":
				// testenv defines the function and tests it with made-up names; extension conformance is not in the light run.
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, match := range call.FindAllSubmatch(content, -1) {
			used = append(used, string(match[1]))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(used)
	used = slices.Compact(used)
	want := slices.Sorted(slices.Values(append(slices.Clone(cellPackages), daemonPackage)))
	if !slices.Equal(used, want) {
		t.Errorf("the test packages name the Cargo targets %v, this command builds %v", used, want)
	}
}
