package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os"

	"image-api/internal/api"
	"image-api/internal/jobdb"

	"github.com/go-chi/chi/v5"
	_ "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

func main() {
	dbDSN := os.Getenv("JOB_DB_DSN")
	if dbDSN == "" {
		log.Fatal("JOB_DB_DSN is required")
	}

	db, err := jobdb.Open(dbDSN)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if err := jobdb.Init(db); err != nil {
		log.Fatal(err)
	}

	router := chi.NewRouter()
	api.HandlerFromMux(&server{db: db}, router)

	addr := ":8000"
	log.Printf("api listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, router))
}

type server struct {
	db *sql.DB
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

	job, err := jobdb.InsertJob(s.db, payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create job")
		return
	}

	writeJSON(w, api.JobResponse{
		Id:              mustParseUUID(job.ID),
		Status:          job.Status,
		CroppedImageUrl: nil,
		Error:           nil,
		CreatedAt:       job.CreatedAt,
		UpdatedAt:       job.UpdatedAt,
	}, http.StatusOK)
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
