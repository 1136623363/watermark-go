CREATE TABLE IF NOT EXISTS download_fallback_events (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  request_id CHAR(32) NOT NULL,
  mode VARCHAR(16) NOT NULL DEFAULT '',
  status VARCHAR(32) NOT NULL DEFAULT '',
  task_id VARCHAR(64) NOT NULL DEFAULT '',
  share_id CHAR(24) NOT NULL DEFAULT '',
  source_url TEXT NULL,
  media_url TEXT NULL,
  media_type VARCHAR(16) NOT NULL DEFAULT '',
  user_id BIGINT UNSIGNED NULL,
  client_ip VARBINARY(16) NULL,
  user_agent VARCHAR(512) NOT NULL DEFAULT '',
  bytes_transferred BIGINT UNSIGNED NOT NULL DEFAULT 0,
  duration_ms INT UNSIGNED NOT NULL DEFAULT 0,
  origin_status INT UNSIGNED NOT NULL DEFAULT 0,
  error_message VARCHAR(1024) NOT NULL DEFAULT '',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_request_id (request_id),
  KEY idx_mode_created (mode, created_at),
  KEY idx_status_created (status, created_at),
  KEY idx_user_created (user_id, created_at),
  KEY idx_task_id (task_id),
  KEY idx_updated_at (updated_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

ALTER TABLE parse_attempts
  ADD KEY idx_user_created (user_id, created_at);

ALTER TABLE parse_attempts
  ADD KEY idx_created_duration (created_at, duration_ms);
