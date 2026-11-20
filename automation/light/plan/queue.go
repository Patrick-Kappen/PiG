// SPDX-License-Identifier: MIT

package main

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// The queue runs one piece of the light run on one runner: the test binaries it needs built once, then its tests from
// one queue, the longest first, by several workers.
//
//	go run ./automation/light/plan -queue -kind fast -workers 6 -bin tmp/light/bin
//	go run ./automation/light/plan -queue -kind pig -part 2/3 -workers 4
//	go run ./automation/light/plan -pieces -piece-seconds 900
//
// A worker takes the next unit when it is free, so no processor waits while a test is left, and a run that is wrong
// about a test's seconds is only a little less even. It prints the tests' events as go test -json does, for gotestsum,
// and says in -report how long the build and the tests took and how busy the workers were.
//
// Every test has one kind, and a piece is the tests of one kind, or one part of them:
//
//	fast    starts no program and takes under a second, or is not measured yet. Every pull request's first green.
//	slow    starts no program and takes a second or more
//	pig     starts the built pig
//	go      starts the go command: it builds something
//	node    starts Node or Pi (and the few that start Python or tmux)
//	rust    starts cargo, rustc or sbcl: it builds a cell
//
// light/programs.txt says what a test starts and light/timings.txt how long it takes; both are measured. A line in
// light/moved.txt puts one test in another kind by hand. -pieces prints the pieces a run needs: fast, and as many parts
// of each other kind as its seconds ask for, so a kind that grows gets another part by itself.

const (
	// minAloneSeconds is the measured time from which a test that starts no program is slow and a unit of its own.
	// Shorter tests of a package run together in one process: starting a test binary takes milliseconds, but a
	// package's tests share what its TestMain and its first tests set up.
	minAloneSeconds = 1
	// secondsPerShortTest and secondsPerProgramTest are what a test without measured seconds counts for.
	secondsPerShortTest   = 0.01
	secondsPerProgramTest = 0.5
	// maxTogetherSeconds bounds a unit of tests that start a program. They stay together in one process, because
	// they share what the first of them builds (the pig binary, a cell); a package with many of them is cut into
	// units of this size so that its parts can be even and its tests use more than one worker.
	maxTogetherSeconds = 120
	// unitTimeout ends a unit that hangs.
	unitTimeout = "20m"

	kindFast = "fast"
	kindSlow = "slow"
)

// kinds are the kinds in the order a run reports them; fast is the first stage, the others follow it.
var kinds = []string{kindFast, kindSlow, "pig", "go", "node", "rust"}

// unit is what one worker runs at a time: tests of one package, in one process.
type unit struct {
	Pkg     pkg
	Run     string  // the -test.run pattern
	Tests   int     // how many tests the pattern names
	Seconds float64 // what the seconds file expects
}

// programs holds light/programs.txt: the tests that start a program, each with what it starts.
type programs map[heavyTest]string

func readPrograms(name string) (programs, error) {
	content, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	return parsePrograms(bytes.NewReader(content), name)
}

// parsePrograms reads one test per line: what it starts, then directory:TestName. Lines that start with # are comments.
// light/moved.txt has the same form and may also say fast or slow.
func parsePrograms(r io.Reader, name string) (programs, error) {
	list := programs{}
	scanner := bufio.NewScanner(r)
	for line := 1; scanner.Scan(); line++ {
		entry, _, _ := strings.Cut(scanner.Text(), "#")
		fields := strings.Fields(entry)
		if len(fields) == 0 {
			continue
		}
		dir, test, ok := strings.Cut(fields[len(fields)-1], ":")
		if len(fields) != 2 || !ok || dir == "" || test == "" {
			return nil, fmt.Errorf("%s:%d: want what the test starts and package:Test, got %q", name, line, scanner.Text())
		}
		kind := fields[0]
		if kind == "python" {
			kind = "node" // the few tests that start Python or tmux run with the ones that start Node
		}
		if !slices.Contains(kinds, kind) {
			return nil, fmt.Errorf("%s:%d: %q is not one of %s, python", name, line, fields[0], strings.Join(kinds, ", "))
		}
		list[heavyTest{dir, test}] = kind
	}
	return list, scanner.Err()
}

// kindOf is the kind of one test: what the files say it starts, or fast or slow by its measured seconds.
func kindOf(test heavyTest, measured timings, starts programs) string {
	if kind, listed := starts[test]; listed {
		return kind
	}
	if measured.tests[test] >= minAloneSeconds {
		return kindSlow
	}
	return kindFast
}

