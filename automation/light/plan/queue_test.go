// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The first stage runs every test but the ones the heavy list leaves out and the ones that start a program, and every
// one of them once: a test is in exactly one unit.
func TestEveryTestThatStartsNoProgramIsInExactlyOneUnit(t *testing.T) {
	pkgs := []pkg{
		{Path: "example.com/m/a", Rel: "a", Tests: []string{"TestLong", "TestShort", "TestStartsPig", "TestHeavy", "ExampleA", "FuzzA"}},
		{Path: "example.com/m/b", Rel: "b", Tests: []string{"TestLong", "TestOther"}},
		{Path: "example.com/m/c", Rel: "c", Tests: []string{"TestStartsNode"}},
	}
	heavy := heavyList{tests: []heavyTest{{"a", "TestHeavy"}}}
	measured := timings{tests: map[heavyTest]float64{{"a", "TestLong"}: 12, {"a", "TestShort"}: 0.4, {"b", "TestLong"}: 3}}
	starts := programs{{"a", "TestStartsPig"}: "pig", {"c", "TestStartsNode"}: "node"}

	units := queueUnits(pkgs, heavy, measured, starts)

	ran := map[heavyTest]int{}
	for _, u := range units {
		pattern := regexp.MustCompile(u.Run)
		matched := 0
		for _, name := range u.Pkg.Tests {
			if pattern.MatchString(name) {
				ran[heavyTest{u.Pkg.Rel, name}]++
				matched++
			}
		}
		if matched != u.Tests {
			t.Errorf("%s %s matches %d tests, the unit says %d", u.Pkg.Rel, u.Run, matched, u.Tests)
		}
	}
	want := map[heavyTest]int{
		{"a", "TestLong"}: 1, {"a", "TestShort"}: 1, {"a", "ExampleA"}: 1, {"a", "FuzzA"}: 1,
		{"b", "TestLong"}: 1, {"b", "TestOther"}: 1,
	}
	if len(ran) != len(want) {
		t.Errorf("the units run %v, want %v", ran, want)
	}
	for test, n := range want {
		if ran[test] != n {
			t.Errorf("%s:%s is in %d units, want %d", test.Dir, test.Name, ran[test], n)
		}
	}
}

// A test with a second or more is a unit of its own, and the units come longest first: that is the order the workers
// take them in.
func TestALongTestIsAUnitOfItsOwnAndTheLongestUnitIsFirst(t *testing.T) {
	pkgs := []pkg{
		{Path: "example.com/m/a", Rel: "a", Tests: []string{"TestQuick", "TestThreeSeconds", "TestJustUnderASecond"}},
		{Path: "example.com/m/b", Rel: "b", Tests: []string{"TestTwentySeconds"}},
	}
	measured := timings{tests: map[heavyTest]float64{
		{"a", "TestThreeSeconds"}: 3, {"a", "TestJustUnderASecond"}: 0.9, {"b", "TestTwentySeconds"}: 20,
	}}

	units := queueUnits(pkgs, heavyList{}, measured, programs{})

	var got []string
	for _, u := range units {
		got = append(got, u.Pkg.Rel+" "+u.Run)
	}
	want := []string{"b ^TestTwentySeconds$", "a ^TestThreeSeconds$", "a ^(TestJustUnderASecond|TestQuick)$"}
	if !slices.Equal(got, want) {
		t.Errorf("units = %q, want %q", got, want)
	}
	if !slices.IsSortedFunc(units, func(a, b unit) int { return cmp.Compare(b.Seconds, a.Seconds) }) {
		t.Errorf("the units are not longest first: %v", units)
	}
}

// One pattern is one command-line argument, and the kernel bounds its size: a package with more test names than fit
// gets more than one unit for its short tests.
func TestShortTestsThatDoNotFitOnePatternAreSplit(t *testing.T) {
	p := pkg{Path: "example.com/m/a", Rel: "a"}
	name := "Test" + strings.Repeat("x", 996)
	for i := range 250 {
		p.Tests = append(p.Tests, name+string(rune('A'+i%26))+string(rune('a'+i/26)))
	}

	units := queueUnits([]pkg{p}, heavyList{}, timings{}, programs{})

	if len(units) < 2 {
		t.Fatalf("%d tests of 1,000 bytes are in %d unit, want more than one", len(p.Tests), len(units))
	}
	tests := 0
	for _, u := range units {
		if len(u.Run) > maxPatternBytes {
			t.Errorf("a pattern is %d bytes, over the %d the plan allows", len(u.Run), maxPatternBytes)
		}
		tests += u.Tests
	}
	if tests != len(p.Tests) {
		t.Errorf("the units hold %d tests, want %d", tests, len(p.Tests))
	}
}

