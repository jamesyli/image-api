CREATE TABLE IF NOT EXISTS jobs (
  id CHAR(36) PRIMARY KEY,
  status VARCHAR(32) NOT NULL,
  payload JSON NOT NULL,
  result JSON,
  error TEXT,
  created_at VARCHAR(32) NOT NULL,
  updated_at VARCHAR(32) NOT NULL
);
