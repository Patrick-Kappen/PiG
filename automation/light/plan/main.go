// SPDX-License-Identifier: MIT

// Command plan says what one runner of the light run tests.
//
// The light run spreads its tests over several runners, called shards. Every shard computes its own part from the
// checkout alone, so the shards need no job before them and cannot disagree: the same commit gives the same plan.
//
//	go run ./automation/light/plan -shard 2/8 -out tmp/light/plan
//
// writes five files:
//
//	sliced.pkgs     the packages with many tests. Every shard runs them, each with its own share.
//	run.re          the -run pattern of this shard's share of those packages.
//	skip-sliced.re  the -skip pattern that goes with it.
//	whole.pkgs      the other packages that this shard runs with all their tests.
//	skip.re         the -skip pattern of the single tests light/heavy.txt leaves out.
//
// A shard's share of a sliced package has two parts. The tests light/timings.txt gives seconds for are dealt out one
// by one, longest first, so no shard gets two long tests of one package while another gets none; this shard's are
// named in run.re and the other shards' in skip-sliced.re. Every other test goes by the length of its name: the tests
// whose name length leaves this shard's remainder when divided by the number of shards. Every name has one length, so
// every test is in exactly one shard, also a test this program has never seen and a test whose seconds are out of date.
//
// A package or a test is left out when light/heavy.txt names it.
//
//	go run ./automation/light/plan -timings-from 'results/*/test-*.json' > light/timings.txt
//
// writes the seconds file from the go test -json results of an earlier run.
//
//	go run ./automation/light/plan -tag-only pigfault ./durable/core/driver
//
// prints the -run pattern of the tests that exist only with that build tag, so a tagged run does not repeat the tests
// the untagged run already ran.
//
//	go run ./automation/light/plan -queue -workers 4
//
// plans nothing for a shard: it builds every package's test binary once and runs the tests that start no program from
// one queue (queue.go).
package main

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// minSlicedTests is the number of tests from which a package is sliced over all shards. A sliced package is linked
	// and started on every shard, so slicing pays only where the tests take much longer than that. Seconds do not
	// decide it: a small package with long tests stays whole, because its tests have never run without each other.
	minSlicedTests = 150
	// minTimedTestSeconds and minTimedPackageSeconds are the smallest entries -timings-from writes: shorter tests are
	// spread well enough by the length of their name.
	minTimedTestSeconds    = 1
	minTimedPackageSeconds = 10
	// maxPatternBytes keeps a pattern inside the size the kernel accepts for one command-line argument (128 KiB).
	maxPatternBytes = 100_000
)

type pkg struct {
	Path  string   // import path
	Rel   string   // directory below the repository root, with forward slashes: what heavy.txt and timings.txt name
	Files []string // absolute paths of its test files for the default build
	Tests []string // names of its top-level tests, fuzz targets and examples
}

// shardPlan is what one shard runs.
type shardPlan struct {
	Sliced []string // import paths, longest first: the go command starts them in this order
	Whole  []string
	Run    string // -run for the sliced packages
	Skip   string // -skip for the sliced packages: the timed tests of the other shards; empty when there are none
	// Seconds is what the seconds file expects of this shard: its timed tests and its whole packages.
	Seconds float64
}

