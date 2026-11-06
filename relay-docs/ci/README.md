# X-Relay CI/CD & Architecture Reference (`relay-docs/ci/`)

## Repository Layout
- `relay-ssh/` — Multi-core Go WebSocket-to-SSH encapsulation bridge (`x-relay-bridge`), preserving exact 101 handshake, leftover pipelined SSH frame forwarding, TCP half-close (`CloseWrite`), and kernel socket tuning.
- `relay-security/` — Layer-4 Nginx stream config (`nginx.conf.template`) and Layer-7 Go ingress guard (`guard.go`) for per-IP rate limiting, burst banning, and CDN IP extraction.
- `relay-web/` — Web assets, SSH HTML banner (`banner.txt`), and future management panel files.
- `relay-utilities/` — Container runtime orchestration (`entrypoint.sh`, `supervisord.conf`, `wait-for-bridge.sh`, `deploy.sh`) and 10-second cgroup/proc telemetry (`metrics.go`).
- `relay-rust/` — Multi-threaded Tokio Rust `badvpn-udpgw` implementation with 120s idle UDP socket reaper.
- `relay-developments/` — Sandbox for experimental protocols and staging features before merging to `main`.
- `relay-docs/ci/` — CI/CD pipeline documentation and build specifications (`x-relay.yml` template).

## Docker Image Naming Convention
- **Image Name:** `${DOCKERHUB_USERNAME}/x-relay:latest` and `${DOCKERHUB_USERNAME}/x-relay:${GITHUB_SHA}`
