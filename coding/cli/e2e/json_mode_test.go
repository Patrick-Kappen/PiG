package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Upstream print-mode.ts: text and json output are one function over one session, so --mode json honours the same
// tool selection, attachments and session name as -p. Pig's separate JSON runner dropped @image attachments,
// --exclude-tools, the default builtin tool set, and --name.
var _ = Describe("json mode uses print mode's session options", startsPig, func() {
	It("leaves out a tool that --exclude-tools names", within, func(ctx SpecContext) {
		got := run(fauxPig(ctx, GinkgoT().TempDir(), "--mode", "json", "-xt", "bash", "Run: expr 20 + 22"))
		Expect(got.Code).To(Equal(0), got.Stderr)
		Expect(toolExecutionEnd(jsonLines(got.Stdout), "bash")).To(HaveKeyWithValue("isError", true), "excluded bash ran")
	})

	It("leaves ls inactive: upstream's default tools are read, bash, edit, write", within, func(ctx SpecContext) {
		got := run(fauxPig(ctx, GinkgoT().TempDir(), "--mode", "json", "Run: ls here"))
		Expect(got.Code).To(Equal(0), got.Stderr)
		Expect(toolExecutionEnd(jsonLines(got.Stdout), "ls")).To(HaveKeyWithValue("isError", true))
	})

	It("passes an image attachment to the provider", within, func(ctx SpecContext) {
		var encoded bytes.Buffer
		img := image.NewRGBA(image.Rect(0, 0, 2, 2))
		img.Set(0, 0, color.RGBA{R: 255, A: 255})
		Expect(png.Encode(&encoded, img)).To(Succeed())
		imagePath := filepath.Join(GinkgoT().TempDir(), "sample.png")
		Expect(os.WriteFile(imagePath, encoded.Bytes(), 0o600)).To(Succeed())

		got := run(fauxPig(ctx, GinkgoT().TempDir(), "--mode", "json", "@"+imagePath, "describe sample image"))
		Expect(got.Code).To(Equal(0), got.Stderr)
		Expect(finalAssistantText(jsonLines(got.Stdout))).To(Equal("cli-image-mime-ok"), "the faux provider answers this when it saw the image")
	})

	It("persists the session's --name", within, func(ctx SpecContext) {
		agentDir := GinkgoT().TempDir()
		got := run(fauxPig(ctx, agentDir, "--mode", "json", "--name", "json-named", "reply with exactly: named"))
		Expect(got.Code).To(Equal(0), got.Stderr)
		lines := jsonLines(got.Stdout)
		Expect(lines).NotTo(BeEmpty())
		Expect(lines[0]).To(HaveKeyWithValue("type", "session"), "line 1 is the session header")
		Expect(sessionInfoName(agentDir, lines[0]["id"].(string))).To(Equal("json-named"))
	})
})

// Pi's sdk.ts:258-265 gives an explicit --tools list precedence over --no-tools and --no-builtin-tools:
// allowedToolNames = tools ?? (noTools === "all" ? [] : undefined) and initialActiveToolNames = tools ?? (noTools ? []
// : defaults), then the denylist is subtracted. Measured against the exact Pi 0.87.1 oracle in --mode json with the
// faux provider (bash, read and write prompts).
var _ = Describe("an explicit --tools list in json mode", startsPig, func() {
	prompts := map[string]string{"bash": "Run: expr 20 + 22", "read": "Run: read parity-read-target.txt"}
	var entries []TableEntry
	for _, flags := range []struct {
		name string
		args []string
		runs map[string]bool
	}{
		{"wins over --no-tools", []string{"--no-tools", "--tools", "bash"}, map[string]bool{"bash": true, "read": false}},
		{"wins over --no-builtin-tools", []string{"--no-builtin-tools", "--tools", "read"}, map[string]bool{"bash": false, "read": true}},
		{"loses what --exclude-tools names", []string{"--tools", "bash,read", "--exclude-tools", "bash"}, map[string]bool{"bash": false, "read": true}},
	} {
		for _, tool := range []string{"bash", "read"} {
			entries = append(entries, Entry(fmt.Sprintf("%s: %s runs = %t", flags.name, tool, flags.runs[tool]), within, flags.args, tool, flags.runs[tool]))
		}
	}
	DescribeTable("decides which tools run",
		func(ctx SpecContext, flags []string, tool string, runs bool) {
			args := append([]string{"--mode", "json"}, flags...)
			got := run(fauxPig(ctx, GinkgoT().TempDir(), append(args, prompts[tool])...))
			Expect(got.Code).To(Equal(0), got.Stderr)
			end := toolExecutionEnd(jsonLines(got.Stdout), tool)
			notFound := ContainSubstring("Tool " + tool + " not found")
			if runs {
				notFound = Not(notFound)
			}
			Expect(fmt.Sprint(end["result"])).To(notFound, "%v", end)
		},
		entries,
	)
})

// finalAssistantText returns the text of the last assistant message_end.
func finalAssistantText(lines []map[string]any) string {
	text := ""
	for _, line := range lines {
		if line["type"] != "message_end" {
			continue
		}
		message, _ := line["message"].(map[string]any)
		if message["role"] != "assistant" {
			continue
		}
		text = ""
		content, _ := message["content"].([]any)
		for _, block := range content {
			if block, ok := block.(map[string]any); ok && block["type"] == "text" {
				text += block["text"].(string)
			}
		}
	}
	return text
}

// sessionInfoName returns the name the session's file records in its last session_info entry.
func sessionInfoName(agentDir, sessionID string) string {
	GinkgoHelper()
	files, err := filepath.Glob(filepath.Join(agentDir, "sessions", "*", "*"+sessionID+".jsonl"))
	Expect(err).NotTo(HaveOccurred())
	Expect(files).To(HaveLen(1), "the session file of %s", sessionID)
	data, err := os.ReadFile(files[0])
	Expect(err).NotTo(HaveOccurred())
	name := ""
	for line := range strings.Lines(string(data)) {
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) == nil && entry["type"] == "session_info" {
			name, _ = entry["name"].(string)
		}
	}
	return name
}
