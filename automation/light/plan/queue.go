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

// The queue is the light run's first stage on one runner: the tests that start no program, built once and then run
// from one queue, the longest first, by as many workers as the runner has processors.
//
//	go run ./automation/light/plan -queue -workers 4 -bin tmp/light/bin
//
// A worker takes the next unit when it is free, so no processor waits while a test is left, and a run that is wrong
// about a test's seconds is only a little less even. It prints the tests' events as go test -json does, for gotestsum,
// and says in -report how long the build and the tests took and how busy the workers were.

const (
	// minAloneSeconds is the measured time from which a test is a unit of its own. Shorter tests of a package run
	// together in one process: starting a test binary takes milliseconds, but a package's tests share what its
	// TestMain and its first tests set up.
	minAloneSeconds = 1
	// secondsPerShortTest is what a test without measured seconds counts for when the units are put in order.
	secondsPerShortTest = 0.01
	// unitTimeout ends a unit that hangs. No unit of this stage comes near it.
	unitTimeout = "10m"
)

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
func parsePrograms(r io.Reader, name string) (programs, error) {
	list := programs{}
	scanner := bufio.NewScanner(r)
	for line := 1; scanner.Scan(); line++ {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		dir, test, ok := strings.Cut(fields[len(fields)-1], ":")
		if len(fields) != 2 || !ok || dir == "" || test == "" {
			return nil, fmt.Errorf("%s:%d: want what the test starts and package:Test, got %q", name, line, scanner.Text())
		}
		list[heavyTest{dir, test}] = fields[0]
	}
	return list, scanner.Err()
}

// queueUnits returns the units of the packages' tests that start no program, the longest first.
//
// A test with a second or more in the seconds file is a unit of its own. A package's other tests are one unit, or
// more when their names do not fit one pattern. A test that neither file names is one of those other tests: a new test
// runs here until a measurement says it starts a program.
func queueUnits(pkgs []pkg, heavy heavyList, measured timings, starts programs) []unit {
	var units []unit
	for _, p := range pkgs {
		var together []string
		for _, name := range p.Tests {
			test := heavyTest{p.Rel, name}
			if slices.Contains(heavy.tests, test) {
				continue
			}
			if _, startsOne := starts[test]; startsOne {
				continue
			}
			if seconds := measured.tests[test]; seconds >= minAloneSeconds {
				units = append(units, unit{Pkg: p, Run: "^" + regexp.QuoteMeta(name) + "$", Tests: 1, Seconds: seconds})
				continue
			}
			together = append(together, name)
		}
		slices.Sort(together)
		for len(together) > 0 {
			n, size := 0, len("^()$")
			for n < len(together) && (n == 0 || size+len(regexp.QuoteMeta(together[n]))+1 <= maxPatternBytes) {
				size += len(regexp.QuoteMeta(together[n])) + 1
				n++
			}
			quoted := make([]string, n)
			seconds := 0.0
			for i, name := range together[:n] {
				quoted[i] = regexp.QuoteMeta(name)
				seconds += max(measured.tests[heavyTest{p.Rel, name}], secondsPerShortTest)
			}
			units = append(units, unit{Pkg: p, Run: "^(" + strings.Join(quoted, "|") + ")$", Tests: n, Seconds: seconds})
			together = together[n:]
		}
	}
	slices.SortStableFunc(units, func(a, b unit) int {
		return cmp.Or(cmp.Compare(b.Seconds, a.Seconds), cmp.Compare(a.Pkg.Path, b.Pkg.Path), cmp.Compare(a.Run, b.Run))
	})
	return units
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

// runUnit runs one unit's tests and gives every event line but the package's own result to write. It reports whether
// the tests passed.
func runUnit(ctx context.Context, root string, u unit, binary string, write func(line []byte)) (bool, error) {
	// test2json starts the test binary, turns what it prints into events and ends with the package's result.
	cmd := exec.CommandContext(ctx, "go", "tool", "test2json", "-t", "-p", u.Pkg.Path, //nolint:gosec // G204: the program is literal go; the arguments are fixed flags, an import path go list gave, a binary this command built and a pattern of test names from this checkout.
		binary, "-test.v=test2json", "-test.paniconexit0", "-test.timeout="+unitTimeout, "-test.run", u.Run)
	cmd.Dir = filepath.Join(root, filepath.FromSlash(u.Pkg.Rel))
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, err
	}
	if err := cmd.Start(); err != nil {
		return false, err
	}
	passed := false
	reader := bufio.NewReader(stdout)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			var event testEvent
			if json.Unmarshal(line, &event) == nil && event.Test == "" {
				switch event.Action {
				case "pass":
					passed = true
					continue
				case "fail", "skip", "start":
					continue
				case "output":
					// Each unit prints the package's last line; the package's result says it once.
					if event.Output == "PASS\n" || event.Output == "FAIL\n" {
						continue
					}
				}
			}
			write(line)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return false, errors.Join(err, cmd.Wait())
			}
			break
		}
	}
	// A test that fails makes test2json end with a status; the events already say which test.
	if err := cmd.Wait(); err != nil {
		if _, isExit := errors.AsType[*exec.ExitError](err); !isExit {
			return false, err
		}
		return false, nil
	}
	return passed, nil
}

// queue builds and runs the first stage.
func queue(ctx context.Context, root string, light []pkg, heavy heavyList, measured timings, starts programs, workers int, binDir string, stdout, stderr, report io.Writer) error {
	units := queueUnits(light, heavy, measured, starts)
	var withUnits []pkg
	seen := map[string]bool{}
	tests, alone := 0, 0
	for _, u := range units {
		if !seen[u.Pkg.Path] {
			seen[u.Pkg.Path] = true
			withUnits = append(withUnits, u.Pkg)
		}
		tests += u.Tests
		if u.Tests == 1 && u.Seconds >= minAloneSeconds {
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
