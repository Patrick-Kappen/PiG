package env

import (
	"testing"
	"time"
)

// eventuallyTimeout bounds a poll of eventually. It is the terminal tests' own bound (terminalTestTimeout), which a file that also builds on
// Windows cannot name.
const eventuallyTimeout = 20 * time.Second

// eventually polls what only the system can tell, with no event to wait for.
func eventually(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(eventuallyTimeout)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
