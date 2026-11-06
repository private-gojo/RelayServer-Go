# WP01: Architecture and Project Purpose

> **Status:** Implementation and GitHub CI validated; Cloud Run runtime validation pending.
>
> **Scope:** Describe transport behavior, process boundaries, and responsibilities of the Go, Rust, SSH, and ingress components.
>
> **Change record**
> - **Changed:** Replaced the original Python relay implementation with a Go bridge while retaining the established SSH+WS behavior.
> - **Added:** Explicit component boundaries, raw-stream behavior, and the separate UDPGW path.
> - **Removed:** Python runtime and Python relay dependency from the container.

## Purpose

RelayServer-Go provides an SSH-over-WebSocket-upgrade tunneling relay for compatible clients. The Go bridge parses the HTTP/1.1 Upgrade request, emits the configured `101 Switching Protocols` response, connects to the local SSH service, and forwards bytes in both directions.

The Upgrade handshake is used to establish the stream. The bridge does not decode or generate RFC 6455 WebSocket data frames after the handshake; the tunnel payload is relayed as a raw TCP byte stream.

The Rust UDPGW executable is an independent companion process. It implements the BadVPN UDPGW protocol for compatible clients that require UDP forwarding. It is not part of the Go SSH+WS byte-forwarding loop.

## Connection Paths

### SSH+WS

1. Cloud Run receives the HTTP/1.1 Upgrade request on the configured container port.
2. Nginx Stream passes the TCP connection to the Go bridge at `127.0.0.1:8081`.
3. The Go bridge reads the HTTP headers up to `CRLF CRLF`, with a 16 KiB header limit and a 4-second handshake timeout.
4. The bridge detects a WebSocket Upgrade request and sends the synthetic `101 Switching Protocols` response.
5. Any bytes received after the HTTP header terminator in the same read are preserved and sent to the SSH backend.
6. The bridge connects to Dropbear at `127.0.0.1:2224` and runs two concurrent relay directions.
7. TCP half-close is used where available so EOF in one direction does not unnecessarily discard remaining traffic in the other direction.

Requests without a WebSocket Upgrade header receive HTTP 403. Health probes at `/healthz` and `/health` receive HTTP 200.

### UDPGW

The Rust UDPGW process listens on `127.0.0.1:7300`. It is started and supervised separately from the Go bridge. Its configured runtime includes a 120-second UDP idle timeout, up to 1,000 clients, and up to 1,024 connections per client.

## Process Responsibilities

| Process | Role | Listener / destination |
| --- | --- | --- |
| Nginx Stream | Layer-4 public ingress and TCP proxy | Cloud Run `PORT` → `127.0.0.1:8081` |
| Go bridge | HTTP Upgrade parsing and bidirectional SSH stream relay | `127.0.0.1:8081` → `127.0.0.1:2224` |
| Dropbear | SSH authentication and encrypted session handling | `127.0.0.1:2224` |
| Rust UDPGW | BadVPN UDP gateway protocol | `127.0.0.1:7300` |
| Supervisor | Process lifecycle and restart handling | Container process manager |

Nginx remains in the design during initial Cloud Run validation. Replacing it with another reverse proxy is a separate optimization decision and should follow measurements from the deployed service.

## Relay and Security Defaults

| Setting | Current value |
| --- | ---: |
| Go relay buffer | 256 KiB |
| HTTP header limit | 16 KiB |
| Handshake timeout | 4 seconds |
| Relay idle timeout | 600 seconds |
| TCP keepalive idle / interval / count | 15 s / 10 s / 3 |
| Maximum active sessions per client IP | 1,024 |
| Rate window | 10 seconds |
| Requests per window before ban | 5,000 |
| Temporary ban | 30 seconds |

The guard skips per-IP accounting when no valid client IP is available. This avoids treating every connection from the local Nginx address as one shared client. Nginx and Cloud Run limits remain separate controls.

The current client-IP selection uses the rightmost valid address in `X-Forwarded-For` for the direct Cloud Run `run.app` ingress path. Confirm the resulting `real_ip` in the deployed logs before relying on it. If an external proxy or load balancer is introduced, re-evaluate the trusted forwarding-hop selection instead of blindly trusting client-supplied headers.

## Runtime and Build

- Go toolchain: `1.27.3` in CI and the Go builder image.
- Go module language version: `1.27`.
- Rust UDPGW: built with Cargo from the source retained in `relay-rust/`; the Docker build runs `cargo test --locked` before the release build.
- Dropbear: built from the pinned source version in the Dockerfile using the X-Sorcerer crypto profile.
- Runtime: Alpine Linux with Nginx Stream, Supervisor, Dropbear, Go bridge, and Rust UDPGW.

The Go runtime is responsible for scheduling goroutines. The application does not force `GOMAXPROCS` to the host CPU count; actual CPU allocation and throttling are measured from cgroup diagnostics in Cloud Run.

## Related Work Packages

- [WP02: Cloud Run Deployment](../deployment/cloud-run.md)
- [WP03: Runtime Diagnostics and Operations](../operations/diagnostics.md)
- [WP04: Cloud Run Validation and Performance Testing](../testing/cloud-run-validation.md)
