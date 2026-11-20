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

// fakeRun is a runner over made-up outcomes: per call, the result of each test the unit names. "exit" ends the
// process while that test runs, so it and the tests after it have no result.
func fakeRun(t *testing.T, calls *[]string, outcomes ...map[string]string) runner {
	t.Helper()
	return func(u unit) (attempt, error) {
		call := len(*calls)
		*calls = append(*calls, u.Run)
		if call >= len(outcomes) {
			t.Fatalf("unexpected run %d: %s", call+1, u.Run)
		}
		var a attempt
		a.ok = true
		add := func(event testEvent) {
			line, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			a.lines = append(a.lines, append(line, '\n'))
		}
		for _, name := range u.Names {
			outcome := outcomes[call][name]
			add(testEvent{Action: "run", Package: u.Pkg.Path, Test: name})
			add(testEvent{Action: "output", Package: u.Pkg.Path, Test: name, Output: "run " + string(rune('1'+call)) + " of " + name + "\n"})
			if outcome == "exit" {
				a.ok = false
				break
			}
			if outcome == "fail" {
				a.ok = false
			}
			add(testEvent{Action: outcome, Package: u.Pkg.Path, Test: name})
		}
		return a, nil
	}
}

// lastResults reads the result each test has in the lines, and fails when a test has more than one.
func lastResults(t *testing.T, lines [][]byte) map[string]string {
	t.Helper()
	results := map[string]string{}
	for _, line := range lines {
		var event testEvent
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		if event.Action == "pass" || event.Action == "fail" || event.Action == "skip" {
			if results[event.Test] != "" {
				t.Errorf("%s has two results in the lines", event.Test)
			}
			results[event.Test] = event.Action
		}
	}
	return results
}

// A test that fails runs once more, alone. When it passes then, the unit passes and the test is named as a flake,
// with what its failed run printed; the lines hold one result per test.
func TestATestThatFailsAndThenPassesAloneIsAFlakeAndTheUnitPasses(t *testing.T) {
	p := pkg{Path: "example.com/m/a", Rel: "a"}
	var calls []string
	run := fakeRun(t, &calls,
		map[string]string{"TestA": "pass", "TestB": "fail", "TestC": "pass"},
		map[string]string{"TestB": "pass"})

	lines, ok, flakes, err := runTwice(run, unitOf(p, []string{"TestA", "TestB", "TestC"}, 3), timings{})

	if err != nil || !ok {
		t.Fatalf("ok = %t, err = %v, want the unit to pass", ok, err)
	}
	if want := []string{"^(TestA|TestB|TestC)$", "^TestB$"}; !slices.Equal(calls, want) {
		t.Errorf("runs = %q, want %q", calls, want)
	}
	if len(flakes) != 1 || flakes[0].Test != "TestB" || flakes[0].Output != "run 1 of TestB\n" {
		t.Errorf("flakes = %+v, want TestB with the output of its failed run", flakes)
	}
	if got, want := lastResults(t, lines), map[string]string{"TestA": "pass", "TestB": "pass", "TestC": "pass"}; len(got) != 3 || got["TestA"] != want["TestA"] || got["TestB"] != want["TestB"] || got["TestC"] != want["TestC"] {
		t.Errorf("results = %v, want %v", got, want)
	}
}

// A test that fails alone too fails the unit, and is no flake.
func TestATestThatFailsTwiceFailsTheUnit(t *testing.T) {
	p := pkg{Path: "example.com/m/a", Rel: "a"}
	var calls []string
	run := fakeRun(t, &calls, map[string]string{"TestA": "pass", "TestB": "fail"}, map[string]string{"TestB": "fail"})

	lines, ok, flakes, err := runTwice(run, unitOf(p, []string{"TestA", "TestB"}, 2), timings{})

	if err != nil || ok || len(flakes) != 0 {
		t.Errorf("ok = %t, flakes = %+v, err = %v, want a failed unit and no flake", ok, flakes, err)
	}
	if got := lastResults(t, lines); got["TestA"] != "pass" || got["TestB"] != "fail" {
		t.Errorf("results = %v, want TestA passed and TestB failed", got)
	}
}