// timings holds light/timings.txt: measured seconds of the longer tests and packages.
type timings struct {
	tests map[heavyTest]float64
	pkgs  map[string]float64
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "plan:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("plan", flag.ContinueOnError)
	flags.SetOutput(stderr)
	shard := flags.String("shard", "1/1", "this shard and the number of shards, as `index/total`; the first shard is 1")
	out := flags.String("out", "", "directory to write the plan's files to")
	heavyFile := flags.String("heavy", "light/heavy.txt", "the file that names the packages and tests the light run leaves out")
	timingsFile := flags.String("timings", "light/timings.txt", "the file with the measured seconds of the longer tests and packages")
	timingsFrom := flags.Bool("timings-from", false, "print a seconds file from the go test -json results given as arguments (file names or patterns)")
	tagOnly := flags.String("tag-only", "", "print the -run pattern of the tests that exist only with this build `tag`, for the packages given as arguments")
	queueMode := flags.Bool("queue", false, "build every test binary once and run the tests that start no program from one queue, the longest first")
	workers := flags.Int("workers", runtime.NumCPU(), "with -queue: how many units run at a time")
	programsFile := flags.String("programs", "light/programs.txt", "with -queue: the file that names the tests that start a program")
	binDir := flags.String("bin", "", "with -queue: `directory` for the test binaries (default: a temporary one)")
	reportFile := flags.String("report", "", "with -queue: `file` for the seconds of the build and the tests (default: standard error)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}

	switch {
	case *tagOnly != "":
		pattern, err := tagOnlyPattern(ctx, root, *tagOnly, flags.Args())
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, pattern)
		return err
	case *timingsFrom:
		pkgs, err := listPackages(ctx, root, "", "./...")
		if err != nil {
			return err
		}
		return writeTimings(stdout, pkgs, flags.Args())
	}

	index, total, err := parseShard(*shard)
	if err != nil {
		return err
	}
	if *out == "" && !*queueMode {
		return errors.New("-out is required")
	}
	heavy, err := readHeavy(*heavyFile)
	if err != nil {
		return err
	}
	measured, err := readTimings(*timingsFile)
	if err != nil {
		return err
	}
	pkgs, err := listPackages(ctx, root, "", "./...")
	if err != nil {
		return err
	}
	var light []pkg
	leftOut := 0
	for _, p := range pkgs {
		switch {
		case len(p.Files) == 0:
		case heavy.matches(p.Rel):
			leftOut++
		default:
			light = append(light, p)
		}
	}
	skip, err := skipPattern(light, heavy)
	if err != nil {
		return fmt.Errorf("%s: %w", *heavyFile, err)
	}
	if *queueMode {
		starts, err := readPrograms(*programsFile)
		if err != nil {
			return err
		}
		report := stderr
		if *reportFile != "" {
			file, err := os.Create(*reportFile)
			if err != nil {
				return err
			}
			defer file.Close() //nolint:errcheck // the report is a few lines for the reader of the run; the tests' result does not depend on it
			report = file
		}
		return queue(ctx, root, light, heavy, measured, starts, *workers, *binDir, stdout, stderr, report)
	}
	plans, err := planShards(light, measured, total)
	if err != nil {
		return err
	}
	plan := plans[index]

	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	for name, content := range map[string]string{
		"sliced.pkgs":    lines(plan.Sliced),
		"whole.pkgs":     lines(plan.Whole),
		"run.re":         plan.Run + "\n",
		"skip-sliced.re": joinPatterns(skip, plan.Skip) + "\n",
		"skip.re":        skip + "\n",
	} {
		if err := os.WriteFile(filepath.Join(*out, name), []byte(content), 0o644); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(stderr, "plan: shard %d/%d runs %d packages whole and its share of %d sliced packages; the seconds file expects %.0f s of timed tests and whole packages here (%.0f to %.0f s over the shards); light/heavy.txt leaves %d packages and %d single tests out\n",
		index+1, total, len(plan.Whole), len(plan.Sliced), plan.Seconds,
		slices.MinFunc(plans, bySeconds).Seconds, slices.MaxFunc(plans, bySeconds).Seconds, leftOut, len(heavy.tests))
	return err
}

func bySeconds(a, b shardPlan) int { return cmp.Compare(a.Seconds, b.Seconds) }

// lines writes one entry per line; no entries is an empty file, which the make file tests for.
func lines(entries []string) string {
	if len(entries) == 0 {
		return ""
	}
	return strings.Join(entries, "\n") + "\n"
}

// joinPatterns returns a pattern that matches what either pattern matches; an empty second pattern adds nothing.
func joinPatterns(first, second string) string {
	if second == "" {
		return first
	}
	return first + "|" + second
}

// parseShard reads "2/6" as index 1 of 6.
func parseShard(s string) (index, total int, err error) {
	first, second, ok := strings.Cut(s, "/")
	if ok {
		index, err = strconv.Atoi(first)
		if err == nil {
			total, err = strconv.Atoi(second)
		}
	}
	if !ok || err != nil || total < 1 || index < 1 || index > total {
		return 0, 0, fmt.Errorf("-shard %q: want index/total with 1 <= index <= total", s)
	}
	return index - 1, total, nil
}

// planShards gives each of total shards its part of the packages.
//
// A package is sliced over all shards when it has many tests. The timed tests of the sliced
// packages are dealt out longest first, each to the shard that so far has the fewest seconds of that test's package
// and, among those, the fewest seconds in all: that spreads the long tests of one package and keeps the shards even.
// The packages that are not sliced are dealt out the same way, longest first, each to the shard with the fewest
// seconds of whole packages.
func planShards(pkgs []pkg, measured timings, total int) ([]shardPlan, error) {
	plans := make([]shardPlan, total)
	var sliced, whole []pkg
	for _, p := range pkgs {
		if total > 1 && len(p.Tests) >= minSlicedTests {
			sliced = append(sliced, p)
		} else {
			whole = append(whole, p)
		}
	}

	// A test is dealt out by its name alone, because -run and -skip go by name and one go test run holds all sliced
	// packages: two sliced packages with a timed test of one name get it on the same shard.
	seconds := map[string]float64{}
	owners := map[string][]string{}
	for _, p := range sliced {
		for _, name := range p.Tests {
			if s := measured.tests[heavyTest{p.Rel, name}]; s > 0 {
				seconds[name] += s
				owners[name] = append(owners[name], p.Rel)
			}
		}
	}
	names := slices.SortedFunc(maps.Keys(seconds), func(a, b string) int {
		return cmp.Or(cmp.Compare(seconds[b], seconds[a]), cmp.Compare(a, b))
	})
	ofPackage := map[string][]float64{}
	inAll := make([]float64, total)
	placed := make([][]string, total)
	for _, name := range names {
		best := 0
		for shard := 1; shard < total; shard++ {
			if cmp.Or(cmp.Compare(load(ofPackage, owners[name], shard), load(ofPackage, owners[name], best)), cmp.Compare(inAll[shard], inAll[best])) < 0 {
				best = shard
			}
		}
		for _, rel := range owners[name] {
			if ofPackage[rel] == nil {
				ofPackage[rel] = make([]float64, total)
			}
			ofPackage[rel][best] += measured.tests[heavyTest{rel, name}]
		}
		inAll[best] += seconds[name]
		placed[best] = append(placed[best], regexp.QuoteMeta(name))
	}

	slices.SortFunc(sliced, func(a, b pkg) int {
		return cmp.Or(cmp.Compare(measured.pkgs[b.Rel], measured.pkgs[a.Rel]), cmp.Compare(len(b.Tests), len(a.Tests)), cmp.Compare(a.Path, b.Path))
	})
	slices.SortFunc(whole, func(a, b pkg) int {
		return cmp.Or(cmp.Compare(measured.pkgs[b.Rel], measured.pkgs[a.Rel]), cmp.Compare(len(b.Tests), len(a.Tests)), cmp.Compare(a.Path, b.Path))
	})
	ofWhole := make([]float64, total)
	for i, p := range whole {
		best := 0
		for shard := 1; shard < total; shard++ {
			if ofWhole[shard] < ofWhole[best] {
				best = shard
			}
		}
		// A package without seconds counts for a little, most tests first, so those are spread too.
		ofWhole[best] += max(measured.pkgs[p.Rel], 1-float64(i)/float64(len(whole)+1))
		plans[best].Whole = append(plans[best].Whole, p.Path)
		plans[best].Seconds += measured.pkgs[p.Rel]
	}

	for shard := range plans {
		plan := &plans[shard]
		for _, p := range sliced {
			plan.Sliced = append(plan.Sliced, p.Path)
		}
		slices.Sort(plan.Whole)
		plan.Seconds += inAll[shard]
		plan.Run = runPattern(shard, total)
		if len(placed[shard]) > 0 {
			slices.Sort(placed[shard])
			plan.Run += "|^(" + strings.Join(placed[shard], "|") + ")$"
		}
		var others []string
		for other, list := range placed {
			if other != shard {
				others = append(others, list...)
			}
		}
		if len(others) > 0 {
			slices.Sort(others)
			plan.Skip = "^(" + strings.Join(others, "|") + ")$"
		}
		if len(plan.Run) > maxPatternBytes || len(plan.Skip) > maxPatternBytes {
			return nil, fmt.Errorf("the seconds file times so many tests that a pattern is over %d bytes; give seconds to fewer tests", maxPatternBytes)
		}
	}
	return plans, nil
}

// load is the seconds a shard already has of the packages a test belongs to.
func load(ofPackage map[string][]float64, rels []string, shard int) float64 {
	sum := 0.0
	for _, rel := range rels {
		if ofPackage[rel] != nil {
			sum += ofPackage[rel][shard]
		}
	}
	return sum
}

// runPattern matches the test names whose length in characters is index modulo total.
func runPattern(index, total int) string {
	return fmt.Sprintf("^(.{%d})*.{%d}$", total, index)
}

// heavyList holds what light/heavy.txt leaves out of the light run.
type heavyList struct {
	patterns []string    // packages: a directory, or a directory followed by /...
	tests    []heavyTest // single tests
}

// heavyTest names one test: its package's directory below the repository root, and its name.
type heavyTest struct{ Dir, Name string }

func (h heavyList) matches(rel string) bool {
	return slices.ContainsFunc(h.patterns, func(pattern string) bool {
		if dir, ok := strings.CutSuffix(pattern, "/..."); ok {
			return rel == dir || strings.HasPrefix(rel, dir+"/")
		}
		return rel == pattern
	})
}

func readHeavy(name string) (heavyList, error) {
	content, err := os.ReadFile(name)
	if err != nil {
		return heavyList{}, err
	}
	return parseHeavy(bytes.NewReader(content), name)
}

// parseHeavy reads one entry per line: a package's directory below the repository root, such a directory followed by
// /... for everything below it, or directory:TestName for one test of a package. Every entry carries its reason after
// a #; a line without one is an error, so nothing leaves the light run without saying why.
func parseHeavy(r io.Reader, name string) (heavyList, error) {
	var list heavyList
	scanner := bufio.NewScanner(r)
	for line := 1; scanner.Scan(); line++ {
		entry, reason, hasReason := strings.Cut(scanner.Text(), "#")
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if !hasReason || strings.TrimSpace(reason) == "" {
			return heavyList{}, fmt.Errorf("%s:%d: %s has no reason after a #", name, line, entry)
		}
		if strings.ContainsAny(entry, " \t") {
			return heavyList{}, fmt.Errorf("%s:%d: %q is not one package or one test", name, line, entry)
		}
		if dir, test, ok := strings.Cut(entry, ":"); ok {
			list.tests = append(list.tests, heavyTest{Dir: dir, Name: test})
		} else {
			list.patterns = append(list.patterns, entry)
		}
	}
	return list, scanner.Err()
}

// skipPattern returns the -skip pattern of the single tests the heavy list leaves out. -skip goes by name alone and
// one go test run holds many packages, so an entry is an error when its test does not exist (the list would go stale
// unnoticed) and when another package of the light run has a test of that name (it would be skipped as well).
func skipPattern(light []pkg, heavy heavyList) (string, error) {
	if len(heavy.tests) == 0 {
		return "^$", nil // no top-level test has the empty name
	}
	var names []string
	for _, entry := range heavy.tests {
		found := false
		for _, p := range light {
			if !slices.Contains(p.Tests, entry.Name) {
				continue
			}
			if p.Rel != entry.Dir {
				return "", fmt.Errorf("%s:%s: %s has a test of that name too, and -skip goes by name", entry.Dir, entry.Name, p.Rel)
			}
			found = true
		}
		if !found {
			return "", fmt.Errorf("%s:%s: the light run has no such test", entry.Dir, entry.Name)
		}
		names = append(names, regexp.QuoteMeta(entry.Name))
	}
	slices.Sort(names)
	return "^(" + strings.Join(slices.Compact(names), "|") + ")$", nil
}

// readTimings reads the seconds file. Without the file the plan has no seconds and spreads by name length and by
// number of tests alone.
func readTimings(name string) (timings, error) {
	content, err := os.ReadFile(name)
	if errors.Is(err, os.ErrNotExist) {
		return timings{}, nil
	}
	if err != nil {
		return timings{}, err
	}
	return parseTimings(bytes.NewReader(content), name)
}

// parseTimings reads one entry per line: the seconds, then a package's directory below the repository root for the
// whole package, or directory:TestName for one test. Lines that start with # are comments.
func parseTimings(r io.Reader, name string) (timings, error) {
	measured := timings{tests: map[heavyTest]float64{}, pkgs: map[string]float64{}}
	scanner := bufio.NewScanner(r)
	for line := 1; scanner.Scan(); line++ {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		var seconds float64
		var err error
		if len(fields) == 2 {
			seconds, err = strconv.ParseFloat(fields[0], 64)
		}
		if len(fields) != 2 || err != nil || seconds <= 0 {
			return timings{}, fmt.Errorf("%s:%d: want seconds above zero and a package or package:Test, got %q", name, line, scanner.Text())
		}
		if dir, test, ok := strings.Cut(fields[1], ":"); ok {
			measured.tests[heavyTest{dir, test}] = seconds
		} else {
			measured.pkgs[fields[1]] = seconds
		}
	}
	return measured, scanner.Err()
}

// writeTimings prints a seconds file from go test -json results: the top-level tests and the packages that took
// long enough to matter, longest first. A sliced package's seconds are the sum over the shards that ran a share.
func writeTimings(w io.Writer, pkgs []pkg, patterns []string) error {
	rels := map[string]string{}
	for _, p := range pkgs {
		rels[p.Path] = p.Rel
	}
	var files []string
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return err
		}
		files = append(files, matches...)
	}
	if len(files) == 0 {
		return errors.New("-timings-from: no result file matches")
	}
	tests := map[heavyTest]float64{}
	packages := map[string]float64{}
	for _, name := range files {
		content, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		if err := addTimings(bytes.NewReader(content), rels, tests, packages); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}

	type entry struct {
		seconds float64
		name    string
	}
	var entries []entry
	for test, seconds := range tests {
		if seconds >= minTimedTestSeconds {
			entries = append(entries, entry{seconds, test.Dir + ":" + test.Name})
		}
	}
	for rel, seconds := range packages {
		if seconds >= minTimedPackageSeconds {
			entries = append(entries, entry{seconds, rel})
		}
	}
	slices.SortFunc(entries, func(a, b entry) int {
		return cmp.Or(cmp.Compare(b.seconds, a.seconds), cmp.Compare(a.name, b.name))
	})
	var out bytes.Buffer
	// The tag is in two pieces so that the license check reads this file's own declaration and not this string.
	out.WriteString("# SPDX-License-" + "Identifier: MIT\n#\n")
	out.WriteString("# Measured seconds of the longer tests and packages of the light run, for automation/light/plan to spread them evenly over the\n")
	out.WriteString("# shards. Written by `go run ./automation/light/plan -timings-from` from the go test -json results of a run; not edited by hand.\n")
	out.WriteString("# Nothing checks this file against the tests: a test that is missing here or has old seconds still runs exactly once,\n")
	out.WriteString("# the shards are only less even. A pull request does not have to touch it.\n")
	for _, e := range entries {
		out.WriteString(strconv.FormatFloat(e.seconds, 'f', 0, 64) + " " + e.name + "\n")
	}
	_, err := w.Write(out.Bytes())
	return err
}

