# Metadata about this makefile and position
MKFILE_PATH := $(lastword $(MAKEFILE_LIST))
CURRENT_DIR := $(patsubst %/,%,$(dir $(realpath $(MKFILE_PATH))))

# System information
GOOS=$(shell go env GOOS)
GOARCH=$(shell go env GOARCH)

# Project information
NAME := "consul-esm"
GIT_COMMIT ?= $(shell git rev-parse --short HEAD)
GIT_DESCRIBE ?= $(shell git describe --tags --always)
VERSION := $(shell awk -F\" '/^[ \t]+Version/ { print $$2; exit }' "${CURRENT_DIR}/version/version.go")
PRERELEASE := $(shell awk -F\" '/^[ \t]+VersionPrerelease/ { print $$2; exit }' "${CURRENT_DIR}/version/version.go")

# Tags specific for building
GOTAGS ?=

# List of ldflags
LD_FLAGS ?= \
	-s -w \
	-X github.com/hashicorp/consul-esm/version.Name=${NAME} \
	-X github.com/hashicorp/consul-esm/version.GitCommit=${GIT_COMMIT} \
	-X github.com/hashicorp/consul-esm/version.GitDescribe=${GIT_DESCRIBE}

# for CRT build process
version:
	@echo ${VERSION}${PRERELEASE}
.PHONY: version

# dev builds and installs the project locally.
dev:
	@echo "==> Installing ${NAME} for ${GOOS}/${GOARCH}"
	@env CGO_ENABLED="0" \
		go install -ldflags "${LD_FLAGS}" -tags "${GOTAGS}"
.PHONY: dev

# dev docker builds
docker:
	@env CGO_ENABLED="0" go build -ldflags "${LD_FLAGS}" -o $(NAME)
	mkdir -p dist/linux/amd64/
	cp consul-esm dist/linux/amd64/
	env DOCKER_BUILDKIT=1 docker build -t consul-esm .
.PHONY: docker

test:
	@echo "==> Testing ${NAME}"
	@go test -timeout=60s -tags="${GOTAGS}" ${TESTARGS} ./...
.PHONY: test

test-race:
	@echo "==> Testing ${NAME}"
	@go test -race -timeout=60s -tags="${GOTAGS}" ${TESTARGS} ./...
.PHONY: test-race

# clean removes any previous binaries
clean:
	@rm -rf "${CURRENT_DIR}/dist/"
	@rm -f "consul-esm"
.PHONY: clean

# noop command to get build pipeline working
dev-tree:
	@true
.PHONY: dev-tree