// A test that ends the process takes the tests after it with it. It runs again alone, the ones that never started run
// again together, and no test is left without a result.
func TestTheTestsAProcessThatEndedLeftBehindRunAgain(t *testing.T) {
	p := pkg{Path: "example.com/m/a", Rel: "a"}
	var calls []string
	run := fakeRun(t, &calls,
		map[string]string{"TestA": "pass", "TestB": "exit"},
		map[string]string{"TestB": "pass"},
		map[string]string{"TestC": "pass", "TestD": "pass"})

	lines, ok, flakes, err := runTwice(run, unitOf(p, []string{"TestA", "TestB", "TestC", "TestD"}, 4), timings{})

	if err != nil || !ok {
		t.Fatalf("ok = %t, err = %v, want the unit to pass", ok, err)
	}
	if want := []string{"^(TestA|TestB|TestC|TestD)$", "^TestB$", "^(TestC|TestD)$"}; !slices.Equal(calls, want) {
		t.Errorf("runs = %q, want %q", calls, want)
	}
	if len(flakes) != 1 || flakes[0].Test != "TestB" {
		t.Errorf("flakes = %+v, want the test that ended the process", flakes)
	}
	if got := lastResults(t, lines); len(got) != 4 {
		t.Errorf("results = %v, want one for each of the four tests", got)
	}
}

// A piece that is asked for again runs what did not pass in its earlier run and nothing else.
func TestAPieceThatRunsAgainLeavesOutWhatPassedBefore(t *testing.T) {
	p := pkg{Path: "example.com/m/a", Rel: "a"}
	q := pkg{Path: "example.com/m/b", Rel: "b"}
	dir := t.TempDir()
	var earlier strings.Builder
	for _, event := range []testEvent{
		{Action: "run", Package: p.Path, Test: "TestA"}, {Action: "pass", Package: p.Path, Test: "TestA"},
		{Action: "run", Package: p.Path, Test: "TestB"}, {Action: "run", Package: p.Path, Test: "TestB/sub"},
		{Action: "pass", Package: p.Path, Test: "TestB/sub"}, {Action: "fail", Package: p.Path, Test: "TestB"},
		{Action: "skip", Package: q.Path, Test: "TestSkipped"}, {Action: "pass", Package: q.Path, Test: "TestAlone"},
		{Action: "fail", Package: p.Path},
	} {
		line, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		earlier.Write(append(line, '\n'))
	}
	file := filepath.Join(dir, "earlier.json")
	if err := writeFile(file, earlier.String()); err != nil {
		t.Fatal(err)
	}

	before, err := readEarlier(file)
	if err != nil {
		t.Fatal(err)
	}
	units := []unit{
		unitOf(p, []string{"TestA", "TestB", "TestNeverRan"}, 3), unitOf(q, []string{"TestSkipped"}, 1), unitOf(q, []string{"TestAlone"}, 5),
	}
	left := leftToRun(units, before, timings{})

	var got []string
	for _, u := range left {
		got = append(got, u.Pkg.Rel+" "+u.Run)
	}
	if want := []string{"a ^(TestB|TestNeverRan)$"}; !slices.Equal(got, want) {
		t.Errorf("left to run = %q, want %q", got, want)
	}
	if none, err := readEarlier(filepath.Join(dir, "absent.json")); err != nil || none != nil {
		t.Errorf("no file: %v, %v, want no earlier run and no error", none, err)
	}
}

