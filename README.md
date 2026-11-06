# RelayServer-Go (`x-relay`)

Multi-core Go + Rust SSH+WS encapsulation relay server for Cloud Run (Gen2) and CDN edge routing.

## Directory Structure
```text
RelayServer-Go/
├── relay-ssh/
│   └── bridge.go                    # Concurrent Go WS-to-SSH encapsulation bridge with 256 KiB relay buffers
├── relay-security/
│   ├── guard.go                     # GFE X-Forwarded-For client-IP extraction and per-IP ingress guard
│   └── nginx.conf.template          # Nginx Layer-4 TCP passthrough and stream diagnostics
├── relay-web/
│   └── banner.txt                   # HTML SSH server banner (ready for future web panel)
├── relay-utilities/
│   ├── metrics.go                   # 10s cgroup CPU/Memory + /proc FD/RSS + session telemetry
│   ├── entrypoint.sh                # Account provisioning, RSA/ECDSA/Ed25519 keygen, Cloud Run diagnostics
│   ├── supervisord.conf             # Process supervisor (nginx, dropbear, x-relay-bridge, udpgw)
│   ├── wait-for-bridge.sh           # Race-free startup gate for Nginx
│   └── deploy.sh                    # Cloud Run Gen2 deployment script
├── relay-rust/                      # Tokio multi-threaded Rust badvpn-udpgw (120s idle socket reaper)
├── relay-developments/              # Staging directory for experimental features before merging to main
├── relay-docs/ci/README.md          # CI/CD and architecture documentation
├── .github/workflows/RelayServer-Go.yml # Branch CI, race tests, benchmarks and image build
├── Dockerfile                       # 4-stage build (Go + Rust + Dropbear modern cipher profile -> Alpine runtime)
├── go.mod
└── LICENSE                          # AGPL-3.0-or-later (root project)
```

## Runtime and Testing

- Go toolchain: 1.27.3. The Go runtime manages GOMAXPROCS using the container CPU quota; the bridge does not force NumCPU.
- Default SSH account: `gojo:xrelay`. Set `ACCOUNTS` to provision deployment-specific credentials.
- The bridge advertises the source repository through the `X-Source-Code` response header.
- Nginx remains the Layer-4 TCP front end pending Cloud Run integration and load testing.
- The CI workflow runs Go formatting checks, vet, race-enabled tests, guard benchmarks, and a Docker build. Feature-branch image tags use the commit SHA; only main updates `latest`.

## Licensing

Copyright (C) 2026 Gojo Research.

The root project is licensed under GNU AGPL-3.0-or-later. Modified versions operated as network services must provide remote users an opportunity to obtain the corresponding source under AGPL section 13. This license governs derivatives; it does not prevent independent reimplementations of the same general idea.

The independent Rust UDPGW component under `relay-rust/` retains its own MIT license and original copyright notice. The root AGPL license does not replace that component-level license.
