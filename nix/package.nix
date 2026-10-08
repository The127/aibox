# aibox as released: the static Linux binary of the release in
# release.json, with QEMU, virtiofsd and bubblewrap from nixpkgs in front
# of its PATH. The VM image is downloaded on the first run, as with the
# other packages.
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
in
stdenvNoCC.mkDerivation (finalAttrs: {
  pname = "aibox";
  inherit (release) version;

  src = fetchurl {
    url = "https://github.com/The127/aibox/releases/download/v${finalAttrs.version}/aibox_${finalAttrs.version}_linux_amd64.tar.gz";
    inherit (release) hash;
  };

  sourceRoot = ".";

  nativeBuildInputs = [ makeBinaryWrapper ];

  installPhase = ''
    runHook preInstall
    install -Dm755 aibox $out/bin/aibox
    wrapProgram $out/bin/aibox --prefix PATH : ${
      lib.makeBinPath [
        qemu_kvm
        virtiofsd
        bubblewrap
      ]
    }
    runHook postInstall
  '';

  meta = {
    description = "Run Claude Code inside a microVM, with your project folder mounted into it";
    homepage = "https://github.com/The127/aibox";
    license = lib.licenses.asl20;
    sourceProvenance = [ lib.sourceTypes.binaryNativeCode ];
    platforms = [ "x86_64-linux" ];
    mainProgram = "aibox";
  };
})
