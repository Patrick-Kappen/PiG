// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// The targets this command builds into are found by name. Every name here must be one a test package gives
// testenv.SharedCargoTargetDir, or the build lands in a directory no test reads; and every such name must be here, or
// that package's tests start from an empty target.
func TestTheTargetNamesAreTheOnesTheTestPackagesUse(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	call := regexp.MustCompile(`testenv\.SharedCargoTargetDir\("([^"]+)"`)
	var used []string
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".upstream", "node_modules", "tmp", "testenv", "extension-conformance":
				// testenv defines the function and tests it with made-up names; extension conformance is not in the light run.
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, match := range call.FindAllSubmatch(content, -1) {
			used = append(used, string(match[1]))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(used)
	used = slices.Compact(used)
	want := slices.Sorted(slices.Values(append(slices.Clone(cellPackages), daemonPackage)))
	if !slices.Equal(used, want) {
		t.Errorf("the test packages name the Cargo targets %v, this command builds %v", used, want)
	}
}

// checkout is an SDK directory with files of a known, recent time, as a checkout leaves them.
type checkout struct {
	sdk      string
	files    []string
	digest   string
	checkout time.Time
}

func newCheckout(t *testing.T) checkout {
	t.Helper()
	c := checkout{sdk: filepath.Join(t.TempDir(), "extensions", "sdk-rs"), checkout: time.Now().Truncate(time.Second)}
	for name, content := range map[string]string{
		"Cargo.toml":       "[package]\nname = \"pig-sdk\"\n",
		"src/lib.rs":       "pub fn a() {}\n",
		"src/protocol.rs":  "pub fn b() {}\n",
		"target/debug/out": "built in place: not a source",
	} {
		path := filepath.Join(c.sdk, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, c.checkout, c.checkout); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	if c.digest, c.files, err = sdkDigest(c.sdk); err != nil {
		t.Fatal(err)
	}
	return c
}

// builtTarget makes a Cargo target as a build of the SDK at sdk leaves it: the dependency file that names the SDK's
// sources and Cargo's record of when the build started.
func builtTarget(t *testing.T, sdk string, built time.Time) string {
	t.Helper()
	target := t.TempDir()
	addBuild(t, target, "0123456789abcdef", sdk, built)
	return target
}

func addBuild(t *testing.T, target, unit, sdk string, built time.Time) {
	t.Helper()
	deps := filepath.Join(target, "release", "deps")
	fingerprint := filepath.Join(target, "release", ".fingerprint", "pig-sdk-"+unit)
	for _, dir := range []string{deps, fingerprint} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	depInfo := filepath.Join(deps, "libpig_sdk-"+unit+".rlib") + ": " + filepath.Join(sdk, "src", "lib.rs") + " " + filepath.Join(sdk, "src", "protocol.rs") + "\n"
	if err := os.WriteFile(filepath.Join(deps, "pig_sdk-"+unit+".d"), []byte(depInfo), 0o600); err != nil {
		t.Fatal(err)
	}
	invoked := filepath.Join(fingerprint, "invoked.timestamp")
	if err := os.WriteFile(invoked, []byte("This file has an mtime of when this was started."), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(invoked, built, built); err != nil {
		t.Fatal(err)
	}
}

func stampOf(t *testing.T, target string, c checkout, built time.Time) {
	t.Helper()
	latest, err := latestSDKBuild(target, c.sdk)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeStamp(target, stamp{Digest: c.digest, Built: built.UnixNano(), Latest: latest.UnixNano()}); err != nil {
		t.Fatal(err)
	}
}

func modTime(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.ModTime()
}

// Every target was built on main from exactly these files, an hour before this checkout: the files' times go back to
// just before the earliest of those builds, so Cargo finds every build newer than its sources.
func TestReuseSetsTheTimesBackWhenEveryTargetWasBuiltFromTheseFiles(t *testing.T) {
	c := newCheckout(t)
	first, second := c.checkout.Add(-time.Hour), c.checkout.Add(-50*time.Minute)
	targets := []string{builtTarget(t, c.sdk, first.Add(time.Second)), builtTarget(t, c.sdk, second.Add(time.Second)), filepath.Join(t.TempDir(), "never-built")}
	stampOf(t, targets[0], c, first)
	stampOf(t, targets[1], c, second)

	done, why, err := reuse(c.sdk, c.digest, c.files, targets)
	if err != nil || !done {
		t.Fatalf("reuse = %v, %q, %v; want it done", done, why, err)
	}
	for _, file := range c.files {
		if got := modTime(t, file); !got.Before(first) {
			t.Errorf("%s has time %v, want one before the earliest build at %v", file, got, first)
		}
	}
	if built := filepath.Join(c.sdk, "target", "debug", "out"); slices.Contains(c.files, built) {
		t.Errorf("%s counts as a source of the SDK", built)
	}
}

// Whatever is not exactly so leaves the times alone, and Cargo builds: a file that differs, a file that is new, a
// target with a build and no stamp, and a target where something built the SDK after the stamp was written.
func TestReuseLeavesTheTimesAloneWhenATargetWasNotBuiltFromTheseFiles(t *testing.T) {
	for name, change := range map[string]func(t *testing.T, c *checkout, targets []string){
		"one byte of one file differs": func(t *testing.T, c *checkout, _ []string) {
			path := filepath.Join(c.sdk, "src", "lib.rs")
			if err := os.WriteFile(path, []byte("pub fn a() { }\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, c.checkout, c.checkout); err != nil {
				t.Fatal(err)
			}
		},
		"a file is new": func(t *testing.T, c *checkout, _ []string) {
			path := filepath.Join(c.sdk, "src", "new.rs")
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, c.checkout, c.checkout); err != nil {
				t.Fatal(err)
			}
		},
		"a target holds a build without a stamp": func(t *testing.T, _ *checkout, targets []string) {
			if err := os.Remove(filepath.Join(targets[1], stampName)); err != nil {
				t.Fatal(err)
			}
		},
		"a target holds a newer build of the SDK than its stamp": func(t *testing.T, c *checkout, targets []string) {
			addBuild(t, targets[1], "fedcba9876543210", c.sdk, c.checkout.Add(-time.Minute))
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := newCheckout(t)
			built := c.checkout.Add(-time.Hour)
			targets := []string{builtTarget(t, c.sdk, built.Add(time.Second)), builtTarget(t, c.sdk, built.Add(time.Second))}
			stampOf(t, targets[0], c, built)
			stampOf(t, targets[1], c, built)
			change(t, &c, targets)
			digest, files, err := sdkDigest(c.sdk)
			if err != nil {
				t.Fatal(err)
			}

			done, why, err := reuse(c.sdk, digest, files, targets)
			if err != nil || done || why == "" {
				t.Fatalf("reuse = %v, %q, %v; want it not done, with the reason", done, why, err)
			}
			for _, file := range files {
				if got := modTime(t, file); !got.Equal(c.checkout) {
					t.Errorf("%s has time %v, want the checkout's %v", file, got, c.checkout)
				}
			}
		})
	}
}

// A build of a copy of the SDK that a test staged elsewhere is another unit: it is newer than the stamp and does not
// stand in the way.
func TestReuseIgnoresBuildsOfASDKCopyElsewhere(t *testing.T) {
	c := newCheckout(t)
	built := c.checkout.Add(-time.Hour)
	target := builtTarget(t, c.sdk, built.Add(time.Second))
	stampOf(t, target, c, built)
	addBuild(t, target, "fedcba9876543210", filepath.Join(t.TempDir(), "staged", "sdk-rs"), c.checkout.Add(-time.Minute))

	if done, why, err := reuse(c.sdk, c.digest, c.files, []string{target}); err != nil || !done {
		t.Fatalf("reuse = %v, %q, %v; want it done", done, why, err)
	}
}

// Nothing built anywhere: there is no build to set the times before.
func TestReuseDoesNothingWithoutABuild(t *testing.T) {
	c := newCheckout(t)
	done, why, err := reuse(c.sdk, c.digest, c.files, []string{filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")})
	if err != nil || done || why == "" {
		t.Fatalf("reuse = %v, %q, %v; want it not done, with the reason", done, why, err)
	}
	if got := modTime(t, c.files[0]); !got.Equal(c.checkout) {
		t.Errorf("%s has time %v, want the checkout's %v", c.files[0], got, c.checkout)
	}
}

// The digest is of paths and contents: the same files give the same digest whenever they were written, and a renamed
// file gives another.
func TestSDKDigestIsOfPathsAndContents(t *testing.T) {
	c := newCheckout(t)
	later := c.checkout.Add(time.Hour)
	for _, file := range c.files {
		if err := os.Chtimes(file, later, later); err != nil {
			t.Fatal(err)
		}
	}
	if digest, _, err := sdkDigest(c.sdk); err != nil || digest != c.digest {
		t.Errorf("after only the times changed the digest is %s, %v; want %s", digest, err, c.digest)
	}
	if err := os.Rename(filepath.Join(c.sdk, "src", "protocol.rs"), filepath.Join(c.sdk, "src", "wire.rs")); err != nil {
		t.Fatal(err)
	}
	if digest, _, err := sdkDigest(c.sdk); err != nil || digest == c.digest {
		t.Errorf("after a file was renamed the digest is still %s (%v)", digest, err)
	}
}
