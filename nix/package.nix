# aibox as released: the binary of the release in release.json for the
# system. On Linux it is static and gets QEMU, virtiofsd and bubblewrap
# from nixpkgs in front of its PATH. On macOS it is signed ad hoc with the
# entitlement of Virtualization.framework and needs nothing else. The VM
# image is downloaded on the first run, as with the other packages.
{
  lib,
  stdenvNoCC,
  fetchurl,
  makeBinaryWrapper,
  qemu_kvm,
  virtiofsd,
  bubblewrap,
}:

let
  release = lib.importJSON ./release.json;
  system = stdenvNoCC.hostPlatform.system;
  platforms = {
    x86_64-linux = "linux_amd64";
    aarch64-darwin = "darwin_arm64";
  };
  isLinux = stdenvNoCC.hostPlatform.isLinux;
in
stdenvNoCC.mkDerivation (finalAttrs: {
  pname = "aibox";
  inherit (release) version;

  src = fetchurl {
    url = "https://github.com/The127/aibox/releases/download/v${finalAttrs.version}/aibox_${finalAttrs.version}_${
      platforms.${system} or (throw "aibox has no release for ${system}")
    }.tar.gz";
    hash = release.hashes.${system};
  };

  sourceRoot = ".";

  nativeBuildInputs = lib.optionals isLinux [ makeBinaryWrapper ];

  installPhase = ''
    runHook preInstall
    install -Dm755 aibox $out/bin/aibox
  ''
  + lib.optionalString isLinux ''
    wrapProgram $out/bin/aibox --prefix PATH : ${
      lib.makeBinPath [
        qemu_kvm
        virtiofsd
        bubblewrap
      ]
    }
  ''
  + ''
    runHook postInstall
  '';

  # the signature covers the bytes of the binary, and macOS starts it with
  # the entitlement only while they are as released, so nothing strips it
  dontStrip = true;

  meta = {
    description = "Run Claude Code inside a microVM, with your project folder mounted into it";
    homepage = "https://github.com/The127/aibox";
    license = lib.licenses.asl20;
    sourceProvenance = [ lib.sourceTypes.binaryNativeCode ];
    platforms = builtins.attrNames platforms;
    mainProgram = "aibox";
  };
})
