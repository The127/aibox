# list available recipes
default:
    @just --list

# build the static binary
[linux]
build:
    CGO_ENABLED=0 go build -o bin/aibox ./cmd/aibox

# build the binary with cgo for Virtualization.framework, which runs it only signed with the entitlement
[macos]
build: && (sign "bin/aibox")
    CGO_ENABLED=1 go build -o bin/aibox ./cmd/aibox

# sign a build of aibox with the entitlement Virtualization.framework asks for
[macos]
[private]
sign path:
    codesign --force --sign - --entitlements cmd/aibox/aibox.entitlements {{path}}

# list the packages aibox is built from on the system, which is all that builds there
[private]
packages goos goarch:
    @GOOS={{goos}} GOARCH={{goarch}} go list -deps -f '{{{{if and .Module .Module.Main}}.{{{{slice .ImportPath (len .Module.Path)}}{{{{end}}' ./cmd/aibox

# the architecture of the VM: QEMU on Linux runs amd64, Virtualization.framework on macOS arm64
vm-arch := if os() == "macos" { "arm64" } else { "amd64" }

# build the init of the VM, which the image copies in
init arch=vm-arch:
    GOOS=linux GOARCH={{arch}} CGO_ENABLED=0 go build -o image/aibox-init ./cmd/aibox-init

# build the VM image into out/
[linux]
image: init
    miso build -o out image

# build the VM image for arm64 into out/, with Apple's container tool until miso runs on macOS
[macos]
image: init
    rm -rf out/build
    container build --file image/Containerfile --target out --output type=local,dest=out/build --cpus 8 --memory 8g image
    @# the tool puts its files in folders of its own below the destination
    for f in vmlinuz os.ext4; do \
      found=$(find out/build -type f -name "$f"); \
      [ "$(printf '%s\n' "$found" | grep -c .)" = 1 ] || { echo "the build left no single $f in out/build"; exit 1; }; \
      mv "$found" out/; \
    done
    rm -rf out/build

# build the VM image for arm64 into out/ with docker, as the release does on an arm64 Linux runner
image-docker-arm64: (init "arm64")
    docker build --file image/Containerfile --target out --output type=local,dest=out image

# pack the VM image in out/ into the archive a release carries for the architecture
# with only the two files: tar of macOS would add the extended attributes as ._ files
image-archive arch:
    COPYFILE_DISABLE=1 tar -czf out/aibox-image_{{arch}}.tar.gz -C out vmlinuz os.ext4

# build aibox and run it on this repo with the image from out/
aibox *args: build
    ./bin/aibox run --image out {{args}}

# copy the VM image to where aibox run looks for it
[linux]
install-image: image
    mkdir -p ~/.aibox/image
    cp --reflink=auto out/vmlinuz out/os.ext4 ~/.aibox/image/

# copy the VM image to where aibox run looks for it, as a clone on APFS
[macos]
install-image: image
    mkdir -p ~/.aibox/image
    cp -c out/vmlinuz out/os.ext4 ~/.aibox/image/

# install the aibox binary into Go's bin folder and the image into ~/.aibox
[linux]
install: install-image
    CGO_ENABLED=0 go install ./cmd/aibox

# install aibox where go install puts programs, built with cgo and signed, and the image into ~/.aibox
[macos]
install: install-image
    #!/bin/sh
    set -e
    bin=$(go env GOBIN)
    bin=${bin:-$(go env GOPATH)/bin}
    mkdir -p "$bin"
    CGO_ENABLED=1 go build -o "$bin/aibox" ./cmd/aibox
    just sign "$bin/aibox"

# test
test:
    go test -race ./...

# test with a coverage profile
cover:
    go test -race -coverprofile=coverage.out -covermode=atomic ./...

# lint all code for Linux, and for macOS and Windows the host side, which is all that builds there
lint:
    GOOS=linux GOARCH=amd64 golangci-lint run ./...
    GOOS=darwin GOARCH=arm64 golangci-lint run $(just packages darwin arm64)
    GOOS=windows GOARCH=amd64 golangci-lint run $(just packages windows amd64)

# format
fmt:
    golangci-lint fmt ./...

# check the package dependency rules in arch-go.yml
arch:
    go tool -modfile=hack/tools/go.mod arch-go

# describe the package dependency rules in prose
arch-describe:
    go tool -modfile=hack/tools/go.mod arch-go describe

# validate the goreleaser config
release-check:
    go run github.com/goreleaser/goreleaser/v2@latest check

# build the release artifacts into dist/ without publishing or signing, which needs a Mac for the macOS binary
release-snapshot:
    HOMEBREW_TAP_TOKEN=none go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean --skip=sign

# check for known vulnerabilities in reachable code
vuln:
    go tool -modfile=hack/tools/go.mod govulncheck ./...

# check that every file has license and copyright information
reuse:
    reuse lint

# check that no em dash is in the repository
prose:
    bash hack/check-prose.sh

# test the check of the kernel configurations
kernel-config:
    sh image/check-kernel-config-test

# build the documentation site into docs/book
docs:
    mdbook build docs

# install the git hooks
hooks:
    lefthook install

# one-time setup after cloning
setup: hooks

# everything that must pass before a push
ci: lint arch reuse prose kernel-config docs build cover vuln
