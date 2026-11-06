#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Gojo Research
# ============================================================
# GOJO X-RELAY — CONTAINER ENTRYPOINT
# ============================================================

if ! grep -q "/bin/false" /etc/shells; then
    echo "/bin/false" >> /etc/shells
fi

if [ -n "$ACCOUNTS" ]; then
    IFS=',' read -ra ADDR <<< "$ACCOUNTS"
    for i in "${ADDR[@]}"; do
        USER=${i%%:*}
        PASS=${i#*:}
        id -u "$USER" &>/dev/null || adduser -D -s /bin/false "$USER"
        echo "$USER:$PASS" | chpasswd
    done
else
    id -u gojo &>/dev/null || adduser -D -s /bin/false gojo
    echo "gojo:xrelay" | chpasswd
fi

# Generate full suite of Dropbear host keys (RSA, ECDSA, Ed25519)
if [ ! -f /etc/dropbear/dropbear_rsa_host_key ]; then
    dropbearkey -t rsa -f /etc/dropbear/dropbear_rsa_host_key -s 2048
fi
if [ ! -f /etc/dropbear/dropbear_ecdsa_host_key ]; then
    dropbearkey -t ecdsa -f /etc/dropbear/dropbear_ecdsa_host_key -s 521
fi
if [ ! -f /etc/dropbear/dropbear_ed25519_host_key ]; then
    dropbearkey -t ed25519 -f /etc/dropbear/dropbear_ed25519_host_key
fi

export PORT="${PORT:-8080}"
sed "s/__PORT__/${PORT}/" /etc/x-relay/nginx.conf.template > /etc/nginx/nginx.conf

echo "=== [STARTUP DIAGNOSTICS] ==="
echo "[DIAG] resolver configuration:"
if [ -r /etc/resolv.conf ]; then
    cat /etc/resolv.conf
else
    echo "[DIAG] /etc/resolv.conf is unavailable"
fi
echo "[DIAG] configured nameservers:"
grep -E '^[[:space:]]*nameserver[[:space:]]+' /etc/resolv.conf 2>/dev/null || true
echo "[DIAG] online CPU entries: $(grep -c '^processor' /proc/cpuinfo 2>/dev/null || echo unknown)"
echo "[DIAG] file descriptor limit: $(ulimit -n)"
echo "[DIAG] cgroup cpu.max:"
cat /sys/fs/cgroup/cpu.max 2>/dev/null || true
echo "[DIAG] cgroup cpu.stat:"
cat /sys/fs/cgroup/cpu.stat 2>/dev/null || cat /sys/fs/cgroup/cpu/cpu.stat 2>/dev/null || true
echo "[DIAG] cgroup memory.current / memory.max:"
cat /sys/fs/cgroup/memory.current /sys/fs/cgroup/memory.max 2>/dev/null || true
echo "[DIAG] process status:"
grep -E '^(VmRSS|Threads):' /proc/self/status 2>/dev/null || true
echo "=== [STARTUP DIAGNOSTICS END] ==="

echo "=== [BOOT CHECK] Rust UDPGW version ==="
badvpn-udpgw --version
echo "=========================================="

echo "=== [BOOT CHECK] nginx config test ==="
nginx -t
echo "======================================="

/usr/bin/supervisord -c /etc/supervisord.conf &
SUPERVISOR_PID=$!

shutdown() {
    echo "[ENTRYPOINT] Received SIGTERM, shutting down supervisord..."
    kill -s SIGTERM "$SUPERVISOR_PID"
    wait "$SUPERVISOR_PID"
    echo "[ENTRYPOINT] Shutdown complete"
}

trap shutdown SIGTERM SIGINT

wait "$SUPERVISOR_PID"
