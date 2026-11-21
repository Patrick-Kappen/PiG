//go:build linux

package e2e

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
	"golang.org/x/sys/unix"

	"github.com/MichaelKinsy/PiG/ai"
)

// Upstream main.ts selects the mode from the standard streams: a stdout that is not a terminal, or a stdin that is
// not a terminal, selects print mode even when the other stream is a terminal.
var _ = Describe("pig's mode follows the standard streams", startsPig, func() {
	// `pig "q" > out.txt` from a terminal: stdin is a terminal, stdout a file.
	It("prints when stdout is redirected to a file and stdin is a terminal", needsPTY, within, func(ctx SpecContext) {
		master, slave := openPTY()
		go func() { _, _ = io.Copy(io.Discard, master) }()
		outPath := filepath.Join(GinkgoT().TempDir(), "out.txt")
		out, err := os.Create(outPath)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(out.Close)
		cmd := fauxPig(ctx, GinkgoT().TempDir(), "reply with exactly: redirected-ok")
		cmd.Stdin, cmd.Stdout = slave, out
		Expect(run(cmd).Code).To(Equal(0))
		Expect(os.ReadFile(outPath)).To(BeEquivalentTo("redirected-ok\n"))
	})
})

// Pi 0.87.1 print-mode.ts:49-64 handles SIGTERM and SIGHUP with numeric 128+signum exits and leaves SIGINT to the
// default signal action. The wait status tells that action from a normal exit with code 130.
var _ = Describe("print mode ends on a signal as Pi does", startsPig, func() {
	DescribeTable("while a tool call runs",
		func(ctx SpecContext, signal syscall.Signal, code int, killedBySignal bool) {
			workDir := GinkgoT().TempDir()
			cmd := exec.Command(pigBinary, "--model", "test-faux/faux-1", "--no-extensions", "--print", "Run: sleep for a while")
			cmd.Dir = workDir
			cmd.Env = append(os.Environ(), "PIG_HOME="+GinkgoT().TempDir(), "PIG_TEST_FAUX=1", "PIG_TEST_FAUX_SCENARIO=parity-basic")
			// Its own process group, so the signal reaches only this run.
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			session, err := gexec.Start(cmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			// SIGINT takes the default action, as in Pi: pig dies at once, and the faux call's shell, which runs in its
			// own process group, keeps sleeping. What is left in the working directory is killed when the spec ends.
			DeferCleanup(func() {
				session.Kill()
				killProcessesIn(workDir)
			})

			// The faux tool is running, so the signal lands in a run and not in the startup.
			Eventually(filepath.Join(workDir, ai.TestFauxToolStartedMarker)).WithContext(ctx).Should(BeAnExistingFile())
			session.Signal(signal)
			Eventually(session).WithContext(ctx).Should(gexec.Exit(code))
			status := session.Command.ProcessState.Sys().(syscall.WaitStatus)
			Expect(status.Signaled()).To(Equal(killedBySignal), "wait status %v", status)
		},
		Entry("SIGTERM exits 143", within, syscall.SIGTERM, 143, false),
		Entry("SIGHUP exits 129", within, syscall.SIGHUP, 129, false),
		Entry("SIGINT kills it, as the default action does", within, syscall.SIGINT, 130, true),
	)
})

// Whether `node -p "require('os').homedir()"` throws without HOME on this host decides whether the run fails (with
// Node's message, writing nothing) or succeeds, so the host's user database cannot change the verdict.
var _ = Describe("startup where no home directory exists", startsPig, startsNode, func() {
	// What Node's os.homedir() throws where no HOME is set and the user database has no entry for the process's user.
	const message = "A system error occurred: uv_os_homedir returned ENOENT (no such file or directory)"

	It("follows Node", within, func(ctx SpecContext) {
		root := GinkgoT().TempDir()
		var environment []string
		for _, kv := range os.Environ() {
			name, _, _ := strings.Cut(kv, "=")
			switch strings.ToUpper(name) {
			case "HOME", "USERPROFILE", "PIG_HOME", "PIG_CODING_AGENT_DIR", "XDG_CONFIG_HOME":
			default:
				environment = append(environment, kv)
			}
		}
		environment = append(environment, "PIG_HOME="+filepath.Join(root, "pighome"), "PIG_CODING_AGENT_DIR="+filepath.Join(root, "agent"), "PIG_TEST_FAUX=1", "PIG_TEST_FAUX_SCENARIO=parity-basic", "PIG_OFFLINE=1")

		probe := exec.CommandContext(ctx, "node", "-p", `require('node:os').homedir()`)
		probe.Env = environment
		_, probeErr := probe.Output()
		probeExit, nodeThrows := errors.AsType[*exec.ExitError](probeErr)
		if nodeThrows {
			Expect(string(probeExit.Stderr)).To(ContainSubstring("uv_os_homedir"), "node failed for another reason")
		} else {
			Expect(probeErr).NotTo(HaveOccurred())
		}

		cmd := exec.CommandContext(ctx, pigBinary, "--model", "test-faux/faux-1", "--no-extensions", "-p", "reply with exactly: ok")
		cmd.Env, cmd.Dir = environment, GinkgoT().TempDir()
		got := run(cmd)
		if !nodeThrows {
			Expect(got).To(And(HaveField("Code", 0), HaveField("Stdout", ContainSubstring("ok"))), "node finds a home directory here, so pig must start")
			return
		}
		Expect(got).To(And(HaveField("Code", 1), HaveField("Stderr", ContainSubstring(message)), HaveField("Stdout", BeEmpty())), "node throws here, so pig must exit 1")
	})
})

// openPTY allocates a pseudo-terminal pair of 24 rows by 80 columns, closed when the spec ends.
func openPTY() (master, slave *os.File) {
	GinkgoHelper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(master.Close)
	Expect(unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0)).To(Succeed())
	number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	Expect(err).NotTo(HaveOccurred())
	slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(slave.Close)
	Expect(unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: 80})).To(Succeed())
	return master, slave
}

// killProcessesIn kills every process whose working directory is dir.
func killProcessesIn(dir string) {
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		if cwd, err := os.Readlink(filepath.Join("/proc", entry.Name(), "cwd")); err == nil && cwd == want {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}
