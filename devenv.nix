# SPDX-License-Identifier: MIT
# The PiG development shell: the toolchain CI's Linux jobs verify, at the versions the repository pins.
#
# Every version comes from the file CI reads it from, so moving a pin needs no edit here:
#   Go     .go-version          GOTOOLCHAIN makes the go command fetch that release (checked against the Go checksum database)
#   Node   .node-version        the official release (nix/devenv/node.nix)
#   npm    NPM_VERSION in .github/workflows/ci.yml, installed by automation/ci/install-npm-toolchain.sh as in CI
#   Rust   RUST_VERSION in .github/workflows/ci.yml, from rust-overlay
#   SBCL   automation/dev/toolchains.lock, the official binary install-sbcl.sh installs (nix/devenv/sbcl.nix)
# The rest is the package set of automation/ci/install-native-test-packages.sh.
#
# The shell starts on cd through the devenv shell hook once the checkout is trusted with `devenv allow`, or with
# `devenv shell`. `pig-toolchain-check` runs CI's "Verify toolchain identities" checks.
{
  pkgs,
  lib,
  ...
}:

let
  ciEnv =
    name:
    let
      lines = lib.splitString "\n" (builtins.readFile ./.github/workflows/ci.yml);
      found = lib.findFirst (line: builtins.match "  ${name}: (.*)" line != null) null lines;
    in
    if found == null then
      throw "devenv.nix: .github/workflows/ci.yml has no ${name} in its env block"
    else
      builtins.head (builtins.match "  ${name}: (.*)" found);

  goVersion = lib.fileContents ./.go-version;
  nodeVersion = lib.fileContents ./.node-version;
  npmVersion = ciEnv "NPM_VERSION";
  rustVersion = ciEnv "RUST_VERSION";

  # The toolchains.lock row for one tool on this host, as install-sbcl.sh selects it.
  lockRow =
    tool: os: arch:
    let
      rows = map (lib.splitString "\t") (
        builtins.filter (line: line != "" && !(lib.hasPrefix "#" line)) (
          lib.splitString "\n" (builtins.readFile ./automation/dev/toolchains.lock)
        )
      );
      match = lib.findFirst (
        columns:
        builtins.elemAt columns 0 == tool
        && builtins.elemAt columns 2 == os
        && builtins.elemAt columns 3 == arch
      ) null rows;
    in
    if match == null then
      null
    else
      {
        version = builtins.elemAt match 1;
        url = builtins.elemAt match 5;
        sha256 = builtins.elemAt match 6;
      };
  host = pkgs.stdenv.hostPlatform;
  lockOs = if host.isDarwin then "darwin" else "linux";
  lockArch = if host.isAarch64 then "arm64" else "amd64";
  sbclSource = lockRow "sbcl-source" "any" "any";
  sbclBinary = lockRow "sbcl" lockOs lockArch;
  # Only an official binary of the pinned release itself; install-sbcl.sh builds from source where the binary is older.
  sbcl =
    if sbclBinary != null && sbclBinary.version == sbclSource.version then
      pkgs.callPackage ./nix/devenv/sbcl.nix { row = sbclBinary; }
    else
      null;

  node = pkgs.callPackage ./nix/devenv/node.nix { version = nodeVersion; };

  toolchainCheck = pkgs.writeShellScriptBin "pig-toolchain-check" ''
    set -euo pipefail
    cd "''${DEVENV_ROOT:?pig-toolchain-check: run it in the devenv shell}"
    test "$(go env GOVERSION)" = "go$(tr -d '\r\n' < .go-version)"
    test "$(node --version)" = "v$(cat .node-version)"
    test "$(npm --version)" = "${npmVersion}"
    test "$(rustc --version | cut -d' ' -f2)" = "${rustVersion}"
    if [ -n "''${PIG_SBCL:-}" ]; then
      test "$("$PIG_SBCL" --version)" = "SBCL $(awk -F'\t' '!/^#/ && $1=="sbcl-source" {print $2}' automation/dev/toolchains.lock)"
    fi
    echo "toolchain: go${goVersion}, node v${nodeVersion}, npm ${npmVersion}, rust ${rustVersion}''${PIG_SBCL:+, sbcl ${
      if sbcl == null then "" else sbcl.version
    }}"
  '';
in
{
  languages.rust = {
    enable = true;
    channel = "stable";
    version = rustVersion;
    # rustfmt and clippy: make lint-rust, as CI's rustup install names them.
    components = [
      "rustc"
      "cargo"
      "clippy"
      "rustfmt"
    ];
  };

  packages = [
    # go 1.27 bootstraps the pinned release through GOTOOLCHAIN below.
    pkgs.go_1_27
    node
    pkgs.python312
    pkgs.uv

    # install-native-test-packages.sh: coding-agent fixtures, the native clipboard fixtures (C toolchain, pkg-config,
    # libxcb headers) and the X11 selection tests (Xvfb, xclip).
    pkgs.tmux
    pkgs.fd
    pkgs.ripgrep
    pkgs.gnumake
    pkgs.gcc
    pkgs.pkg-config
    pkgs.xorg.libxcb.dev
    pkgs.xorg.xvfb
    pkgs.xclip

    # Commands the scripts and tests call.
    pkgs.git
    pkgs.gh
    pkgs.jq
    pkgs.curl
    pkgs.coreutils
    pkgs.gnutar
    pkgs.gzip
    pkgs.bzip2
    pkgs.xz
    pkgs.unzip
    pkgs.which
    pkgs.file
    pkgs.procps
    pkgs.util-linux
    pkgs.openssl
    pkgs.openssh
    pkgs.actionlint
    pkgs.shellcheck

    toolchainCheck
  ]
  ++ lib.optional (sbcl != null) sbcl;

  env = {
    GOTOOLCHAIN = "go${goVersion}";
  }
  // lib.optionalAttrs (sbcl != null) {
    PIG_SBCL = "${sbcl}/bin/sbcl";
    SBCL_HOME = "${sbcl}/lib/sbcl";
  };

  # The lockfile-pinned npm, installed and put first on PATH as CI's "Install pinned npm" step does. The install runs
  # only when that npm is missing or another version, so entering the shell stays fast.
  enterShell = ''
    npm_bin="$DEVENV_ROOT/automation/ci/npm-toolchain/node_modules/.bin"
    if [ "$("$npm_bin/npm" --version 2>/dev/null)" != "${npmVersion}" ]; then
      echo "devenv: installing npm ${npmVersion} (automation/ci/install-npm-toolchain.sh)" >&2
      "$DEVENV_ROOT/automation/ci/install-npm-toolchain.sh" >&2 || echo "devenv: the npm install failed; npm is Node's own" >&2
    fi
    export PATH="$npm_bin:$PATH"
  '';

  # devenv refuses to install hooks when a global core.hooksPath is set, and then also skips enterShell. The
  # repository's hooks are not devenv's: install none.
  tasks."devenv:git-hooks:install".exec = lib.mkForce "exit 0";
}
