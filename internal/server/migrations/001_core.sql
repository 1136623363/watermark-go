CREATE TABLE IF NOT EXISTS parse_results (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  share_id CHAR(24) NOT NULL,
  url_hash CHAR(64) NOT NULL,
  source_url TEXT NOT NULL,
  normalized_url VARCHAR(1024) NOT NULL,
  platform VARCHAR(64) NOT NULL,
  result_type VARCHAR(32) NOT NULL,
  title VARCHAR(512) NOT NULL DEFAULT '',
  cover_url TEXT NULL,
  author_name VARCHAR(255) NOT NULL DEFAULT '',
  result_json JSON NOT NULL,
  status TINYINT NOT NULL DEFAULT 1,
  hit_count BIGINT UNSIGNED NOT NULL DEFAULT 0,
  last_hit_at DATETIME NULL,
  expires_at DATETIME NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_share_id (share_id),
  UNIQUE KEY uk_url_hash (url_hash),
  KEY idx_platform_created (platform, created_at),
  KEY idx_updated_at (updated_at),
  KEY idx_expires_at (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS parse_attempts (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  url_hash CHAR(64) NOT NULL,
  platform VARCHAR(64) NOT NULL DEFAULT '',
  user_id BIGINT UNSIGNED NULL,
  client_ip VARBINARY(16) NULL,
  parser VARCHAR(64) NOT NULL DEFAULT '',
  success TINYINT NOT NULL DEFAULT 0,
  error_code VARCHAR(64) NOT NULL DEFAULT '',
  error_message VARCHAR(1024) NOT NULL DEFAULT '',
  duration_ms INT UNSIGNED NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_url_hash_created (url_hash, created_at),
  KEY idx_platform_created (platform, created_at),
  KEY idx_success_created (success, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS parse_tasks (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  task_id CHAR(32) NOT NULL,
  task_type VARCHAR(64) NOT NULL,
  status VARCHAR(32) NOT NULL,
  payload_json JSON NOT NULL,
  result_json JSON NULL,
  error_message VARCHAR(1024) NOT NULL DEFAULT '',
  retry_count INT UNSIGNED NOT NULL DEFAULT 0,
  available_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  started_at DATETIME NULL,
  finished_at DATETIME NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_task_id (task_id),
  KEY idx_status_available (status, available_at),
  KEY idx_created_at (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS admin_users (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  username VARCHAR(64) NOT NULL,
  password_hash VARCHAR(255) NOT NULL,
  role VARCHAR(32) NOT NULL DEFAULT 'owner',
  status TINYINT NOT NULL DEFAULT 1,
  last_login_at DATETIME NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_username (username)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS admin_audit_logs (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  admin_id BIGINT UNSIGNED NULL,
  action VARCHAR(128) NOT NULL,
  target_type VARCHAR(64) NOT NULL DEFAULT '',
  target_id VARCHAR(128) NOT NULL DEFAULT '',
  request_json JSON NULL,
  client_ip VARBINARY(16) NULL,
  user_agent VARCHAR(512) NOT NULL DEFAULT '',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_admin_created (admin_id, created_at),
  KEY idx_action_created (action, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS runtime_settings (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  setting_key VARCHAR(128) NOT NULL,
  setting_json JSON NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_setting_key (setting_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
