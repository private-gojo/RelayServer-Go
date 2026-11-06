# WP04: Cloud Run Validation and Performance Testing

> **Status:** Go checks, Rust tests/build, Docker build, and branch image publication pass in GitHub Actions. Live Cloud Run deployment and A/B speed testing are pending.
>
> **Scope:** Separate CI validation from deployed-environment validation and define a repeatable comparison against X-Sorcerer.
>
> **Change record**
> - **Changed:** CI results are explicitly separated from runtime and performance claims.
> - **Added:** Deployment smoke tests, SSH+WS/UDPGW tests, stress-test observations, and an A/B test method.
> - **Removed:** No source code or runtime behavior.

## Validation Status

| Layer | Current status | Evidence / next check |
| --- | --- | --- |
| Go formatting | Passed | GitHub Actions WP3 |
| Go vet | Passed | GitHub Actions WP4 |
| Go unit and race tests | Passed | GitHub Actions WP5 |
| Go microbenchmarks | Passed | GitHub Actions WP6 |
| Go bridge build | Passed | GitHub Actions WP8 |
| Rust UDPGW tests and release build | Passed as part of Docker build | Docker build runs `cargo test --locked` then `cargo build --locked --release` |
| Docker image build | Passed | GitHub Actions WP12 |
| Cloud Run revision startup | Pending | Deploy and inspect revision logs |
| SSH+WS client compatibility | Pending | Connect with the same client/config used for X-Sorcerer |
| UDPGW forwarding | Pending | Test a UDP-dependent workload |
| Long-lived connection/reconnect | Pending | Test timeout, disconnect, and reconnect behavior |
| Stress and resource behavior | Pending | Observe sessions, CPU throttling, memory, FDs, and log errors |
| X-Sorcerer speed comparison | Pending | Run controlled A/B tests on the same network path |

## CI Microbenchmark Baseline

Latest recorded CI benchmark (GitHub-hosted AMD EPYC 9V74 runner):

| Benchmark | Result | Allocations |
| --- | ---: | ---: |
| Ingress guard | 241.5 ns/op | 96 B/op, 4 allocs/op |
| HTTP Upgrade header parser | 230.1 ns/op | 240 B/op, 4 allocs/op |

These measure isolated Go functions. They are not tunnel throughput, end-to-end latency, or a direct performance comparison with X-Sorcerer.

## Cloud Run Smoke Test

After deploying the feature image or the merged main image:

1. Confirm Cloud Run reports the latest revision as ready.
2. Inspect startup logs and verify Go bridge, Nginx, Dropbear, and UDPGW startup.
3. Confirm the selected `real_ip` is the actual test client's public IP.
4. Connect an SSH+WS client and verify the HTTP Upgrade succeeds.
5. Verify the SSH session authenticates and transfers data in both directions.
6. Disconnect the client and inspect session end reason and active-session count.
7. Test the Rust UDPGW path with an application that actually uses UDP.
8. Keep a connection open long enough to test idle handling, Cloud Run request timeout, and client reconnect behavior.

Use [WP03: Runtime Diagnostics and Operations](../operations/diagnostics.md) to interpret the logs.

## Stress Test

Increase load in controlled steps rather than starting at maximum concurrency. At each step, record:

- Concurrent SSH+WS sessions and connection establishment rate.
- Download and upload throughput, latency, jitter, and reconnect frequency.
- Container CPU cores, CPU throttling delta, RSS, cgroup memory, and open file descriptors.
- Guard rejections, handshake failures, SSH backend errors, and Nginx upstream errors.
- Whether all session counters return toward baseline after clients disconnect.

Cloud Run configuration values are limits and starting points, not a promise that the container can sustain the configured concurrency. Tune concurrency, instance count, and CPU only after observing actual resource behavior.

## A/B Comparison Against X-Sorcerer

Use the same phone, SIM, carrier, client application/version, SSH credentials profile, destination, Cloud Run region, and test location. Keep DNS, client transport settings, and test server location consistent.

Recommended sequence:

1. Run X-Sorcerer, then RelayServer-Go.
2. Repeat in the reverse order to reduce time-of-day bias.
3. Perform at least three paired runs for download and upload.
4. Record median throughput plus latency, jitter, connection stability, and reconnect events.
5. Compare Cloud Run resource metrics for the matching test windows.

Record the following for each run:

| Field | X-Sorcerer | RelayServer-Go |
| --- | --- | --- |
| Revision / image tag |  |  |
| Date and local time |  |  |
| Network / carrier |  |  |
| Client app and version |  |  |
| Download Mbps |  |  |
| Upload Mbps |  |  |
| Idle latency ms |  |  |
| Loaded latency ms |  |  |
| Jitter ms |  |  |
| Disconnects / reconnects |  |  |
| Average / peak CPU |  |  |
| Peak memory |  |  |

Do not compare a single speedtest run as a definitive result. Cellular load, radio conditions, route selection, and Cloud Run instance placement can change between tests.

## Acceptance Conditions Before Performance Tuning

- Cloud Run revision becomes ready without container startup errors.
- All four supervised processes start and remain running.
- SSH+WS transfers bidirectionally without truncation or unexplained resets.
- The selected client IP is confirmed from the real Cloud Run request headers.
- Per-IP guard accounting does not collapse clients into a localhost or shared unknown bucket.
- UDPGW accepts a compatible client and forwards the intended UDP traffic.
- No unexplained growth in sessions, memory, or open file descriptors after disconnects.
- A/B results are repeatable enough to identify a meaningful performance difference.

Only after these checks should Nginx replacement, buffer adjustments, cipher changes, or concurrency tuning be considered.
