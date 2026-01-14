CREATE TABLE IF NOT EXISTS outbox (
  id CHAR(36) PRIMARY KEY,
  job_id CHAR(36) NOT NULL,
  payload JSON NOT NULL,
  published_at VARCHAR(32),
  attempts INT NOT NULL DEFAULT 0,
  last_error TEXT,
  created_at VARCHAR(32) NOT NULL,
  updated_at VARCHAR(32) NOT NULL
);
