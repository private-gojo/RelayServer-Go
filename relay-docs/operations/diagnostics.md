# WP03: Runtime Diagnostics and Operations

> **Status:** Log routing and diagnostic code pass GitHub CI; Cloud Run log visibility must be confirmed after deployment.
>
> **Scope:** Identify startup, process, session, ingress, and resource messages in Cloud Run Logs.
>
> **Change record**
> - **Changed:** Supervisor-managed process logs are routed to container stdout/stderr.
> - **Added:** Log categories, important fields, and a first-response checklist.
> - **Removed:** File-only Supervisor process logs as the primary log destination.

## Log Sources

| Prefix / source | Purpose |
| --- | --- |
| `[STARTUP DIAGNOSTICS]` / `[DIAG]` | Resolver configuration, CPU count, file descriptor limit, cgroup CPU/memory, and process status |
| `[BOOT CHECK]` | Rust UDPGW version and Nginx configuration validation |
| `[X-RELAY-BRIDGE] [BOOT]` | Go version, online CPUs, GOMAXPROCS, listener addresses, buffer size, timeout, and guard limits |
| `[SESSION <id>]` | Connection start/end, peer and selected client IP, handshake and SSH-connect timing, bytes, and end reason |
| `[SECURITY]` | Connections rejected by the ingress guard |
| `[METRICS]` | Session totals, byte counters, CPU, throttling, RSS, file descriptors, cgroup memory, and scheduler lag |
| Nginx `stream_diag` | Connection ID, remote address, status, duration, upstream connect time, bytes, and upstream address |
| Supervisor process output | Nginx, Dropbear, Go bridge, and Rust UDPGW process stdout/stderr |
| `[SHUTDOWN]` | Signal receipt, listener closure, session drain, and shutdown timeout |

Supervisor writes its own process output and child process stdout/stderr to `/dev/stdout` and `/dev/stderr`, with rotation disabled for those streams. Cloud Run should therefore collect the same process output in the revision logs.

## Important Metric Fields

The Go bridge emits a metrics line every 10 seconds.

| Field | Meaning |
| --- | --- |
| `active_sessions` | Current bridge sessions |
| `total_sessions` | Cumulative accepted session handlers |
| `rejected_conns` | Connections rejected by the Go guard |
| `client_to_ssh_bytes` / `ssh_to_client_bytes` | Cumulative forwarded bytes in each direction |
| `process_cpu_cores` | Process CPU usage expressed as CPU cores over the sample interval |
| `container_cpu_cores` | Cgroup CPU usage expressed as CPU cores over the sample interval |
| `cpu_quota` | Raw cgroup CPU quota value when available |
| `cpu_throttled_delta` | Change in cgroup throttled time during the sample |
| `rss_kb` | Process resident memory |
| `open_fds` | Open file descriptor count |
| `cgroup_memory_current` / `cgroup_memory_max` | Current cgroup memory and configured maximum |
| `scheduler_lag_ms` | Delay relative to the metrics loop's scheduled sample |

A value of `n/a` means the corresponding procfs or cgroup metric was unavailable in that runtime. It is not itself a service failure.

## Client IP Verification

The current code selects the rightmost valid IP from `X-Forwarded-For` for the direct Cloud Run `run.app` ingress path. Every Cloud Run deployment must verify the session field:

```text
real_ip=<expected client public IP>
```

If it shows `unknown`, the bridge did not find a valid forwarded address and will skip per-IP guard accounting for that connection. If it shows a proxy address or a different client, stop load testing and inspect the actual header chain before changing guard policy.

Do not blindly trust `CF-Connecting-IP`, `True-Client-IP`, or arbitrary client-supplied forwarding headers. If an external proxy is added later, define and verify the trusted proxy chain first.

## First-Response Checklist

1. Confirm the startup diagnostics and Go `[BOOT]` messages appear.
2. Confirm Nginx passes its `nginx -t` check and starts after the Go bridge listener is ready.
3. Confirm Dropbear starts and UDPGW prints its version.
4. Check `real_ip` in a session log before evaluating per-IP limits.
5. Confirm `[METRICS]` continues at roughly 10-second intervals.
6. After disconnecting a test client, confirm session counts decrease and the session end reason is recorded.
7. For rejected or disconnected sessions, correlate bridge session logs with Nginx stream logs.

## Common Signals

| Signal | Initial interpretation |
| --- | --- |
| No bridge `[BOOT]` message | Go process failed before binding or the container did not reach bridge startup |
| Nginx wait script times out | Bridge did not open `127.0.0.1:8081` within the startup gate |
| `ssh_connect_error` | Go bridge could not connect to Dropbear on the configured SSH backend |
| `handshake_timeout` | Client did not finish the HTTP header block within four seconds |
| `handshake_header_too_large` | Header exceeded the 16 KiB parser limit |
| `security_rate_limited` | Per-IP burst or active-session limit rejected the connection |
| CPU throttling increases under load | Compare container CPU use and Cloud Run CPU allocation before changing concurrency |
| Memory or FD count continually increases | Inspect active sessions, disconnect cleanup, and resource limits |

See [WP04: Cloud Run Validation and Performance Testing](../testing/cloud-run-validation.md) before attributing a runtime symptom to a specific code path.
