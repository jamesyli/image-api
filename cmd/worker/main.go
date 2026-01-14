package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"image-api/internal/jobdb"

	_ "github.com/go-sql-driver/mysql"
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

	mux := http.NewServeMux()
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
		log.Printf("received job message: %s", jobID)

		claimed, err := jobdb.StartJob(db, jobID)
		if err != nil {
			http.Error(w, "failed to start job", http.StatusInternalServerError)
			return
		}
		if !claimed {
			log.Printf("job already claimed: %s", jobID)
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

		result, err := processJob(job.Payload)
		if err != nil {
			if err := jobdb.FailJob(db, job.ID, err.Error()); err != nil {
				log.Printf("failed to mark job %s failed: %v", job.ID, err)
			}
			http.Error(w, "job failed", http.StatusInternalServerError)
			return
		}

		if err := jobdb.CompleteJob(db, job.ID, result); err != nil {
			log.Printf("failed to mark job %s done: %v", job.ID, err)
			http.Error(w, "job completion failed", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("worker listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}

func processJob(payload json.RawMessage) (json.RawMessage, error) {
	var data map[string]any
	if err := json.Unmarshal(payload, &data); err != nil {
		return nil, err
	}

	if raw, ok := data["delay_seconds"]; ok {
		switch v := raw.(type) {
		case float64:
			if v > 0 {
				time.Sleep(time.Duration(minFloat64(v, 30)) * time.Second)
			}
		case int:
			if v > 0 {
				time.Sleep(time.Duration(minFloat64(float64(v), 30)) * time.Second)
			}
		}
	}

	result, err := json.Marshal(map[string]any{
		"ok":   true,
		"echo": data,
	})
	if err != nil {
		return nil, err
	}

	return result, nil
}

func minFloat64(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
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
