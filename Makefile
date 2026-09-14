.PHONY: build test verify-deps
build:
	CGO_ENABLED=0 go build -o bin/kubectl-survive ./cmd/kubectl-survive
test:
	go test ./... -count=1
verify-deps:
	@if go list -deps ./... | grep -q '^k8s.io/kubernetes'; then \
		echo "ERROR: k8s.io/kubernetes must not be a dependency before milestone 3"; exit 1; fi
	@echo "dependency guard: ok"
