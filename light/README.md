<!-- SPDX-License-Identifier: MIT -->

# The light run

The light run is what a push to a pull request gets. It answers one question: does this change break the product? It runs the tests, lints the files the change touches and runs `go vet` for the platforms besides Linux.

```sh
light/run.sh                                                          # everything, in the light image
light/run.sh make -f light/light.mk -k -j4 light-test LIGHT_SHARD=2/10  # one test shard, as a CI runner runs it
light/run.sh bash                                                     # a shell in the image
```

`light/run.sh` builds the image the first time. It needs podman or docker and nothing else on the host.

## How it runs

In CI every part has a runner of its own (`.github/workflows/light.yml`), so the parts run beside each other and none waits for another:

| Job | What it runs |
|---|---|
| Test (1 to 10) | The tests, spread over ten runners |
| Vet (one per platform) | `go vet` for Windows, macOS and Android/arm64 |
| Lint | golangci-lint, reporting only the files the change touches |
| Light | The verdict: green when every job above is green |

Four rules shape it:

- **Every part runs once.** Every test is in exactly one shard. A runner compiles what its packages import once, before its test runs start. The lint already holds `go vet` for Linux and the tests compile the Linux build, so `go vet` runs only for the other platforms.
- **Every part runs beside the others.** On a test runner the packages run beside each other on all its processors.
- **No limit is part of a run that goes well.** The long tests are spread so that a shard's share of the longest package is a few minutes. The two limits `light.mk` sets are sized for a shard without the cache (see below): it is slow, not red.
- **What does not answer the question is left to the heavy run.** `heavy.txt` names those packages and tests, each with its reason.

## How the tests are spread

`automation/light/plan` works out what one shard runs, from the checkout alone. Every runner computes its own share, so no job plans for the others and two runners cannot disagree.

- A package with 150 tests or more runs on **every** shard, each with its share of the tests.
  - The tests `timings.txt` gives seconds for are dealt out one by one, longest first, each to the shard that so far has the fewest seconds of that package. So no shard gets two long tests of one package while another gets none.
  - Every other test goes by the length of its name: with ten shards, shard 3 runs the tests whose name length leaves 2 when divided by 10. Every name has one length, so every test is in exactly one shard, also a test added tomorrow and a test whose seconds are out of date.
- The other packages run whole on **one** shard, dealt out the same way by their seconds. A small package stays whole however long its tests take: its tests have never run without each other.
- The number of shards is the number of entries in the workflow's matrix; a runner reads its place and the total from the matrix itself.

`timings.txt` holds the seconds of the tests of 5 seconds or more and of the packages of 10 seconds or more. It is written from a run's results and not edited by hand:

```sh
gh run download <run> --dir tmp/results
light/run.sh make -f light/light.mk light-timings LIGHT_RESULTS='tmp/results/*/test-*.json'
```

Nothing checks it against the tests, and a pull request does not have to touch it: with old seconds the shards are less even, and that is all.

Why seconds and not only names: few tests hold most of the time. On GitHub 51 tests of 30 seconds or more held 39% of the test seconds, and by name length alone one shard's share of `coding/cli` was 208 seconds and another's 581. On a slower runner such a share passed the ten minutes.

## The run in pieces

On the branch `light-next` the run is pieces of a few minutes, each a job of its own: a runner is free again soon, and a piece that failed can be run again without the others (`.github/workflows/light.yml`).

Every test has one kind:

| Kind | What the test starts |
|---|---|
| `fast` | nothing, and it takes under a second, or nobody has measured it yet |
| `slow` | nothing, and it takes a second or more |
| `pig` | the built `pig` |
| `go` | the go command: it builds something |
| `node` | Node or Pi (and the few that start Python or tmux) |
| `rust` | cargo, rustc or sbcl: it builds a cell |

- `programs.txt` says what a test starts and `timings.txt` how long it takes. Both are measured, not written by hand.
- `moved.txt` puts one test in another kind by hand: the kind, the test, and the reason. To move a test, add a line.
- A new test needs no line anywhere. It is `fast` until a measurement says otherwise: slower there, not wrong.

The first layer starts at once: `Fast` (the fast tests), `Lint` and `Bookkeeping` beside each other, and `Light` is green when they are. The second layer starts when `Light` is green: every other kind, in as many parts as its seconds ask for (`make -f light/light.mk light-pieces`), beside each other. `All tests` is green when everything is.

A piece (`automation/light/plan -queue`) builds the test binaries it needs once and runs its tests from one queue, the longest first: a worker takes the next unit when it is free.

- A slow test is a unit of its own: starting a test binary takes milliseconds, and the long tests are what has to be spread.
- A package's fast tests are one unit, in one process: they share what the package's `TestMain` sets up.
- A package's tests that start a program stay together in units of at most two minutes: they share what the first of them builds, the `pig` binary for one.
- The fast and the slow tests get more workers than processors (`LIGHT_FAST_WORKERS`), because part of them wait for a timer and not for a processor. The kinds that start a program get a worker per processor.

