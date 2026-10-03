# list available recipes
default:
    @just --list

# build the static binary
build:
    CGO_ENABLED=0 go build -o bin/aibox ./cmd/aibox

# build the VM image into out/
image:
    miso build -o out image

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

# everything that must pass before a push
ci: lint arch build cover vuln
