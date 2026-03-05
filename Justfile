set shell := ["bash", "-euo", "pipefail", "-c"]

default:
  @just --list

generate-models:
  go run ./cmd/modelcataloggen -out ./pkg/ai/models_generated.go

build:
  just generate-models
  go build -o gopher ./cmd/gopher

# Apply Go formatting to all packages.
fmt:
  go fmt ./...

# Verify files are gofmt-formatted (CI parity).
fmt-check:
  @files="$(gofmt -l .)"; \
  if [ -n "$files" ]; then \
    echo "these files are not gofmt-formatted:"; \
    echo "$files"; \
    exit 1; \
  fi

vet:
  go vet ./...

staticcheck-install:
  go install honnef.co/go/tools/cmd/staticcheck@v0.6.1

staticcheck:
  staticcheck ./...

govulncheck:
  go run golang.org/x/vuln/cmd/govulncheck@latest ./...

mod-check:
  go mod tidy -diff

workflow-lint:
  go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.11

lint: fmt-check vet staticcheck govulncheck mod-check workflow-lint