// addTimings adds the seconds of the events of one go test -json result.
func addTimings(r io.Reader, rels map[string]string, tests map[heavyTest]float64, packages map[string]float64) error {
	decoder := json.NewDecoder(r)
	for {
		var event struct {
			Action, Package, Test string
			Elapsed               float64
		}
		if err := decoder.Decode(&event); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
		rel, known := rels[event.Package]
		if !known || (event.Action != "pass" && event.Action != "fail") {
			continue
		}
		switch {
		case event.Test == "":
			packages[rel] += event.Elapsed
		case !strings.Contains(event.Test, "/"):
			tests[heavyTest{rel, event.Test}] += event.Elapsed
		}
	}
}

// listPackages returns the packages that match the patterns, with the tests of their test files for the default build
// and, when tags is not empty, those build tags.
func listPackages(ctx context.Context, root, tags string, patterns ...string) ([]pkg, error) {
	args := []string{"list", "-json=ImportPath,Dir,TestGoFiles,XTestGoFiles"}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	out, err := goCommand(ctx, append(args, patterns...)...)
	if err != nil {
		return nil, err
	}
	var pkgs []pkg
	for decoder := json.NewDecoder(bytes.NewReader(out)); ; {
		var listed struct {
			ImportPath, Dir           string
			TestGoFiles, XTestGoFiles []string
		}
		if err := decoder.Decode(&listed); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("go list: %w", err)
		}
		// The directory and not the import path names a package in heavy.txt and timings.txt, because the workspace
		// holds modules of their own below the root.
		rel, err := filepath.Rel(root, listed.Dir)
		if err != nil {
			return nil, err
		}
		p := pkg{Path: listed.ImportPath, Rel: filepath.ToSlash(rel)}
		for _, file := range slices.Concat(listed.TestGoFiles, listed.XTestGoFiles) {
			path := filepath.Join(listed.Dir, file)
			names, err := testNames(path, nil)
			if err != nil {
				return nil, err
			}
			p.Files = append(p.Files, path)
			p.Tests = append(p.Tests, names...)
		}
		pkgs = append(pkgs, p)
	}
	return pkgs, nil
}

