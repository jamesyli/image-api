# image-api
Provide image processing service

## Overview
This repo contains:
- HTTP service that accepts jobs and stores them in a SQLite job database.
- Worker process that claims pending jobs and executes them.

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

Create a job:
```bash
curl -X POST http://127.0.0.1:8000/jobs/image-crop \
  -H 'content-type: application/json' \
  -d '{"imageUrl": "https://domain.com/image.jpg", "x": 100, "y": 50, "width": 200, "height": 200}'
```

## Configuration
- `JOB_DB_DSN`: MySQL DSN (required)
- `JOB_POLL_INTERVAL`: worker polling interval in seconds (default `1.0`)

## OpenAPI
- Spec: `openapi.yaml`
- Regenerate server types (requires `oapi-codegen` in PATH): `go generate ./internal/api`
