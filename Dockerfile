# bridgectl publishes only version tags (no :latest), so pin an explicit
# version. Override with --build-arg BRIDGECTL_VERSION=vX.Y.Z.
ARG BRIDGECTL_VERSION=v1.3.0
FROM ghcr.io/orchael/bridgectl:${BRIDGECTL_VERSION}

USER root
RUN apt-get update && apt-get install -y --no-install-recommends git python3 python3-pip && rm -rf /var/lib/apt/lists/*
WORKDIR /app/bosun
COPY requirements.txt requirements-dev.txt ./
RUN pip3 install --break-system-packages --no-cache-dir -r requirements.txt -r requirements-dev.txt

COPY bosun ./bosun
COPY prompts ./prompts
COPY proto ./proto
COPY config ./config
COPY scripts/gen-proto.sh ./scripts/gen-proto.sh

# Generate the gRPC stubs Bosun uses to drive bridgectl, then drop the
# generator: it is several hundred MB and is not needed at runtime.
RUN ./scripts/gen-proto.sh && pip3 uninstall --break-system-packages -y grpcio-tools

# The base image configures the provider CLIs (codex/claude/opencode) for the
# unprivileged "bridge" user, so run as that user rather than root. The UID is
# given numerically so Kubernetes can enforce runAsNonRoot, which cannot verify
# a username.
RUN chown -R bridge:bridge /app/bosun
ENV PYTHONPATH=/app/bosun
ENV HOME=/home/bridge
USER 1001
ENTRYPOINT []
CMD ["python3", "-m", "uvicorn", "bosun.app:app", "--host", "0.0.0.0", "--port", "8080"]
