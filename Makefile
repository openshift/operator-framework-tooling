include .bingo/Variables.mk

.PHONY: test
test:
	go test -mod=mod ./...

.PHONY: build
build:
	go build -mod=mod ./cmd/...

.PHONY: lint
lint: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) run $(GOLANGCI_LINT_ARGS)