// queueUnits returns the units of the packages' tests of one kind, the longest first.
//
// A slow test is a unit of its own. A package's fast tests are one unit, or more when their names do not fit one
// pattern. A package's tests of a kind that starts a program are one unit, or several of at most maxTogetherSeconds.
// A test that no file names is fast: a new test runs in the first stage until a measurement says otherwise.
func queueUnits(pkgs []pkg, heavy heavyList, measured timings, starts programs, kind string) []unit {
	var units []unit
	for _, p := range pkgs {
		var together []string
		for _, name := range p.Tests {
			test := heavyTest{p.Rel, name}
			if slices.Contains(heavy.tests, test) || kindOf(test, measured, starts) != kind {
				continue
			}
			if kind == kindSlow {
				units = append(units, unit{Pkg: p, Run: "^" + regexp.QuoteMeta(name) + "$", Tests: 1, Seconds: measured.tests[test]})
				continue
			}
			together = append(together, name)
		}
		slices.Sort(together)
		perTest, limit := secondsPerShortTest, 0.0
		if kind != kindFast {
			perTest, limit = secondsPerProgramTest, maxTogetherSeconds
		}
		for len(together) > 0 {
			n, size, seconds := 0, len("^()$"), 0.0
			for n < len(together) {
				next := max(measured.tests[heavyTest{p.Rel, together[n]}], perTest)
				if n > 0 && (size+len(regexp.QuoteMeta(together[n]))+1 > maxPatternBytes || (limit > 0 && seconds+next > limit)) {
					break
				}
				size += len(regexp.QuoteMeta(together[n])) + 1
				seconds += next
				n++
			}
			quoted := make([]string, n)
			for i, name := range together[:n] {
				quoted[i] = regexp.QuoteMeta(name)
			}
			units = append(units, unit{Pkg: p, Run: "^(" + strings.Join(quoted, "|") + ")$", Tests: n, Seconds: seconds})
			together = together[n:]
		}
	}
	slices.SortStableFunc(units, byLongestFirst)
	return units
}

func byLongestFirst(a, b unit) int {
	return cmp.Or(cmp.Compare(b.Seconds, a.Seconds), cmp.Compare(a.Pkg.Path, b.Pkg.Path), cmp.Compare(a.Run, b.Run))
}

// partOf returns the units of one of total parts. The units are dealt out longest first, each to the part that has
// the fewest seconds so far, so the parts are even and every unit is in exactly one.
func partOf(units []unit, index, total int) []unit {
	parts := make([][]unit, total)
	load := make([]float64, total)
	for _, u := range slices.SortedStableFunc(slices.Values(units), byLongestFirst) {
		best := 0
		for part := 1; part < total; part++ {
			if load[part] < load[best] {
				best = part
			}
		}
		parts[best] = append(parts[best], u)
		load[best] += u.Seconds
	}
	return parts[index]
}

// piece names one job of a run: a kind, and which part of it.
type piece struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Part string `json:"part"`
}

// runPieces returns the pieces after the first stage: each other kind in as many parts as its seconds ask for, at
// seconds per part. A kind without tests has no piece.
func runPieces(pkgs []pkg, heavy heavyList, measured timings, starts programs, seconds float64) []piece {
	var pieces []piece
	for _, kind := range kinds[1:] {
		total := 0.0
		units := queueUnits(pkgs, heavy, measured, starts, kind)
		for _, u := range units {
			total += u.Seconds
		}
		if len(units) == 0 {
			continue
		}
		parts := min(max(1, int(math.Ceil(total/seconds))), len(units))
		for part := 1; part <= parts; part++ {
			name := kind
			if parts > 1 {
				name = fmt.Sprintf("%s %d/%d", kind, part, parts)
			}
			pieces = append(pieces, piece{Name: name, Kind: kind, Part: fmt.Sprintf("%d/%d", part, parts)})
		}
	}
	return pieces
}

// binaryBase is the name the go command gives a package's test binary, without ".test": the last element of the
// import path, or the one before it when the last is a major version such as v2.
func binaryBase(importPath string) string {
	base := path.Base(importPath)
	if majorVersion.MatchString(base) && base != "v0" && base != "v1" {
		return path.Base(path.Dir(importPath))
	}
	return base
}

var majorVersion = regexp.MustCompile(`^v[0-9]+$`)

// buildGroups splits the packages so that no two packages of a group get the same binary name: the go command builds a
// group with one command, into one directory.
func buildGroups(pkgs []pkg) [][]pkg {
	var groups [][]pkg
	var taken []map[string]bool
	for _, p := range pkgs {
		base := binaryBase(p.Path)
		placed := false
		for i := range groups {
			if !taken[i][base] {
				groups[i], taken[i][base], placed = append(groups[i], p), true, true
				break
			}
		}
		if !placed {
			groups, taken = append(groups, []pkg{p}), append(taken, map[string]bool{base: true})
		}
	}
	return groups
}

