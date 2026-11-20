// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Every test the heavy list does not leave out has one kind and is in exactly one unit of that kind: the pieces
// together run every test once.
func TestEveryTestIsInExactlyOneUnitOfItsKind(t *testing.T) {
	pkgs := []pkg{
		{Path: "example.com/m/a", Rel: "a", Tests: []string{"TestLong", "TestShort", "TestStartsPig", "TestHeavy", "ExampleA", "FuzzA", "TestMoved"}},
		{Path: "example.com/m/b", Rel: "b", Tests: []string{"TestLong", "TestOther"}},
		{Path: "example.com/m/c", Rel: "c", Tests: []string{"TestStartsNode", "TestBuildsACell"}},
	}
	heavy := heavyList{tests: []heavyTest{{"a", "TestHeavy"}}}
	measured := timings{tests: map[heavyTest]float64{{"a", "TestLong"}: 12, {"a", "TestShort"}: 0.4, {"b", "TestLong"}: 3, {"a", "TestMoved"}: 0.1}}
	starts := programs{{"a", "TestStartsPig"}: "pig", {"c", "TestStartsNode"}: "node", {"c", "TestBuildsACell"}: "rust", {"a", "TestMoved"}: "slow"}

	ran := map[heavyTest][]string{}
	for _, kind := range kinds {
		for _, u := range queueUnits(pkgs, heavy, measured, starts, kind) {
			pattern := regexp.MustCompile(u.Run)
			matched := 0
			for _, name := range u.Pkg.Tests {
				if pattern.MatchString(name) {
					ran[heavyTest{u.Pkg.Rel, name}] = append(ran[heavyTest{u.Pkg.Rel, name}], kind)
					matched++
				}
			}
			if matched != u.Tests {
				t.Errorf("%s %s matches %d tests, the unit says %d", u.Pkg.Rel, u.Run, matched, u.Tests)
			}
		}
	}
	want := map[heavyTest]string{
		{"a", "TestLong"}: "slow", {"a", "TestShort"}: "fast", {"a", "ExampleA"}: "fast", {"a", "FuzzA"}: "fast",
		{"a", "TestStartsPig"}: "pig", {"a", "TestMoved"}: "slow",
		{"b", "TestLong"}: "slow", {"b", "TestOther"}: "fast",
		{"c", "TestStartsNode"}: "node", {"c", "TestBuildsACell"}: "rust",
	}
	if len(ran) != len(want) {
		t.Errorf("the units run %v, want %v", ran, want)
	}
	for test, kind := range want {
		if !slices.Equal(ran[test], []string{kind}) {
			t.Errorf("%s:%s is in the units of %v, want once in %s", test.Dir, test.Name, ran[test], kind)
		}
	}
}

// A slow test is a unit of its own and the units come longest first: that is the order the workers take them in. A
// package's fast tests are one unit.
func TestASlowTestIsAUnitOfItsOwnAndTheLongestUnitIsFirst(t *testing.T) {
	pkgs := []pkg{
		{Path: "example.com/m/a", Rel: "a", Tests: []string{"TestQuick", "TestThreeSeconds", "TestJustUnderASecond"}},
		{Path: "example.com/m/b", Rel: "b", Tests: []string{"TestTwentySeconds"}},
	}
	measured := timings{tests: map[heavyTest]float64{
		{"a", "TestThreeSeconds"}: 3, {"a", "TestJustUnderASecond"}: 0.9, {"b", "TestTwentySeconds"}: 20,
	}}
	name := func(units []unit) []string {
		var names []string
		for _, u := range units {
			names = append(names, u.Pkg.Rel+" "+u.Run)
		}
		return names
	}

	slow := queueUnits(pkgs, heavyList{}, measured, programs{}, kindSlow)
	fast := queueUnits(pkgs, heavyList{}, measured, programs{}, kindFast)

	if got, want := name(slow), []string{"b ^TestTwentySeconds$", "a ^TestThreeSeconds$"}; !slices.Equal(got, want) {
		t.Errorf("slow units = %q, want %q", got, want)
	}
	if got, want := name(fast), []string{"a ^(TestJustUnderASecond|TestQuick)$"}; !slices.Equal(got, want) {
		t.Errorf("fast units = %q, want %q", got, want)
	}
}

