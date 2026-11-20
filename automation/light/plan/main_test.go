// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// runsOn returns the shards whose patterns select a test of a sliced package: -run matches and -skip does not.
func runsOn(t *testing.T, plans []shardPlan, name string) []int {
	t.Helper()
	var shards []int
	for shard, plan := range plans {
		selected := regexp.MustCompile(plan.Run).MatchString(name)
		skipped := plan.Skip != "" && regexp.MustCompile(plan.Skip).MatchString(name)
		if selected && !skipped {
			shards = append(shards, shard)
		}
	}
	return shards
}

// The shards may not lose a test and may not run one twice: whatever a test is called, and whether the seconds file
// knows it or not, exactly one shard selects it.
func TestEveryTestOfASlicedPackageIsInExactlyOneShard(t *testing.T) {
	names := []string{"Test", "TestA", "FuzzParse", "ExampleHost_Close", "TestÜberläufeWerdenGezählt", "Test_日本語", "TestDots.And(Parens)"}
	for length := range 120 {
		names = append(names, "Test"+strings.Repeat("x", length))
	}
	big := pkg{Path: "example.test/big", Rel: "big", Tests: slices.Concat(names, make([]string, minSlicedTests))}
	measured := timings{tests: map[heavyTest]float64{}, pkgs: map[string]float64{}}
	for i, name := range names {
		if i%3 == 0 { // every third test has seconds, the others go by name length
			measured.tests[heavyTest{"big", name}] = float64(5 + i)
		}
	}
	measured.tests[heavyTest{"big", "TestRemovedLongAgo"}] = 500 // out of date: no such test any more
	for total := 1; total <= 16; total++ {
		plans, err := planShards([]pkg{big}, measured, total)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range append(names, "TestAddedTomorrow", "TestRemovedLongAgo") {
			if shards := runsOn(t, plans, name); len(shards) != 1 {
				t.Errorf("with %d shards, %q runs on shards %v, want exactly one", total, name, shards)
			}
		}
	}
}

// A package below the slicing size runs on one shard with all its tests, however many seconds it has; a package from
// that size on runs on every shard.
func TestEveryPackageIsPlannedOnceOrSlicedOverAllShards(t *testing.T) {
	var pkgs []pkg
	for i := range 40 {
		pkgs = append(pkgs, pkg{Path: fmt.Sprintf("example.test/small%02d", i), Rel: fmt.Sprintf("small%02d", i), Tests: make([]string, i%7+1)})
	}
	pkgs = append(pkgs,
		pkg{Path: "example.test/many", Rel: "many", Tests: make([]string, minSlicedTests)},
		pkg{Path: "example.test/more", Rel: "more", Tests: make([]string, 4*minSlicedTests)},
		pkg{Path: "example.test/long", Rel: "long", Tests: make([]string, 3)},
	)
	measured := timings{tests: map[heavyTest]float64{}, pkgs: map[string]float64{"long": 900, "many": 200, "more": 20, "small03": 30}}
	sliced := []string{"example.test/many", "example.test/more"} // longest first

	for total := 1; total <= 9; total++ {
		plans, err := planShards(pkgs, measured, total)
		if err != nil {
			t.Fatal(err)
		}
		runs := map[string]int{}
		for shard, plan := range plans {
			if total > 1 && !slices.Equal(plan.Sliced, sliced) {
				t.Errorf("shard %d of %d slices %v, want %v", shard+1, total, plan.Sliced, sliced)
			}
			for _, path := range plan.Whole {
				runs[path]++
			}
		}
		for _, p := range pkgs {
			want := 1
			if total > 1 && slices.Contains(sliced, p.Path) {
				want = 0
			}
			if runs[p.Path] != want {
				t.Errorf("with %d shards, %s runs whole on %d shards, want %d", total, p.Path, runs[p.Path], want)
			}
		}
	}
}

