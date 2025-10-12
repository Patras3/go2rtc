# Dockerfile for go2rtc with keepalived support
# This image combines go2rtc with keepalived for high availability setups
# Requires keepalive configuration for HA mode

# Use Python 3.11 Alpine as base (lightweight)
FROM python:3.11-alpine

# Build argument for target architecture (amd64, arm64, etc.)
ARG TARGETARCH

# Install required packages
# - keepalived: for VRRP high availability
# - tini: proper init system for containers
# - ffmpeg: video processing support for go2rtc
# - curl: for health checks and API calls in vrrp_notify script
# - bash: required for entrypoint and vrrp_notify scripts
RUN apk add --no-cache \
    keepalived \
    tini \
    ffmpeg \
    curl \
    bash

# Install Intel hardware acceleration drivers (only for amd64)
# These enable hardware-accelerated video encoding/decoding on Intel CPUs
# Note: These packages only exist in x86_64 repos, skip for other architectures
RUN if [ "${TARGETARCH}" = "amd64" ]; then \
      apk add --no-cache --repository=http://dl-cdn.alpinelinux.org/alpine/edge/community \
      libva-intel-driver intel-media-driver || true; \
    fi

# Copy go2rtc binary
# Note: Build go2rtc first with: CGO_ENABLED=0 go build -ldflags "-s -w" -trimpath
COPY go2rtc /usr/local/bin/go2rtc
RUN chmod +x /usr/local/bin/go2rtc

# Copy entrypoint script (starts keepalived + go2rtc)
COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

# Copy VRRP notification script (called by keepalived on state changes)
COPY docker/vrrp_notify.sh /usr/local/bin/vrrp_notify.sh
RUN chmod +x /usr/local/bin/vrrp_notify.sh

# Set working directory to /config
WORKDIR /config

# Create volume for persistent configuration
# Expected files:
# - /config/go2rtc.yaml (required - go2rtc configuration)
# - /config/keepalived.conf (optional - for HA setup with keepalived)
VOLUME ["/config"]

# Use tini as init system (handles signals properly)
# Then run entrypoint.sh which starts keepalived (if configured) and go2rtc
ENTRYPOINT ["/sbin/tini", "--", "/usr/local/bin/entrypoint.sh"]

# Default command (empty, entrypoint handles everything)
CMD []

# Metadata
LABEL maintainer="Patras3 <patrykdadas@gmail.com>"
LABEL description="go2rtc with keepalived support for high availability setups. Requires keepalive configuration."
LABEL org.opencontainers.image.source="https://github.com/Patras3/go2rtc"
