# Image processing as a service

## Design

### Code

The codebase is organized around small, reusable packages:
- `internal/netfetch` handles safe downloads with scheme/redirect/size guards.
- `internal/imageproc` focuses on decode/validate/crop/encode logic.
- `internal/uploader` defines a minimal `Uploader` interface, with implementations for GCS (`internal/gcs`) and local storage (`internal/localstore`).

To add a new storage backend, implement the `Uploader` interface (e.g., S3 or Azure Blob) and wire it into the worker with an env switch. The download/crop/encode steps stay the same.

### System
Three services: API (ingest/status), Publisher (outbox → Pub/Sub), Worker (processing). MySQL stores jobs and the outbox, so work survives crashes and retries.

Availability and scalability come from stateless services that scale independently on Cloud Run, with Pub/Sub decoupling ingestion from processing.

Health checks: `/healthz` for liveness and `/readyz` for DB/Pub/Sub readiness; Cloud Run probes use them.

### Security

Input image URLs are validated to allow only `http`/`https`, redirects are limited, and downloads are size-capped (Content-Length check + hard read limit). Images are further constrained by a maximum pixel count to avoid large memory usage.

## How to use

### Production api

```
https://image-api-128408048796.us-south1.run.app
```

Create a job:
```bash
POST /jobs/image-crop \
  -H 'content-type: application/json' \
  -d '{"imageUrl": "https://domain.com/image.jpg", "x": 100, "y": 50, "width": 200, "height": 200}'
```

Check job status:
```bash
GET /jobs/{uuid}
```

Response
```
{
  "id": "e3d48021-ef94-4850-9dc9-2210e4e9dcb3",
  "created_at": "2026-01-14T22:29:01Z",
  "status": "done",
  "croppedImageUrl": "https://storage.googleapis.com/jli-images/crops/e3d48021-ef94-4850-9dc9-2210e4e9dcb3.jpg",
  "error": null,
  "updated_at": "2026-01-14T22:29:02Z"
}
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
