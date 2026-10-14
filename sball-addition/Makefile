# sball-addition - managed hook system for the sballd (PVM) service
#
# Common targets:
#   make build            build bin/sball-addition
#   make test             vet + unit tests + integration tests
#   make install          install with default settings (no systemd changes)
#   make install-systemd  install and point sballd.service at the wrapper
#   make tarball          release/sball-addition-<version>.tar.gz (binary + all files)
#   make rpm              RPM via rpmbuild (uses the tarball)
#   make deb              DEB via dpkg-deb (no debhelper needed)
#   make bundle           release/...-bundle/: tarball + RPM + DEB + SHA256SUMS
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
DEB_ARCH  ?= $(shell uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
LDFLAGS   := -s -w \
	-X $(NAME)/internal/version.Version=$(VERSION) \
	-X $(NAME)/internal/version.Commit=$(COMMIT) \
	-X $(NAME)/internal/version.BuildDate=$(BUILDDATE)

SHELL     := /bin/bash
BIN       := bin/$(NAME)
DIST      := release
INSTALL_ARGS ?=

.PHONY: all modes build fmt vet unit integration test cover clean install install-systemd uninstall tarball rpm deb bundle spec-verify hooks-check man-check help

all: build

modes: ## restore the executable bits (after a copy/zip that dropped them)
	@chmod +x scripts/*.sh scripts/sballd-wrapper 2>/dev/null || true
	@chmod +x test/integration/run.sh test/integration/fake-pvm.py 2>/dev/null || true
	@chmod +x docs/hook-template.sh lib/common.sh 2>/dev/null || true
	@chmod +x hooks/pre-quorate.d/*.sh hooks/post-quorate.d/*.sh 2>/dev/null || true

build: modes ## build the binary
	@mkdir -p bin
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build $(GOFLAGS) -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/sball-addition
	@echo "built $(BIN)"

fmt: ## gofmt the tree
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

vet: ## go vet
	$(GO) vet ./...

unit: ## unit tests
	$(GO) test ./... -count=1

integration: build modes ## end-to-end tests with a fake PVM socket
	./test/integration/run.sh

test: vet unit man-check integration ## everything

cover: ## unit tests with coverage
	$(GO) test ./... -coverprofile=coverage.out -count=1
	$(GO) tool cover -func=coverage.out | tail -1

hooks-check: build ## validate the shipped sample hooks
	$(BIN) --root=/ list --phase=pre-quorate || true

man-check: ## render every man page with groff when available
	@if command -v groff >/dev/null 2>&1; then \
		for page in docs/*.8; do groff -man -Tutf8 -z "$$page" || exit 1; done; \
		echo "man pages ok"; \
	else \
		echo "groff not installed, skipping"; \
	fi

install: build modes ## install (binary, config, hooks, samples)
	./scripts/install.sh --binary=$(BIN) $(INSTALL_ARGS)

install-systemd: build modes ## install and wire up systemd
	./scripts/install.sh --binary=$(BIN) --with-systemd --enable-service $(INSTALL_ARGS)

uninstall: ## remove everything
	./scripts/uninstall.sh

tarball: build modes ## build a distributable tarball
	@STAGE=$(DIST)/$(NAME)-$(VERSION); \
	rm -rf "$$STAGE"; \
	for d in bin etc lib scripts docs hooks/pre-quorate.d hooks/post-quorate.d \
	         packaging/systemd/sballd.service.d packaging/logrotate.d packaging/sysconfig \
	         packaging/debian deploy/ansible test/integration; do mkdir -p "$$STAGE/$$d"; done; \
	cp -a $(BIN) "$$STAGE/bin/"; \
	cp -a etc/sball-addition.conf "$$STAGE/etc/"; \
	cp -a lib/common.sh "$$STAGE/lib/"; \
	cp -a scripts/sballd-wrapper scripts/install.sh scripts/uninstall.sh \
	      scripts/verify-node.sh scripts/rollout.sh "$$STAGE/scripts/"; \
	cp -a packaging/systemd/*.service packaging/systemd/*.timer "$$STAGE/packaging/systemd/"; \
	cp -a packaging/systemd/sballd.service.d "$$STAGE/packaging/systemd/"; \
	cp -a packaging/logrotate.d/sball-addition "$$STAGE/packaging/logrotate.d/"; \
	cp -a packaging/sysconfig/sball-addition "$$STAGE/packaging/sysconfig/"; \
	cp -a packaging/$(NAME).spec packaging/deploy-notes.txt "$$STAGE/packaging/"; \
	cp -a packaging/debian/*.sh packaging/debian/postinst packaging/debian/prerm \
	      packaging/debian/postrm packaging/debian/changelog packaging/debian/copyright \
	      packaging/debian/lintian-overrides "$$STAGE/packaging/debian/"; \
	cp -a deploy/ansible/sball-addition.yml "$$STAGE/deploy/ansible/"; \
	cp -a hooks/pre-quorate.d/*.sh "$$STAGE/hooks/pre-quorate.d/"; \
	cp -a hooks/post-quorate.d/*.sh "$$STAGE/hooks/post-quorate.d/"; \
	cp -a docs/*.8 docs/hook-template.sh "$$STAGE/docs/"; \
	cp -a test/integration/run.sh test/integration/fake-pvm.py "$$STAGE/test/integration/"; \
	cp -a README.md PRODUCTION.md Makefile "$$STAGE/"; \
	printf 'sball-addition %s\ncommit %s\nbuilt %s\n' "$(VERSION)" "$(COMMIT)" "$(BUILDDATE)" > "$$STAGE/BUILDINFO"; \
	tar -C $(DIST) -czf $(DIST)/$(NAME)-$(VERSION).tar.gz $(NAME)-$(VERSION); \
	echo "wrote $(DIST)/$(NAME)-$(VERSION).tar.gz"

bundle: tarball rpm deb ## production bundle: tarball + RPM + DEB + SHA256SUMS + notes
	@STAGE=$(DIST)/$(NAME)-$(VERSION)-bundle; \
	rm -rf "$$STAGE"; mkdir -p "$$STAGE"; \
	cp $(DIST)/$(NAME)-$(VERSION).tar.gz $(DIST)/$(NAME)-$(VERSION)-$(RELEASE)*.rpm "$$STAGE/" 2>/dev/null || \
	    cp $(DIST)/$(NAME)-$(VERSION).tar.gz $(DIST)/*.rpm "$$STAGE/"; \
	cp $(DIST)/$(NAME)_$(VERSION)-$(RELEASE)_*.deb "$$STAGE/" 2>/dev/null || true; \
	cp PRODUCTION.md "$$STAGE/README-DEPLOY.md" 2>/dev/null || true; \
	( cd "$$STAGE" && sha256sum * > SHA256SUMS ); \
	VERSION="$(VERSION)" COMMIT="$(COMMIT)" BUILDDATE="$(BUILDDATE)" \
	    envsubst < packaging/deploy-notes.txt > "$$STAGE/DEPLOY-NOTES.txt" 2>/dev/null || \
	    cp packaging/deploy-notes.txt "$$STAGE/DEPLOY-NOTES.txt"; \
	echo "wrote $$STAGE/ (verify with: cd $$STAGE && sha256sum -c SHA256SUMS)"

deb: build ## build a DEB (requires dpkg-deb; no debhelper needed)
	@VERSION=$(VERSION) RELEASE=$(RELEASE) ARCH=$(DEB_ARCH) DIST=$(DIST) BINARY=$(BIN) \
	    ./packaging/debian/build-deb.sh

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
