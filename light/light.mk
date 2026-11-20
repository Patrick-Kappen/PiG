# SPDX-License-Identifier: MIT

# The light run: what every push to a pull request gets (.github/workflows/light.yml).
# It answers one question: does this change break the product?
#
#   light/run.sh                                                     everything, in the light image
#   light/run.sh make -f light/light.mk -k -j4 light-test LIGHT_SHARD=2/10  one test shard, as a CI runner runs it
#   make -f light/light.mk -k -j4                                    on a host that has the toolchains itself
#
# How it is laid out:
# - Every part runs once. The tests are spread over shards and every test is in exactly one (automation/light/plan). A package is
#   compiled once per runner, before the tests start. The lint already holds go vet for Linux, and the tests compile
#   the Linux build, so go vet runs only for the other platforms.
# - Every part runs beside the others. In CI each go vet platform, the lint and each test shard has a runner of its
#   own; on a runner the packages run beside each other on all its processors.
# - No limit is part of a run that goes well. automation/light/plan spreads the long tests so that a shard's share of the longest
#   package is a few minutes. The two limits below (LIGHT_TEST_TIMEOUT, PIG_TEST_WAIT_TIMEOUT_SEC) are sized for a
#   shard that restored no cache: it is slow, not red.
# - What does not answer the question is left to the heavy run: light/heavy.txt names it and says why.
#
# The light run stands on its own. Everything it needs is in light/ (the image's Containerfile, this file, heavy.txt
# and timings.txt) and in automation/light/ (its two programs, plan and rustdeps: the repository exempts the tests of
# CI tooling from the rule for tests of ported code only below automation/, test/testdata-provenance.toml). It includes no other make file and no other make file includes it, it calls no script under
# automation/ci, and it reads no workflow, no test selection and no generated report.
# From the rest of the repository it takes the product: the code and its tests, the versions go.mod and the lock files
# name, the lint rules in .golangci.yml, and the Pi the tests compare PiG with (light-fixtures).

# The commit the lint compares with: only findings in files changed since then are reported. CI passes the pull
# request's base (HEAD^1 of the merge commit it tests).
LIGHT_BASE ?= origin/main
LIGHT_OUT ?= tmp/light
# This runner's shard and the number of shards, as index/total. 1/1 is every test on one machine.
LIGHT_SHARD ?= 1/1
LIGHT_CPUS ?= $(shell nproc)
# How long a test waits for a subprocess, an extension or a build before it calls it a hang (internal/testbudget).
# The tests' own two minutes assume that the Rust SDK is built already. A shard that restored no cache builds it inside
# a test, beside three other test binaries; on a GitHub-hosted runner that took more than two minutes and failed the
# test. A test that passes does not wait longer for this.
export PIG_TEST_WAIT_TIMEOUT_SEC ?= 300
# How long one package's tests may take on a shard before the go command ends them. Without the cache, on the slower
# of GitHub's runners, a shard's share of coding/cli passed the go command's own ten minutes (run 44714966342): its
# tests were waiting for Rust builds, none hung.
LIGHT_TEST_TIMEOUT ?= 20m
# The platforms besides Linux that have code of their own: Windows (206 files), macOS (20 files) and, through
# android-arm64, the Linux code on arm64 and without cgo. go vet compiles and type-checks, test files included, so a
# file that does not build there fails here, on Linux. windows-arm64 is left to the heavy run: no file is specific to
# an architecture on Windows.
LIGHT_VET_PLATFORMS := windows-amd64 darwin-arm64 android-arm64
# The npm trees the tests import, each installed from its own lock file. durable/interop holds the published Pi Durable
# of a release pin; for a commit pin the pin tool builds it (light-fixtures).
LIGHT_NPM_TREES := test/parity/interface-extractor internal/typebox/testdata/npm ai/testdata/sdk \
	$(if $(shell grep -Eqs '^ref=[0-9a-f]{40}$$' .upstream/pin || echo release),durable/interop)

SHELL := bash
.SHELLFLAGS := -o pipefail -c
.DEFAULT_GOAL := light
light_comma := ,
light_plan := $(LIGHT_OUT)/plan

