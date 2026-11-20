// SPDX-License-Identifier: MIT

// Command rustdeps sees to it that the Rust SDK is built once, on main, and not again by every test shard.
//
// Several test packages build Rust extension cells, each package into a persistent Cargo target of its own
// (internal/testenv.SharedCargoTargetDir). Two things made every shard build the SDK anew inside its tests:
//
//   - An empty target: the first build compiles the SDK and its dependencies, a minute or more on a busy 4-CPU
//     runner.
//   - The file times of a checkout: Cargo rebuilds a path dependency when a source file is newer than its last
//     build, and after a checkout every file is. With the dependencies built already, one test took 37 s instead of
//     5.5 s for that reason alone, in every package and on every shard.
//
// Without a flag (the light run's Warm job, on main) the command builds the SDK and its dependencies into each of
// those targets, and the pinned Pi's env daemon, and writes beside each build a stamp: a digest of the SDK's files
// and the time of the build.
//
// The daemon is built from env's directory, as env's tests build it: rustup takes the toolchain from the directory
// cargo is started in, and the daemon's own directory names another toolchain than the image's. Built from there, it
// was built with a compiler the tests do not use, and every shard built it again.
//
// With -reuse (every test shard, before its tests) it builds nothing. When every target holds a stamp of exactly the
// SDK files of this checkout, it sets those files' times back to just before the builds, so Cargo finds the SDK
// built. A checkout whose SDK differs, by one byte or one file, keeps its times and Cargo builds it: the times are
// set back only for files that are the ones that were built.
//
//	go run ./automation/light/rustdeps
//	go run ./automation/light/rustdeps -reuse
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/MichaelKinsy/PiG/coding/extension/host/runtimecell"
	"github.com/MichaelKinsy/PiG/internal/testenv"
)

// cellPackages are the names the light run's test packages give testenv.SharedCargoTargetDir in their TestMain for the
// Rust SDK's cells. A name that no package uses any more only costs a build; main_test.go fails on it.
var cellPackages = []string{"cli", "coding", "runtimecell", "subprocess"}

// daemonPackage is the name env's tests give the target of the pinned Pi's env daemon.
const daemonPackage = "env-oracle"

// stampName is the file in a Cargo target that says which SDK that target was built from.
const stampName = "light-rust-sdk.json"

// stamp says which Rust SDK a Cargo target was built from, and when.
type stamp struct {
	// Digest is the digest of the SDK's files (sdkDigest).
	Digest string `json:"digest"`
	// Built is the time just before the build into this target started, in unix nanoseconds. A source file older than
	// this is older than the build.
	Built int64 `json:"built"`
	// Latest is the time of the newest build of this checkout's SDK in the target when the stamp was written, in unix
	// nanoseconds. A newer one means something built the SDK after the stamp, from files the stamp does not describe.
	Latest int64 `json:"latest"`
}