// The long tests of one package are spread: with six tests of 100 s and three shards every shard gets two, and the
// longest tests of two packages do not meet on one shard.
func TestTimedTestsAreDealtOutEvenlyPerPackageAndInAll(t *testing.T) {
	measured := timings{tests: map[heavyTest]float64{}, pkgs: map[string]float64{"cli": 900, "host": 600}}
	cli := pkg{Path: "example.test/cli", Rel: "cli"}
	host := pkg{Path: "example.test/host", Rel: "host"}
	for i := range 6 {
		name := fmt.Sprintf("TestCLI%d", i)
		cli.Tests = append(cli.Tests, name)
		measured.tests[heavyTest{"cli", name}] = 100
	}
	for i, seconds := range []float64{300, 20, 10} {
		name := fmt.Sprintf("TestHost%d", i)
		host.Tests = append(host.Tests, name)
		measured.tests[heavyTest{"host", name}] = seconds
	}
	timedCLI, timedHost := slices.Clone(cli.Tests), slices.Clone(host.Tests)
	// Both are sliced packages: besides their timed tests they have many short ones.
	cli.Tests = append(cli.Tests, make([]string, minSlicedTests)...)
	host.Tests = append(host.Tests, make([]string, minSlicedTests)...)
	plans, err := planShards([]pkg{cli, host}, measured, 3)
	if err != nil {
		t.Fatal(err)
	}
	perShard := func(tests []string) []int {
		counts := make([]int, len(plans))
		for _, name := range tests {
			shards := runsOn(t, plans, name)
			if len(shards) != 1 {
				t.Fatalf("%s runs on shards %v, want exactly one", name, shards)
			}
			counts[shards[0]]++
		}
		return counts
	}
	if got := perShard(timedCLI); !slices.Equal(got, []int{2, 2, 2}) {
		t.Errorf("the six 100 s tests are spread %v over the shards, want 2 each", got)
	}
	// The 300 s test takes a shard; the two short ones go to the two shards without it.
	if got := perShard(timedHost); !slices.Equal(got, []int{1, 1, 1}) {
		t.Errorf("the three tests of the second package are spread %v, want 1 each", got)
	}
	seconds := []float64{plans[0].Seconds, plans[1].Seconds, plans[2].Seconds}
	if slices.Max(seconds)-slices.Min(seconds) > 300 {
		t.Errorf("the shards expect %v seconds; the 300 s test may not be the whole difference and more", seconds)
	}
}

// -run and -skip go by name alone, so a timed test's name is dealt out once even when two sliced packages have it.
func TestATimedNameSharedByTwoSlicedPackagesIsOnOneShard(t *testing.T) {
	a := pkg{Path: "example.test/a", Rel: "a", Tests: slices.Concat([]string{"TestShared", "TestOnlyA"}, make([]string, minSlicedTests))}
	b := pkg{Path: "example.test/b", Rel: "b", Tests: slices.Concat([]string{"TestShared"}, make([]string, minSlicedTests))}
	measured := timings{tests: map[heavyTest]float64{{"a", "TestShared"}: 40, {"b", "TestShared"}: 7, {"a", "TestOnlyA"}: 30}, pkgs: map[string]float64{}}
	plans, err := planShards([]pkg{a, b}, measured, 4)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"TestShared", "TestOnlyA"} {
		if shards := runsOn(t, plans, name); len(shards) != 1 {
			t.Errorf("%s runs on shards %v, want exactly one", name, shards)
		}
	}
}

// The packages that are not sliced are dealt out by their seconds, longest first.
func TestWholePackagesAreDealtOutBySeconds(t *testing.T) {
	var pkgs []pkg
	measured := timings{tests: map[heavyTest]float64{}, pkgs: map[string]float64{}}
	for i, seconds := range []float64{50, 40, 30, 20, 10, 10} {
		rel := fmt.Sprintf("p%d", i)
		pkgs = append(pkgs, pkg{Path: "example.test/" + rel, Rel: rel, Tests: make([]string, 1)})
		measured.pkgs[rel] = seconds
	}
	pkgs = append(pkgs, pkg{Path: "example.test/untimed", Rel: "untimed", Tests: make([]string, 9)})
	plans, err := planShards(pkgs, measured, 2)
	if err != nil {
		t.Fatal(err)
	}
	if plans[0].Seconds != 80 || plans[1].Seconds != 80 {
		t.Errorf("the shards expect %.0f and %.0f seconds of whole packages, want 80 each", plans[0].Seconds, plans[1].Seconds)
	}
	if len(plans[0].Whole)+len(plans[1].Whole) != len(pkgs) {
		t.Errorf("%d of %d packages are planned", len(plans[0].Whole)+len(plans[1].Whole), len(pkgs))
	}
}

func TestParseShard(t *testing.T) {
	for _, tc := range []struct {
		in           string
		index, total int
	}{{"1/1", 0, 1}, {"1/6", 0, 6}, {"6/6", 5, 6}} {
		index, total, err := parseShard(tc.in)
		if err != nil || index != tc.index || total != tc.total {
			t.Errorf("parseShard(%q) = %d, %d, %v; want %d, %d", tc.in, index, total, err, tc.index, tc.total)
		}
	}
	for _, in := range []string{"", "3", "0/6", "7/6", "a/6", "1/0", "1/-2", "1/6/2"} {
		if _, _, err := parseShard(in); err == nil {
			t.Errorf("parseShard(%q) succeeded, want an error", in)
		}
	}
}

