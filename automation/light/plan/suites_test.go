// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// A suite's one Test function only starts its specs, so no kind of the queue has a unit of a suite, and a run with a
// suite has one piece for the suites.
func TestASuiteIsLeftOutOfTheQueueAndGetsAPieceOfItsOwn(t *testing.T) {
	pkgs := []pkg{
		{Path: "example.com/m/a", Rel: "a", Tests: []string{"TestLong", "TestShort"}},
		{Path: "example.com/m/a/e2e", Rel: "a/e2e", Tests: []string{"TestE2E"}, Suite: true},
	}
	measured := timings{tests: map[heavyTest]float64{{"a", "TestLong"}: 12}}
	for _, kind := range kinds {
		for _, u := range queueUnits(pkgs, heavyList{}, measured, programs{}, kind) {
			if u.Pkg.Suite {
				t.Errorf("kind %s has a unit of the suite %s", kind, u.Pkg.Rel)
			}
		}
	}
	var names []string
	for _, p := range runPieces(pkgs, heavyList{}, measured, programs{}, 900) {
		names = append(names, p.Name+" "+p.Kind+":"+p.Part)
	}
	if want := []string{"slow slow:1/1", "suites suites:1/1"}; !slices.Equal(names, want) {
		t.Errorf("pieces = %q, want %q", names, want)
	}
	for _, p := range runPieces(pkgs[:1], heavyList{}, measured, programs{}, 900) {
		if p.Kind == kindSuites {
			t.Error("a run without a suite has a piece for the suites")
		}
	}
}

// Ginkgo's report becomes one test per spec: a spec that needed its second attempt passes and is named as flaky with
// what its first attempt reported, a failed spec carries its failure as output, a set-up node appears only when it
// failed, and a suite has its own result.
func TestASuitesReportBecomesOneTestPerSpec(t *testing.T) {
	const report = `[
	 {"SuitePath": "/repo/a/e2e", "SuiteSucceeded": false, "RunTime": 3000000000, "SpecReports": [
	  {"LeafNodeType": "SynchronizedBeforeSuite", "State": "passed", "RunTime": 1000000000, "NumAttempts": 1},
	  {"ContainerHierarchyTexts": ["print mode"], "LeafNodeText": "prints", "LeafNodeType": "It", "State": "passed", "RunTime": 250000000, "NumAttempts": 1},
	  {"ContainerHierarchyTexts": ["print mode", "on a signal"], "LeafNodeText": "exits 143", "LeafNodeType": "It", "State": "passed", "RunTime": 500000000, "NumAttempts": 2,
	   "AdditionalFailures": [{"State": "failed", "Failure": {"Message": "Failure recorded during attempt 1:\nexit code 1", "Location": {"FileName": "/repo/a/e2e/x_test.go", "LineNumber": 7}}}]},
	  {"ContainerHierarchyTexts": ["print mode"], "LeafNodeText": "reads </dev/null", "LeafNodeType": "It", "State": "failed", "RunTime": 100000000, "NumAttempts": 2,
	   "Failure": {"Message": "Expected 1 to equal 0", "Location": {"FileName": "/repo/a/e2e/x_test.go", "LineNumber": 12}}, "CapturedGinkgoWriterOutput": "what pig wrote\n"},
	  {"ContainerHierarchyTexts": ["print mode"], "LeafNodeText": "waits", "LeafNodeType": "It", "State": "skipped"}
	 ]},
	 {"SuitePath": "/repo/b/e2e", "SuiteSucceeded": false, "RunTime": 1000000000, "SpecialSuiteFailureReasons": ["Suite set-up failed"], "SpecReports": [
	  {"LeafNodeType": "BeforeSuite", "State": "failed", "RunTime": 1000000000, "NumAttempts": 1, "Failure": {"Message": "no pig", "Location": {"FileName": "/repo/b/e2e/s_test.go", "LineNumber": 3}}}
	 ]}
	]`
	var reports []suiteReport
	if err := json.Unmarshal([]byte(report), &reports); err != nil {
		t.Fatal(err)
	}
	byDir := map[string]pkg{"/repo/a/e2e": {Path: "example.com/m/a/e2e", Rel: "a/e2e", Suite: true}, "/repo/b/e2e": {Path: "example.com/m/b/e2e", Rel: "b/e2e", Suite: true}}

	lines, result, err := suiteEvents(reports, byDir, time.Unix(0, 0))

	if err != nil {
		t.Fatal(err)
	}
	var got []string
	output := map[string]string{}
	for _, line := range lines {
		var event testEvent
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("not an event: %q: %v", line, err)
		}
		switch event.Action {
		case "output":
			output[event.Test] += event.Output
		case "pass", "fail", "skip":
			got = append(got, strings.TrimPrefix(event.Package, "example.com/m/")+" | "+event.Test+" | "+event.Action+" | "+time.Duration(event.Elapsed*float64(time.Second)).String())
		}
	}
	want := []string{
		"a/e2e | print mode prints | pass | 250ms",
		"a/e2e | print mode on a signal exits 143 | pass | 500ms",
		"a/e2e | print mode reads <-dev-null | fail | 100ms",
		"a/e2e | print mode waits | skip | 0s",
		"a/e2e |  | fail | 3s",
		"b/e2e | [BeforeSuite] | fail | 1s",
		"b/e2e |  | fail | 1s",
	}
	if !slices.Equal(got, want) {
		t.Errorf("results =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if want := "failed: Expected 1 to equal 0\n    /repo/a/e2e/x_test.go:12\nwhat pig wrote\n"; output["print mode reads <-dev-null"] != want {
		t.Errorf("the failed spec's output = %q, want %q", output["print mode reads <-dev-null"], want)
	}
	if !strings.Contains(output[""], "Suite set-up failed") {
		t.Errorf("the suites' own output = %q, want the reason the second suite failed", output[""])
	}
	if result.specs != 4 || result.skipped != 1 || !slices.Equal(result.failed, []string{"a/e2e", "b/e2e"}) {
		t.Errorf("specs = %d, skipped = %d, failed = %q; want 4 specs that ran, 1 skipped, both suites failed", result.specs, result.skipped, result.failed)
	}
	if len(result.flakes) != 1 || result.flakes[0].Test != "print mode on a signal exits 143" || !strings.Contains(result.flakes[0].Output, "exit code 1\n    /repo/a/e2e/x_test.go:7") {
		t.Errorf("flakes = %+v, want the spec that passed on its second attempt with its first attempt's failure", result.flakes)
	}
}

