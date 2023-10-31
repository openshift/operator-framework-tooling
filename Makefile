include .bingo/Variables.mk

.PHONY: test
test:
	go test ./...

.PHONY: build
build:
	go build ./cmd/...

.PHONY: lint
lint: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) run $(GOLANGCI_LINT_ARGS)