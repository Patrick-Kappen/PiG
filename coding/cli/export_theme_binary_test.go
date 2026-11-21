package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Pi 0.99.2 main.ts:633 runs `--export` before initTheme (main.ts:898), so theme.ts getResolvedThemeColors loads the
// system theme (`currentThemeName ?? SYSTEM_THEME_NAME`, theme.ts:904) and export-html/index.ts:112-125 derives the page
// colors from its userMessageBg. The expected lines are what `pi --export` 0.99.2 writes. Neither the terminal
// color-mode environment nor the plain fixture changes them: the export reads Color values, not ANSI escapes.
func TestExportCLIUsesTheSystemThemeColors(t *testing.T) {
	bin := buildPigBinaryForSignalTest(t)
	for _, colorterm := range []string{"truecolor", "24bit", ""} {
		t.Run("COLORTERM="+colorterm, func(t *testing.T) {
			t.Setenv("COLORTERM", colorterm)
			dir := t.TempDir()
			session := filepath.Join(dir, "s.jsonl")
			header := `{"type":"session","version":3,"id":"s1","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/tmp"}` + "\n"
			if err := os.WriteFile(session, []byte(header), 0o600); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(dir, "out.html")
			_, stderr, code := runPigForModeTest(t, bin, nil, nil, "--export", session, out)
			if code != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr)
			}
			html, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{
				"--accent: #800080;", "--borderAccent: #800080;", "--border: #000080;", "--success: #008000;",
				"--error: #800000;", "--warning: #808000;", "--userMessageBg: #000000;",
				"--exportPageBg: rgb(0, 0, 0);", "--exportCardBg: rgb(0, 0, 0);", "--exportInfoBg: rgb(20, 15, 0);",
			} {
				if !strings.Contains(string(html), want) {
					t.Errorf("export is missing %q", want)
				}
			}
		})
	}
}