// buildBinaries compiles and links the test binary of every package once and returns each package's binary.
func buildBinaries(ctx context.Context, pkgs []pkg, dir string, stderr io.Writer) (map[string]string, error) {
	binaries := map[string]string{}
	for i, group := range buildGroups(pkgs) {
		out, err := filepath.Abs(filepath.Join(dir, fmt.Sprint(i)))
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(out, 0o755); err != nil {
			return nil, err
		}
		args := []string{"test", "-c", "-o", out + string(filepath.Separator)}
		for _, p := range group {
			args = append(args, p.Path)
			binaries[p.Path] = filepath.Join(out, binaryBase(p.Path)+".test")
		}
		cmd := exec.CommandContext(ctx, "go", args...) //nolint:gosec // G204: the program is literal go; the arguments are fixed flags, a directory this command made and the import paths go list gave.
		cmd.Stdout, cmd.Stderr = stderr, stderr
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("go test -c: %w", err)
		}
	}
	for importPath, binary := range binaries {
		if _, err := os.Stat(binary); err != nil {
			return nil, fmt.Errorf("%s: the go command built no test binary: %w", importPath, err)
		}
	}
	return binaries, nil
}

// testEvent is the part of a go test -json line this command reads.
type testEvent struct {
	Time    time.Time
	Action  string
	Package string
	Test    string  `json:",omitempty"`
	Elapsed float64 `json:",omitzero"`
	Output  string  `json:",omitempty"`
}

// queueResult is what running the units came to.
type queueResult struct {
	failed  []string       // the packages with a unit that failed
	busy    time.Duration  // the units' times added up
	longest []unitDuration // the units that took longest
	elapsed map[string]float64
}

type unitDuration struct {
	unit    unit
	seconds float64
}

// runQueue runs the units with the given number of workers and writes the tests' events to out. A package's own result
// is written once, when its last unit has ended: a package is in several units, and each ends with a result line.
func runQueue(ctx context.Context, root string, units []unit, binaries map[string]string, workers int, out io.Writer) (queueResult, error) {
	var (
		mu       sync.Mutex
		writeErr error
		result   = queueResult{elapsed: map[string]float64{}}
		failed   = map[string]bool{}
		left     = map[string]int{}
	)
	for _, u := range units {
		left[u.Pkg.Path]++
	}
	write := func(event testEvent) {
		line, err := json.Marshal(event)
		if err == nil {
			_, err = out.Write(append(line, '\n'))
		}
		writeErr = cmp.Or(writeErr, err)
	}
	next := make(chan unit)
	var wg sync.WaitGroup
	for range max(workers, 1) {
		wg.Go(func() {
			for u := range next {
				started := time.Now()
				ok, err := runUnit(ctx, root, u, binaries[u.Pkg.Path], func(line []byte) {
					mu.Lock()
					defer mu.Unlock()
					_, err := out.Write(line)
					writeErr = cmp.Or(writeErr, err)
				})
				took := time.Since(started)
				mu.Lock()
				if err != nil {
					write(testEvent{Time: time.Now(), Action: "output", Package: u.Pkg.Path, Output: "light queue: " + err.Error() + "\n"})
				}
				if !ok || err != nil {
					failed[u.Pkg.Path] = true
				}
				result.busy += took
				result.elapsed[u.Pkg.Path] += took.Seconds()
				result.longest = append(result.longest, unitDuration{u, took.Seconds()})
				left[u.Pkg.Path]--
				if left[u.Pkg.Path] == 0 {
					action := "pass"
					if failed[u.Pkg.Path] {
						action = "fail"
					}
					write(testEvent{Time: time.Now(), Action: action, Package: u.Pkg.Path, Elapsed: result.elapsed[u.Pkg.Path]})
				}
				mu.Unlock()
			}
		})
	}
	for _, u := range units {
		next <- u
	}
	close(next)
	wg.Wait()

	result.failed = slices.Sorted(maps.Keys(failed))
	slices.SortFunc(result.longest, func(a, b unitDuration) int { return cmp.Compare(b.seconds, a.seconds) })
	return result, writeErr
}

// ioGrace is how long a unit's output may stay open after its test process has ended: a process the tests left
// behind holds the pipe open, and the unit is done all the same.
const ioGrace = 5 * time.Second