# The packages that run whole are many and small, the sliced ones few and long. A quarter of the processors goes to the
# first, the rest to the second, so together they keep every processor busy and none is asked twice.
light_p_whole := $(shell echo $$(( $(LIGHT_CPUS) / 4 > 0 ? $(LIGHT_CPUS) / 4 : 1 )))
light_p_sliced := $(shell echo $$(( $(LIGHT_CPUS) - $(light_p_whole) > 0 ? $(LIGHT_CPUS) - $(light_p_whole) : 1 )))

# light_part <name> <command>: run the command and record its exit status and seconds in $(LIGHT_OUT)/times/<name>.
# Its output goes to $(LIGHT_OUT)/<name>.log and is printed when the command ends, so the lines of two parts never mix.
define light_part
@mkdir -p $(LIGHT_OUT)/times; start=$$(date +%s); status=0; { $(2); } > $(LIGHT_OUT)/$(1).log 2>&1 || status=$$?; \
	seconds=$$(( $$(date +%s) - start )); echo "$(1) $$status $$seconds" > $(LIGHT_OUT)/times/$(1); \
	{ echo "== $(1): exit $$status after $$seconds s"; cat $(LIGHT_OUT)/$(1).log; }; exit $$status
endef

# light_gotestsum <name> <skip file>: gotestsum runs go test and reports it: a line per package as it ends, the failed
# tests once more at the end, and the number of tests that passed, failed and were skipped. The go test arguments
# follow it. -count=1 because the go command's test cache does not see what a Node or Pi child process reads.
# -skip takes the plan's pattern: skip.re leaves out the single tests light/heavy.txt names, skip-sliced.re also the
# timed tests that belong to another shard.
light_gotestsum = gotestsum --format pkgname-and-test-fails --format-hide-empty-pkg --jsonfile $(LIGHT_OUT)/$(1).json -- -count=1 -timeout $(LIGHT_TEST_TIMEOUT) -skip "$$(cat $(light_plan)/$(2))"

light: light-test light-lint light-vet

light-vet: $(addprefix light-vet-,$(LIGHT_VET_PLATFORMS))

# The platforms as a JSON list: the workflow gives each a runner of its own and reads the list here.
light-vet-platforms:
	@printf '[%s]\n' "$$(printf '"%s"$(light_comma)' $(LIGHT_VET_PLATFORMS) | sed 's/$(light_comma)$$//')"

# A cross build has no cgo; Android has to say so.
light-vet-%:
	$(call light_part,vet-$*,$(if $(filter android-%,$*),CGO_ENABLED=0 )GOOS=$(word 1,$(subst -, ,$*)) GOARCH=$(word 2,$(subst -, ,$*)) go vet ./...)

light-lint:
	$(call light_part,lint,go tool golangci-lint run --allow-parallel-runners --build-tags=integration$(light_comma)live$(light_comma)parity --new-from-rev="$$(git merge-base HEAD $(LIGHT_BASE))" --whole-files ./...)

# What this shard tests: automation/light/plan writes the packages it runs whole, the packages every shard runs a share of, the
# patterns of this shard's share, and the pattern of the single tests light/heavy.txt leaves out.
light-plan:
	$(call light_part,plan,go run ./automation/light/plan -shard $(LIGHT_SHARD) -out $(light_plan))

# What the tests need before they start, in the order it is made:
# - the npm trees they import. npm writes node_modules/.package-lock.json when it installs, so a tree is installed
#   again only when its lock file is newer;
# - the Pi they compare PiG with. .upstream/pin names it, and the repository's pin tool (automation/pin) builds that Pi
#   from its source and checks the build: the tests check the same stamp and accept no other;
# - the mirror, that Pi's source, which the tests read.
# The Pi and the mirror are kept under ~/.cache, so a second run finds them.
define light_fixtures
( for tree in $(LIGHT_NPM_TREES); do \
	[ "$$tree/node_modules/.package-lock.json" -nt "$$tree/package-lock.json" ] \
		|| { (cd "$$tree" && npm ci --ignore-scripts --no-audit --no-fund) && touch "$$tree/node_modules/.package-lock.json"; } || exit 1; \
done ) \
&& python3 automation/pin/pin.py install-oracle \
&& python3 automation/pin/pin.py check-oracle \
&& automation/gen/mirror-upstream.sh
endef

light-fixtures:
	$(call light_part,fixtures,$(light_fixtures))

