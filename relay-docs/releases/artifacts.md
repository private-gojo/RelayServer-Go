# WP05: Release Artifacts and Versioning

> **Status:** CI benchmark artifacts and commit-tagged branch images are available. Stable versioned releases are intentionally pending Cloud Run validation.
>
> **Scope:** Define where build outputs belong and how future versioned releases should be collected and verified.
>
> **Change record**
> - **Changed:** Distinguished temporary GitHub Actions artifacts from permanent release assets and container images.
> - **Added:** A release naming, checksum, and publication policy.
> - **Removed:** No committed binary build output; generated executables should not be stored in the source tree.

## Artifact Categories

| Output | Storage | Purpose |
| --- | --- | --- |
| Benchmark results | GitHub Actions run artifact | Temporary evidence from CI microbenchmarks |
| Feature container image | Docker Hub, commit-specific tag | Deploy and validate an exact feature-branch build |
| Stable container image | Docker Hub, semantic version tag | Reproducible deployment of a tested release |
| Release notes and metadata | GitHub Release | Version summary, source commit, image digest, validation status |
| SBOM and provenance | GitHub Release assets or attached attestations | Component inventory and build provenance |

Do not commit generated binaries, Docker layers, or benchmark output into the source repository. Keep source and release outputs separate.

## Upstream UDPGW Artifact Pattern

The upstream [RustBadVpnUdpgw Releases](https://github.com/Dean6767/RustBadVpnUdpgw/releases) page provides a useful example. Its v0.2.0 release publishes architecture-specific Linux musl archives and a matching SHA-256 file for each archive:

- `badvpn-udpgw-v0.2.0-x86_64-linux-musl.tar.gz`
- `badvpn-udpgw-v0.2.0-x86_64-linux-musl.tar.gz.sha256`
- `badvpn-udpgw-v0.2.0-aarch64-linux-musl.tar.gz`
- `badvpn-udpgw-v0.2.0-aarch64-linux-musl.tar.gz.sha256`

This is appropriate for a standalone binary distributed across CPU architectures. RelayServer-Go is primarily a Cloud Run container service, so the versioned OCI container image should be the primary deployable release artifact.

## Proposed RelayServer-Go Release Layout

After live Cloud Run and performance validation, a tagged release should include:

- Docker image: `DOCKERHUB_USERNAME/x-relay:vX.Y.Z`.
- Immutable image digest recorded in release notes or `image-digest.txt`.
- `SBOM.spdx.json` for the published image.
- `checksums.txt` for downloadable release files.
- `validation.md` summarizing CI, Cloud Run smoke tests, and A/B test status.
- Optional standalone Go or Rust binary archives only if there is a supported direct-binary use case and the target platforms have been built and tested.

Any binary archive should include the OS, architecture, and libc target in the file name, and each archive must have a matching SHA-256 checksum. Do not label an architecture as supported until its build and runtime behavior have been verified.

## When to Publish

Do not create a stable release just because GitHub Actions is green. Current CI validates compilation, tests, microbenchmarks, and container construction. It does not prove that the service deploys correctly or performs well on Cloud Run.

Recommended release gate:

1. CI passes on the release candidate.
2. Cloud Run revision starts and all supervised services remain healthy.
3. SSH+WS, UDPGW, reconnect, and resource checks pass in the actual deployment.
4. A/B testing against X-Sorcerer has been recorded.
5. Release image digest, SBOM, checksums, and validation notes are generated and attached to the GitHub Release.

Until that gate is met, use commit-specific feature image tags and GitHub Actions benchmark artifacts. Keep `latest` reserved for merges to `main`.
