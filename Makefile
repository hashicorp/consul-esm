# Metadata about this makefile and position
MKFILE_PATH := $(lastword $(MAKEFILE_LIST))
CURRENT_DIR := $(patsubst %/,%,$(dir $(realpath $(MKFILE_PATH))))

# System information
GOOS=$(shell go env GOOS)
GOARCH=$(shell go env GOARCH)
GOPATH=$(shell go env GOPATH)

# Project information
GOVERSION := 1.10
PROJECT := $(CURRENT_DIR:$(GOPATH)/src/%=%)
NAME := $(notdir $(PROJECT))
GIT_COMMIT ?= $(shell git rev-parse --short HEAD)
GIT_DESCRIBE ?= $(shell git describe --tags --always)
VERSION := $(shell awk -F\" '/Version/ { print $$2; exit }' "${CURRENT_DIR}/version/version.go")

# Tags specific for building
GOTAGS ?=

# Number of procs to use
GOMAXPROCS ?= 4

# List all our actual files, excluding vendor
GOFILES ?= $(shell go list ./... | grep -v /vendor/)

# Default os-arch combination to build
XC_OS ?= darwin freebsd linux netbsd openbsd solaris windows
XC_ARCH ?= 386 amd64 arm
XC_EXCLUDE ?= darwin/arm solaris/386 solaris/arm windows/arm netbsd/arm

# List of ldflags
LD_FLAGS ?= \
	-s \
	-w \
	-X ${PROJECT}/version.Name=${NAME} \
	-X ${PROJECT}/version.GitCommit=${GIT_COMMIT} \
	-X ${PROJECT}/version.GitDescribe=${GIT_DESCRIBE}

# Create a cross-compile target for every os-arch pairing. This will generate
# a make target for each os/arch like "make linux/amd64" as well as generate a
# meta target (build) for compiling everything.
define make-xc-target
  $1/$2:
  ifneq (,$(findstring ${1}/${2},$(XC_EXCLUDE)))
		@printf "%s%20s %s\n" "-->" "${1}/${2}:" "${PROJECT} (excluded)"
  else
		@printf "%s%20s %s\n" "-->" "${1}/${2}:" "${PROJECT}"
		@docker run \
			--interactive \
			--rm \
			--dns="8.8.8.8" \
			--volume="${CURRENT_DIR}:/go/src/${PROJECT}" \
			--workdir="/go/src/${PROJECT}" \
			"golang:${GOVERSION}" \
			env \
				CGO_ENABLED="0" \
				GOOS="${1}" \
				GOARCH="${2}" \
				go build \
				  -a \
					-o="pkg/${1}_${2}/${NAME}${3}" \
					-ldflags "${LD_FLAGS}" \
					-tags "${GOTAGS}"
  endif
  .PHONY: $1/$2

  $1:: $1/$2
  .PHONY: $1

  build:: $1/$2
  .PHONY: build
endef
$(foreach goarch,$(XC_ARCH),$(foreach goos,$(XC_OS),$(eval $(call make-xc-target,$(goos),$(goarch),$(if $(findstring windows,$(goos)),.exe,)))))

# dev builds and installs the project locally.
dev:
	@echo "==> Installing ${NAME} for ${GOOS}/${GOARCH}"
	@rm -f "${GOPATH}/pkg/${GOOS}_${GOARCH}/${PROJECT}/version.a"
	mkdir -p pkg/$(GOOS)_$(GOARCH)/ bin/
	go install -ldflags '$(LD_FLAGS)' -tags '$(GOTAGS)'
	cp $(GOPATH)/bin/consul bin/
	cp $(GOPATH)/bin/consul pkg/$(GOOS)_$(GOARCH)
.PHONY: dev

test:
	@echo "==> Testing ${NAME}"
	@go test -timeout=30s -parallel=20 -tags="${GOTAGS}" ${GOFILES} ${TESTARGS}
.PHONY: test

# dist builds the binaries and then signs and packages them for distribution
dist:
ifndef GPG_KEY
	@echo "==> ERROR: No GPG key specified! Without a GPG key, this release cannot"
	@echo "           be signed. Set the environment variable GPG_KEY to the ID of"
	@echo "           the GPG key to continue."
	@exit 127
else
	@$(MAKE) -f "${MKFILE_PATH}" _cleanup _tag
	@$(MAKE) -f "${MKFILE_PATH}" -j4 build
	@$(MAKE) -f "${MKFILE_PATH}" _compress _checksum _sign
endif
.PHONY: dist

# _cleanup removes any previous binaries
_cleanup:
	@rm -rf "${CURRENT_DIR}/pkg/"
	@rm -rf "${CURRENT_DIR}/bin/"
.PHONY: _cleanup

# _tag creates the git tag for this release
_tag:
	@echo "==> Tagging..."
	@git commit \
		--allow-empty \
		--gpg-sign="${GPG_KEY}" \
		--message "Release v${VERSION}" \
		--quiet \
		--signoff
	@git tag \
		--annotate \
		--create-reflog \
		--local-user "${GPG_KEY}" \
		--message "Version ${VERSION}" \
		--sign \
		"v${VERSION}" master
.PHONY: _tag

# _compress compresses all the binaries in pkg/* as tarball and zip.
_compress:
	@mkdir -p "${CURRENT_DIR}/pkg/dist"
	@for platform in $$(find ./pkg -mindepth 1 -maxdepth 1 -type d); do \
		osarch=$$(basename "$$platform"); \
		if [ "$$osarch" = "dist" ]; then \
			continue; \
		fi; \
		\
		ext=""; \
		if test -z "$${osarch##*windows*}"; then \
			ext=".exe"; \
		fi; \
		cd "$$platform"; \
		tar -czf "${CURRENT_DIR}/pkg/dist/${NAME}_${VERSION}_$${osarch}.tgz" "${NAME}$${ext}"; \
		zip -q "${CURRENT_DIR}/pkg/dist/${NAME}_${VERSION}_$${osarch}.zip" "${NAME}$${ext}"; \
		cd - &>/dev/null; \
	done
.PHONY: _compress

# _checksum produces the checksums for the binaries in pkg/dist
_checksum:
	@cd "${CURRENT_DIR}/pkg/dist" && \
		shasum --algorithm 256 * > ${CURRENT_DIR}/pkg/dist/${NAME}_${VERSION}_SHA256SUMS && \
		cd - &>/dev/null
.PHONY: _checksum

# _sign signs the binaries using the given GPG_KEY. This should not be called
# as a separate function.
_sign:
	@echo "==> Signing ${PROJECT} at v${VERSION}"
	@gpg \
		--default-key "${GPG_KEY}" \
		--detach-sig "${CURRENT_DIR}/pkg/dist/${NAME}_${VERSION}_SHA256SUMS"
	@echo "--> Do not forget to run:"
	@echo ""
	@echo "    git push && git push --tags"
	@echo ""
	@echo "And then upload the binaries in dist/!"
.PHONY: _sign