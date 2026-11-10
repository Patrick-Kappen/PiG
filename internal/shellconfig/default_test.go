package shellconfig

import (
	"errors"
	"testing"
)

// countLookups makes Default resolve through lookup, counting the calls, with nothing kept from before.
func countLookups(t *testing.T, lookup func() (Config, error)) *int {
	t.Helper()
	calls := new(int)
	saved := resolveDefault
	resolveDefault = func() (Config, error) {
		*calls++
		return lookup()
	}
	reset := func() {
		defaultShell.mu.Lock()
		defaultShell.config = nil
		defaultShell.mu.Unlock()
	}
	reset()
	t.Cleanup(func() {
		resolveDefault = saved
		reset()
	})
	return calls
}

// Default resolves the shell once per process and hands every caller its own copy (D234).
func TestDefaultResolvesTheShellOncePerProcess(t *testing.T) {
	calls := countLookups(t, func() (Config, error) { return ForBash("/opt/bash"), nil })
	first, err := Default()
	if err != nil || first.Path != "/opt/bash" {
		t.Fatalf("first: %+v, %v", first, err)
	}
	first.Args[0] = "changed"
	second, err := Default()
	if err != nil || second.Path != "/opt/bash" || second.Args[0] != "-c" {
		t.Fatalf("second: %+v, %v (a caller's change must not reach the next)", second, err)
	}
	if *calls != 1 {
		t.Fatalf("lookups: %d, want 1", *calls)
	}
}

// A failed lookup is not kept: a shell installed later is found by the next call, as Pi looks again every time.
func TestDefaultLooksAgainAfterAFailure(t *testing.T) {
	missing := true
	calls := countLookups(t, func() (Config, error) {
		if missing {
			return Config{}, errors.New("no bash")
		}
		return ForBash("/opt/bash"), nil
	})
	if _, err := Default(); err == nil {
		t.Fatal("no shell: want the error")
	}
	missing = false
	if got, err := Default(); err != nil || got.Path != "/opt/bash" {
		t.Fatalf("after install: %+v, %v", got, err)
	}
	if *calls != 2 {
		t.Fatalf("lookups: %d, want 2", *calls)
	}
}
