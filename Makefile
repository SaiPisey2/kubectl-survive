VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo devel)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null)
DATE    ?= $(shell git log -1 --format=%cI 2>/dev/null)
PKG     := github.com/SaiPisey2/kubectl-survive/internal/version
LDFLAGS := -s -w -X $(PKG).version=$(VERSION) -X $(PKG).commit=$(COMMIT) -X $(PKG).date=$(DATE)

.PHONY: build test verify-deps verify-replaces snapshot release-check clean
build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/kubectl-survive ./cmd/kubectl-survive
test:
	go test ./... -count=1
# The analysis core must stay free of the scheduler tree: spec §7.2 promises
# survivability verdicts keep working when the scheduler checks are disabled on
# a version mismatch, and that promise is only real if the code cannot import it.
#
# internal/fix is in the list for a second reason: generating a remediation
# candidate is pure, and keeping it that way is what lets the whole ladder be
# tested without a cluster. Proving a candidate needs the scheduler, so that
# lives in internal/verify instead.
CORE := ./internal/domain/... ./internal/health/... ./internal/snapshot/... \
        ./internal/workload/... ./internal/pdbcheck/... ./internal/spread/... \
        ./internal/volumepin/... ./internal/survive/... ./internal/fix/...

verify-deps:
	@if go list -deps $(CORE) | grep -q '^k8s.io/kubernetes'; then \
		echo "ERROR: the survivability core must not import k8s.io/kubernetes;"; \
		echo "       the scheduler belongs behind internal/sched"; exit 1; fi
	@echo "core dependency guard: ok"

verify-replaces:
	@hack/verify-replaces.sh

# Authorship is a hard requirement and tooling defaults try to add trailers,
# so this is a control rather than a matter of care. BASE defaults to
# origin/main; override for a branch built from something else.
verify-attribution:
	@hack/verify-attribution.sh $(BASE)
release-check:
	goreleaser check
# Full cross-platform build with no publishing, including the krew manifest.
snapshot:
	goreleaser release --snapshot --clean --skip=sbom
clean:
	rm -rf bin dist
