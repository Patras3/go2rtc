#!/usr/bin/env bash
set -e

# Ensure /var/run exists (and is empty on each container start)
mkdir -p /var/run

# Remove any stale keepalived PID files from a previous unclean shutdown
rm -f /var/run/keepalived.pid /var/run/keepalived_vrrp.pid 2>/dev/null || true

# 1) Start keepalived in the background if /config/keepalived.conf exists
if [ -f /config/keepalived.conf ]; then
    echo "[entrypoint] Found keepalived.conf, starting keepalived in debug mode (background)."
    keepalived \
      -f /config/keepalived.conf \
      --dont-fork \
      --log-console \
      --log-detail \
      -D \
      -p /var/run/keepalived.pid \
      -r /var/run/keepalived_vrrp.pid &

    # Grab keepalived's PID
    KEEPALIVED_PID=$!

    # Wait a little to see if keepalived fails immediately
    sleep 2

    # Check if keepalived is still running
    if ! kill -0 "$KEEPALIVED_PID" 2>/dev/null; then
      echo "[entrypoint] ERROR: keepalived exited or failed to start."
      exit 1
    fi

    echo "[entrypoint] keepalived is running (PID: $KEEPALIVED_PID)."
else
    echo "[entrypoint] No /config/keepalived.conf found. Not starting keepalived."
fi

# 2) Now start go2rtc in the foreground (so if it fails, container fails)
echo "[entrypoint] Starting go2rtc in foreground..."
exec go2rtc -config /config/go2rtc.yaml
