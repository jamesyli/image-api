CREATE TABLE IF NOT EXISTS idempotency_keys (
  idempotency_key VARCHAR(128) PRIMARY KEY,
  request_hash CHAR(64) NOT NULL,
  job_id CHAR(36) NOT NULL,
  created_at VARCHAR(32) NOT NULL
);
