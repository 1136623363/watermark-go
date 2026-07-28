ALTER TABLE parse_results
  MODIFY COLUMN share_id VARCHAR(64) NOT NULL;

ALTER TABLE download_fallback_events
  MODIFY COLUMN share_id VARCHAR(64) NOT NULL DEFAULT '';

ALTER TABLE parse_tasks
  ADD COLUMN max_attempts INT UNSIGNED NOT NULL DEFAULT 2 AFTER retry_count;

ALTER TABLE parse_tasks
  ADD COLUMN request_id VARCHAR(128) NOT NULL DEFAULT '' AFTER next_attempt_at,
  ADD COLUMN client_id VARCHAR(128) NOT NULL DEFAULT '' AFTER request_id,
  ADD KEY idx_task_request (task_type, request_id, client_id);

ALTER TABLE admin_users
  ADD COLUMN role VARCHAR(32) NOT NULL DEFAULT 'owner' AFTER username;

CREATE TABLE IF NOT EXISTS admin_audit_logs (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  username VARCHAR(64) NOT NULL,
  action VARCHAR(128) NOT NULL,
  resource VARCHAR(128) NOT NULL DEFAULT '',
  resource_id VARCHAR(128) NOT NULL DEFAULT '',
  details_json JSON NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_username_created (username, created_at),
  KEY idx_action_created (action, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
