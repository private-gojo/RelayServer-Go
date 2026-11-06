# WP02: Cloud Run Deployment

> **Status:** Deployment instructions reviewed against the current repository script and Google Cloud documentation; live deployment validation pending.
>
> **Scope:** Build and deploy the current container to Cloud Run Gen2, configure credentials, and collect runtime logs.
>
> **Change record**
> - **Changed:** Documented the repository's current Cloud Build → Cloud Run deployment path.
> - **Added:** Prerequisites, service settings, credential guidance, and verification commands.
> - **Removed:** No runtime configuration or deployment behavior.

## Prerequisites

- A Google Cloud project with billing enabled.
- Google Cloud CLI authenticated to the intended project.
- Cloud Run and Cloud Build enabled, plus the container image registry used by the deployment script.
- Permission to build container images and deploy or update Cloud Run services.

Set the project and region before running the script:

```bash
gcloud auth login
gcloud config set project PROJECT_ID
gcloud config set run/region asia-east1
```

## Deploy Using the Repository Script

Run from the repository root:

```bash
bash relay-utilities/deploy.sh
```

The script currently:

1. Reads the active Google Cloud project and Cloud Run region.
2. Builds the local repository with Cloud Build and tags the image as `gcr.io/PROJECT_ID/x-relay:latest`.
3. Deploys the `x-relay` service on Cloud Run Gen2.

The current deployment settings are:

| Setting | Value |
| --- | ---: |
| Container port | 8080 |
| CPU | 2 vCPU |
| Memory | 2 GiB |
| Request concurrency | 500 |
| Minimum instances | 1 |
| Maximum instances | 16 |
| Request timeout | 3,600 seconds |
| CPU allocation | Always allocated |
| Session affinity | Enabled |
| Public invocation | Enabled |

These are the current script values, not proven optimal capacity settings. Tune them only after observing Cloud Run CPU, memory, throttling, connection count, and real client performance.

## Credentials

When `ACCOUNTS` is not set, the entrypoint creates the initial account:

- Username: `gojo`
- Password: `xrelay`

This default is intended for initial smoke testing. Set a deployment-specific `ACCOUNTS` value before leaving a service publicly accessible. Format:

```text
user:password
user1:password1,user2:password2
```

Use Cloud Run Secret Manager integration for production credentials. Do not commit passwords to the repository or include them in shell history.

Cloud Run's `PORT` environment variable is supplied by the platform. Nginx uses it for the public listener. The Go bridge, Dropbear, and UDPGW use their internal loopback listeners; they do not need separate public ports.

## Logs and Deployment Checks

After deployment, read the recent service logs:

```bash
gcloud run services logs read x-relay \
  --region asia-east1 \
  --limit 200
```

The expected log groups are documented in [WP03: Runtime Diagnostics and Operations](../operations/diagnostics.md).

Check the latest revision and service status:

```bash
gcloud run services describe x-relay \
  --region asia-east1 \
  --format='yaml(status.url,status.latestReadyRevisionName,status.conditions)'
```

A successful deployment only confirms that Cloud Run accepted and started the revision. It does not confirm SSH+WS compatibility, UDP forwarding, or throughput. Complete the checks in [WP04: Cloud Run Validation and Performance Testing](../testing/cloud-run-validation.md).

## Cloud Run Streaming Considerations

Cloud Run supports WebSocket requests without special service configuration. WebSocket connections are still subject to the Cloud Run request timeout. The documented maximum request timeout is 60 minutes; clients need reconnect handling when requests time out or the server disconnects. Session affinity is best-effort and does not guarantee that a reconnect reaches the same instance. Do not enable end-to-end HTTP/2 for this transport.

References:

- [Google Cloud: Using WebSockets on Cloud Run](https://docs.cloud.google.com/run/docs/triggering/websockets)
- [Google Cloud: Configure request timeout](https://docs.cloud.google.com/run/docs/configuring/request-timeout)
- [Google Cloud: Deploy container images to Cloud Run](https://docs.cloud.google.com/run/docs/deploying)

## Deployment Notes

- The repository script currently uses the `gcr.io` image naming path. The GitHub Actions workflow separately publishes Docker Hub branch images. They are different image publishing paths.
- The GitHub Actions branch image is tagged with the commit SHA. It is not automatically selected by `deploy.sh`.
- The current service keeps Nginx Stream in front of the Go bridge. Do not remove Nginx until live connection, diagnostics, and performance tests demonstrate that a replacement or direct ingress path is safe.
