package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"image-api/internal/api"
	"image-api/internal/jobdb"

	"cloud.google.com/go/pubsub"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"
	_ "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	middleware "github.com/oapi-codegen/chi-middleware"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	dbDSN := os.Getenv("JOB_DB_DSN")
	if dbDSN == "" {
		fatal("JOB_DB_DSN is required")
	}
	projectID := os.Getenv("GCP_PROJECT_ID")
	if projectID == "" {
		fatal("GCP_PROJECT_ID is required")
	}
	topicName := os.Getenv("PUBSUB_TOPIC")
	if topicName == "" {
		fatal("PUBSUB_TOPIC is required")
	}
	pubsubMode := os.Getenv("PUBSUB_MODE")
	if pubsubMode == "" {
		pubsubMode = "cloud"
	}

	db, err := jobdb.Open(dbDSN)
	if err != nil {
		fatal("failed to open job db", "err", err)
	}
	defer db.Close()

	pubsubClient, err := pubsub.NewClient(context.Background(), projectID)
	if err != nil {
		fatal("failed to create pubsub client", "err", err)
	}
	defer pubsubClient.Close()

	publisher := pubsubClient.Topic(topicName)
	defer publisher.Stop()

	if pubsubMode == "emulator" {
		if err := ensureTopicWithRetry(context.Background(), pubsubClient, topicName, 10, 500*time.Millisecond); err != nil {
			fatal("failed to ensure pubsub topic", "err", err)
		}
	}

	router := chi.NewRouter()
	router.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	router.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := checkAPIReady(r.Context(), db, publisher); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	specPath := os.Getenv("OPENAPI_SPEC_PATH")
	if specPath == "" {
		specPath = "openapi.yaml"
	}
	swagger, err := loadOpenAPISpec(specPath)
	if err != nil {
		fatal("failed to load openapi spec", "err", err)
	}
	if err := swagger.Validate(context.Background()); err != nil {
		fatal("invalid openapi spec", "err", err)
	}

	apiRouter := chi.NewRouter()
	apiRouter.Use(middleware.OapiRequestValidator(swagger))
	api.HandlerFromMux(&server{db: db, publisher: publisher}, apiRouter)
	router.Mount("/", apiRouter)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port
	slog.Info("api listening", "addr", addr)
	if err := http.ListenAndServe(addr, router); err != nil {
		fatal("api server failed", "err", err)
	}
}

type server struct {
	db        *sql.DB
	publisher *pubsub.Topic
}