func main() {
	reuseOnly := flag.Bool("reuse", false, "build nothing: set the SDK's file times back when every target was built from exactly these files")
	flag.Parse()
	if err := run(context.Background(), *reuseOnly); err != nil {
		fmt.Fprintln(os.Stderr, "rustdeps:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, reuseOnly bool) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	sdk := filepath.Join(root, "extensions", "sdk-rs")
	targets := make([]string, len(cellPackages))
	for i, pkg := range cellPackages {
		if targets[i], err = testenv.SharedCargoTargetDir(pkg, filepath.Join(sdk, "Cargo.lock"), filepath.Join(sdk, "Cargo.toml")); err != nil {
			return err
		}
	}
	digest, files, err := sdkDigest(sdk)
	if err != nil {
		return err
	}
	reused, why, err := reuse(sdk, digest, files, targets)
	if err != nil {
		return err
	}
	if reused {
		fmt.Fprintln(os.Stderr, "rustdeps: the Rust SDK of this checkout is the one that was built; its files' times are set back and Cargo does not build it again")
	} else {
		fmt.Fprintln(os.Stderr, "rustdeps: Cargo builds the Rust SDK:", why)
	}
	if reuseOnly {
		return nil
	}

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
	for i, target := range targets {
		started := time.Now()
		if err := cargoBuild(ctx, cell, target, "build", "--release", "--quiet"); err != nil {
			return fmt.Errorf("the Rust SDK's dependencies for %s: %w", cellPackages[i], err)
		}
		// A target that was reused holds the stamp of the build Cargo just found; its time stays that build's.
		if _, stamped, err := readStamp(target); err != nil {
			return err
		} else if reused && stamped {
			continue
		}
		latest, err := latestSDKBuild(target, sdk)
		if err != nil {
			return err
		}
		if err := writeStamp(target, stamp{Digest: digest, Built: started.UnixNano(), Latest: latest.UnixNano()}); err != nil {
			return err
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
	if err := cargoBuild(ctx, filepath.Join(root, "env"), target, "build", "--release", "--locked", "--quiet", "--manifest-path", manifest); err != nil {
		return fmt.Errorf("the pinned Pi's env daemon: %w", err)
	}
	return nil
}

// sdkDigest returns a digest of every file below the SDK's directory, by path and content, and those files. The
// directory target is left out: it is where Cargo builds when it is asked to build in place.
func sdkDigest(sdk string) (digest string, files []string, err error) {
	h := sha256.New()
	err = filepath.WalkDir(sdk, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "target" && path != sdk {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(sdk, path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(content)
		// WalkDir visits in lexical order, so the digest does not depend on the directory's own order.
		h.Write([]byte(filepath.ToSlash(rel)))
		h.Write([]byte{0})
		h.Write(sum[:])
		files = append(files, path)
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	if len(files) == 0 {
		return "", nil, fmt.Errorf("%s holds no file", sdk)
	}
	return hex.EncodeToString(h.Sum(nil)), files, nil
}

// reuse sets the SDK files' times back to just before the builds when every target that holds a build holds one of
// exactly these files. It reports whether it did, and why not otherwise.
//
// It is all or nothing because the files have one time and every target reads it: a time older than one target's
// build of other files would make Cargo use that build.
func reuse(sdk, digest string, files, targets []string) (done bool, why string, err error) {
	earliest := int64(0)
	for _, target := range targets {
		if _, err := os.Stat(filepath.Join(target, "release", ".fingerprint")); errors.Is(err, fs.ErrNotExist) {
			continue // nothing built here: Cargo builds whatever the files' times are
		} else if err != nil {
			return false, "", err
		}
		s, ok, err := readStamp(target)
		if err != nil {
			return false, "", err
		}
		if !ok {
			return false, target + " holds a build without a stamp", nil
		}
		if s.Digest != digest {
			return false, target + " was built from other SDK files than this checkout's", nil
		}
		latest, err := latestSDKBuild(target, sdk)
		if err != nil {
			return false, "", err
		}
		if latest.UnixNano() > s.Latest {
			return false, target + " holds a build of the SDK newer than its stamp", nil
		}
		if earliest == 0 || s.Built < earliest {
			earliest = s.Built
		}
	}
	if earliest == 0 {
		return false, "no target holds a build yet", nil
	}
	before := time.Unix(0, earliest).Add(-time.Second)
	for _, file := range files {
		if err := os.Chtimes(file, before, before); err != nil {
			return false, "", err
		}
	}
	return true, "", nil
}

// latestSDKBuild returns the time of the newest build, in a target, of the SDK at this path: Cargo's own record of
// when it started that build. The target also holds builds of copies of the SDK that tests stage elsewhere; those are
// other units and are left out. The zero time means the target holds no build of this SDK.
func latestSDKBuild(target, sdk string) (time.Time, error) {
	depInfos, err := filepath.Glob(filepath.Join(target, "release", "deps", "pig_sdk-*.d"))
	if err != nil {
		return time.Time{}, err
	}
	var latest time.Time
	for _, depInfo := range depInfos {
		content, err := os.ReadFile(depInfo)
		if err != nil {
			return time.Time{}, err
		}
		if !strings.Contains(string(content), sdk+string(filepath.Separator)) {
			continue
		}
		unit := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(depInfo), "pig_sdk-"), ".d")
		info, err := os.Stat(filepath.Join(target, "release", ".fingerprint", "pig-sdk-"+unit, "invoked.timestamp"))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return time.Time{}, err
		}
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
	}
	return latest, nil
}

func readStamp(target string) (s stamp, ok bool, err error) {
	content, err := os.ReadFile(filepath.Join(target, stampName))
	if errors.Is(err, fs.ErrNotExist) {
		return stamp{}, false, nil
	}
	if err != nil {
		return stamp{}, false, err
	}
	if err := json.Unmarshal(content, &s); err != nil {
		return stamp{}, false, nil //nolint:nilerr // a stamp that does not parse is no stamp: Cargo builds
	}
	return s, true, nil
}

func writeStamp(target string, s stamp) error {
	content, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(target, stampName), content, 0o644)
}

func cargoBuild(ctx context.Context, dir, target string, args ...string) error {
	cmd := exec.CommandContext(ctx, "cargo", args...) //nolint:gosec // G204: the program is literal cargo; the arguments are the fixed build flags above and a manifest path below this checkout.
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	fmt.Fprintln(os.Stderr, "rustdeps: cargo", args[0], "->", target)
	return cmd.Run()
}
