package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strconv"
	"time"

	"image-api/internal/jobdb"

	_ "github.com/go-sql-driver/mysql"
)

func main() {
	dbDSN := os.Getenv("JOB_DB_DSN")
	if dbDSN == "" {
		log.Fatal("JOB_DB_DSN is required")
	}

	pollInterval := 1 * time.Second
	if raw := os.Getenv("JOB_POLL_INTERVAL"); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil {
			pollInterval = time.Duration(v * float64(time.Second))
		}
	}

	db, err := jobdb.Open(dbDSN)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if err := jobdb.Init(db); err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	for {
		job, ok, err := jobdb.ClaimJob(ctx, db)
		if err != nil {
			log.Printf("claim failed: %v", err)
			time.Sleep(pollInterval)
			continue
		}
		if !ok {
			time.Sleep(pollInterval)
			continue
		}

		result, err := processJob(job.Payload)
		if err != nil {
			if err := jobdb.FailJob(db, job.ID, err.Error()); err != nil {
				log.Printf("failed to mark job %s failed: %v", job.ID, err)
			}
			continue
		}

		if err := jobdb.CompleteJob(db, job.ID, result); err != nil {
			log.Printf("failed to mark job %s done: %v", job.ID, err)
		}
	}
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
