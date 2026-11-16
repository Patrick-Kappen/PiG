# SPDX-License-Identifier: MIT
# The official Node.js release that .node-version names, as CI's actions/setup-node installs it. The oracle tests
# accept only that release (internal/testenv.RequireQualifiedNode), and nixpkgs carries another one.
#
# The digests are the release's SHASUMS256.txt entries (https://nodejs.org/dist/v<version>/SHASUMS256.txt). When
# .node-version moves, evaluation stops here until the new release's digests are added.
{
  lib,
  stdenv,
  fetchurl,
  patchelf,
  version,
}:

let
  digests = {
    "24.22.1" = {
      x86_64-linux = "6c172a9ab6718f7b801fb5a7bd486af2ee5e32758086a04b84fcce2aa77095e6";
      aarch64-linux = "dbabf7a1cbf947e6059ffad2c27b80041db97dea1e553b62afdee7f13fca336e";
      x86_64-darwin = "65e450f90a6eaacd5000f611861c4ce65fadc87f813795dc4ed0c0e5f2fca2d4";
      aarch64-darwin = "654ba79de6155647e22321932b45f2a1ccb705976cc337c9b8e278beb085a628";
    };
  };
  platforms = {
    x86_64-linux = "linux-x64";
    aarch64-linux = "linux-arm64";
    x86_64-darwin = "darwin-x64";
    aarch64-darwin = "darwin-arm64";
  };
  system = stdenv.hostPlatform.system;
  platform = platforms.${system} or (throw "nix/devenv/node.nix: no Node.js release for ${system}");
  sha256 =
    digests.${version}.${system} or (throw ''
      nix/devenv/node.nix: no digest for Node.js ${version} on ${system}.
      .node-version moved: add the entries of https://nodejs.org/dist/v${version}/SHASUMS256.txt.
    '');
in
stdenv.mkDerivation {
  pname = "nodejs-official";
  inherit version;
  src = fetchurl {
    url = "https://nodejs.org/dist/v${version}/node-v${version}-${platform}.tar.xz";
    inherit sha256;
  };
  nativeBuildInputs = lib.optional stdenv.hostPlatform.isLinux patchelf;
  dontConfigure = true;
  dontBuild = true;
  dontStrip = true;
  installPhase = ''
    runHook preInstall
    mkdir -p $out
    cp -r bin include lib share $out/
    runHook postInstall
  '';
  # Not autoPatchelfHook: patchelf 0.19 corrupts this binary (it segfaults before the loader runs) when one call sets
  # both the interpreter and the RPATH. Setting the RPATH first and the interpreter in a second call works.
  postFixup = lib.optionalString stdenv.hostPlatform.isLinux ''
    patchelf --set-rpath ${lib.makeLibraryPath [ stdenv.cc.cc.lib ]} $out/bin/node
    patchelf --set-interpreter "$(cat $NIX_CC/nix-support/dynamic-linker)" $out/bin/node
  '';
  doInstallCheck = true;
  installCheckPhase = ''
    got=$($out/bin/node --version)
    [ "$got" = "v${version}" ] || { echo "node --version: $got, want v${version}" >&2; exit 1; }
  '';
}
