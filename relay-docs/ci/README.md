# X-Relay CI/CD & Architecture Reference (`relay-docs/ci/`)

## Repository Layout
- `relay-ssh/` — Multi-core Go WebSocket-to-SSH encapsulation bridge (`x-relay-bridge`), preserving exact 101 handshake, leftover pipelined SSH frame forwarding, TCP half-close (`CloseWrite`), and kernel socket tuning.
- `relay-security/` — Layer-4 Nginx stream config (`nginx.conf.template`) and Go ingress guard (`guard.go`) for per-client rate limiting using the GFE-appended X-Forwarded-For client position.
- `relay-web/` — Web assets, SSH HTML banner (`banner.txt`), and future management panel files.
- `relay-utilities/` — Container runtime orchestration (`entrypoint.sh`, `supervisord.conf`, `wait-for-bridge.sh`, `deploy.sh`) and 10-second cgroup/proc telemetry (`metrics.go`).
- `relay-rust/` — Multi-threaded Tokio Rust `badvpn-udpgw` implementation with 120s idle UDP socket reaper.
- `relay-developments/` — Sandbox for experimental protocols and staging features before merging to `main`.
- `relay-docs/ci/` — CI/CD pipeline documentation and build specifications (`.github/workflows/RelayServer-Go.yml`).

## Docker Image Naming Convention
- **Image Name:** `${DOCKERHUB_USERNAME}/x-relay:latest` and `${DOCKERHUB_USERNAME}/x-relay:${GITHUB_SHA}`


## Active Workflow

The active workflow is `.github/workflows/RelayServer-Go.yml`. It runs on pushes to branches, pull requests targeting `main`, and manual dispatch. Steps are numbered with the `WP` prefix for consistent CI log navigation.

Feature branches run formatting, vet, race-enabled tests, guard benchmarks, and a container build. Pushes to feature branches publish SHA-tagged test images without updating `latest`. Pushes to `main` publish both SHA and `latest` tags.

## Runtime Guard Notes

The Go bridge parses the original HTTP headers before selecting a client address. For GFE ingress, it uses the second-to-last valid IP in `X-Forwarded-For`, which represents the client address when Google appends the client and load-balancer addresses. Direct Cloud Run testing is required to verify the deployed header path.