func TestHeavyListMatchesAPackageAndEverythingBelowATree(t *testing.T) {
	heavy, err := parseHeavy(strings.NewReader(`
# a comment

test/perfgate   # measures memory
bench/...       # benchmark tooling
`), "heavy.txt")
	if err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]bool{
		"test/perfgate":        true,
		"test/perfgate/inner":  false,
		"test/perfgates":       false,
		"bench":                true,
		"bench/sessionscale":   true,
		"bench/a/b/c":          true,
		"benchmarks":           false,
		"coding/cli":           false,
		"internal/bench/thing": false,
	} {
		if got := heavy.matches(rel); got != want {
			t.Errorf("matches(%q) = %v, want %v", rel, got, want)
		}
	}
}

// Nothing leaves the light run without a reason.
func TestHeavyListRejectsAnEntryWithoutAReason(t *testing.T) {
	for _, text := range []string{"test/perfgate\n", "test/perfgate #\n", "test/perfgate #   \n", "two words # reason\n", "test/docs:TestA\n"} {
		if _, err := parseHeavy(strings.NewReader(text), "heavy.txt"); err == nil {
			t.Errorf("parseHeavy(%q) succeeded, want an error", text)
		}
	}
}

// One test of a package can be left out. The entry must name a test that exists, and no other package of the light
// run may have a test of that name: -skip goes by name and would skip that one too.
func TestSkipPatternNamesTheSingleTestsTheHeavyListLeavesOut(t *testing.T) {
	light := []pkg{
		{Path: "example.test/docs", Rel: "docs", Tests: []string{"TestFragments", "TestLinks"}},
		{Path: "example.test/cli", Rel: "cli", Tests: []string{"TestLinks", "TestRun"}},
	}
	parse := func(text string) heavyList {
		t.Helper()
		heavy, err := parseHeavy(strings.NewReader(text), "heavy.txt")
		if err != nil {
			t.Fatal(err)
		}
		return heavy
	}

	if got, err := skipPattern(light, parse("")); err != nil || regexp.MustCompile(got).MatchString("TestRun") {
		t.Errorf("without single tests the pattern is %q, %v; want one that matches no test", got, err)
	}

	got, err := skipPattern(light, parse("docs:TestFragments # needs the full history\ncli:TestRun # needs a quiet machine\n"))
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(got)
	for name, want := range map[string]bool{"TestFragments": true, "TestRun": true, "TestLinks": false, "TestRunner": false, "XTestRun": false} {
		if pattern.MatchString(name) != want {
			t.Errorf("%q matches %q: %v, want %v", got, name, !want, want)
		}
	}
	// Joined with a shard's own -skip pattern it still skips both, and what that pattern names.
	joined := regexp.MustCompile(joinPatterns(got, "^(TestOther)$"))
	for name, want := range map[string]bool{"TestFragments": true, "TestOther": true, "TestLinks": false} {
		if joined.MatchString(name) != want {
			t.Errorf("the joined pattern matches %q: %v, want %v", name, !want, want)
		}
	}

	for _, text := range []string{
		"docs:TestGone # removed long ago\n",    // no such test
		"docs:TestLinks # also a test of cli\n", // would skip cli's test too
		"cli:TestFragments # the test is in docs\n",
	} {
		if got, err := skipPattern(light, parse(text)); err == nil {
			t.Errorf("skipPattern with %q = %q, want an error", text, got)
		}
	}
}

func TestParseTimings(t *testing.T) {
	measured, err := parseTimings(strings.NewReader("# comment\n\n235 coding/cli:TestAuth\n199 internal/photonimage\n7.5 ai:TestRetries\n"), "timings.txt")
	if err != nil {
		t.Fatal(err)
	}
	if measured.tests[heavyTest{"coding/cli", "TestAuth"}] != 235 || measured.tests[heavyTest{"ai", "TestRetries"}] != 7.5 || measured.pkgs["internal/photonimage"] != 199 {
		t.Errorf("parseTimings read %v and %v", measured.tests, measured.pkgs)
	}
	for _, text := range []string{"coding/cli\n", "x coding/cli\n", "0 coding/cli\n", "-3 coding/cli\n", "5 coding/cli extra\n"} {
		if _, err := parseTimings(strings.NewReader(text), "timings.txt"); err == nil {
			t.Errorf("parseTimings(%q) succeeded, want an error", text)
		}
	}
}