Nothing is compiled twice: `Fast` compiles the change, packs what main's cache did not hold, and every piece unpacks that over the cache it restored.

`programs.txt` is measured with every package's tests in one process, one test at a time, under `strace` for the programs that process started while each test ran. The measuring is not in this directory yet.

## What main prepares

A pull request saves no cache. On a push to main the `Warm` job is the one writer: it starts from what the previous push saved, runs no test, and saves

- every test of the light run, compiled and linked;
- the programs the tests build while they run, compiled with the flags the tests build them with (`light-warm-programs` in `light.mk` lists them);
- the npm trees' downloads, the Pi the tests compare with and its source;
- the Rust SDK and its dependencies, built into the Cargo target of every package whose tests build Rust cells, and the pinned Pi's env daemon (`automation/light/rustdeps`);
- the lint's analysis of every package.

Every test shard and the lint restore that, on a pull request and on main. A shard then compiles what the change touched and its tests' own cells, and the lint analyses the packages the change touched and the ones that import them.

Three things made a shard build again what `Warm` had built, and each is why a line above reads as it does:

- **Build flags.** The go command keeps a compiled package per set of flags. A test that builds `pig` with `-trimpath`, without cgo or with the strip build tags compiled its whole tree, in every shard that had such a test. `Warm` now makes those builds too.
- **File times.** Cargo builds a path dependency again when a source file is newer than its last build, and after a checkout every file is. `Warm` leaves a stamp beside each build that names the SDK files it was built from; a shard whose SDK files are exactly those sets their times back before its tests start (`light-rust-reuse`), and a shard whose SDK differs by one byte keeps its times and builds.
- **The toolchain.** rustup takes the toolchain from the directory cargo is started in. The pinned Pi's env daemon names another toolchain than the image's in its own directory, so it is built from `env/`, where env's tests build it.

A test run saves nothing: every run builds cells under new temporary paths, and that would grow the cache from push to push.

Without the cache a shard builds the Rust SDK inside its tests, for each package again and beside three other test binaries. On GitHub-hosted runners that took longer than the two minutes a test waits for its extension, and on the slower runners a shard's share of `coding/cli` then took longer than the go command's ten minutes per package. `light.mk` therefore sets the tests' wait bound (`PIG_TEST_WAIT_TIMEOUT_SEC`) to five minutes and the time per package (`LIGHT_TEST_TIMEOUT`) to twenty: a run without the cache is slow, not red. A test that passes does not wait longer for either.

What the tests still build in a shard: a Rust test crate that brings a lock file of its own, or none, resolves other versions of the SDK's dependencies than the SDK's lock file names, and Cargo builds the SDK once more for those.

`go vet` uses no cache: each platform is compiled from nothing on its own runner and is done before the tests are.

## It stands on its own

Everything the light run uses is in this directory, in `automation/light/` and in `.github/workflows/light.yml`:

| File | What it holds |
|---|---|
| `Containerfile` | The image: Ubuntu, Go, Node, npm, Rust, SBCL, libxcb, gotestsum and the apt packages, each with its version here |
| `light.mk` | What runs: the tests, the lint, `go vet`, and what the tests need first |
| `automation/light/plan/` | The program that works out the pieces and runs a piece's queue, with its tests |
| `heavy.txt` | The packages and tests the light run leaves to the heavy run, each with its reason |
| `timings.txt` | The measured seconds of the longer tests and packages, for the plan |
| `programs.txt` | The tests that start a program, each with what it starts |
| `moved.txt` | The tests put in another kind by hand, each with its reason |
| `automation/light/rustdeps/` | The program that builds what the tests' Rust builds start from, for `Warm` |
| `run.sh` | Starts a command in the image against this checkout |

The two programs are below `automation/` and not here because of one rule of the repository: a commit that changes a test's expected values together with its code has to cite the Pi behaviour that changed, and `test/testdata-provenance.toml` exempts CI tooling from that only in directories below `automation/`. Both are listed there.

It does not use `ci.yml` or any other workflow, the `Makefile`, a script under `automation/ci`, `.go-version`, `.node-version`, `toolchains.lock`, a test selection by path, a tier list or a generated report. A change to one of those does not change the light run.

From the rest of the repository it takes the product, because that is what it tests:

- the code and its tests;
- `go.mod`, `go.sum` and the npm and Cargo lock files: the Go the code needs, the linter and the dependencies;
- `.golangci.yml`: the lint rules;
- `.upstream/pin` and `automation/pin`: the Pi that the tests compare PiG with. The pin names it, and the pin tool builds it from source and checks the build. The tests check the same stamp, so they accept no other build.

## Leaving something to the heavy run

Add a line to `heavy.txt`: a package's directory, a directory followed by `/...`, or `directory:TestName` for one test, and after a `#` the reason. An entry without a reason fails the plan, and so does a test that does not exist.

## Changing a version

Edit `Containerfile`. The next run builds a new image, because the image is named after the hash of that file. Go is the one exception: when `go.mod` names a later Go than the image holds, `go` fetches that one itself.
