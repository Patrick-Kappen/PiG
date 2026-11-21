package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/MichaelKinsy/PiG/internal/coding/pigversion"
)

var _ = Describe("print mode", startsPig, func() {
	// Upstream main.ts passes every positional message after the first to print mode, and print-mode.ts sends each
	// as its own prompt after the initial message. Pig sent only the first and exited 0.
	Describe("with several positional messages", func() {
		It("prints the last prompt's answer in text mode", within, func(ctx SpecContext) {
			got := run(fauxPig(ctx, GinkgoT().TempDir(), "-p", "reply with exactly: first", "reply with exactly: second"))
			Expect(got).To(And(HaveField("Code", 0), HaveField("Stdout", "second\n")))
		})

		It("sends each as a user message in json mode", within, func(ctx SpecContext) {
			got := run(fauxPig(ctx, GinkgoT().TempDir(), "--mode", "json", "reply with exactly: first", "reply with exactly: second"))
			Expect(got.Code).To(Equal(0), got.Stderr)
			var prompts []string
			for _, line := range jsonLines(got.Stdout) {
				message, _ := line["message"].(map[string]any)
				if line["type"] != "message_end" || message["role"] != "user" {
					continue
				}
				content, _ := message["content"].([]any)
				for _, block := range content {
					if block, ok := block.(map[string]any); ok && block["type"] == "text" {
						prompts = append(prompts, block["text"].(string))
					}
				}
			}
			Expect(prompts).To(Equal([]string{"reply with exactly: first", "reply with exactly: second"}))
		})

		// Upstream json mode with no message writes the session header and sends nothing; pig refused to start.
		It("writes only the session header in json mode without a message", within, func(ctx SpecContext) {
			got := run(fauxPig(ctx, GinkgoT().TempDir(), "--mode", "json"))
			Expect(got.Code).To(Equal(0), got.Stderr)
			Expect(jsonLines(got.Stdout)).To(ConsistOf(HaveKeyWithValue("type", "session")))
		})
	})

	// Upstream print mode binds the session to extensions: a prompt an agent_settled handler sends runs before the
	// initial prompt resolves, so the printed answer is that run's. Pig bound no session actions in print mode, so
	// sendUserMessage failed and the first answer was printed.
	It("waits for the prompt an extension sends from agent_settled", startsNode, within, func(ctx SpecContext) {
		fixture, err := filepath.Abs(filepath.Join("..", "testdata", "session-actions.mjs"))
		Expect(err).NotTo(HaveOccurred())
		home := GinkgoT().TempDir()
		cmd := exec.CommandContext(ctx, pigBinary, "--model", "test-faux/faux-1", "-e", fixture, "-p", "reply with exactly: loop-start")
		cmd.Dir = GinkgoT().TempDir()
		cmd.Env = append(os.Environ(), "PIG_HOME="+home, "PIG_CODING_AGENT_DIR="+filepath.Join(home, "agent"), "PIG_TEST_FAUX=1", "PIG_TEST_FAUX_SCENARIO=parity-basic")
		Expect(run(cmd)).To(And(HaveField("Code", 0), HaveField("Stdout", "loop-done\n")))
	})
})

var _ = Describe("the order of pig's startup checks", startsPig, func() {
	// pig --fork <id> --no-session exits 1 with Pi's error before any session work (main.ts validateForkFlags).
	It("rejects --fork with --no-session with Pi's error", within, func(ctx SpecContext) {
		got := run(fauxPig(ctx, GinkgoT().TempDir(), "-p", "--fork", "abc", "--no-session", "hello"))
		Expect(got).To(Equal(result{Code: 1, Stderr: "Error: --fork cannot be combined with --no-session\n"}))
	})

	// main.ts validateSessionIdFlags returns early only when --session-id is absent; an explicitly empty id fails
	// assertValidSessionId, so Pi 1.1.0 'pi --session-id "" -p hi' exits 1 with this error before any model or session
	// work.
	It("rejects an empty --session-id with Pi's error", within, func(ctx SpecContext) {
		got := run(fauxPig(ctx, GinkgoT().TempDir(), "--session-id", "", "-p", "hi"))
		Expect(got).To(Equal(result{Code: 1, Stderr: "Error: Session id must be non-empty, contain only alphanumeric characters, '-', '_', and '.', and start and end with an alphanumeric character\n"}))
	})

	// Pi prints the version before validateForkFlags runs, so a conflicting --fork does not stop --version.
	It("prints the version before it checks the fork flags", within, func(ctx SpecContext) {
		got := run(fauxPig(ctx, GinkgoT().TempDir(), "--version", "--fork", "x", "--no-session"))
		Expect(got).To(And(HaveField("Code", 0), HaveField("Stderr", BeEmpty()), HaveField("Stdout", WithTransform(strings.TrimSpace, Equal(pigversion.Version)))))
	})

	// Pi exports before validateForkFlags runs, so a conflicting --fork does not stop --export. main.ts:647
	// console.log: the status line is stdout.
	It("exports before it checks the fork flags", within, func(ctx SpecContext) {
		dir := GinkgoT().TempDir()
		session := filepath.Join(dir, "s.jsonl")
		header := `{"type":"session","version":3,"id":"s1","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/tmp"}` + "\n"
		Expect(os.WriteFile(session, []byte(header), 0o600)).To(Succeed())
		got := run(fauxPig(ctx, GinkgoT().TempDir(), "--export", session, filepath.Join(dir, "out.html"), "--fork", "x", "--no-session"))
		Expect(got).To(And(HaveField("Code", 0), HaveField("Stderr", BeEmpty()), HaveField("Stdout", HavePrefix("Exported to: "))))
	})
})