// The seconds file comes from go test -json results: top-level tests and packages, a sliced package summed over the
// shards that ran a share of it, subtests and packages outside the list left out.
func TestAddTimingsSumsTestsAndPackagesOfGoTestJSON(t *testing.T) {
	rels := map[string]string{"example.test/cli": "cli", "example.test/ai": "ai"}
	tests := map[heavyTest]float64{}
	packages := map[string]float64{}
	for _, result := range []string{
		`{"Action":"run","Package":"example.test/cli","Test":"TestA"}
{"Action":"pass","Package":"example.test/cli","Test":"TestA","Elapsed":12.5}
{"Action":"pass","Package":"example.test/cli","Test":"TestA/sub","Elapsed":9}
{"Action":"skip","Package":"example.test/cli","Test":"TestSkipped","Elapsed":0}
{"Action":"pass","Package":"example.test/cli","Elapsed":40}
`,
		`{"Action":"fail","Package":"example.test/cli","Test":"TestB","Elapsed":30}
{"Action":"fail","Package":"example.test/cli","Elapsed":35}
{"Action":"pass","Package":"example.test/elsewhere","Test":"TestC","Elapsed":99}
{"Action":"pass","Package":"example.test/ai","Elapsed":3}
`,
	} {
		if err := addTimings(strings.NewReader(result), rels, tests, packages); err != nil {
			t.Fatal(err)
		}
	}
	if len(tests) != 2 || tests[heavyTest{"cli", "TestA"}] != 12.5 || tests[heavyTest{"cli", "TestB"}] != 30 {
		t.Errorf("tests = %v, want TestA 12.5 and TestB 30", tests)
	}
	if len(packages) != 2 || packages["cli"] != 75 || packages["ai"] != 3 {
		t.Errorf("packages = %v, want cli 75 and ai 3", packages)
	}
}

// What -timings-from writes, parseTimings reads back.
func TestWrittenTimingsReadBack(t *testing.T) {
	dir := t.TempDir()
	result := dir + "/test-sliced.json"
	if err := writeFile(result, `{"Action":"pass","Package":"example.test/cli","Test":"TestLong","Elapsed":61.4}
{"Action":"pass","Package":"example.test/cli","Test":"TestShort","Elapsed":0.2}
{"Action":"pass","Package":"example.test/cli","Elapsed":70}
{"Action":"pass","Package":"example.test/tiny","Elapsed":2}
`); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	pkgs := []pkg{{Path: "example.test/cli", Rel: "coding/cli"}, {Path: "example.test/tiny", Rel: "tiny"}}
	if err := writeTimings(&out, pkgs, []string{dir + "/test-*.json"}); err != nil {
		t.Fatal(err)
	}
	measured, err := parseTimings(&out, "timings.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(measured.tests) != 1 || measured.tests[heavyTest{"coding/cli", "TestLong"}] != 61 || len(measured.pkgs) != 1 || measured.pkgs["coding/cli"] != 70 {
		t.Errorf("read back %v and %v, want the long test with 61 s and the package with 70 s", measured.tests, measured.pkgs)
	}
	if err := writeTimings(&out, pkgs, []string{dir + "/none-*.json"}); err == nil {
		t.Error("writeTimings without a matching result file succeeded, want an error")
	}
}

func TestTestNamesAreWhatGoTestRuns(t *testing.T) {
	const src = `package p

import "testing"

func TestMain(m *testing.M)       {}
func Test(t *testing.T)           {}
func TestParsesAFrame(t *testing.T) {}
func Test_underscore(t *testing.T) {}
func Testify(t *testing.T)        {} // lower case after the prefix: not a test
func FuzzDecode(f *testing.F)     {}
func ExampleHost()                {}
func Examples()                   {} // lower case after the prefix: not an example
func BenchmarkDecode(b *testing.B) {}
func helper(t *testing.T)         {}

type suite struct{}

func (suite) TestMethod(t *testing.T) {}
`
	got, err := testNames("p_test.go", src)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Test", "TestParsesAFrame", "Test_underscore", "FuzzDecode", "ExampleHost"}
	if !slices.Equal(got, want) {
		t.Errorf("testNames = %v, want %v", got, want)
	}
}

func writeFile(name, content string) error { return os.WriteFile(name, []byte(content), 0o600) }
