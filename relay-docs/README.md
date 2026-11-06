# RelayServer-Go Documentation

This documentation describes the purpose, architecture, deployment, runtime diagnostics, validation process, and release-artifact policy for RelayServer-Go.

> **Document status:** GitHub CI is green. Live Cloud Run validation and end-to-end performance comparison are pending.
>
> **Change record**
> - **Changed:** Documentation is grouped by engineering work package.
> - **Added:** Architecture, deployment, operations, validation, and release-artifact guides.
> - **Removed:** Redundant repository directory listings from the root README.

| Work package | Document | Purpose |
| --- | --- | --- |
| WP01 | [Architecture and Project Purpose](architecture/overview.md) | Transport behavior, process roles, boundaries, and connection flow |
| WP02 | [Cloud Run Deployment](deployment/cloud-run.md) | Prerequisites, deployment, configuration, and logs |
| WP03 | [Runtime Diagnostics and Operations](operations/diagnostics.md) | Startup, session, resource, Nginx, and process log interpretation |
| WP04 | [Cloud Run Validation and Performance Testing](testing/cloud-run-validation.md) | Real-environment test plan and X-Sorcerer A/B comparison |
| WP05 | [Release Artifacts and Versioning](releases/artifacts.md) | CI artifacts, Docker image tags, and future versioned releases |
| WP06 | [GitHub Actions and CI Reference](ci/README.md) | WP-numbered workflow stages and pipeline outputs |

## Documentation Conventions

Each work-package document starts with a `WP<number>` identifier and a change record:

- **Changed:** Existing behavior or documentation clarified or updated.
- **Added:** New behavior, instructions, or reference material.
- **Removed:** Material intentionally deleted. Use `None` when nothing was removed.

Each document also states its validation status. Static review and green CI do not imply that the feature has passed a live Cloud Run deployment or a real network speed test.
