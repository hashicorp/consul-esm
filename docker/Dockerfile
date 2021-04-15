### Builder
#
FROM golang:latest AS builder
LABEL maintainer "John Eikenberry <jae@zhar.net>"

ARG LD_FLAGS
ARG GOTAGS

WORKDIR "/go/src/github.com/hashicorp/consul-esm"

COPY . .

RUN ls

RUN \
  CGO_ENABLED="0" \
  GO111MODULE=on \
  go build -a -o "/consul-esm" \
    -ldflags "${LD_FLAGS}" -tags "${GOTAGS}"

### Final
#
FROM alpine:latest
LABEL maintainer "John Eikenberry <jae@zhar.net>"

# UID and GID of consul-esm user and group.
# These are the defaults, this makes them explicit and overridable.
ARG UID=100
ARG GID=1000

# Create a consul-esm user and group first so the IDs get set the same way,
# even as the rest of this may change over time.
RUN addgroup -g ${GID} consul-esm && \
    adduser -u ${UID} -S -G consul-esm consul-esm

# Set up certificates, and dumb-init.
RUN apk add --no-cache ca-certificates dumb-init

# Install consul-esm
COPY --from=builder "/consul-esm" "/bin/consul-esm"

# The agent will be started with /consul-esm/config as the configuration
# directory so you can add additional config files in that location.
RUN mkdir -p /consul-esm/data && \
    mkdir -p /consul-esm/config && \
    chown -R consul-esm:consul-esm /consul-esm

# Expose the consul-esm data directory as a volume since that's where
# shared results should be rendered.
VOLUME /consul-esm/data

# The entry point script uses dumb-init as the top-level process to reap any
# zombie processes created by consul-esm sub-processes.
COPY "docker/alpine/docker-entrypoint.sh" "/bin/docker-entrypoint.sh"
ENTRYPOINT ["/bin/docker-entrypoint.sh"]
USER ${UID}:${GID}

# Run consul-esm by default
CMD ["/bin/consul-esm"]
