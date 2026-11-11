# bridgectl publishes only version tags (no :latest), so pin an explicit
# version. Override with --build-arg BRIDGECTL_VERSION=vX.Y.Z.
ARG BRIDGECTL_VERSION=v1.4.4

FROM golang:1.27-bookworm AS build
# Stamped into the binary so `bosun version` inside the image reports the
# release it was built from. Defaults to "dev" for local builds.
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags="-s -w -X github.com/everydaydevopsio/bosun/internal/version.Version=${VERSION}" \
      -o /out/bosun ./cmd/bosun

FROM ghcr.io/orchael/bridgectl:${BRIDGECTL_VERSION}

# Root only long enough to install git. The runtime USER below is numeric, so
# this username never reaches a Kubernetes runAsNonRoot check.
# hadolint ignore=DL3066
USER root
# git is Bosun's own dependency: the reviewer clones with it and the agent
# prompts run it. bridgectl does not need it, so the base image will not ship it.
#
# On amd64 apt reads a dated Ubuntu snapshot rather than the live mirrors. A
# live mirror mid-sync can advertise packages it does not yet serve -- on
# 2026-11-10 security.ubuntu.com listed perl 5.38.2-3.2ubuntu0.7, which git
# depends on, while returning 404 for the files, and every image build failed
# for over half an hour. A snapshot never changes, so the build is reproducible
# and immune to that. Bump UBUNTU_SNAPSHOT to pick up security fixes.
#
# arm64 cannot do the same: snapshot.ubuntu.com does not serve ubuntu-ports
# (it answers 401). It keeps the live ports mirror, a separate one from
# security.ubuntu.com, with retries for brief outages.
#
# The original sources are restored afterwards so the runtime image's apt
# configuration is the base image's. The grep fails the build if the rewrite
# ever stops matching, rather than silently falling back to the live mirror.
#
# git itself stays unpinned: the snapshot date is the pin.
ARG UBUNTU_SNAPSHOT=20261108T000000Z
# hadolint ignore=DL3008
RUN set -eux; \
    sources=/etc/apt/sources.list.d/ubuntu.sources; \
    cp "$sources" /tmp/ubuntu.sources; \
    if [ "$(dpkg --print-architecture)" = amd64 ]; then \
      sed -i -E "s#^URIs: http://(archive|security)\.ubuntu\.com/ubuntu/?\$#URIs: https://snapshot.ubuntu.com/ubuntu/${UBUNTU_SNAPSHOT}/#" "$sources"; \
      [ "$(grep -c '^URIs: https://snapshot.ubuntu.com/' "$sources")" -eq 2 ]; \
    fi; \
    apt-get -o Acquire::Retries=5 update; \
    apt-get -o Acquire::Retries=5 install -y --no-install-recommends git; \
    git --version; \
    mv /tmp/ubuntu.sources "$sources"; \
    rm -rf /var/lib/apt/lists/*
WORKDIR /app/bosun
COPY prompts ./prompts
COPY config ./config
COPY scripts/*review-adapter*.js scripts/provider-usage.js scripts/provider-reads*.js ./scripts/
RUN node --test scripts/*review-adapter.test.js scripts/provider-reads.test.js
COPY --from=build /out/bosun /usr/local/bin/bosun

# The base image sets org.opencontainers.image.* to bridgectl's identity, and an
# image that still carries it has no bosun binary in it. `bosun review` reads the
# title label off the image loaded into the Kind node to refuse a stale or
# foreign tag before it submits a Job that would fail with an opaque exec error.
# ARG is redeclared because the build-stage one does not cross the FROM.
ARG VERSION=dev
LABEL org.opencontainers.image.title="bosun" \
      org.opencontainers.image.description="Event-driven AI code-review controller for Kubernetes" \
      org.opencontainers.image.source="https://github.com/everydaydevopsio/bosun" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}"

# The base image configures the provider CLIs (codex/claude/opencode) for the
# unprivileged "bridge" user, so run as that user rather than root. The UID is
# given numerically so Kubernetes can enforce runAsNonRoot, which cannot verify
# a username.
RUN chown -R bridge:bridge /app/bosun /usr/local/bin/bosun
ENV HOME=/home/bridge
USER 1001
ENTRYPOINT []
CMD ["/usr/local/bin/bosun"]
