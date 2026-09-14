VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo devel)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null)
DATE    ?= $(shell git log -1 --format=%cI 2>/dev/null)
PKG     := github.com/SaiPisey2/kubectl-survive/internal/version
LDFLAGS := -s -w -X $(PKG).version=$(VERSION) -X $(PKG).commit=$(COMMIT) -X $(PKG).date=$(DATE)

.PHONY: build test verify-deps snapshot release-check clean
build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/kubectl-survive ./cmd/kubectl-survive
test:
	go test ./... -count=1
verify-deps:
	@if go list -deps ./... | grep -q '^k8s.io/kubernetes'; then \
		echo "ERROR: k8s.io/kubernetes must not be a dependency before milestone 3"; exit 1; fi
	@echo "dependency guard: ok"
release-check:
	goreleaser check
# Full cross-platform build with no publishing, including the krew manifest.
snapshot:
	goreleaser release --snapshot --clean --skip=sbom
clean:
	rm -rf bin dist
