# gormgate. Run `make help` for the list.
#
# The repository is several Go modules: the library at the root, the
# integration suite in itest/, the Django parity project in parity/goproj/
# and one module per example under examples/. Targets that say "all
# modules" cover all of them.

GO       ?= go
# Globbed so that adding an example needs no edit here; CI globs too.
EXAMPLES := $(wildcard examples/*/)
MODULES  := . itest parity/goproj $(EXAMPLES)
DOCSIMG  ?= squidfunk/mkdocs-material:9.5.44
# staticcheck is installed on demand; GOPATH/bin is not assumed to be on PATH.
GOBIN    := $(shell $(GO) env GOPATH)/bin
STATICCHECK := $(GOBIN)/staticcheck

.PHONY: help build test lint fmt fmt-check vet staticcheck docs docs-cli \
	docs-cli-check docs-serve integration parity itest-up itest-down

## help: list the targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'

## build: compile every module
build:
	@for m in $(MODULES); do (cd $$m && $(GO) build ./...) || exit 1; done

## test: unit tests; no database needed
test:
	$(GO) test ./...

## lint: gofmt, go vet and staticcheck over every module
lint: fmt-check vet staticcheck docs-cli-check

fmt:
	gofmt -w $(MODULES)

fmt-check:
	@unformatted=$$(gofmt -l $(MODULES)); \
	if [ -n "$$unformatted" ]; then echo "not gofmt'd:"; echo "$$unformatted"; exit 1; fi

vet:
	$(GO) vet ./...
	cd itest && $(GO) vet -tags integration ./...
	cd itest && $(GO) vet -tags 'parity integration' ./...
	cd parity/goproj && $(GO) vet ./...
	@for m in $(EXAMPLES); do (cd $$m && $(GO) vet ./...) || exit 1; done

staticcheck: $(STATICCHECK)
	$(STATICCHECK) ./...
	cd itest && $(STATICCHECK) ./... && $(STATICCHECK) -tags integration ./... \
		&& $(STATICCHECK) -tags 'parity integration' ./...
	cd parity/goproj && $(STATICCHECK) ./...
	@for m in $(EXAMPLES); do (cd $$m && $(STATICCHECK) ./...) || exit 1; done

# Pinned to the version CI uses, so that a new staticcheck release cannot
# disagree with the build here.
STATICCHECK_VERSION ?= 2026.2.1

$(STATICCHECK):
	$(GO) install honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)

## itest-up: start every database service and wait until healthy
itest-up:
	$(MAKE) -C itest up

## itest-down: stop the database services
itest-down:
	$(MAKE) -C itest down

## integration: conformance and gorm parity on one vendor (VENDOR=pg18)
VENDOR ?= pg18
integration:
	cd itest && GORMGATE_ITEST_REQUIRE=1 GORMGATE_ITEST_VENDORS=$(VENDOR) \
		$(GO) test -tags integration -run 'TestSmoke|TestConformance|TestGormParity|TestPropertyEvolution' -count=1 .

## parity: run the same scenarios against a real Django 6.0 (needs Docker)
parity:
	cd itest && GORMGATE_ITEST_REQUIRE=1 \
		$(GO) test -tags 'parity integration' -run TestDjangoParity -count=1 -v .

## docs-cli: regenerate the usage blocks in docs/commands from the real CLI
docs-cli:
	scripts/gen-cli-docs.sh

docs-cli-check:
	scripts/gen-cli-docs.sh --check

## docs: build the documentation site into site/ (--strict, so a broken
##       link or a missing code snippet fails)
docs:
	docker run --rm -v "$(CURDIR)":/docs $(DOCSIMG) build --strict

## docs-serve: serve the documentation at http://localhost:8000
docs-serve:
	docker run --rm -it -p 8000:8000 -v "$(CURDIR)":/docs $(DOCSIMG)
