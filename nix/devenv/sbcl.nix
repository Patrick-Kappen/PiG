# SPDX-License-Identifier: MIT
# The SBCL that automation/dev/toolchains.lock pins, from the same checksummed official binary that
# automation/ci/install-sbcl.sh installs in CI. nixpkgs carries another release. Only the platforms with an official
# binary of the pinned release have a row; the shell leaves SBCL out elsewhere.
{
  lib,
  stdenv,
  fetchurl,
  autoPatchelfHook,
  zlib,
  row,
}:

stdenv.mkDerivation {
  pname = "sbcl-official";
  inherit (row) version;
  src = fetchurl {
    name = "sbcl-${row.version}-binary.tar.bz2";
    inherit (row) url sha256;
  };
  nativeBuildInputs = lib.optional stdenv.hostPlatform.isLinux autoPatchelfHook;
  buildInputs = lib.optional stdenv.hostPlatform.isLinux zlib;
  dontConfigure = true;
  dontBuild = true;
  dontStrip = true;
  installPhase = ''
    runHook preInstall
    INSTALL_ROOT=$out sh install.sh
    runHook postInstall
  '';
  doInstallCheck = true;
  installCheckPhase = ''
    test "$(SBCL_HOME=$out/lib/sbcl $out/bin/sbcl --version)" = "SBCL ${row.version}"
  '';
}
