package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/internal/testbudget"
	"github.com/MichaelKinsy/PiG/internal/testenv"
)

// runPigForModeTest runs the pig binary with the faux provider and returns its
// stdout, stderr, and exit code. stdin and stdout are the given files; a nil
// stdout captures into a pipe. A nil stdin is the null device.
func runPigForModeTest(t *testing.T, bin string, stdin, stdout *os.File, args ...string) (string, string, int) {
	t.Helper()
	return runPigForModeTestIn(t, bin, t.TempDir(), stdin, stdout, args...)
}

// runPigForModeTestIn is runPigForModeTest with the agent directory, which
// holds the run's session files, chosen by the caller. Runs sharing an agent
// directory also share a working directory, so --continue finds the session.
func runPigForModeTestIn(t *testing.T, bin, agentDir string, stdin, stdout *os.File, args ...string) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testbudget.Wait(t))
	defer cancel()
	cmd := modePigCommand(ctx, t, bin, agentDir, args...)
	// A nil *os.File stored in cmd.Stdin is not a nil interface: Windows rejects its invalid handle, where a nil interface gets the null device.
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var outBuf, errBuf bytes.Buffer
	if stdout != nil {
		cmd.Stdout = stdout
	} else {
		cmd.Stdout = &outBuf
	}
	cmd.Stderr = &errBuf
	err := testenv.Run(t, cmd)
	if ctx.Err() != nil {
		t.Fatalf("pig %v did not exit (interactive mode waiting for keys?)\nstdout: %q\nstderr: %s", args, outBuf.String(), errBuf.String())
	}
	code := 0
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run pig: %v", err)
	}
	return outBuf.String(), errBuf.String(), code
}

// modePigCommand builds the pig command that runPigForModeTestIn runs: the
// faux provider, a private PIG_HOME, and the agent directory's work
// directory as the working directory.
func modePigCommand(ctx context.Context, t *testing.T, bin, agentDir string, args ...string) *exec.Cmd {
	t.Helper()
	workDir := filepath.Join(agentDir, "work")
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, bin, append([]string{"--model", "test-faux/faux-1", "--no-extensions"}, args...)...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"PIG_HOME="+t.TempDir(),
		"PIG_CODING_AGENT_DIR="+agentDir,
		"PIG_TEST_FAUX=1",
		"PIG_TEST_FAUX_SCENARIO=parity-basic",
	)
	return cmd
}

// TestAppModeFollowsStandardStreams runs the pig binary the way a shell does
// and pins upstream main.ts mode selection end to end: a stdout that is not a
// terminal, or a stdin that is not a terminal, selects print mode even when
// the other stream is a terminal.
func TestAppModeFollowsStandardStreams(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the pig binary")
	}
	bin := buildPigBinaryForSignalTest(t)

	// `pig "q" > out.txt` from a terminal: stdin is a terminal, stdout a file.
	t.Run("stdout redirected to a file", func(t *testing.T) {
		outPath := filepath.Join(t.TempDir(), "out.txt")
		stderr, code := runPigOnTerminalStdinToFile(t, bin, outPath, "reply with exactly: redirected-ok")
		data, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatal(err)
		}
		if code != 0 || string(data) != "redirected-ok\n" {
			t.Fatalf("exit %d, file %q, stderr %s; want print mode output %q", code, data, stderr, "redirected-ok\n")
		}
	})

	// `pig < /dev/null`: no message, stdin not a terminal. Upstream runs print
	// mode, sends nothing, and exits 0.
	t.Run("stdin not a terminal without a message", func(t *testing.T) {
		devNull, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = devNull.Close() }()
		stdout, stderr, code := runPigForModeTest(t, bin, devNull, nil)
		if code != 0 || strings.TrimSpace(stdout) != "" {
			t.Fatalf("exit %d, stdout %q, stderr %s; want exit 0 and no output", code, stdout, stderr)
		}
	})
}

// TestPrintModeWithoutMessageReportsLastMessage ports upstream
// print-mode.test.ts "returns non-zero on assistant error": text mode reports
// the session's last message even when no prompt is sent, so `pig -c -p` on a
// session that ended in an error prints that error and exits 1.
func TestPrintModeWithoutMessageReportsLastMessage(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the pig binary")
	}
	bin := buildPigBinaryForSignalTest(t)
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = devNull.Close() }()
	agentDir := t.TempDir()
	const failure = `test-faux: unhandled request "no scripted answer"`
	_, stderr, code := runPigForModeTestIn(t, bin, agentDir, devNull, nil, "-p", "no scripted answer")
	if code != 1 || strings.TrimSpace(stderr) != failure {
		t.Fatalf("first run: exit %d, stderr %q; want exit 1 with %q", code, stderr, failure)
	}
	stdout, stderr, code := runPigForModeTestIn(t, bin, agentDir, devNull, nil, "-c", "-p")
	if code != 1 || strings.TrimSpace(stderr) != failure || stdout != "" {
		t.Fatalf("no-message run: exit %d, stdout %q, stderr %q; want exit 1 with %q", code, stdout, stderr, failure)
	}
}
