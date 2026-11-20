// SPDX-License-Identifier: MIT

// Command rustdeps builds what the tests' Rust builds start from, before any test runs.
//
// Several test packages build Rust extension cells, each package into a persistent Cargo target of its own
// (internal/testenv.SharedCargoTargetDir). The first build into an empty target compiles the Rust SDK and its
// dependencies, which takes a minute or more on a busy 4-CPU runner, inside a test that waits two minutes for its
// extension. This command does those first builds, one target after the other, so a test compiles its own cell only.
// It also builds the pinned Pi's env daemon, which the env tests compare PiG's with.
//
// The light run's Warm job runs it on main and saves the targets; the test shards restore them.
//
//	go run ./automation/light/rustdeps
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/MichaelKinsy/PiG/coding/extension/host/runtimecell"
	"github.com/MichaelKinsy/PiG/internal/testenv"
)

// cellPackages are the names the light run's test packages give testenv.SharedCargoTargetDir in their TestMain for the
// Rust SDK's cells. A name that no package uses any more only costs a build; main_test.go fails on it.
var cellPackages = []string{"cli", "coding", "runtimecell", "subprocess"}

// daemonPackage is the name env's tests give the target of the pinned Pi's env daemon.
const daemonPackage = "env-oracle"

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "rustdeps:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	sdk := filepath.Join(root, "extensions", "sdk-rs")
	// A cell depends on the SDK by absolute path and Cargo keys that unit on the path, so the dependencies are built
	// through a cell crate as the product renders it: the SDK built from its own manifest is another unit.
	cell, err := os.MkdirTemp("", "light-rust-cell-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(cell) //nolint:errcheck // a leftover temporary directory is not this command's failure
	if err := runtimecell.WriteRustWarmCell(cell, sdk); err != nil {
		return err
	}
	for _, pkg := range cellPackages {
		target, err := testenv.SharedCargoTargetDir(pkg, filepath.Join(sdk, "Cargo.lock"), filepath.Join(sdk, "Cargo.toml"))
		if err != nil {
			return err
		}
		if err := cargoBuild(ctx, cell, target, "build", "--release", "--quiet"); err != nil {
			return fmt.Errorf("the Rust SDK's dependencies for %s: %w", pkg, err)
		}
	}

	daemon := filepath.Join(root, ".upstream", "current", "packages", "env", "daemon")
	manifest := filepath.Join(daemon, "Cargo.toml")
	if _, err := os.Stat(manifest); err != nil {
		return nil //nolint:nilerr // a pin without a daemon has nothing to build
	}
	target, err := testenv.SharedCargoTargetDir(daemonPackage, filepath.Join(daemon, "Cargo.lock"), manifest)
	if err != nil {
		return err
	}
	if err := cargoBuild(ctx, daemon, target, "build", "--release", "--locked", "--quiet", "--manifest-path", manifest); err != nil {
		return fmt.Errorf("the pinned Pi's env daemon: %w", err)
	}
	return nil
}

func cargoBuild(ctx context.Context, dir, target string, args ...string) error {
	cmd := exec.CommandContext(ctx, "cargo", args...) //nolint:gosec // G204: the program is literal cargo; the arguments are the fixed build flags above and a manifest path below this checkout.
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	fmt.Fprintln(os.Stderr, "rustdeps: cargo", args[0], "->", target)
	return cmd.Run()
}
