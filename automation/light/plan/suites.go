// SPDX-License-Identifier: MIT

package main

// Ginkgo suites.
//
// A package whose test files import Ginkgo is a suite: its tests are specs, and the one Test function it has only
// starts them. To the go command the whole suite is that one test, so the queue leaves a suite out and this file
// runs it with the ginkgo command instead: one binary per suite, as many processes as there are workers, a second
// attempt for a spec that fails, and Ginkgo's own JSON and JUnit reports kept beside the results.
//
// The reports are turned into the events go test -json writes, one test per spec, so that what reads the results of
// the other pieces reads these too. A spec that needed its second attempt passes and is named as flaky.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// ginkgoPath is the import that makes a package a suite, and the module of the ginkgo command go.mod pins.
	ginkgoPath = "github.com/onsi/ginkgo/v2"
	kindSuites = "suites"
	// suiteAttempts is how often a spec may run: once more after it failed, as a test of the queue does.
	suiteAttempts = 2
)

// suiteReport and specReport are what this command reads of Ginkgo's JSON report.
type suiteReport struct {
	SuitePath                  string
	SuiteSucceeded             bool
	RunTime                    time.Duration
	SpecialSuiteFailureReasons []string
	SpecReports                []specReport
}

type specReport struct {
	ContainerHierarchyTexts []string
	LeafNodeText            string
	LeafNodeType            string
	State                   string
	RunTime                 time.Duration
	NumAttempts             int
	Failure                 specFailure
	AdditionalFailures      []struct{ Failure specFailure }
	CapturedGinkgoWriterOutput,
	CapturedStdOutErr string
}

type specFailure struct {
	Message  string
	Location struct {
		FileName   string
		LineNumber int
	}
}

func (f specFailure) String() string {
	if f.Message == "" {
		return ""
	}
	return fmt.Sprintf("%s\n    %s:%d\n", f.Message, f.Location.FileName, f.Location.LineNumber)
}

// name is the spec's full text: its containers' and its own, as Ginkgo prints it. A slash would make it a subtest to
// what reads the events.
func (s specReport) name() string {
	texts := append(slices.Clone(s.ContainerHierarchyTexts), s.LeafNodeText)
	if s.LeafNodeType != "It" {
		texts = []string{"[" + s.LeafNodeType + "]"}
	}
	return strings.ReplaceAll(strings.Join(texts, " "), "/", "-")
}

// suitesOf returns the packages that are suites.
func suitesOf(pkgs []pkg) []pkg {
	var suites []pkg
	for _, p := range pkgs {
		if p.Suite {
			suites = append(suites, p)
		}
	}
	return suites
}

// suiteResult is what the reports of a run came to.
type suiteResult struct {
	specs, skipped int
	failed         []string // the directories of the suites with a failure
	flakes         []flake
}

// suiteEvents turns Ginkgo's reports into go test -json events: per spec a run and a result, with what a failed
// attempt left as output, and per suite its own result. Set-up and tear-down nodes appear only when they failed.
func suiteEvents(reports []suiteReport, byDir map[string]pkg, now time.Time) ([][]byte, suiteResult, error) {
	var lines [][]byte
	var result suiteResult
	add := func(event testEvent) error {
		event.Time = now
		line, err := json.Marshal(event)
		lines = append(lines, append(line, '\n'))
		return err
	}
	for _, report := range reports {
		p, known := byDir[filepath.Clean(report.SuitePath)]
		if !known {
			p = pkg{Path: report.SuitePath, Rel: report.SuitePath}
		}
		for _, spec := range report.SpecReports {
			action := "fail"
			switch spec.State {
			case "passed":
				action = "pass"
			case "skipped", "pending":
				action = "skip"
			}
			if spec.LeafNodeType != "It" && action != "fail" {
				continue
			}
			name := spec.name()
			var output strings.Builder
			for _, failure := range spec.AdditionalFailures {
				output.WriteString(failure.Failure.String())
			}
			switch action {
			case "pass":
				result.specs++
				if spec.NumAttempts > 1 {
					result.flakes = append(result.flakes, flake{Pkg: p, Test: name, Output: output.String()})
				}
			case "skip":
				result.skipped++
			default:
				result.specs++
				fmt.Fprintf(&output, "%s: %s%s%s", spec.State, spec.Failure, spec.CapturedGinkgoWriterOutput, spec.CapturedStdOutErr)
			}
			events := []testEvent{{Action: "run", Package: p.Path, Test: name}}
			if output.Len() > 0 {
				events = append(events, testEvent{Action: "output", Package: p.Path, Test: name, Output: strings.TrimRight(output.String(), "\n") + "\n"})
			}
			events = append(events, testEvent{Action: action, Package: p.Path, Test: name, Elapsed: spec.RunTime.Seconds()})
			for _, event := range events {
				if err := add(event); err != nil {
					return nil, suiteResult{}, err
				}
			}
		}
		action := "pass"
		if !report.SuiteSucceeded {
			action = "fail"
			result.failed = append(result.failed, p.Rel)
			if reasons := strings.Join(report.SpecialSuiteFailureReasons, "; "); reasons != "" {
				if err := add(testEvent{Action: "output", Package: p.Path, Output: reasons + "\n"}); err != nil {
					return nil, suiteResult{}, err
				}
			}
		}
		if err := add(testEvent{Action: action, Package: p.Path, Elapsed: report.RunTime.Seconds()}); err != nil {
			return nil, suiteResult{}, err
		}
	}
	return lines, result, nil
}

