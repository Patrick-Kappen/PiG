// Package e2e is a trial of a Ginkgo suite for the tests of coding/cli that run the pig binary and use none of the
// package's unexported code. A spec carries a label for every kind of program it starts, so a run selects by label:
//
//	ginkgo -p --label-filter='pig && !node' ./coding/cli/e2e
//
// The specs are the tests of app_mode_binary_test.go, print_signal_exit_test.go and no_home_directory_test.go that
// qualify, with the same commands and the same expected values.
package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
)

// What a spec starts. The names are the kinds of light/programs.txt, so the same names can label a pytest, a cargo or
// a vitest test.
var (
	startsPig  = Label("pig")
	startsNode = Label("node")
	needsPTY   = Label("pty")
)

// within bounds a spec; its context ends then, and with it every program the spec started with that context.
var within = SpecTimeout(2 * time.Minute)

var pigBinary string

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "coding/cli end to end")
}

// One process builds pig, once, with the flags of compilePigBinary; every process of the run gets its path.
var _ = SynchronizedBeforeSuite(func() []byte {
	path, err := gexec.Build("github.com/MichaelKinsy/PiG/cmd/pig", "-pgo=off")
	Expect(err).NotTo(HaveOccurred())
	return []byte(path)
}, func(path []byte) {
	pigBinary = string(path)
})

var _ = SynchronizedAfterSuite(func() {}, gexec.CleanupBuildArtifacts)

// result is what a pig run that ended left behind.
type result struct {
	Stdout, Stderr string
	Code           int
}

// fauxPig is the pig command with the faux provider, a config root of its own, and the agent directory's work
// directory as its working directory. Runs that share an agent directory share a session.
func fauxPig(ctx context.Context, agentDir string, args ...string) *exec.Cmd {
	GinkgoHelper()
	workDir := filepath.Join(agentDir, "work")
	Expect(os.MkdirAll(workDir, 0o700)).To(Succeed())
	cmd := exec.CommandContext(ctx, pigBinary, append([]string{"--model", "test-faux/faux-1", "--no-extensions"}, args...)...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"PIG_HOME="+GinkgoT().TempDir(),
		"PIG_CODING_AGENT_DIR="+agentDir,
		"PIG_TEST_FAUX=1",
		"PIG_TEST_FAUX_SCENARIO=parity-basic",
	)
	return cmd
}

// run runs the command to its end. A standard stream the caller set stays as it is; the others are captured.
func run(cmd *exec.Cmd) result {
	GinkgoHelper()
	var stdout, stderr bytes.Buffer
	if cmd.Stdout == nil {
		cmd.Stdout = &stdout
	}
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		code = exit.ExitCode()
	} else {
		Expect(err).NotTo(HaveOccurred(), "pig %v\nstdout: %s\nstderr: %s", cmd.Args[1:], &stdout, &stderr)
	}
	return result{Stdout: stdout.String(), Stderr: stderr.String(), Code: code}
}

// jsonLines reads json mode's output: one JSON object a line.
func jsonLines(stdout string) []map[string]any {
	GinkgoHelper()
	var lines []map[string]any
	scanner := bufio.NewScanner(strings.NewReader(stdout))
	scanner.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for scanner.Scan() {
		var line map[string]any
		Expect(json.Unmarshal(scanner.Bytes(), &line)).To(Succeed(), "stdout line is not JSON: %q", scanner.Text())
		lines = append(lines, line)
	}
	return lines
}
