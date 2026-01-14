package jobdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

type OutboxMessage struct {
	ID      string
	JobID   string
	Payload json.RawMessage
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

func InsertJobWithOutbox(db *sql.DB, payload json.RawMessage) (Job, OutboxMessage, error) {
	createdAt := NowISO()
	jobID := uuid.NewString()
	outboxID := uuid.NewString()
	outboxPayload, err := json.Marshal(map[string]string{"jobId": jobID})
	if err != nil {
		return Job{}, OutboxMessage{}, err
	}

	tx, err := db.Begin()
	if err != nil {
		return Job{}, OutboxMessage{}, err
	}

	if _, err := tx.Exec(
		`INSERT INTO jobs (id, status, payload, result, error, created_at, updated_at)
		 VALUES (?, ?, ?, NULL, NULL, ?, ?)`,
		jobID, "pending", string(payload), createdAt, createdAt,
	); err != nil {
		_ = tx.Rollback()
		return Job{}, OutboxMessage{}, err
	}

	if _, err := tx.Exec(
		`INSERT INTO outbox (id, job_id, payload, published_at, attempts, last_error, created_at, updated_at)
		 VALUES (?, ?, ?, NULL, 0, NULL, ?, ?)`,
		outboxID, jobID, string(outboxPayload), createdAt, createdAt,
	); err != nil {
		_ = tx.Rollback()
		return Job{}, OutboxMessage{}, err
	}

	if err := tx.Commit(); err != nil {
		return Job{}, OutboxMessage{}, err
	}

	job := Job{
		ID:        jobID,
		Status:    "pending",
		Payload:   payload,
		Result:    nil,
		Error:     sql.NullString{},
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}

	return job, OutboxMessage{ID: outboxID, JobID: jobID, Payload: outboxPayload}, nil
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

func ClaimOutboxBatch(ctx context.Context, db *sql.DB, limit int) ([]OutboxMessage, error) {
	// Selecting unpublished rows while holding locks so other publishers skip them.
	// Attempts are incremented inside the same transaction to record delivery tries.
	// Unpublished rows are identified by published_at IS NULL.
	if limit <= 0 {
		return nil, nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}

	rows, err := tx.QueryContext(
		ctx,
		`SELECT id, job_id, payload FROM outbox
		 WHERE published_at IS NULL
		 ORDER BY created_at
		 LIMIT ?
		 FOR UPDATE SKIP LOCKED`,
		limit,
	)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	defer rows.Close()

	var messages []OutboxMessage
	for rows.Next() {
		var msg OutboxMessage
		var payload string
		if err := rows.Scan(&msg.ID, &msg.JobID, &payload); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		msg.Payload = json.RawMessage(payload)
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		_ = tx.Rollback()
		return nil, err
	}

	for _, msg := range messages {
		if _, err := tx.Exec(
			`UPDATE outbox SET attempts = attempts + 1, updated_at = ? WHERE id = ?`,
			NowISO(), msg.ID,
		); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return messages, nil
}

func MarkOutboxPublished(db *sql.DB, outboxID string) error {
	_, err := db.Exec(
		`UPDATE outbox SET published_at = ?, last_error = NULL, updated_at = ? WHERE id = ?`,
		NowISO(), NowISO(), outboxID,
	)
	return err
}

func RecordOutboxError(db *sql.DB, outboxID string, errMsg string) error {
	_, err := db.Exec(
		`UPDATE outbox SET last_error = ?, updated_at = ? WHERE id = ?`,
		errMsg, NowISO(), outboxID,
	)
	return err
}

func StartJob(db *sql.DB, jobID string) (bool, error) {
	// Start a pending job by transitioning it to in_progress if it is still pending.
	tx, err := db.Begin()
	if err != nil {
		return false, err
	}

	result, err := tx.Exec(
		`UPDATE jobs SET status = 'in_progress', updated_at = ? WHERE id = ? AND status = 'pending'`,
		NowISO(), jobID,
	)
	if err != nil {
		_ = tx.Rollback()
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		_ = tx.Rollback()
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, err
	}

	return affected == 1, nil
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
