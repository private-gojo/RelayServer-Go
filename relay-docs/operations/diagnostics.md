# WP03: Runtime Diagnostics and Operations

> **Status:** Lifecycle counters, structured Go bridge logs, Nginx JSON access logs, and Cloud Monitoring export are covered by branch tests. Cloud Run IAM and live metric ingestion still require deployment validation.
>
> **Scope:** Identify startup, connection, session, ingress, resource, and component messages in Cloud Run Logs and Cloud Monitoring.
>
> **Change record**
> - **Changed:** Session metrics now count established SSH-backed relay connections separately from accepted TCP connections and unfinished handshakes.
> - **Changed:** Cgroup throttling deltas are normalized to microseconds.
> - **Added:** Structured JSON bridge events, per-instance metadata, throughput rates, lifecycle/error counters, and Cloud Monitoring custom metrics.
> - **Added:** Nginx Stream access logs use structured JSON with a component field.
> - **Removed:** Ambiguous active_sessions, total_sessions, and unitless cpu_throttled_delta metric names.

## Log Sources

| Component / field | Purpose |
| --- | --- |
| component=go-bridge | Startup, accepted connections, handshake failures, SSH backend failures, session close, and shutdown |
| component=telemetry | 10-second lifecycle, byte, throughput, CPU, memory, FD, and scheduler snapshots |
| component=cloud-monitoring | Custom metric export failures |
| component=nginx-stream | Structured Nginx Stream connection summaries |
| Nginx stderr | Nginx configuration, listener, and proxy errors; emitted at warning level or higher |
| Supervisor child output | Native Dropbear and UDPGW process output |

Go bridge logs are JSON. Cloud Logging reads the severity, message, component, instance_id, service_name, and revision_name fields. Cloud Run also attaches its cloud_run_revision monitored-resource labels to container logs.

Nginx Stream access records are JSON with component=nginx-stream. Dropbear and UDPGW retain their native output formats; their stdout/stderr is collected by Cloud Run, with stderr severity preserved by the platform.

## Lifecycle Metrics

All Go counters are process-local. Each metric log/Cloud Monitoring time series includes an instance identifier, so values from separate Cloud Run instances must be summed when viewing service-wide activity.

| Field | Meaning |
| --- | --- |
| active_connections | Current accepted TCP connection handlers |
| active_handshakes | Connections still processing the initial HTTP header / Upgrade response |
| active_ssh_sessions | Active relay connections after the SSH backend TCP connection succeeds |
| total_connections | Cumulative accepted connection handlers on this instance |
| total_ssh_sessions | Cumulative successful SSH-backend relay connections on this instance |
| peak_active_ssh_sessions | Highest concurrent SSH-backed relay connections seen by this instance |
| failed_handshakes | HTTP handshake attempts that did not complete successfully |
| failed_ssh_connections | Failed connections from the bridge to the Dropbear backend |
| session_errors | Bridge sessions ending due to a classified error |
| rejected_connections | Connections rejected by the ingress guard |
| client_to_ssh_bytes_total / ssh_to_client_bytes_total | Cumulative forwarded bytes per instance |
| client_to_ssh_mbps / ssh_to_client_mbps | Directional throughput calculated from byte-counter deltas over the sample interval |

active_ssh_sessions is a live count of SSH-backed relay connections, not a count of unique human users or authenticated SSH accounts. One client can open multiple connections, and one Cloud Run service can run multiple instances.

A health check is included in total_connections but does not increment failed_handshakes or total_ssh_sessions.

## Cloud Monitoring Custom Metrics

When running on Cloud Run and metadata is available, the Go bridge writes the following metric family to the project:

