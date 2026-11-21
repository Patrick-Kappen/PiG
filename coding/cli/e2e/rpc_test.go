package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("rpc mode", startsPig, func() {
	// agent-session.ts abort ends with waitForIdle, which returns at once when no agent run is active at that point
	// (isIdle). A prompt read just before it is still in its preflight, so Pi 1.1.0 answers the abort first and lets the
	// prompt run: for `prompt` then `abort` in one input batch it writes the abort response, then the prompt response
	// (stdin open or closed). PiG joined every admitted prompt before answering, so the abort answered after the run,
	// or not at all when stdin ended.
	It("answers an abort that finds no run before the prompt read just before it", within, func(ctx SpecContext) {
		cmd := fauxPig(ctx, GinkgoT().TempDir(), "--mode", "rpc", "--no-session", "--no-extensions", "--offline", "--model", "test-faux/faux-1")
		cmd.Stdin = stdinFile(`{"type":"prompt","id":"p","message":"hello"}` + "\n" + `{"type":"abort","id":"a"}` + "\n")
		got := run(cmd)
		Expect(got.Code).To(Equal(0), got.Stderr)
		var order []string
		for line := range strings.SplitSeq(strings.TrimSpace(got.Stdout), "\n") {
			var record struct {
				Type string `json:"type"`
				ID   string `json:"id"`
			}
			if json.Unmarshal([]byte(line), &record) == nil && record.Type == "response" {
				order = append(order, record.ID)
			}
		}
		Expect(len(order)).To(BeNumerically(">=", 2), "responses %q in\n%s", order, got.Stdout)
		Expect(order[:2]).To(Equal([]string{"a", "p"}), "the abort is answered before the prompt")
	})

	// Pi 0.99.2 RPC export_html (rpc-mode.ts:598-600) exports with the settings theme when getThemeByName loads it, else
	// with the theme main.ts:898 initTheme selected (agent-session.ts:4212-4227, theme.ts:551-571, 631-640, 903-906).
	// Both resolve a name to the system theme, a built-in theme, or `<name>.json` in the agent's themes directory by
	// file name. The expected values are what `pi --mode rpc --session s.jsonl` 0.99.2 writes for the same settings
	// and theme files.
	DescribeTable("exports with the configured theme",
		func(ctx SpecContext, settings, accent, pageBg string) {
			agentDir := GinkgoT().TempDir()
			writeCustomTheme(agentDir, "file-name", "json-name", "#123456")
			writeCustomTheme(agentDir, "dark", "dark", "#abcdef")
			Expect(os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(settings), 0o600)).To(Succeed())
			// Pi moves a .jsonl file in the agent directory's root into sessions/ at startup, so the session lives
			// elsewhere. Pi main.ts:694-704 exits 1 when the stored session cwd does not exist, so the session records its
			// own directory.
			sessionDir := GinkgoT().TempDir()
			session := filepath.Join(sessionDir, "s.jsonl")
			sessionCwd, err := json.Marshal(sessionDir)
			Expect(err).NotTo(HaveOccurred())
			lines := `{"type":"session","version":3,"id":"s1","timestamp":"2026-01-01T00:00:00.000Z","cwd":` + string(sessionCwd) + `}` + "\n" +
				`{"type":"message","id":"m1","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","content":"hi","timestamp":1}}` + "\n"
			Expect(os.WriteFile(session, []byte(lines), 0o600)).To(Succeed())

			cmd := fauxPig(ctx, agentDir, "--mode", "rpc", "--offline", "--session", session)
			cmd.Stdin = stdinFile(`{"id":"1","type":"export_html","outputPath":"out.html"}` + "\n")
			got := run(cmd)
			Expect(got).To(And(HaveField("Code", 0), HaveField("Stdout", ContainSubstring(`"success":true`))))
			html, err := os.ReadFile(filepath.Join(agentDir, "work", "out.html"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(html)).To(And(ContainSubstring("--accent: "+accent+";"), ContainSubstring("--exportPageBg: "+pageBg+";")))
		},
		Entry("no theme set", within, `{}`, "#800080", "rgb(0, 0, 0)"),
		Entry("a custom theme by its file name", within, `{"theme":"file-name"}`, "#123456", "#21252c"),
		Entry("a JSON name does not select its file", within, `{"theme":"json-name"}`, "#800080", "rgb(0, 0, 0)"),
		Entry("a custom file does not replace a built-in theme", within, `{"theme":"dark"}`, "#a798d7", "#21252c"),
	)
})

// writeCustomTheme writes the built-in dark theme under another name and accent into the agent's themes directory.
func writeCustomTheme(agentDir, file, name, accent string) {
	GinkgoHelper()
	darkJSON, err := os.ReadFile(filepath.Join("..", "..", "..", "tui", "theme_dark.json"))
	Expect(err).NotTo(HaveOccurred())
	var theme map[string]any
	Expect(json.Unmarshal(darkJSON, &theme)).To(Succeed())
	theme["name"] = name
	theme["colors"].(map[string]any)["accent"] = accent
	data, err := json.Marshal(theme)
	Expect(err).NotTo(HaveOccurred())
	Expect(os.MkdirAll(filepath.Join(agentDir, "themes"), 0o700)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(agentDir, "themes", file+".json"), data, 0o600)).To(Succeed())
}
