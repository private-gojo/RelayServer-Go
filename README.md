# RelayServer-Go

SSH over WebSocket upgrade (SSH+WS) tunneling encapsulation relay, implemented in Go and packaged with a Rust-based BadVPN UDP gateway.

RelayServer-Go provides a concurrent SSH transport endpoint for compatible SSH+WS clients. The Go bridge handles the HTTP/1.1 Upgrade handshake and relays the resulting connection as a bidirectional TCP byte stream to the SSH backend. A separate Rust UDPGW process provides the BadVPN UDP gateway protocol for compatible UDP-over-TCP clients.

## Project Purpose

1. **SSH+WS transport** — parse the HTTP/1.1 Upgrade request, return the relay handshake response, preserve bytes received alongside the headers, and forward the SSH stream in both directions.
2. **SSH service** — run Dropbear as the SSH backend with a modern algorithm profile and configurable accounts.
3. **UDP gateway** — run the separate Rust/Tokio BadVPN UDPGW implementation for clients that need UDP forwarding.
4. **Ingress and security** — retain Nginx as a Layer-4 TCP stream proxy while Cloud Run behavior, connection limits, and client-IP handling are validated.
5. **Operations and diagnostics** — expose startup, session, resource, and process logs through Cloud Run Logs.

The WebSocket Upgrade exchange is used as the transport bootstrap. After the `101 Switching Protocols` response, the bridge forwards the tunnel as a raw TCP byte stream; it does not implement RFC 6455 WebSocket data-frame encoding.

## Runtime Components

| Component | Responsibility |
| --- | --- |
| Go bridge | HTTP Upgrade parsing, synthetic `101` response, pipelined-byte forwarding, full-duplex relay, timeouts, socket options, and session accounting |
| Dropbear | SSH authentication and encrypted SSH sessions |
| Rust UDPGW | Separate Tokio-based BadVPN UDP gateway on loopback port `7300` |
| Nginx Stream | Public Cloud Run `PORT` listener and TCP forwarding to the Go bridge |
| Supervisor | Starts and restarts Nginx, Dropbear, Go bridge, and UDPGW |
| Diagnostics | Startup checks, 10-second resource metrics, Nginx access logs, and per-session logs |

## Connection Flow

**SSH+WS:** Cloud Run HTTP/1.1 ingress → Nginx Stream → Go bridge → Dropbear SSH (`127.0.0.1:2224`).

**UDP forwarding:** compatible client → SSH tunnel → local UDPGW service (`127.0.0.1:7300`). UDPGW is a separate process; it is not the Go WebSocket relay.

## Current Runtime Defaults

- Public container port: Cloud Run-provided `PORT` (defaults to `8080` in the container).
- Go bridge: `127.0.0.1:8081`.
- Dropbear: `127.0.0.1:2224`.
- UDPGW: `127.0.0.1:7300`.
- Relay buffer: `256 KiB`.
- Handshake timeout: `4 seconds`.
- Relay idle timeout: `600 seconds`.
- Default SSH account when `ACCOUNTS` is unset: `gojo:xrelay`.

Set deployment-specific credentials with `ACCOUNTS` before exposing a production service. Prefer Cloud Run Secret Manager integration rather than storing passwords in source code or shell history. The default account is for initial testing only.

## Deploy to Google Cloud Run

The repository includes a deployment script:

```bash
gcloud auth login
gcloud config set project PROJECT_ID
gcloud config set run/region asia-east1
bash relay-utilities/deploy.sh
```

The script builds the container with Cloud Build and deploys the `x-relay` Cloud Run service. Its current settings include Gen2, port `8080`, 2 vCPU, 2 GiB memory, 500 request concurrency, 16 maximum instances, one minimum instance, a 3600-second request timeout, session affinity, and always-allocated CPU.

Read [WP02: Cloud Run Deployment](relay-docs/deployment/cloud-run.md) before changing the service configuration. Cloud Run WebSocket connections are subject to the request timeout; clients must reconnect when a connection expires, and session affinity is best-effort.

## Testing and Validation

GitHub Actions validates Go formatting, `go vet`, race-enabled unit tests, microbenchmarks, the Go bridge build, Rust UDPGW tests/build, and the Docker image build. Feature-branch images are published with commit-specific tags; only `main` updates the `latest` tag.

**CI success is not the same as a successful Cloud Run deployment.** Live Cloud Run startup, SSH+WS compatibility, UDP forwarding, session behavior, and end-to-end speed must still be tested in the deployed environment.

See [WP04: Cloud Run Validation and Performance Testing](relay-docs/testing/cloud-run-validation.md) for the deployment checklist and the A/B comparison plan against X-Sorcerer.

## Documentation

- [WP01: Architecture and Project Purpose](relay-docs/architecture/overview.md)
- [WP02: Cloud Run Deployment](relay-docs/deployment/cloud-run.md)
- [WP03: Runtime Diagnostics and Operations](relay-docs/operations/diagnostics.md)
- [WP04: Cloud Run Validation and Performance Testing](relay-docs/testing/cloud-run-validation.md)
- [WP05: Release Artifacts and Versioning](relay-docs/releases/artifacts.md)
- [CI Workflow Reference](relay-docs/ci/README.md)

## License

Copyright (C) 2026 Gojo Research.

The RelayServer-Go project code is licensed under GNU AGPL-3.0-or-later. The separate `relay-rust/` UDPGW component retains its upstream MIT license and copyright notices. See [LICENSE](LICENSE), [NOTICE](NOTICE), and [relay-rust/LICENSE](relay-rust/LICENSE).
