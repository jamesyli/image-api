package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"image-api/internal/api"
	"image-api/internal/gcs"
	"image-api/internal/imageproc"
	"image-api/internal/jobdb"
	"image-api/internal/localstore"
	"image-api/internal/netfetch"
	"image-api/internal/uploader"

	"cloud.google.com/go/storage"
	_ "github.com/go-sql-driver/mysql"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	dbDSN := os.Getenv("JOB_DB_DSN")
	if dbDSN == "" {
		fatal("JOB_DB_DSN is required")
	}

	db, err := jobdb.Open(dbDSN)
	if err != nil {
		fatal("failed to open job db", "err", err)
	}
	defer db.Close()

	backend := os.Getenv("UPLOAD_BACKEND")
	if backend == "" {
		backend = "gcs"
	}

	makePublic := envBool("GCS_PUBLIC", true)
	allowACLFailure := envBool("GCS_PUBLIC_SKIP_ACL_ERRORS", false)
	maxBytes := envInt64("IMAGE_MAX_BYTES", 10*1024*1024)
	maxPixels := envInt("IMAGE_MAX_PIXELS", 25_000_000)
	jpegQuality := envInt("IMAGE_JPEG_QUALITY", 90)

	var uploader uploader.Uploader
	var localDir string
	if backend == "local" {
		localDir = os.Getenv("LOCAL_STORAGE_DIR")
		if localDir == "" {
			localDir = "/tmp/image-api"
		}
		baseURL := os.Getenv("LOCAL_STORAGE_BASE_URL")
		if baseURL == "" {
			baseURL = "http://localhost:8001/files"
		}
		uploader = localstore.NewUploader(localDir, baseURL)
	} else {
		bucket := os.Getenv("GCS_BUCKET")
		if bucket == "" {
			fatal("GCS_BUCKET is required")
		}
		storageClient, err := storage.NewClient(context.Background())
		if err != nil {
			fatal("failed to create storage client", "err", err)
		}
		defer storageClient.Close()
		uploader = gcs.NewUploader(storageClient, bucket, makePublic, allowACLFailure)
	}

	processor := newJobProcessor(
		&http.Client{Timeout: 20 * time.Second},
		uploader,
		imageproc.Limits{MaxBytes: maxBytes, MaxPixels: maxPixels},
		jpegQuality,
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		checkCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.PingContext(checkCtx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/pubsub/jobs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var envelope pubSubEnvelope
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}

		jobID, err := decodeJobID(envelope)
		if err != nil {
			http.Error(w, "invalid message", http.StatusBadRequest)
			return
		}
		slog.Info("received job message", "job_id", jobID)

		claimed, err := jobdb.StartJob(db, jobID)
		if err != nil {
			http.Error(w, "failed to start job", http.StatusInternalServerError)
			return
		}
		if !claimed {
			slog.Info("job already claimed", "job_id", jobID)
			w.WriteHeader(http.StatusOK)
			return
		}

		job, ok, err := jobdb.GetJob(db, jobID)
		if err != nil {
			http.Error(w, "failed to fetch job", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "job not found", http.StatusNotFound)
			return
		}

		result, err := processor.Process(r.Context(), job.ID, job.Payload)
		if err != nil {
			if err := jobdb.FailJob(db, job.ID, err.Error()); err != nil {
				slog.Error("failed to mark job failed", "job_id", job.ID, "err", err)
			}
			http.Error(w, "job failed", http.StatusInternalServerError)
			return
		}

		if err := jobdb.CompleteJob(db, job.ID, result); err != nil {
			slog.Error("failed to mark job done", "job_id", job.ID, "err", err)
			http.Error(w, "job completion failed", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
	})

	if backend == "local" && localDir != "" && envBool("LOCAL_STORAGE_SERVE", true) {
		fileServer := http.FileServer(http.Dir(localDir))
		mux.Handle("/files/", http.StripPrefix("/files/", fileServer))
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	slog.Info("worker listening", "addr", ":"+port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		fatal("worker server failed", "err", err)
	}
}

type jobProcessor struct {
	httpClient  *http.Client
	uploader    uploader.Uploader
	limits      imageproc.Limits
	jpegQuality int
}

func newJobProcessor(client *http.Client, uploader uploader.Uploader, limits imageproc.Limits, quality int) *jobProcessor {
	return &jobProcessor{
		httpClient:  client,
		uploader:    uploader,
		limits:      limits,
		jpegQuality: quality,
	}
}

func (p *jobProcessor) Process(ctx context.Context, jobID string, payload json.RawMessage) (json.RawMessage, error) {
	var req api.ImageCropRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, err
	}
	if req.ImageUrl == "" {
		return nil, errors.New("imageUrl is required")
	}

	data, _, err := netfetch.Download(ctx, p.httpClient, req.ImageUrl, netfetch.Options{
		MaxBytes: p.limits.MaxBytes,
	})
	if err != nil {
		return nil, err
	}

	img, err := imageproc.DecodeImage(data)
	if err != nil {
		return nil, err
	}
	if err := imageproc.ValidateImage(img, p.limits.MaxPixels); err != nil {
		return nil, err
	}

	cropped, err := imageproc.CropImage(img, imageproc.Crop{
		X:      req.X,
		Y:      req.Y,
		Width:  req.Width,
		Height: req.Height,
	})
	if err != nil {
		return nil, err
	}

	jpegBytes, err := imageproc.EncodeJPEG(cropped, p.jpegQuality)
	if err != nil {
		return nil, err
	}

	objectName := fmt.Sprintf("crops/%s.jpg", jobID)
	publicURL, err := p.uploader.Upload(ctx, objectName, jpegBytes, "image/jpeg")
	if err != nil {
		return nil, err
	}

	return json.Marshal(map[string]any{
		"croppedImageUrl": publicURL,
	})
}

type pubSubEnvelope struct {
	Message struct {
		Data string `json:"data"`
	} `json:"message"`
}

func decodeJobID(envelope pubSubEnvelope) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(envelope.Message.Data)
	if err != nil {
		return "", err
	}
	var payload struct {
		JobID string `json:"jobId"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", err
	}
	if payload.JobID == "" {
		return "", errMissingJobID
	}
	return payload.JobID, nil
}

var errMissingJobID = errors.New("jobId is required")

func fatal(msg string, attrs ...any) {
	slog.Error(msg, attrs...)
	os.Exit(1)
}

func envInt64(key string, fallback int64) int64 {
	if raw := os.Getenv(key); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return v
		}
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if raw := os.Getenv(key); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil {
			return v
		}
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	if raw := os.Getenv(key); raw != "" {
		if v, err := strconv.ParseBool(raw); err == nil {
			return v
		}
	}
	return fallback
}