// The go command names a package's test imports; a package that imports Ginkgo is a suite.
func TestAPackageWhoseTestsImportGinkgoIsASuite(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go command")
	}
	root := t.TempDir()
	for name, content := range map[string]string{
		"go.mod":            "module example.com/m\n\ngo 1.24\n",
		"a/a_test.go":       "package a\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\n",
		"a/e2e/e2e_test.go": "package e2e\n\nimport (\n\t\"testing\"\n\n\t_ \"" + ginkgoPath + "\"\n)\n\nfunc TestE2E(t *testing.T) {}\n",
		"b/b_test.go":       "package b_test\n\nimport (\n\t\"testing\"\n\n\t_ \"" + ginkgoPath + "\"\n)\n\nfunc TestB(t *testing.T) {}\n",
		"b/b.go":            "package b\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := writeFile(path, content); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GOWORK", "off")

	pkgs, err := listPackages(t.Context(), root, "", "./...")

	if err != nil {
		t.Fatal(err)
	}
	var suites []string
	for _, p := range suitesOf(pkgs) {
		suites = append(suites, p.Rel)
	}
	if want := []string{"a/e2e", "b"}; !slices.Equal(suites, want) {
		t.Errorf("suites = %q of %d packages, want %q", suites, len(pkgs), want)
	}
}

// The suites of this module, run with the ginkgo command it pins: every spec is a test in the events, the spec that
// fails its first attempt passes and is named as flaky, and Ginkgo's own reports are kept.
func TestTheSuitesRunWithTheGinkgoCommandAndEverySpecIsATest(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go command")
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	suite := pkg{Path: "github.com/MichaelKinsy/PiG/automation/light/plan/testdata/suite", Rel: "automation/light/plan/testdata/suite", Tests: []string{"TestSuite"}, Suite: true}
	t.Setenv("LIGHT_SUITE_TEST_MARKER", t.TempDir())
	reportDir := t.TempDir()
	var events, report, flaky bytes.Buffer

	err = runSuites(t.Context(), root, []pkg{suite}, 2, t.TempDir(), reportDir, &events, os.Stderr, &report, &flaky)

	if err != nil {
		t.Fatalf("error = %v, want none: the spec that failed passed on its second attempt\n%s", err, &events)
	}
	results := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(events.String()), "\n") {
		var event testEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("not an event: %q: %v", line, err)
		}
		if event.Package != suite.Path {
			t.Errorf("an event of package %q, want %q", event.Package, suite.Path)
		}
		if event.Action == "pass" || event.Action == "fail" || event.Action == "skip" {
			results[event.Test] += event.Action
		}
	}
	want := map[string]string{"a spec passes": "pass", "a spec fails its first attempt": "pass", "a spec is skipped": "skip", "a spec reads a-b": "pass", "": "pass"}
	if len(results) != len(want) {
		t.Errorf("results = %v, want %v", results, want)
	}
	for name, action := range want {
		if results[name] != action {
			t.Errorf("%q = %q, want once %s", name, results[name], action)
		}
	}
	for _, line := range []string{"suites: 3 specs of 1 suites", "2 processes; 1 skipped", "flaky: automation/light/plan/testdata/suite:a spec fails its first attempt failed and passed on its second attempt"} {
		if !strings.Contains(report.String(), line) {
			t.Errorf("report = %q, want %q in it", report.String(), line)
		}
	}
	if !strings.Contains(flaky.String(), "## automation/light/plan/testdata/suite:a spec fails its first attempt\n") || !strings.Contains(flaky.String(), "the first attempt fails") {
		t.Errorf("flaky = %q, want the spec and what its first attempt reported", flaky.String())
	}
	for _, name := range []string{"suites.json", "suites.xml"} {
		if info, err := os.Stat(filepath.Join(reportDir, name)); err != nil || info.Size() == 0 {
			t.Errorf("%s: %v, want Ginkgo's own report kept", name, err)
		}
	}
}
