package harness

import (
	"fmt"
	"os"
	"testing"

	"github.com/MichaelKinsy/PiG/internal/testenv"
)

func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "pig-harness-git-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := testenv.IsolateGitEnv(root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = os.RemoveAll(root)
		os.Exit(2)
	}
	code := testenv.RunWithoutGoroutineLeaks(m, endCrashedProcesses)
	_ = os.RemoveAll(root)
	os.Exit(code)
}
