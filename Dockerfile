# bridgectl publishes only version tags (no :latest), so pin an explicit
# version. Override with --build-arg BRIDGECTL_VERSION=vX.Y.Z.
ARG BRIDGECTL_VERSION=v1.3.0

FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/bosun ./cmd/bosun

FROM ghcr.io/orchael/bridgectl:${BRIDGECTL_VERSION}

USER root
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
