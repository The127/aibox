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

# lint
lint:
    golangci-lint run ./...

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
    go run golang.org/x/vuln/cmd/govulncheck@latest ./...

# check that every file has license and copyright information
reuse:
    uvx reuse lint

# everything that must pass before a push
ci: lint arch reuse build cover vuln