// The tests of a package that start a program stay together, because they share what the first of them builds; a
// package with more of them than one unit may hold is cut into several.
func TestTestsThatStartAProgramStayTogetherUpToAUnitsSeconds(t *testing.T) {
	p := pkg{Path: "example.com/m/cli", Rel: "cli"}
	measured := timings{tests: map[heavyTest]float64{}}
	starts := programs{}
	for _, name := range []string{"TestA", "TestB", "TestC", "TestD", "TestE"} {
		p.Tests = append(p.Tests, name)
		measured.tests[heavyTest{"cli", name}] = 50
		starts[heavyTest{"cli", name}] = "pig"
	}
	p.Tests = append(p.Tests, "TestUnmeasured")
	starts[heavyTest{"cli", "TestUnmeasured"}] = "pig"

	units := queueUnits([]pkg{p}, heavyList{}, measured, starts, "pig")

	var got []string
	for _, u := range units {
		got = append(got, u.Run)
		if u.Seconds > maxTogetherSeconds {
			t.Errorf("the unit %s expects %.0f s, over the %d a unit may hold", u.Run, u.Seconds, maxTogetherSeconds)
		}
	}
	want := []string{"^(TestA|TestB)$", "^(TestC|TestD)$", "^(TestE|TestUnmeasured)$"}
	if !slices.Equal(got, want) {
		t.Errorf("units = %q, want %q", got, want)
	}
}

// The parts of a kind hold every unit once and are even: the units are dealt out longest first.
func TestThePartsOfAKindHoldEveryUnitOnceAndAreEven(t *testing.T) {
	var units []unit
	for i, seconds := range []float64{90, 80, 70, 40, 30, 20, 10, 5, 5, 1} {
		units = append(units, unit{Pkg: pkg{Path: "example.com/m/p"}, Run: "^Test" + string(rune('A'+i)) + "$", Tests: 1, Seconds: seconds})
	}
	const total = 3
	seen := map[string]int{}
	var loads []float64
	for part := range total {
		load := 0.0
		for _, u := range partOf(units, part, total) {
			seen[u.Run]++
			load += u.Seconds
		}
		loads = append(loads, load)
	}
	for _, u := range units {
		if seen[u.Run] != 1 {
			t.Errorf("%s is in %d parts, want 1", u.Run, seen[u.Run])
		}
	}
	if slices.Max(loads)-slices.Min(loads) > 20 {
		t.Errorf("the parts hold %v seconds, want them within the shortest long unit of each other", loads)
	}
}

// A run's pieces after the first stage: every other kind that has tests, in as many parts as its seconds ask for.
func TestTheRunsPiecesFollowTheSecondsOfEachKind(t *testing.T) {
	p := pkg{Path: "example.com/m/a", Rel: "a"}
	measured := timings{tests: map[heavyTest]float64{}}
	starts := programs{}
	add := func(name, kind string, seconds float64) {
		p.Tests = append(p.Tests, name)
		measured.tests[heavyTest{"a", name}] = seconds
		if kind != "" {
			starts[heavyTest{"a", name}] = kind
		}
	}
	add("TestFast", "", 0.2)
	add("TestSlowOne", "", 400)
	add("TestSlowTwo", "", 300)
	add("TestSlowThree", "", 250)
	add("TestPig", "pig", 100)
	add("TestNode", "node", 60)

	got := runPieces([]pkg{p}, heavyList{}, measured, starts, 500)

	want := []piece{{"slow 1/2", "slow", "1/2"}, {"slow 2/2", "slow", "2/2"}, {"pig", "pig", "1/1"}, {"node", "node", "1/1"}}
	if !slices.Equal(got, want) {
		t.Errorf("pieces = %v, want %v", got, want)
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

	units := queueUnits([]pkg{p}, heavyList{}, timings{}, programs{}, kindFast)

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
	list, err := parsePrograms(strings.NewReader("# a comment\n\npig coding/cli:TestRuns\nnode ai:TestMatchesPi\npython tui:TestTmux\nslow ai:TestMoved # it waits for a timer\n"), "programs.txt")
	if err != nil {
		t.Fatal(err)
	}
	want := programs{{"coding/cli", "TestRuns"}: "pig", {"ai", "TestMatchesPi"}: "node", {"tui", "TestTmux"}: "node", {"ai", "TestMoved"}: "slow"}
	if len(list) != len(want) {
		t.Errorf("parsed %v, want %v", list, want)
	}
	for test, kind := range want {
		if list[test] != kind {
			t.Errorf("%s:%s is %q, want %q", test.Dir, test.Name, list[test], kind)
		}
	}
	for _, bad := range []string{"coding/cli:TestRuns\n", "pig coding/cli\n", "pig coding/cli:TestRuns extra\n", "pig :TestRuns\n", "heavy ai:TestX\n"} {
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

	units := slices.Concat(queueUnits(pkgs, heavyList{}, measured, programs{}, kindFast), queueUnits(pkgs, heavyList{}, measured, programs{}, kindSlow))
	err := queue(t.Context(), root, units, 2, t.TempDir(), &events, os.Stderr, &report)

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
