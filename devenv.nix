# The tools the justfile needs. With direnv they are on PATH in this folder,
# and aibox takes them into the VM over /nix/store.
{ pkgs, ... }:
{
  languages.go = {
    enable = true;
    package = pkgs.go_1_27;
  };

  packages = with pkgs; [
    golangci-lint
    just
    lefthook
    uv
    # go test -race needs a C compiler
    gcc
  ];
}
