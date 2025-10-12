#!/usr/bin/env bash
#
# vrrp_notify.sh
#
# This script is called by Keepalived whenever we change VRRP state.
# The first argument ($1) will be MASTER, BACKUP, or FAULT (etc.)
# We can do different actions depending on the new state.

STATE="$1"

echo "[vrrp_notify.sh] New VRRP state: $STATE"

case "$STATE" in
  MASTER)
    # We just became MASTER; do something, e.g. notify or restart go2rtc
    echo "[vrrp_notify.sh] MASTER -> Nothing to do"
    #curl -X POST http://localhost:1985/api/restart
    ;;

  BACKUP)
    # We became BACKUP; maybe also do something
    echo "[vrrp_notify.sh] BACKUP -> sending /restart to go2rtc"
    curl -X POST http://localhost:1985/api/restart
    ;;

  FAULT)
    # We are in a FAULT state (e.g. health check failed). Possibly do a different action.
    echo "[vrrp_notify.sh] FAULT ->  sending /restart to go2rtc"
    curl -X POST http://localhost:1985/api/restart
    ;;

  *)
    # Unexpected state
    echo "[vrrp_notify.sh] Unknown state: $STATE"
    ;;
esac

exit 0
