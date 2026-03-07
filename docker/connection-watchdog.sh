#!/usr/bin/env bash
# Connection leak watchdog for go2rtc
# ENV: GO2RTC_MAX_CONSUMERS (default:15, -1=disabled), GO2RTC_WATCHDOG_INTERVAL (default:300s)

MAX_CONSUMERS="${GO2RTC_MAX_CONSUMERS:-15}"
INTERVAL="${GO2RTC_WATCHDOG_INTERVAL:-300}"
API_URL="${GO2RTC_API_URL:-http://127.0.0.1:1985}"

if [ "$MAX_CONSUMERS" = "-1" ]; then
    echo "[watchdog] Connection watchdog DISABLED (GO2RTC_MAX_CONSUMERS=-1)"
    exit 0
fi

echo "[watchdog] Starting connection watchdog (max=${MAX_CONSUMERS}, interval=${INTERVAL}s)"
sleep 10

while true; do
    STREAMS=$(curl -sf "${API_URL}/api/streams" 2>/dev/null)
    if [ -n "$STREAMS" ]; then
        echo "$STREAMS" | python3 -c "
import json, sys
data = json.load(sys.stdin)
mx = int(sys.argv[1])
for name, stream in data.items():
    if not isinstance(stream, dict): continue
    consumers = stream.get(\"consumers\") or []
    if len(consumers) > mx:
        print(f\"{name}:{len(consumers)}\")
" "$MAX_CONSUMERS" 2>/dev/null | while IFS=: read -r sname scount; do
            echo "[watchdog] $(date +%Y-%m-%dT%H:%M:%S) ${sname}: ${scount} consumers (max: ${MAX_CONSUMERS}) — cleanup"
            curl -sf -X DELETE "${API_URL}/api/streams?src=${sname}" >/dev/null 2>&1
            echo "[watchdog] ${sname} reset"
        done
    fi
    sleep "$INTERVAL"
done