// runSuites builds the ginkgo command go.mod pins, runs the suites with it and writes their specs as events. Ginkgo's
// own reports, suites.json and suites.xml, and what it printed, suites.log, stay in reportDir.
func runSuites(ctx context.Context, root string, suites []pkg, procs int, binDir, reportDir string, stdout, stderr, report, flaky io.Writer) error {
	for _, dir := range []*string{&binDir, &reportDir} {
		if *dir != "" {
			continue
		}
		made, err := os.MkdirTemp("", "light-suites-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(made) //nolint:errcheck // a leftover temporary directory is not this command's failure
		*dir = made
	}
	var err error
	if binDir, err = filepath.Abs(binDir); err != nil {
		return err
	}
	if reportDir, err = filepath.Abs(reportDir); err != nil {
		return err
	}
	for _, dir := range []string{binDir, reportDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	ginkgo := filepath.Join(binDir, "ginkgo")
	build := exec.CommandContext(ctx, "go", "build", "-o", ginkgo, ginkgoPath+"/ginkgo") //nolint:gosec // G204: the program is literal go; the arguments are fixed and a directory this command made.
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("go build ginkgo: %w\n%s", err, output)
	}

	jsonReport := filepath.Join(reportDir, "suites.json")
	if err := os.Remove(jsonReport); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	args := []string{
		"--procs", strconv.Itoa(max(procs, 1)), "--flake-attempts", strconv.Itoa(suiteAttempts), "--keep-going", "--no-color",
		"--timeout", unitTimeout, "--json-report", "suites.json", "--junit-report", "suites.xml", "--output-dir", reportDir,
	}
	byDir := map[string]pkg{}
	for _, p := range suites {
		args = append(args, "./"+p.Rel)
		byDir[filepath.Join(root, filepath.FromSlash(p.Rel))] = p
	}
	// What ginkgo prints goes to a file beside its reports, and to standard error only when the run failed: the events
	// carry every spec's result, and a reader of this command's standard error takes each line for a fault.
	log, err := os.Create(filepath.Join(reportDir, "suites.log"))
	if err != nil {
		return err
	}
	defer log.Close() //nolint:errcheck // the log is for the reader of a failed run; the result does not depend on it
	started := time.Now()
	run := exec.CommandContext(ctx, ginkgo, args...) //nolint:gosec // G204: the program is the ginkgo command this function built; the arguments are fixed flags and the directories go list gave.
	run.Dir, run.Stdout, run.Stderr = root, log, log
	runErr := run.Run()
	if runErr != nil {
		if printed, err := os.ReadFile(log.Name()); err == nil {
			_, _ = stderr.Write(printed)
		}
	}

	content, err := os.ReadFile(jsonReport)
	if err != nil {
		return errors.Join(runErr, fmt.Errorf("ginkgo left no report: %w", err))
	}
	var reports []suiteReport
	if err := json.Unmarshal(content, &reports); err != nil {
		return fmt.Errorf("%s: %w", jsonReport, err)
	}
	lines, result, err := suiteEvents(reports, byDir, time.Now())
	if err != nil {
		return err
	}
	for _, line := range lines {
		if _, err := stdout.Write(line); err != nil {
			return err
		}
	}
	var summary, flakes strings.Builder
	fmt.Fprintf(&summary, "suites: %d specs of %d suites in %.0f s, %d processes; %d skipped\n",
		result.specs, len(reports), time.Since(started).Seconds(), max(procs, 1), result.skipped)
	for _, f := range result.flakes {
		name := f.Pkg.Rel + ":" + f.Test
		fmt.Fprintf(&summary, "suites: flaky: %s failed and passed on its second attempt\n", name)
		fmt.Fprintf(&flakes, "## %s\n%s\n", name, f.Output)
	}
	_, err = io.WriteString(report, summary.String())
	if flaky != nil {
		_, flakyErr := io.WriteString(flaky, flakes.String())
		err = errors.Join(err, flakyErr)
	}
	switch {
	case len(result.failed) > 0:
		return fmt.Errorf("specs failed in %s", strings.Join(result.failed, ", "))
	case runErr != nil:
		return fmt.Errorf("ginkgo: %w", runErr)
	}
	return err
}
