#!/bin/bash
# ============================================================
# GOJO X-RELAY — CLOUD RUN DEPLOY SCRIPT (relay-utilities)
# ============================================================

set -e

PROJECT_ID=$(gcloud config get-value project)
SERVICE_NAME="x-relay"
REGION=$(gcloud config get-value run/region 2>/dev/null || echo "asia-east1")

echo "=== Building and pushing x-relay image via Cloud Build ==="
gcloud builds submit --tag "gcr.io/${PROJECT_ID}/${SERVICE_NAME}:latest" .

echo "=== Deploying ${SERVICE_NAME} to Cloud Run (${REGION}) ==="
gcloud run deploy "${SERVICE_NAME}" \
    --image "gcr.io/${PROJECT_ID}/${SERVICE_NAME}:latest" \
    --platform managed \
    --region "${REGION}" \
    --allow-unauthenticated \
    --port 8080 \
    --min-instances 1 \
    --max-instances 16 \
    --concurrency 500 \
    --timeout 3600 \
    --memory 2Gi \
    --cpu 2 \
    --execution-environment gen2 \
    --no-cpu-throttling \
    --session-affinity

echo "=== Deploy complete ==="
echo "Check logs with:"
echo "gcloud run services logs read ${SERVICE_NAME} --region ${REGION} --limit 100"
