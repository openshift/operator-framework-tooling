default: build

.PHONY: test
test:
	go test -mod=mod ./...

.PHONY: build
build:
	go build -o ./ -mod=mod ./cmd/...

.PHONY: lint
lint:
	go tool golangci-lint run $(GOLANGCI_LINT_ARGS)
