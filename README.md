# image-api
Provide image processing service

## Overview
This repo contains:
- HTTP service that accepts jobs and stores them in a MySQL job database.
- Worker process that consumes Pub/Sub push messages and executes jobs.

## Setup
```bash
go mod download
```

## Database
This service expects a MySQL database. The worker uses `SELECT ... FOR UPDATE SKIP LOCKED`,
which requires MySQL 8.0+.

## Run the API
```bash
export JOB_DB_DSN='user:pass@tcp(127.0.0.1:3306)/image_api?parseTime=true'
export GCP_PROJECT_ID='your-project-id'
export PUBSUB_TOPIC='image-jobs'
export PUBSUB_EMULATOR_HOST='127.0.0.1:8085'
export PORT=8080
go run ./cmd/api
```

Create a job:
```bash
curl -X POST http://127.0.0.1:8000/jobs/image-crop \
  -H 'content-type: application/json' \
  -d '{"imageUrl": "https://domain.com/image.jpg", "x": 100, "y": 50, "width": 200, "height": 200}'
```

Check job status:
```bash
curl http://127.0.0.1:8000/jobs/{uuid}
```

## Run the worker
```bash
export JOB_DB_DSN='user:pass@tcp(127.0.0.1:3306)/image_api?parseTime=true'
go run ./cmd/worker
```

## Run with Docker Compose
```bash
docker compose up --build
```
This uses the Pub/Sub emulator; the publisher auto-creates the local topic and push subscription.

Create a job:
```bash
curl -X POST http://127.0.0.1:8000/jobs/image-crop \
  -H 'content-type: application/json' \
  -d '{"imageUrl": "https://domain.com/image.jpg", "x": 100, "y": 50, "width": 200, "height": 200}'
```

## Configuration
- `JOB_DB_DSN`: MySQL DSN (required)
- `GCP_PROJECT_ID`: GCP project ID for Pub/Sub (required for API)
- `PUBSUB_TOPIC`: Pub/Sub topic name for job messages (required for API)
- `PUBSUB_MODE`: set to `emulator` for local Pub/Sub emulator, `cloud` for production
- `PUBSUB_EMULATOR_HOST`: set for local Pub/Sub emulator usage
- `OUTBOX_POLL_INTERVAL`: outbox publisher poll interval in seconds (default `2`)
- `OUTBOX_BATCH_SIZE`: outbox publisher batch size (default `10`)

## OpenAPI
- Spec: `openapi.yaml`
- Regenerate server types (requires `oapi-codegen` in PATH): `go generate ./internal/api`

## Cloud Run deployment
GitHub Actions can deploy two services (API + worker) on every push to `main`.
Set these secrets in your repo:
- `GCP_WORKLOAD_IDENTITY_PROVIDER`
- `GCP_SERVICE_ACCOUNT`
- `GCP_PROJECT_ID`
- `GCP_REGION`
- `GCP_AR_REPO`
- `CLOUDSQL_INSTANCE`
- `JOB_DB_DSN`
- `PUBSUB_PUSH_SERVICE_ACCOUNT`
The workflow creates a Pub/Sub topic `image-jobs` and push subscription `image-jobs-push`.
Ensure the Cloud Run service account running `image-api` has `roles/pubsub.publisher` on the topic.
The Cloud Run service account running `image-publisher` also needs `roles/pubsub.publisher`.

## Migrations
Run migrations locally and in production before starting services:
```bash
JOB_DB_DSN='user:pass@unix(/cloudsql/PROJECT_ID:REGION:INSTANCE)/image_api?parseTime=true' \\
  go run ./cmd/migrate
```
Migration files live in `migrations/`.
