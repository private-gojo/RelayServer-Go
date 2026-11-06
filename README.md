# RelayServer-Go (`x-relay`)

Multi-core Go + Rust high-throughput SSH+WS encapsulation relay server engineered for Cloud Run (Gen2) and CDN edge routing.

## Directory Structure
```text
RelayServer-Go/
├── relay-ssh/
│   └── bridge.go                    # Multi-threaded Go WS-to-SSH encapsulation bridge
├── relay-security/
│   ├── guard.go                     # Per-IP active connection & burst rate limiter + CDN IP extractor
│   └── nginx.conf.template          # Front-end Nginx stream layer & L4 conn limit
├── relay-web/
│   └── banner.txt                   # HTML SSH server banner (ready for future web panel)
├── relay-utilities/
│   ├── metrics.go                   # 10s cgroup v2 CPU/Memory + /proc FD/RSS + session telemetry
│   ├── entrypoint.sh                # Account provisioning, RSA/ECDSA/Ed25519 keygen, diagnostics
│   ├── supervisord.conf             # Process supervisor (nginx, dropbear, x-relay-bridge, udpgw)
│   ├── wait-for-bridge.sh           # Race-free startup gate for Nginx
│   └── deploy.sh                    # Cloud Run Gen2 deployment script
├── relay-rust/                      # Tokio multi-threaded Rust badvpn-udpgw (120s idle socket reaper)
├── relay-developments/              # Staging directory for experimental features before merging to main
├── relay-docs/ci/
│   ├── README.md                    # CI/CD and architecture documentation
│   └── x-relay.yml                  # GitHub Actions workflow template (copy to .github/workflows/x-relay.yml)
├── Dockerfile                       # 4-stage build (Go + Rust + Dropbear all-ciphers -> Alpine runtime)
├── go.mod
└── LICENSE                          # MIT License
```
