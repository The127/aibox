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

# list the packages aibox is built from on macOS, which is all that builds there
[private]
macos-packages:
    @GOOS=darwin GOARCH=arm64 go list -deps -f '{{{{if and .Module .Module.Main}}.{{{{slice .ImportPath (len .Module.Path)}}{{{{end}}' ./cmd/aibox

# build the init of the VM, which the image copies in
[linux]
init:
    GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o image/aibox-init ./cmd/aibox-init

# build the init of the VM for arm64, which the image copies in
[macos]
init:
    GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o image/aibox-init ./cmd/aibox-init

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

# lint all code for Linux, and for macOS the host side, which is all that builds there
lint:
    GOOS=linux GOARCH=amd64 golangci-lint run ./...
    GOOS=darwin GOARCH=arm64 golangci-lint run $(just macos-packages)

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

# install the git hooks
hooks:
    lefthook install

# one-time setup after cloning
setup: hooks

# everything that must pass before a push
ci: lint arch reuse prose build cover vuln
