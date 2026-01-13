package jobdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Job struct {
	ID        string
	Status    string
	Payload   json.RawMessage
	Result    json.RawMessage
	Error     sql.NullString
	CreatedAt string
	UpdatedAt string
}

func Open(dsn string) (*sql.DB, error) {
	// Open a MySQL connection pool for job storage.
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetMaxIdleConns(5)
	db.SetMaxOpenConns(10)
	return db, nil
}

func Init(db *sql.DB) error {
	// Initialize schema and ensure the error column exists for older tables.
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS jobs (
			id CHAR(36) PRIMARY KEY,
			status VARCHAR(32) NOT NULL,
			payload JSON NOT NULL,
			result JSON,
			error TEXT,
			created_at VARCHAR(32) NOT NULL,
			updated_at VARCHAR(32) NOT NULL
		);
	`)
	if err != nil {
		return err
	}
	_, _ = db.Exec(`ALTER TABLE jobs ADD COLUMN error TEXT`)
	var columnType string
	if err := db.QueryRow(`
		SELECT COLUMN_TYPE
		FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = 'jobs' AND column_name = 'id'`,
	).Scan(&columnType); err != nil {
		return err
	}
	columnType = strings.ToLower(columnType)
	if !strings.HasPrefix(columnType, "char(36)") && !strings.HasPrefix(columnType, "varchar(36)") {
		return fmt.Errorf("jobs.id must be CHAR(36) or VARCHAR(36) for UUIDs; migrate existing table")
	}
	return nil
}

func NowISO() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func InsertJob(db *sql.DB, payload json.RawMessage) (Job, error) {
	// Persist a new pending job with the raw payload.
	createdAt := NowISO()
	jobID := uuid.NewString()
	res, err := db.Exec(
		`INSERT INTO jobs (id, status, payload, result, error, created_at, updated_at)
		 VALUES (?, ?, ?, NULL, NULL, ?, ?)`,
		jobID, "pending", string(payload), createdAt, createdAt,
	)
	if err != nil {
		return Job{}, err
	}
	_ = res
	return Job{
		ID:        jobID,
		Status:    "pending",
		Payload:   payload,
		Result:    nil,
		Error:     sql.NullString{},
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}, nil
}

func GetJob(db *sql.DB, jobID string) (Job, bool, error) {
	// Fetch a job by ID; ok=false when not found.
	var payload string
	var result sql.NullString
	var errText sql.NullString
	var job Job

	row := db.QueryRow(
		`SELECT id, status, payload, result, error, created_at, updated_at
		 FROM jobs WHERE id = ?`, jobID,
	)
	if err := row.Scan(&job.ID, &job.Status, &payload, &result, &errText, &job.CreatedAt, &job.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Job{}, false, nil
		}
		return Job{}, false, err
	}

	job.Payload = json.RawMessage(payload)
	if result.Valid {
		job.Result = json.RawMessage(result.String)
	}
	job.Error = errText

	return job, true, nil
}

func ClaimJob(ctx context.Context, db *sql.DB) (Job, bool, error) {
	// Atomically select and mark a pending job as in_progress.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, false, err
	}

	var job Job
	var payload string
	row := tx.QueryRowContext(
		ctx,
		`SELECT id, payload FROM jobs
		 WHERE status = 'pending'
		 ORDER BY created_at
		 LIMIT 1
		 FOR UPDATE SKIP LOCKED`,
	)
	if err := row.Scan(&job.ID, &payload); err != nil {
		_ = tx.Rollback()
		if errors.Is(err, sql.ErrNoRows) {
			return Job{}, false, nil
		}
		return Job{}, false, err
	}

	job.Payload = json.RawMessage(payload)
	job.Status = "in_progress"
	job.UpdatedAt = NowISO()
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE jobs SET status = 'in_progress', updated_at = ? WHERE id = ?`,
		job.UpdatedAt, job.ID,
	); err != nil {
		_ = tx.Rollback()
		return Job{}, false, err
	}

	if err := tx.Commit(); err != nil {
		return Job{}, false, err
	}

	return job, true, nil
}

func CompleteJob(db *sql.DB, jobID string, result json.RawMessage) error {
	// Mark a job as done and store its result JSON.
	_, err := db.Exec(
		`UPDATE jobs SET status = 'done', result = ?, error = NULL, updated_at = ? WHERE id = ?`,
		string(result), NowISO(), jobID,
	)
	return err
}

func FailJob(db *sql.DB, jobID string, errMsg string) error {
	// Mark a job as failed and store the error string.
	_, err := db.Exec(
		`UPDATE jobs SET status = 'failed', error = ?, updated_at = ? WHERE id = ?`,
		errMsg, NowISO(), jobID,
	)
	return err
}