# Compiles what this shard's packages and their tests import, once, before the test runs start beside each other: two
# go commands that start cold at the same moment would each compile the packages both need. go list names those
# packages; the ones that exist only inside a test binary are left to the run that builds that binary.
light-build: | light-plan
	$(call light_part,build,deps=$$(go list -deps -test -f '{{if and .GoFiles (not .ForTest)}}{{.ImportPath}}{{end}}' $$(cat $(light_plan)/sliced.pkgs $(light_plan)/whole.pkgs) | grep -v '\.test$$' | sort -u) && go build $$deps)

# The tests, as three runs beside each other. Each waits for the plan, the compiled packages and the fixtures.
# - light-test-sliced: this shard's share of the tests of the packages with many tests. With one shard no package is
#   sliced and this run has nothing to do.
# - light-test-whole: the other packages of this shard, with all their tests.
# - light-test-pigfault: the durable driver's tests that exist only in the pigfault build, on the first shard. The
#   package's other tests run in the build that ships, in one of the two runs above.
light-test: light-test-sliced light-test-whole $(if $(filter 1/%,$(LIGHT_SHARD)),light-test-pigfault)

light-test-sliced light-test-whole light-test-pigfault: | light-build light-fixtures

light-test-sliced:
	$(call light_part,test-sliced,if [ -s $(light_plan)/sliced.pkgs ]; then \
		$(call light_gotestsum,test-sliced,skip-sliced.re) -p $(light_p_sliced) -run "$$(cat $(light_plan)/run.re)" $$(cat $(light_plan)/sliced.pkgs); \
	else echo "no package is sliced: shard $(LIGHT_SHARD) runs every package whole"; fi)

# Without sliced packages this run has all the processors.
light-test-whole:
	$(call light_part,test-whole,p=$(light_p_whole); [ -s $(light_plan)/sliced.pkgs ] || p=$(LIGHT_CPUS); \
		$(call light_gotestsum,test-whole,skip.re) -p $$p $$(cat $(light_plan)/whole.pkgs))

light-test-pigfault:
	$(call light_part,test-pigfault,$(call light_gotestsum,test-pigfault,skip.re) -tags pigfault -run "$$(go run ./automation/light/plan -tag-only pigfault ./durable/core/driver)" ./durable/core/driver)

# What main prepares for the pull requests (the Warm job), without running a test:
# - light-fixtures: the npm trees, the Pi the tests compare with, its source;
# - light-warm-go: every test of the light run compiled and linked;
# - light-warm-rust: the Rust SDK and its dependencies built into the Cargo target of every package whose tests build
#   Rust cells, and the pinned Pi's env daemon (automation/light/rustdeps).
# A shard that starts from this compiles what its pull request changed and its tests' own cells, nothing else.
light-warm: light-fixtures light-warm-go light-warm-rust

light-warm-go: | light-plan
	$(call light_part,warm-go,go test -c -o /dev/null $$(cat $(light_plan)/sliced.pkgs $(light_plan)/whole.pkgs))

# After the fixtures: the daemon's source is in the mirror they lay out.
light-warm-rust: | light-fixtures
	$(call light_part,warm-rust,go run ./automation/light/rustdeps)

# Writes light/timings.txt from the go test -json results of a run: this checkout's last run, or the files
# LIGHT_RESULTS names (the artifacts of a CI run, for one).
#   gh run download <run> --dir tmp/results && make -f light/light.mk light-timings LIGHT_RESULTS='tmp/results/*/test-*.json'
LIGHT_RESULTS ?= $(LIGHT_OUT)/test-*.json
light-timings:
	go run ./automation/light/plan -timings-from '$(LIGHT_RESULTS)' > light/timings.txt.new && mv light/timings.txt.new light/timings.txt

# Prints the parts of the last light run with their exit status and seconds, and the tests' counts.
light-times:
	@if [ -d $(LIGHT_OUT)/times ]; then cat $(LIGHT_OUT)/times/* | awk 'BEGIN { printf "%-20s %4s %7s\n", "part", "exit", "seconds" } { printf "%-20s %4s %7s\n", $$1, $$2, $$3 }'; else echo "light-times: no light run in $(LIGHT_OUT)"; fi
	@grep -h '^DONE ' $(LIGHT_OUT)/test-*.log 2>/dev/null || true

.PHONY: light light-vet light-vet-platforms light-lint light-plan light-fixtures light-build light-test light-test-sliced light-test-whole \
	light-test-pigfault light-warm light-warm-go light-warm-rust light-timings light-times
