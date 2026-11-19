// SPDX-License-Identifier: MIT
package ciimages

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// CodeQL analyzes the two interpreted SDKs in one matrix job, which every run has, and Go in its own job, which builds every
// package and runs in the full run only (TestLightSecurityRunLeavesOnlySourceAssuranceAndGoCodeQLToTheFullRun).
func TestCodeQLCoversGoAndInterpretedSDKs(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), ".github/workflows/security.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Name     string
			Strategy struct {
				Matrix struct {
					Include []struct {
						Language string
						Config   string
					}
				}
			}
			Steps []imageWorkflowStep
		}
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	wantPaths := map[string][]string{
		"javascript-typescript": {"extensions/sdk-ts", "coding/extension/host/subprocess/runtime-node"},
		"python":                {"extensions/sdk-py"},
	}
	job := workflow.Jobs["codeql"]
	seen := make(map[string]bool)
	for _, target := range job.Strategy.Matrix.Include {
		paths, ok := wantPaths[target.Language]
		if !ok || seen[target.Language] {
			t.Fatalf("unexpected or duplicate CodeQL language %q", target.Language)
		}
		seen[target.Language] = true
		var config struct{ Paths []string }
		if err := yaml.Unmarshal([]byte(target.Config), &config); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(config.Paths, paths) {
			t.Errorf("%s paths = %v, want %v", target.Language, config.Paths, paths)
		}
		for _, path := range config.Paths {
			if _, err := os.Stat(filepath.Join(repoRoot(t), path)); err != nil {
				t.Fatal(err)
			}
		}
	}
	for language := range wantPaths {
		if !seen[language] {
			t.Errorf("CodeQL matrix omits %s", language)
		}
	}
	var initialized, analyzed bool
	for _, step := range job.Steps {
		switch {
		case strings.HasPrefix(step.Uses, "github/codeql-action/init@"):
			initialized = true
			for key, want := range map[string]string{
				"languages":  "${{ matrix.language }}",
				"build-mode": "none",
				"config":     "${{ matrix.config }}",
			} {
				if step.With[key] != want {
					t.Errorf("CodeQL init %s = %q, want %q", key, step.With[key], want)
				}
			}
		case strings.HasPrefix(step.Uses, "actions/setup-go@"), step.Run == "go build ./...":
			t.Error("interpreted-language analysis must not require Go setup or a Go build")
		case strings.HasPrefix(step.Uses, "github/codeql-action/analyze@"):
			analyzed = true
			if !initialized || step.With["category"] != "/language:${{ matrix.language }}" {
				t.Error("analysis must follow init and use a distinct language category")
			}
		}
	}
	if !initialized || !analyzed {
		t.Fatal("CodeQL must initialize and analyze the interpreted SDKs")
	}

	goJob, ok := workflow.Jobs["codeql-go"]
	if !ok {
		t.Fatal("security.yml has no codeql-go job")
	}
	// The check keeps the name it had as a row of the matrix.
	if goJob.Name != "CodeQL (go)" {
		t.Errorf("codeql-go is named %q, want CodeQL (go)", goJob.Name)
	}
	var setup, built bool
	initialized, analyzed = false, false
	for _, step := range goJob.Steps {
		switch {
		case strings.HasPrefix(step.Uses, "actions/setup-go@"):
			setup = true
		case strings.HasPrefix(step.Uses, "github/codeql-action/init@"):
			initialized = true
			if !setup || step.With["languages"] != "go" || step.With["build-mode"] != "manual" || step.With["config"] != "" {
				t.Errorf("CodeQL init for Go has with: %v, want languages go, build-mode manual and no path filter, after Go setup", step.With)
			}
		case step.Run == "go build ./...":
			built = true
			if !initialized || step.If != "" {
				t.Error("the Go build must run after init, unconditionally")
			}
		case strings.HasPrefix(step.Uses, "github/codeql-action/analyze@"):
			analyzed = true
			if !built || step.With["category"] != "/language:go" {
				t.Error("analysis must follow the build and use the Go category")
			}
		}
	}
	if !initialized || !built || !analyzed {
		t.Fatal("CodeQL must initialize, build Go, and analyze")
	}
}