custom.googleapis.com/x_relay/*

The exporter sends one bounded batch every 10 seconds and labels each time series with instance_id. Cloud Run revision resource labels identify the service, revision, configuration, project, and region. The exporter uses the Cloud Run service identity and the metadata server token endpoint; no service-account key is stored in the container.

Required setup:

1. Enable the Cloud Monitoring API:
   ```bash
   gcloud services enable monitoring.googleapis.com --project PROJECT_ID
   ```
2. Grant the Cloud Run runtime service account roles/monitoring.metricWriter on the project:
   ```bash
   gcloud projects add-iam-policy-binding PROJECT_ID \\
     --member="serviceAccount:SERVICE_ACCOUNT_EMAIL" \\
     --role="roles/monitoring.metricWriter"
   ```
3. Deploy a new revision. The exporter logs cloud_monitoring_exporter_started on startup. API/IAM failures appear as custom_metrics_write_failed events under component=cloud-monitoring.

The deployment identity used to run gcloud run deploy is not automatically the Cloud Run runtime service identity. Confirm the service's configured runtime service account before granting the role.

Set CLOUD_MONITORING_ENABLED=false to disable custom metric export. If required Cloud Run metadata is missing, export is skipped without affecting relay traffic.

Cloud Monitoring ingestion is asynchronous; graphs are not frame-by-frame live. Use the 10-second per-instance JSON logs for near-live inspection and Cloud Monitoring for aggregated charts and alerts.

All exported custom metrics are GAUGE time series. `total_*` and `*_bytes_total` fields are per-instance counter snapshots represented as gauges, not Cloud Monitoring CUMULATIVE metrics. For service-wide active connection or session totals, chart `active_ssh_sessions` (or the relevant active metric) with SUM aggregation across the `instance_id` label. Do not sum per-instance peak values to infer a simultaneous service-wide peak.

## Resource Metrics

| Field | Meaning |
| --- | --- |
| process_cpu_cores | Go process CPU usage expressed as CPU cores over the sample interval |
| container_cpu_cores | Cgroup CPU usage expressed as CPU cores over the sample interval |
| cpu_quota | Raw cgroup CPU quota value when available |
| cpu_throttled_delta_usec | Change in throttled CPU time, normalized to microseconds |
| rss_kb | Go process resident memory |
| open_fds | Open file descriptor count |
| cgroup_memory_current / cgroup_memory_max | Current cgroup memory and configured maximum |
| scheduler_lag_ms | Delay relative to the scheduled metrics sample |

For cgroup v2, throttled_usec is already in microseconds. For cgroup v1, throttled_time is nanoseconds and is divided by 1,000 before reporting. The field name now explicitly states its unit.

A value of n/a means the corresponding procfs or cgroup metric was unavailable in that runtime. It is not itself a service failure.

## Client IP Verification

The current code selects the rightmost valid IP from X-Forwarded-For for the direct Cloud Run run.app ingress path. Every Cloud Run deployment must verify the session field:

```text
peer_ip=<expected client public IP>
```

If it shows unknown, the bridge did not find a valid forwarded address and will skip per-IP guard accounting for that connection. If it shows a proxy address or a different client, stop load testing and inspect the actual header chain before changing guard policy.

Do not blindly trust CF-Connecting-IP, True-Client-IP, or arbitrary client-supplied forwarding headers. If an external proxy is added later, define and verify the trusted proxy chain first.

## First-Response Checklist

1. Confirm startup metadata fields and Go component=go-bridge boot events appear.
2. Confirm Nginx passes its nginx -t check and starts after the Go bridge listener is ready.
3. Confirm Dropbear starts and UDPGW prints its version.
4. Check peer_ip in a session log before evaluating per-IP limits.
5. Confirm component=telemetry snapshots continue at roughly 10-second intervals.
6. During concurrent tests, compare active_connections, active_handshakes, and active_ssh_sessions.
7. After disconnecting test clients, confirm active counters decrease and session_closed records include bytes and end reasons.
8. Confirm custom_metrics_write_failed is absent and query a custom.googleapis.com/x_relay/active_ssh_sessions time series.
9. Correlate bridge session events with component=nginx-stream access records and the native Dropbear/UDPGW process output.

## Common Signals

| Signal | Initial interpretation |
| --- | --- |
| No bridge_ready event | Go process failed before binding or the container did not reach bridge startup |
| cloud_monitoring_exporter_disabled_or_unavailable | Monitoring is disabled or required Cloud Run metadata was unavailable |
| custom_metrics_write_failed | Check Monitoring API enablement, runtime service identity, IAM role, and API error body |
| No active_ssh_sessions increase | Handshake did not reach a successful Dropbear backend TCP connection |
| failed_handshakes increases | Clients are timing out, sending oversized headers, or not completing WebSocket Upgrade |
| failed_ssh_connections increases | Go bridge could not connect to Dropbear on the configured SSH backend |
| rejected_connections increases | Per-IP burst or active-session guard rejected a connection |
| CPU throttling increases under load | Compare container CPU use and Cloud Run CPU allocation before changing concurrency |
| Memory or FD count continually increases | Inspect active sessions, disconnect cleanup, and resource limits |

See [WP04: Cloud Run Validation and Performance Testing](../testing/cloud-run-validation.md) before attributing a runtime symptom to a specific code path.
