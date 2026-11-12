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
# apt installs from the base image's live Ubuntu mirrors on both architectures,
# so every build picks up current security fixes; git itself is unpinned. The
# image is therefore not byte-reproducible, by choice (#99).
#
# A live mirror mid-sync can list packages it does not serve yet: on 2026-11-10
# security.ubuntu.com listed a perl update git depends on while answering 404
# for it for over half an hour. Acquire::Retries does not retry a 404, so the
# whole update-and-install is retried with backoff (about 7.5 minutes in all)
# before the build fails.
# hadolint ignore=DL3008
RUN set -eux; \
    for delay in 30 60 120 240 0; do \
      if apt-get -o Acquire::Retries=5 update \
        && apt-get -o Acquire::Retries=5 install -y --no-install-recommends git; then \
        break; \
      fi; \
      [ "$delay" -gt 0 ] || exit 1; \
      echo "apt failed; retrying in ${delay}s" >&2; \
      sleep "$delay"; \
    done; \
    git --version; \
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