// runUnit runs one unit's tests and gives every event line but the package's own result to write. It reports whether
// the tests passed: the test process's own exit status says so.
//
// The test binary is started here and not by test2json, which only converts what the binary prints: test2json ignores
// SIGINT and SIGQUIT, an ignored signal is inherited, and a test that sends its child SIGINT would find it ignored.
func runUnit(ctx context.Context, root string, u unit, binary string, write func(line []byte)) (bool, error) {
	fromTest, toConvert, err := os.Pipe()
	if err != nil {
		return false, err
	}
	test := exec.CommandContext(ctx, binary, "-test.v=test2json", "-test.paniconexit0", "-test.timeout="+unitTimeout, "-test.run", u.Run) //nolint:gosec // G204: a binary this command built and a pattern of test names from this checkout.
	test.Dir = filepath.Join(root, filepath.FromSlash(u.Pkg.Rel))
	test.Stdout, test.Stderr = toConvert, toConvert
	convert := exec.CommandContext(ctx, "go", "tool", "test2json", "-t", "-p", u.Pkg.Path) //nolint:gosec // G204: the program is literal go; the arguments are fixed flags and an import path go list gave.
	convert.Stderr = os.Stderr
	events, err := convert.StdoutPipe()
	if err == nil {
		var input io.WriteCloser
		if input, err = convert.StdinPipe(); err == nil {
			err = convert.Start()
			if err == nil {
				return runConverted(test, convert, fromTest, toConvert, input, events, write)
			}
		}
	}
	return false, errors.Join(err, fromTest.Close(), toConvert.Close())
}

// runConverted starts the test, copies what it prints to the converter and the converter's events to write.
func runConverted(test, convert *exec.Cmd, fromTest, toConvert *os.File, input io.WriteCloser, events io.Reader, write func(line []byte)) (bool, error) {
	startErr := test.Start()
	// The test holds its own copy of the pipe's writing end; this process's copy would keep the reading end open.
	closeErr := toConvert.Close()
	copied := make(chan error, 1)
	go func() {
		_, err := io.Copy(input, fromTest)
		copied <- errors.Join(err, input.Close())
	}()
	read := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(events)
		for {
			line, err := reader.ReadBytes('\n')
			if len(line) > 0 && !isPackageResult(line) {
				write(line)
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					err = nil
				}
				read <- err
				return
			}
		}
	}()

	var testErr error
	if startErr == nil {
		testErr = test.Wait()
	}
	select {
	case <-copied:
	case <-time.After(ioGrace):
	}
	// Closing the reading end ends the copy when a process the tests left behind still holds the pipe.
	closeErr = errors.Join(closeErr, fromTest.Close())
	readErr := <-read
	convertErr := convert.Wait()
	if startErr != nil {
		return false, errors.Join(startErr, closeErr)
	}
	if _, exited := errors.AsType[*exec.ExitError](testErr); testErr != nil && !exited {
		return false, testErr
	}
	return testErr == nil, errors.Join(readErr, convertErr)
}

// isPackageResult reports whether an event line is the package's own result or last line: each unit of a package
// prints one, and the queue writes the package's result once.
func isPackageResult(line []byte) bool {
	var event testEvent
	if json.Unmarshal(line, &event) != nil || event.Test != "" {
		return false
	}
	switch event.Action {
	case "pass", "fail", "skip", "start":
		return true
	case "output":
		return event.Output == "PASS\n" || event.Output == "FAIL\n"
	}
	return false
}

// queue builds the test binaries the units need and runs the units.
func queue(ctx context.Context, root string, units []unit, workers int, binDir string, stdout, stderr, report io.Writer) error {
	var withUnits []pkg
	seen := map[string]bool{}
	tests, alone := 0, 0
	for _, u := range units {
		if !seen[u.Pkg.Path] {
			seen[u.Pkg.Path] = true
			withUnits = append(withUnits, u.Pkg)
		}
		tests += u.Tests
		if u.Tests == 1 {
			alone++
		}
	}
	slices.SortFunc(withUnits, func(a, b pkg) int { return cmp.Compare(a.Path, b.Path) })
	if binDir == "" {
		dir, err := os.MkdirTemp("", "light-queue-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir) //nolint:errcheck // a leftover temporary directory is not this command's failure
		binDir = dir
	}

	started := time.Now()
	binaries, err := buildBinaries(ctx, withUnits, binDir, stderr)
	if err != nil {
		return err
	}
	built := time.Since(started)
	var lines strings.Builder
	fmt.Fprintf(&lines, "queue: built %d test binaries in %.0f s; %d tests in %d units (%d tests alone), %d workers\n",
		len(binaries), built.Seconds(), tests, len(units), alone, workers)

	started = time.Now()
	result, err := runQueue(ctx, root, units, binaries, workers, stdout)
	if err != nil {
		return err
	}
	ran := time.Since(started)
	fmt.Fprintf(&lines, "queue: the tests took %.0f s; the workers were busy %.0f%% of that time\n",
		ran.Seconds(), 100*result.busy.Seconds()/(ran.Seconds()*float64(max(workers, 1))))
	for _, d := range result.longest[:min(5, len(result.longest))] {
		fmt.Fprintf(&lines, "queue: %6.1f s  %s  %d tests\n", d.seconds, d.unit.Pkg.Rel, d.unit.Tests)
	}
	_, err = io.WriteString(report, lines.String())
	if len(result.failed) > 0 {
		return fmt.Errorf("tests failed in %s", strings.Join(result.failed, ", "))
	}
	return err
}