func (s *server) PostJobsImageCrop(w http.ResponseWriter, r *http.Request) {
	// Validate and enqueue an image-crop job payload.
	var req api.ImageCropRequest
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.ImageUrl == "" {
		writeError(w, http.StatusBadRequest, "imageUrl is required")
		return
	}
	if req.Width <= 0 || req.Height <= 0 {
		writeError(w, http.StatusBadRequest, "width and height must be greater than 0")
		return
	}

	payload, err := json.Marshal(req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode payload")
		return
	}

	idemKey := r.Header.Get("Idempotency-Key")
	if idemKey != "" {
		// Reuse the existing job when the same key+payload is retried.
		hash := hashBody(body)
		job, outbox, reused, err := jobdb.InsertJobWithOutboxAndIdempotency(s.db, payload, idemKey, hash)
		if err != nil {
			if errors.Is(err, jobdb.ErrIdempotencyKeyConflict) {
				writeError(w, http.StatusConflict, "idempotency key reused with different payload")
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to create job")
			return
		}
		if !reused {
			// Only publish to Pub/Sub for newly created jobs.
			if err := s.publishJob(r.Context(), outbox.ID, outbox.Payload); err != nil {
				slog.Error("publish failed for job", "job_id", job.ID, "err", err)
			}
		}
		status := http.StatusCreated
		if reused {
			// Same idempotency key and payload: return the existing job instead of creating a new one.
			status = http.StatusOK
		}
		writeJSON(w, api.JobResponse{
			Id:              mustParseUUID(job.ID),
			Status:          job.Status,
			CroppedImageUrl: nil,
			Error:           nil,
			CreatedAt:       job.CreatedAt,
			UpdatedAt:       job.UpdatedAt,
		}, status)
		return
	}

	job, outbox, err := jobdb.InsertJobWithOutbox(s.db, payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create job")
		return
	}

	if err := s.publishJob(r.Context(), outbox.ID, outbox.Payload); err != nil {
		slog.Error("publish failed for job", "job_id", job.ID, "err", err)
	}

	writeJSON(w, api.JobResponse{
		Id:              mustParseUUID(job.ID),
		Status:          job.Status,
		CroppedImageUrl: nil,
		Error:           nil,
		CreatedAt:       job.CreatedAt,
		UpdatedAt:       job.UpdatedAt,
	}, http.StatusCreated)
}

func loadOpenAPISpec(path string) (*openapi3.T, error) {
	loader := openapi3.NewLoader()
	return loader.LoadFromFile(path)
}

func (s *server) GetJobsId(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	// Return job status and any resulting cropped image URL or error.
	job, ok, err := jobdb.GetJob(s.db, id.String())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fetch job")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	writeJSON(w, api.JobResponse{
		Id:              mustParseUUID(job.ID),
		Status:          job.Status,
		CroppedImageUrl: extractCroppedImageURL(job.Result),
		Error:           extractError(job.Error),
		CreatedAt:       job.CreatedAt,
		UpdatedAt:       job.UpdatedAt,
	}, http.StatusOK)
}

func writeJSON(w http.ResponseWriter, v any, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, api.ErrorResponse{Message: message}, status)
}

func extractCroppedImageURL(result json.RawMessage) *string {
	// Pull croppedImageUrl from the stored job result JSON.
	if len(result) == 0 {
		return nil
	}

	var payload map[string]any
	if err := json.Unmarshal(result, &payload); err != nil {
		return nil
	}

	raw, ok := payload["croppedImageUrl"]
	if !ok {
		return nil
	}
	url, ok := raw.(string)
	if !ok || url == "" {
		return nil
	}

	return &url
}

func extractError(errText sql.NullString) *string {
	// Return a non-empty error string if present.
	if !errText.Valid || errText.String == "" {
		return nil
	}
	return &errText.String
}

func mustParseUUID(id string) uuid.UUID {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return uuid.Nil
	}
	return parsed
}

func hashBody(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func checkAPIReady(ctx context.Context, db *sql.DB, topic *pubsub.Topic) error {
	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := db.PingContext(checkCtx); err != nil {
		return err
	}
	_, err := topic.Exists(checkCtx)
	return err
}

func fatal(msg string, attrs ...any) {
	slog.Error(msg, attrs...)
	os.Exit(1)
}

func (s *server) publishJob(ctx context.Context, outboxID string, payload json.RawMessage) error {
	// Publish the outbox payload to Pub/Sub and mark it published on success.
	publishCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	result := s.publisher.Publish(publishCtx, &pubsub.Message{Data: payload})
	if _, err := result.Get(publishCtx); err != nil {
		_ = jobdb.RecordOutboxError(s.db, outboxID, err.Error())
		return err
	}
	return jobdb.MarkOutboxPublished(s.db, outboxID)
}

func ensureTopic(ctx context.Context, client *pubsub.Client, topicName string) error {
	topic := client.Topic(topicName)
	exists, err := topic.Exists(ctx)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = client.CreateTopic(ctx, topicName)
	if status.Code(err) == codes.AlreadyExists {
		return nil
	}
	return err
}

func ensureTopicWithRetry(ctx context.Context, client *pubsub.Client, topicName string, attempts int, delay time.Duration) error {
	var lastErr error
	for i := 0; i < attempts; i++ {
		if err := ensureTopic(ctx, client, topicName); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(delay)
	}
	return lastErr
}
