.PHONY: all check build migrate test test-race test-integration fuzz compose-up compose-down \
        lint lint-fix vuln align align-fix nilaway tidy-check fmt fmt-check tidy docs docs-build \
        changelog release-check clean

GO ?= go
GOLANGCI_LINT ?= golangci-lint
TIMEOUT ?= 5m

# Pinned so a local run matches CI. Bump these together with .github/workflows/ci.yml.
BETTERALIGN_VERSION ?= v0.14.3
GORELEASER_VERSION ?= v2

# Stamped into the binary so `migrate --version` reports something meaningful.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

all: lint test build

# Everything CI runs, in the order CI runs it. This is the pre-PR command.
check: fmt-check tidy-check lint vuln align nilaway test

build:
	$(GO) build -v -ldflags "$(LDFLAGS)" ./...

# The CLI binary, with the version stamped in.
migrate:
	$(GO) build -ldflags "$(LDFLAGS)" -o migrate ./cmd/migrate

# Fast unit tests. No Docker required.
test:
	$(GO) test -count=1 -timeout $(TIMEOUT) ./...

test-race:
	$(GO) test -count=1 -race -timeout $(TIMEOUT) ./...

# Fuzz the parsers that decide what SQL actually runs. FUZZTIME is short by
# default so it fits a pre-commit loop; CI runs each target separately.
# A crasher is written to internal/migrator/testdata/fuzz/ — commit it, it
# becomes a regression case replayed by `make test`.
FUZZTIME ?= 30s
FUZZ_TARGETS := FuzzSplitSQLStatements FuzzParseDirectiveHeader FuzzQuoteIdentifier

fuzz:
	@for target in $(FUZZ_TARGETS); do \
		echo "--> $$target ($(FUZZTIME))"; \
		$(GO) test ./internal/migrator/ -run '^$$' -fuzz "^$$target$$" -fuzztime=$(FUZZTIME) || exit 1; \
	done

# Integration tests (test/, build tag "integration") run against a shared
# PostgreSQL instance: docker-compose locally, a service container in CI.
# Each test creates and drops its own database for isolation.
compose-up:
	docker compose up -d --wait

compose-down:
	docker compose down -v

test-integration: compose-up
	$(GO) test -tags=integration -count=1 -timeout $(TIMEOUT) ./test/...; status=$$?; $(MAKE) compose-down; exit $$status

lint:
	$(GOLANGCI_LINT) run ./...
	$(GOLANGCI_LINT) run --build-tags=integration ./test/...

lint-fix:
	$(GOLANGCI_LINT) run --fix ./...
	$(GOLANGCI_LINT) run --fix --build-tags=integration ./test/...

# Known vulnerabilities in the dependency tree and the Go toolchain.
vuln:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...

# Struct field ordering. `make align-fix` rewrites the files.
align:
	$(GO) run github.com/dkorunic/betteralign/cmd/betteralign@$(BETTERALIGN_VERSION) ./...

align-fix:
	$(GO) run github.com/dkorunic/betteralign/cmd/betteralign@$(BETTERALIGN_VERSION) -apply ./...

nilaway:
	$(GO) run go.uber.org/nilaway/cmd/nilaway@latest ./...

# Fails when go.mod/go.sum are not what `go mod tidy` would produce.
tidy-check:
	$(GO) mod tidy -diff

fmt:
	$(GOLANGCI_LINT) fmt ./...

fmt-check:
	$(GOLANGCI_LINT) fmt --diff ./...

tidy:
	$(GO) mod tidy

# Documentation site (mkdocs-material). Needs: pip install -r requirements-docs.txt
docs:
	mkdocs serve

docs-build:
	mkdocs build --strict

# Preview the changelog the next release would publish.
changelog:
	git cliff --unreleased

# Dry-run the release pipeline without publishing anything.
release-check:
	$(GO) run github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION) check
	$(GO) run github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION) release --snapshot --clean

clean:
	rm -f migrate
	rm -rf dist site
	$(GO) clean -testcache
