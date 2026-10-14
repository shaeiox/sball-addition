# sball-addition - managed hook system for the sballd (PVM) service
#
# Common targets:
#   make build            build bin/sball-addition
#   make test             vet + unit tests + integration tests
#   make install          install with default settings (no systemd changes)
#   make install-systemd  install and point sballd.service at the wrapper
#   make tarball          release/sball-addition-<version>.tar.gz (binary + all files)
#   make rpm              RPM via rpmbuild (uses the tarball)
#   make clean

NAME      := sball-addition
VERSION   ?= 1.0.0
RELEASE   ?= 1
COMMIT    := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILDDATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

GO        ?= go
GOFLAGS   ?=
# Static binary: no glibc version dependency, so one build runs on
# RHEL/CentOS 7 and newer. Override with "make CGO_ENABLED=1 build" if needed.
CGO_ENABLED ?= 0
LDFLAGS   := -s -w \
	-X $(NAME)/internal/version.Version=$(VERSION) \
	-X $(NAME)/internal/version.Commit=$(COMMIT) \
	-X $(NAME)/internal/version.BuildDate=$(BUILDDATE)

SHELL     := /bin/bash
BIN       := bin/$(NAME)
DIST      := release
INSTALL_ARGS ?=

.PHONY: all build fmt vet unit integration test cover clean install install-systemd uninstall tarball rpm spec-verify hooks-check man-check help

all: build

build: ## build the binary
	@mkdir -p bin
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build $(GOFLAGS) -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/sball-addition
	@echo "built $(BIN)"

fmt: ## gofmt the tree
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

vet: ## go vet
	$(GO) vet ./...

unit: ## unit tests
	$(GO) test ./... -count=1

integration: build ## end-to-end tests with a fake PVM socket
	./test/integration/run.sh

test: vet unit integration ## everything

cover: ## unit tests with coverage
	$(GO) test ./... -coverprofile=coverage.out -count=1
	$(GO) tool cover -func=coverage.out | tail -1

hooks-check: build ## validate the shipped sample hooks
	$(BIN) --root=/ list --phase=pre-quorate || true

man-check: ## render the man page with groff when available
	@if command -v groff >/dev/null 2>&1; then \
		groff -man -Tutf8 -z docs/sball-addition.8 && echo "man page ok"; \
	else \
		echo "groff not installed, skipping"; \
	fi

install: build ## install (binary, config, hooks, samples)
	./scripts/install.sh --binary=$(BIN) $(INSTALL_ARGS)

install-systemd: build ## install and wire up systemd
	./scripts/install.sh --binary=$(BIN) --with-systemd --enable-service $(INSTALL_ARGS)

uninstall: ## remove everything
	./scripts/uninstall.sh

tarball: build ## build a distributable tarball
	@STAGE=$(DIST)/$(NAME)-$(VERSION); \
	rm -rf "$$STAGE"; \
	for d in bin etc lib scripts docs hooks/pre-quorate.d hooks/post-quorate.d \
	         packaging/systemd/sballd.service.d packaging/logrotate.d packaging/sysconfig \
	         test/integration; do mkdir -p "$$STAGE/$$d"; done; \
	cp -a $(BIN) "$$STAGE/bin/"; \
	cp -a etc/sball-addition.conf "$$STAGE/etc/"; \
	cp -a lib/common.sh "$$STAGE/lib/"; \
	cp -a scripts/sballd-wrapper scripts/install.sh scripts/uninstall.sh "$$STAGE/scripts/"; \
	cp -a packaging/systemd/*.service "$$STAGE/packaging/systemd/"; \
	cp -a packaging/systemd/sballd.service.d "$$STAGE/packaging/systemd/"; \
	cp -a packaging/logrotate.d/sball-addition "$$STAGE/packaging/logrotate.d/"; \
	cp -a packaging/sysconfig/sball-addition "$$STAGE/packaging/sysconfig/"; \
	cp -a packaging/$(NAME).spec "$$STAGE/packaging/"; \
	cp -a hooks/pre-quorate.d/*.sh "$$STAGE/hooks/pre-quorate.d/"; \
	cp -a hooks/post-quorate.d/*.sh "$$STAGE/hooks/post-quorate.d/"; \
	cp -a docs/sball-addition.8 docs/hook-template.sh "$$STAGE/docs/"; \
	cp -a test/integration/run.sh test/integration/fake-pvm.py "$$STAGE/test/integration/"; \
	cp -a README.md Makefile "$$STAGE/"; \
	printf 'sball-addition %s\ncommit %s\nbuilt %s\n' "$(VERSION)" "$(COMMIT)" "$(BUILDDATE)" > "$$STAGE/BUILDINFO"; \
	tar -C $(DIST) -czf $(DIST)/$(NAME)-$(VERSION).tar.gz $(NAME)-$(VERSION); \
	echo "wrote $(DIST)/$(NAME)-$(VERSION).tar.gz"

rpm: tarball ## build an RPM (requires rpm-build)
	@command -v rpmbuild >/dev/null 2>&1 || { echo "rpmbuild not found" >&2; exit 1; }
	@mkdir -p $(DIST)/rpmbuild/{BUILD,BUILDROOT,RPMS,SOURCES,SPECS,SRPMS}
	@cp $(DIST)/$(NAME)-$(VERSION).tar.gz $(DIST)/rpmbuild/SOURCES/
	@rpmbuild -bb packaging/$(NAME).spec \
		--define "_topdir $(CURDIR)/$(DIST)/rpmbuild" \
		--define "_dbpath $(CURDIR)/$(DIST)/rpmbuild/db" \
		--define "version $(VERSION)" \
		--define "releasenumber $(RELEASE)"
	@find $(DIST)/rpmbuild/RPMS -name '*.rpm' -exec cp {} $(DIST)/ \;
	@ls -l $(DIST)/*.rpm

clean: ## remove build artefacts
	rm -rf bin $(DIST) coverage.out

help: ## list targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'