func goCommand(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "go", args...) //nolint:gosec // G204: the program is literal go; the arguments are list flags, a build tag and package patterns from this program's own command line.
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

// testNames returns the names of the top-level tests, fuzz targets and examples of a test file: what go test runs
// and -run selects. src is the file's content, or nil to read the file.
func testNames(path string, src any) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name.Name == "TestMain" {
			continue
		}
		if slices.ContainsFunc([]string{"Test", "Fuzz", "Example"}, func(prefix string) bool { return isTestName(fn.Name.Name, prefix) }) {
			names = append(names, fn.Name.Name)
		}
	}
	return names, nil
}

// isTestName is the go command's rule: the prefix, then nothing or a character that is not a lower-case letter.
func isTestName(name, prefix string) bool {
	rest, ok := strings.CutPrefix(name, prefix)
	if !ok {
		return false
	}
	first, _ := utf8.DecodeRuneInString(rest)
	return rest == "" || !unicode.IsLower(first)
}

// tagOnlyPattern returns the -run pattern of the tests that the packages have with the build tag and not without it.
func tagOnlyPattern(ctx context.Context, root, tag string, patterns []string) (string, error) {
	if len(patterns) == 0 {
		return "", errors.New("-tag-only needs the packages as arguments")
	}
	plain, err := listPackages(ctx, root, "", patterns...)
	if err != nil {
		return "", err
	}
	tagged, err := listPackages(ctx, root, tag, patterns...)
	if err != nil {
		return "", err
	}
	var without []string
	for _, p := range plain {
		without = append(without, p.Tests...)
	}
	var only []string
	for _, p := range tagged {
		for _, name := range p.Tests {
			if !slices.Contains(without, name) {
				only = append(only, regexp.QuoteMeta(name))
			}
		}
	}
	if len(only) == 0 {
		return "", fmt.Errorf("no test of %s exists only with the build tag %s", strings.Join(patterns, " "), tag)
	}
	slices.Sort(only)
	return "^(" + strings.Join(slices.Compact(only), "|") + ")$", nil
}
