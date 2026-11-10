package shellconfig

import (
	"slices"
	"sync"
)

// resolveDefault is the platform's lookup; a test replaces it to count the lookups.
var resolveDefault = platformDefault

// defaultShell is the default shell this process resolved.
//
// pig additive (D234): Pi resolves the default shell on every getShellConfig call (shell.ts), a `which` process each
// time where /bin/bash does not exist. PiG resolves it once per process, when it is first needed, and every later
// call reads that result: PATH does not change under a running process. A failed lookup is not kept, so a shell
// installed later is still found, as in Pi.
var defaultShell struct {
	mu     sync.Mutex
	config *Config
}

// Default is the platform's default shell, upstream getShellConfig with no custom shell path, resolved once per process.
func Default() (Config, error) {
	defaultShell.mu.Lock()
	defer defaultShell.mu.Unlock()
	if defaultShell.config == nil {
		config, err := resolveDefault()
		if err != nil {
			return config, err
		}
		defaultShell.config = &config
	}
	c := *defaultShell.config
	c.Args = slices.Clone(c.Args)
	return c, nil
}