func TestParsePrograms(t *testing.T) {
	list, err := parsePrograms(strings.NewReader("# a comment\n\npig coding/cli:TestRuns\nnode ai:TestMatchesPi\n"), "programs.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[heavyTest{"coding/cli", "TestRuns"}] != "pig" || list[heavyTest{"ai", "TestMatchesPi"}] != "node" {
		t.Errorf("parsed %v", list)
	}
	for _, bad := range []string{"coding/cli:TestRuns\n", "pig coding/cli\n", "pig coding/cli:TestRuns extra\n", "pig :TestRuns\n"} {
		if _, err := parsePrograms(strings.NewReader(bad), "programs.txt"); err == nil || !strings.Contains(err.Error(), "programs.txt:1") {
			t.Errorf("%q: error %v, want one that names the line", bad, err)
		}
	}
}

// The go command names a test binary after the last element of the import path, or the one before a major version,
// and one command writes a group's binaries into one directory: two packages of one name are in two groups.
func TestPackagesWhoseBinariesHaveOneNameAreBuiltInDifferentGroups(t *testing.T) {
	for importPath, want := range map[string]string{
		"example.com/m/coding/source": "source", "example.com/m/endpoints/v2": "endpoints",
		"example.com/m/v1": "v1", "example.com/m": "m", "example.com/m/x/v10": "x",
	} {
		if got := binaryBase(importPath); got != want {
			t.Errorf("binaryBase(%q) = %q, want %q", importPath, got, want)
		}
	}
	pkgs := []pkg{
		{Path: "example.com/m/coding/source"}, {Path: "example.com/m/coding/extension/source"},
		{Path: "example.com/m/endpoints"}, {Path: "example.com/m/endpoints/v2"}, {Path: "example.com/m/ai"},
	}
	groups := buildGroups(pkgs)
	if len(groups) != 2 {
		t.Fatalf("%d groups, want 2: %v", len(groups), groups)
	}
	placed := 0
	for _, group := range groups {
		names := map[string]bool{}
		for _, p := range group {
			if names[binaryBase(p.Path)] {
				t.Errorf("a group builds two binaries named %s", binaryBase(p.Path))
			}
			names[binaryBase(p.Path)] = true
			placed++
		}
	}
	if placed != len(pkgs) {
		t.Errorf("%d packages in the groups, want %d", placed, len(pkgs))
	}
}

// The queue against a real module: two packages, one test that fails. Every test's events are written, each package's
// own result once although the package ran as several units, and the failed package is named.
func TestTheQueueRunsUnitsOfRealPackagesAndWritesEachPackageResultOnce(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go command")
	}
	root := t.TempDir()
	files := map[string]string{
		"go.mod":         "module example.com/m\n\ngo 1.24\n",
		"a/a_test.go":    "package a\n\nimport \"testing\"\n\nfunc TestOne(t *testing.T) {}\nfunc TestTwo(t *testing.T) {}\nfunc TestLong(t *testing.T) {}\n",
		"b/v2/b_test.go": "package b\n\nimport \"testing\"\n\nfunc TestFails(t *testing.T) { t.Fatal(\"no\") }\nfunc TestPasses(t *testing.T) {}\n",
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := writeFile(path, content); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	pkgs := []pkg{
		{Path: "example.com/m/a", Rel: "a", Tests: []string{"TestOne", "TestTwo", "TestLong"}},
		{Path: "example.com/m/b/v2", Rel: "b/v2", Tests: []string{"TestFails", "TestPasses"}},
	}
	measured := timings{tests: map[heavyTest]float64{{"a", "TestLong"}: 5}}
	var events, report bytes.Buffer

	err := queue(t.Context(), root, pkgs, heavyList{}, measured, programs{}, 2, t.TempDir(), &events, os.Stderr, &report)

	if err == nil || !strings.Contains(err.Error(), "example.com/m/b/v2") || strings.Contains(err.Error(), "example.com/m/a,") {
		t.Errorf("error = %v, want one that names the package whose test failed and no other", err)
	}
	results := map[string][]string{}
	tests := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(events.String()), "\n") {
		var event testEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("not an event: %q: %v", line, err)
		}
		switch {
		case event.Test == "" && (event.Action == "pass" || event.Action == "fail"):
			results[event.Package] = append(results[event.Package], event.Action)
		case event.Test != "" && (event.Action == "pass" || event.Action == "fail"):
			tests[event.Package+":"+event.Test] = event.Action
		}
	}
	if !slices.Equal(results["example.com/m/a"], []string{"pass"}) || !slices.Equal(results["example.com/m/b/v2"], []string{"fail"}) {
		t.Errorf("package results = %v, want a once as pass and b/v2 once as fail", results)
	}
	want := map[string]string{
		"example.com/m/a:TestOne": "pass", "example.com/m/a:TestTwo": "pass", "example.com/m/a:TestLong": "pass",
		"example.com/m/b/v2:TestFails": "fail", "example.com/m/b/v2:TestPasses": "pass",
	}
	if len(tests) != len(want) {
		t.Errorf("test results = %v, want %v", tests, want)
	}
	for name, action := range want {
		if tests[name] != action {
			t.Errorf("%s = %q, want %q", name, tests[name], action)
		}
	}
	if !strings.Contains(report.String(), "5 tests in 3 units (1 tests alone), 2 workers") {
		t.Errorf("report = %q, want the number of tests, units and workers", report.String())
	}
}
