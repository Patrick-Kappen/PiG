package shellconfig

import (
	"os"
	"slices"
	"strings"
	"sync"
)

// resolveDefault is the platform's lookup; a test replaces it to count the lookups.
var resolveDefault = platformDefault

// defaultShell is the default shell this process resolved.
//
// pig additive (D234): Pi resolves the default shell on every getShellConfig call (shell.ts), a `which` process each
// time where /bin/bash does not exist. PiG resolves it when it is first needed and keeps it for the environment it
// was found in (lookupEnvironment): a process whose PATH does not change resolves it once, and a change to the
// variables the lookup reads looks again. A failed lookup is not kept, so a shell installed later is still found, as
// in Pi.
var defaultShell struct {
	mu     sync.Mutex
	config *Config
	env    string
}

// lookupVariables are the environment variables the platform lookups read: PATH and PATHEXT (`which`, `where`,
// exec.LookPath), where Git for Windows is installed, and SystemRoot (the legacy WSL bash).
var lookupVariables = []string{"PATH", "PATHEXT", "ProgramFiles", "ProgramFiles(x86)", "SystemRoot"}

// lookupEnvironment is the value of the variables the lookup reads.
func lookupEnvironment() string {
	values := make([]string, len(lookupVariables))
	for i, name := range lookupVariables {
		values[i] = os.Getenv(name)
	}
	return strings.Join(values, "\x00")
}

// Default is the platform's default shell, upstream getShellConfig with no custom shell path, resolved once for the
// environment the lookup reads.
func Default() (Config, error) {
	env := lookupEnvironment()
	defaultShell.mu.Lock()
	defer defaultShell.mu.Unlock()
	if defaultShell.config == nil || defaultShell.env != env {
		config, err := resolveDefault()
		if err != nil {
			defaultShell.config = nil
			return config, err
		}
		defaultShell.config, defaultShell.env = &config, env
	}
	c := *defaultShell.config
	c.Args = slices.Clone(c.Args)
	return c, nil
}
