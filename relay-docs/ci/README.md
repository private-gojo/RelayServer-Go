# WP06: GitHub Actions and CI Reference

> **Status:** Current feature-branch workflow passed on commit `6bc53cd033e9d99242a73198702e9a0b75146516`.
>
> **Scope:** Explain the active `.github/workflows/RelayServer-Go.yml` pipeline and its outputs.
>
> **Change record**
> - **Changed:** Replaced the old CI reference with the active Go-based workflow description.
> - **Added:** WP-numbered stage map, image tag policy, and the CI-versus-Cloud-Run validation boundary.
> - **Removed:** The obsolete `relay-docs/ci/x-relay.yml` workflow template.

## Active Workflow

File: `.github/workflows/RelayServer-Go.yml`

The workflow runs for branch pushes, pull requests targeting `main`, and manual dispatch. Its job validates Go code and builds or publishes the container image.

| Workflow step | Purpose |
| --- | --- |
| WP1 | Check out the repository |
| WP2 | Install Go 1.27.3 |
| WP3 | Verify `gofmt` output |
| WP4 | Run `go vet ./...` |
| WP5 | Run `go test -race ./...` |
| WP6 | Run Go microbenchmarks with allocation reporting |
| WP7 | Upload `benchmark.txt` as a GitHub Actions artifact |
| WP8 | Build the Linux Go bridge |
| WP9 | Authenticate to Docker Hub |
| WP10 | Set up Docker Buildx |
| WP11 | Build pull-request images without publishing |
| WP12 | Publish feature-branch images using the commit SHA |
| WP13 | Publish the `main` image tags, including `latest` |

## Image Tags

- Feature branches: `DOCKERHUB_USERNAME/x-relay:<commit-sha>`.
- Main branch: `DOCKERHUB_USERNAME/x-relay:<commit-sha>` and `DOCKERHUB_USERNAME/x-relay:latest`.
- Pull requests: build-only; no image is pushed.

Required repository secrets for image publishing:

- `DOCKERHUB_USERNAME`
- `DOCKERHUB_TOKEN`

## Current CI Evidence

The latest successful feature-branch workflow before the documentation update is [Run #9](https://github.com/private-gojo/RelayServer-Go/actions/runs/42220651337), for commit `6bc53cd033e9d99242a73198702e9a0b75146516`.

That run passed formatting, vet, race tests, Go microbenchmarks, Go bridge compilation, Docker authentication, and feature-image publication. The Docker build also runs the Rust UDPGW test suite before producing the runtime image.

## CI Is Not Cloud Run Validation

A green workflow does not establish that Cloud Run started the image, that the real Cloud Run forwarding headers match the IP parser assumption, or that an SSH+WS client can complete a sustained tunnel.

Use [WP04: Cloud Run Validation and Performance Testing](../testing/cloud-run-validation.md) for the actual deployment and A/B test gates.
