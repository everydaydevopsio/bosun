# bridgectl publishes only version tags (no :latest), so pin an explicit
# version. Override with --build-arg BRIDGECTL_VERSION=vX.Y.Z.
ARG BRIDGECTL_VERSION=v1.4.1

FROM golang:1.26-bookworm AS build
# Stamped into the binary so `bosun version` inside the image reports the
# release it was built from. Defaults to "dev" for local builds.
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/bosun ./cmd/bosun

FROM ghcr.io/orchael/bridgectl:${BRIDGECTL_VERSION}

# Root only long enough to install git. The runtime USER below is numeric, so
# this username never reaches a Kubernetes runAsNonRoot check.
# hadolint ignore=DL3066
USER root
# git is deliberately unpinned: Debian bookworm rotates point releases out of
# the archive, so pinning turns every base-image refresh into a broken build.
# hadolint ignore=DL3008
RUN apt-get update && apt-get install -y --no-install-recommends git && rm -rf /var/lib/apt/lists/*
WORKDIR /app/bosun
COPY prompts ./prompts
COPY config ./config
COPY --from=build /out/bosun /usr/local/bin/bosun

# The base image configures the provider CLIs (codex/claude/opencode) for the
# unprivileged "bridge" user, so run as that user rather than root. The UID is
# given numerically so Kubernetes can enforce runAsNonRoot, which cannot verify
# a username.
RUN chown -R bridge:bridge /app/bosun /usr/local/bin/bosun
ENV HOME=/home/bridge
USER 1001
ENTRYPOINT []
CMD ["/usr/local/bin/bosun"]
