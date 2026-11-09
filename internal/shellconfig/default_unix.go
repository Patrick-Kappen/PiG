//go:build !windows

package shellconfig

import (
	"os"
	"os/exec"
)

// Default resolves the shell on unix, mirroring upstream getShellConfig's unix
// branch (shell.ts). It never errors.
//
// Ports packages/coding-agent/src/utils/shell.ts (getShellConfig, unix branch).
func Default() (Config, error) {
	return unixDefault(fileExists, findBash), nil
}

// findBash looks executable up on PATH in process and asks the lookup command only when that finds nothing.
//
// pig additive (D234): Pi always spawns `which` here (shell.ts getShellConfig, findExecutableOnPath), a process on every
// start without /bin/bash, as on NixOS. exec.LookPath finds the first executable match on PATH as `which` does; when it
// finds none (a shell without the execute bit, as on Termux, or a relative PATH entry), `which` decides as in Pi.
func findBash(executable string) string {
	if path, err := exec.LookPath(executable); err == nil {
		return path
	}
	return FindExecutableOnPath(executable)
}

// unixDefault tries /bin/bash, then bash on PATH (findOnPath), then falls back to sh
// (resolved through PATH at spawn time, like upstream's bare "sh").
func unixDefault(exists func(string) bool, findOnPath func(string) string) Config {
	if exists("/bin/bash") {
		return ForBash("/bin/bash")
	}
	if bash := findOnPath("bash"); bash != "" {
		return ForBash(bash)
	}
	return Config{Path: "sh", Args: []string{"-c"}}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