// The queue against a real module: a test that fails, a test that fails once and passes alone, and a test that ends
// the process. Every test's events are written once, each package's own result once although the package ran as
// several units, the failed package is named, and the flake is named with what its failed run printed.
func TestTheQueueRunsUnitsOfRealPackagesAndWritesEachPackageResultOnce(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go command")
	}
	root := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	t.Setenv("LIGHT_QUEUE_TEST_MARKER", marker)
	once := "import (\n\t\"os\"\n\t\"testing\"\n)\n\nfunc firstRun(name string) bool {\n\tpath := os.Getenv(\"LIGHT_QUEUE_TEST_MARKER\") + name\n\tif _, err := os.Stat(path); err == nil {\n\t\treturn false\n\t}\n\t_ = os.WriteFile(path, nil, 0o600)\n\treturn true\n}\n\n"
	files := map[string]string{
		"go.mod":         "module example.com/m\n\ngo 1.24\n",
		"a/a_test.go":    "package a\n\n" + once + "func TestFlaky(t *testing.T) {\n\tif firstRun(\"flaky\") {\n\t\tt.Fatal(\"the first run fails\")\n\t}\n}\nfunc TestOne(t *testing.T) {}\nfunc TestTwo(t *testing.T) {}\nfunc TestLong(t *testing.T) {}\n",
		"b/v2/b_test.go": "package b\n\nimport \"testing\"\n\nfunc TestFails(t *testing.T) { t.Fatal(\"no\") }\nfunc TestPasses(t *testing.T) {}\n",
		"c/c_test.go":    "package c\n\n" + once + "func TestAExits(t *testing.T) {\n\tif firstRun(\"exits\") {\n\t\tos.Exit(3)\n\t}\n}\nfunc TestBNeverStartedTheFirstTime(t *testing.T) {}\n",
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
		{Path: "example.com/m/a", Rel: "a", Tests: []string{"TestFlaky", "TestOne", "TestTwo", "TestLong"}},
		{Path: "example.com/m/b/v2", Rel: "b/v2", Tests: []string{"TestFails", "TestPasses"}},
		{Path: "example.com/m/c", Rel: "c", Tests: []string{"TestAExits", "TestBNeverStartedTheFirstTime"}},
	}
	measured := timings{tests: map[heavyTest]float64{{"a", "TestLong"}: 5}}
	var events, report, flaky bytes.Buffer

	units := slices.Concat(queueUnits(pkgs, heavyList{}, measured, programs{}, kindFast), queueUnits(pkgs, heavyList{}, measured, programs{}, kindSlow))
	err := queue(t.Context(), root, units, measured, nil, 2, t.TempDir(), &events, os.Stderr, &report, &flaky)

	if err == nil || err.Error() != "tests failed in example.com/m/b/v2" {
		t.Errorf("error = %v, want one that names the package whose test failed twice and no other", err)
	}
	results := map[string][]string{}
	tests := map[string][]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(events.String()), "\n") {
		var event testEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("not an event: %q: %v", line, err)
		}
		switch {
		case event.Test == "" && (event.Action == "pass" || event.Action == "fail"):
			results[event.Package] = append(results[event.Package], event.Action)
		case event.Test != "" && (event.Action == "pass" || event.Action == "fail"):
			tests[event.Package+":"+event.Test] = append(tests[event.Package+":"+event.Test], event.Action)
		}
	}
	for importPath, want := range map[string]string{"example.com/m/a": "pass", "example.com/m/b/v2": "fail", "example.com/m/c": "pass"} {
		if !slices.Equal(results[importPath], []string{want}) {
			t.Errorf("results of %s = %v, want once %s", importPath, results[importPath], want)
		}
	}
	want := map[string]string{
		"example.com/m/a:TestFlaky": "pass", "example.com/m/a:TestOne": "pass", "example.com/m/a:TestTwo": "pass", "example.com/m/a:TestLong": "pass",
		"example.com/m/b/v2:TestFails": "fail", "example.com/m/b/v2:TestPasses": "pass",
		"example.com/m/c:TestAExits": "pass", "example.com/m/c:TestBNeverStartedTheFirstTime": "pass",
	}
	if len(tests) != len(want) {
		t.Errorf("test results = %v, want %v", tests, want)
	}
	for name, action := range want {
		if !slices.Equal(tests[name], []string{action}) {
			t.Errorf("%s = %v, want once %s", name, tests[name], action)
		}
	}
	for _, line := range []string{"8 tests in 4 units (1 tests alone), 2 workers", "flaky: a:TestFlaky failed and passed when it ran once more, alone", "flaky: c:TestAExits failed and passed"} {
		if !strings.Contains(report.String(), line) {
			t.Errorf("report = %q, want %q in it", report.String(), line)
		}
	}
	if !strings.Contains(flaky.String(), "## a:TestFlaky\n") || !strings.Contains(flaky.String(), "the first run fails") {
		t.Errorf("flaky = %q, want the test and what its failed run printed", flaky.String())
	}

	// The same piece asked for again, with the results it left: only what failed runs, and the results are the whole piece's.
	file := filepath.Join(t.TempDir(), "earlier.json")
	if err := writeFile(file, events.String()); err != nil {
		t.Fatal(err)
	}
	earlier, err := readEarlier(file)
	if err != nil {
		t.Fatal(err)
	}
	var again, againReport bytes.Buffer
	err = queue(t.Context(), root, units, measured, earlier, 2, t.TempDir(), &again, os.Stderr, &againReport, nil)
	if err == nil || err.Error() != "tests failed in example.com/m/b/v2" {
		t.Errorf("the second run: error = %v, want the same failed package", err)
	}
	if !strings.Contains(againReport.String(), "1 tests in 1 units") {
		t.Errorf("the second run's report = %q, want the one failed test and no other", againReport.String())
	}
	for name := range want {
		_, test, _ := strings.Cut(name, ":")
		if !strings.Contains(again.String(), `"Test":"`+test+`"`) {
			t.Errorf("the second run's results hold nothing of %s", name)
		}
	}
}
