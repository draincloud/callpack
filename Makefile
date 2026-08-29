GOLANGCI_VERSION ?= v2.13.2
ACTIONLINT_VERSION ?= v1.7.12

GOBIN ?= $(or $(shell go env GOBIN),$(shell go env GOPATH)/bin)

# Discovered rather than listed, so a new module is covered without editing this file.
MODULES := $(patsubst ./%/go.mod,%,$(shell find . -name go.mod -not -path './.git/*' | sort))

# GOWORK=off so each module is exercised the way a consumer gets it, resolved from its
# own go.mod rather than through the workspace.
.PHONY: test lint install-tools

test:
	@for m in $(MODULES); do \
		echo "== $$m"; \
		(cd $$m && GOWORK=off go test -v ./...) || exit 1; \
	done

lint:
	@for m in $(MODULES); do \
		echo "== $$m"; \
		(cd $$m && GOWORK=off $(GOBIN)/golangci-lint run ./...) || exit 1; \
	done
	$(GOBIN)/actionlint

install-tools:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
	go install github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

print-%:
	@echo $($*)
