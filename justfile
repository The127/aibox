# list available recipes
default:
    @just --list

# build the static binary
build:
    CGO_ENABLED=0 go build -o bin/aibox ./cmd/aibox

# build the init of the VM, which the image copies in
init:
    GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o image/aibox-init ./cmd/aibox-init

# build the VM image into out/
image: init
    miso build -o out image

# build aibox and run it on this repo with the image from out/
aibox *args: build
    ./bin/aibox run --image out {{args}}

# copy the VM image to where aibox run looks for it
install-image: image
    mkdir -p ~/.aibox/image
    cp --reflink=auto out/vmlinuz out/os.ext4 ~/.aibox/image/

# install the aibox binary into Go's bin folder and the image into ~/.aibox
install: install-image
    CGO_ENABLED=0 go install ./cmd/aibox

# test
test:
    go test -race ./...

# test with a coverage profile
cover:
    go test -race -coverprofile=coverage.out -covermode=atomic ./...

# lint all code for Linux, and for macOS the host side, which is all that builds there
lint:
    GOOS=linux GOARCH=amd64 golangci-lint run ./...
    GOOS=darwin GOARCH=arm64 golangci-lint run $(GOOS=darwin GOARCH=arm64 go list -deps -f '{{{{if and .Module .Module.Main}}.{{{{slice .ImportPath (len .Module.Path)}}{{{{end}}' ./cmd/aibox)

# format
fmt:
    golangci-lint fmt ./...

# check the package dependency rules in arch-go.yml
arch:
    go tool -modfile=hack/tools/go.mod arch-go

# describe the package dependency rules in prose
arch-describe:
    go tool -modfile=hack/tools/go.mod arch-go describe

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
