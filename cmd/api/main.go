package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"image-api/internal/api"
	"image-api/internal/jobdb"

	"cloud.google.com/go/pubsub"
	middleware "github.com/deepmap/oapi-codegen/pkg/chi-middleware"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"
	_ "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func main() {
	dbDSN := os.Getenv("JOB_DB_DSN")
	if dbDSN == "" {
		log.Fatal("JOB_DB_DSN is required")
	}
	projectID := os.Getenv("GCP_PROJECT_ID")
	if projectID == "" {
		log.Fatal("GCP_PROJECT_ID is required")
	}
	topicName := os.Getenv("PUBSUB_TOPIC")
	if topicName == "" {
		log.Fatal("PUBSUB_TOPIC is required")
	}
	pubsubMode := os.Getenv("PUBSUB_MODE")
	if pubsubMode == "" {
		pubsubMode = "cloud"
	}

	db, err := jobdb.Open(dbDSN)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	pubsubClient, err := pubsub.NewClient(context.Background(), projectID)
	if err != nil {
		log.Fatal(err)
	}
	defer pubsubClient.Close()

	publisher := pubsubClient.Topic(topicName)
	defer publisher.Stop()

	if pubsubMode == "emulator" {
		if err := ensureTopic(context.Background(), pubsubClient, topicName); err != nil {
			log.Fatal(err)
		}
	}

	router := chi.NewRouter()
	specPath := os.Getenv("OPENAPI_SPEC_PATH")
	if specPath == "" {
		specPath = "openapi.yaml"
	}
	swagger, err := loadOpenAPISpec(specPath)
	if err != nil {
		log.Fatal(err)
	}
	if err := swagger.Validate(context.Background()); err != nil {
		log.Fatal(err)
	}
	router.Use(middleware.OapiRequestValidator(swagger))
	api.HandlerFromMux(&server{db: db, publisher: publisher}, router)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port
	log.Printf("api listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, router))
}

type server struct {
	db        *sql.DB
	publisher *pubsub.Topic
}

func (s *server) PostJobsImageCrop(w http.ResponseWriter, r *http.Request) {
	// Validate and enqueue an image-crop job payload.
	var req api.ImageCropRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.ImageUrl == "" {
		writeError(w, http.StatusBadRequest, "imageUrl is required")
		return
	}
	if req.Width <= 0 || req.Height <= 0 {
		writeError(w, http.StatusBadRequest, "width and height must be > 0")
		return
	}

	payload, err := json.Marshal(req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode payload")
		return
	}

	job, outbox, err := jobdb.InsertJobWithOutbox(s.db, payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create job")
		return
	}

	if err := s.publishJob(r.Context(), outbox.ID, outbox.Payload); err != nil {
		log.Printf("publish failed for job %s: %v", job.ID, err)
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
