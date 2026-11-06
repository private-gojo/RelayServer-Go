#!/bin/sh
# ============================================================
# WAIT-FOR-BRIDGE (relay-utilities)
# ============================================================

MAX_RETRIES=30
RETRY_COUNT=0

while [ $RETRY_COUNT -lt $MAX_RETRIES ]; do
  if nc -z 127.0.0.1 8081 2>/dev/null; then
    exec /usr/sbin/nginx -g "daemon off;"
  fi
  sleep 1
  RETRY_COUNT=$((RETRY_COUNT + 1))
done

echo "ERROR: Go bridge (x-relay-bridge) did not become ready on 127.0.0.1:8081 in time" >&2
exit 1
