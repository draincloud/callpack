GOLANGCI_VERSION ?= v2.13.2
ACTIONLINT_VERSION ?= v1.7.12

GOBIN ?= $(or $(shell go env GOBIN),$(shell go env GOPATH)/bin)

.PHONY: test lint install-tools

test: 
	go test -v ./...

lint:
	$(GOBIN)/golangci-lint run ./...
	$(GOBIN)/actionlint

install-tools:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
	go install github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

print-%:
	@echo $($*)
