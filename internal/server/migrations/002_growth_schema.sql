CREATE TABLE IF NOT EXISTS app_users (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  public_id CHAR(26) NOT NULL,
  nickname VARCHAR(64) NOT NULL DEFAULT '',
  avatar_url TEXT NULL,
  phone_hash CHAR(64) NOT NULL DEFAULT '',
  status TINYINT NOT NULL DEFAULT 1,
  registered_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_seen_at DATETIME NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_public_id (public_id),
  KEY idx_status_seen (status, last_seen_at),
  KEY idx_phone_hash (phone_hash)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS app_user_identities (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  user_id BIGINT UNSIGNED NOT NULL,
  identity_type VARCHAR(32) NOT NULL,
  identity_key VARCHAR(191) NOT NULL,
  union_key VARCHAR(191) NOT NULL DEFAULT '',
  metadata_json JSON NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_identity (identity_type, identity_key),
  KEY idx_user_type (user_id, identity_type),
  KEY idx_union_key (union_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS platforms (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  platform_key VARCHAR(64) NOT NULL,
  display_name VARCHAR(64) NOT NULL,
  parser_type VARCHAR(32) NOT NULL DEFAULT 'native',
  status VARCHAR(32) NOT NULL DEFAULT 'enabled',
  parse_cost INT UNSIGNED NOT NULL DEFAULT 1,
  sort_order INT NOT NULL DEFAULT 0,
  config_json JSON NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_platform_key (platform_key),
  KEY idx_status_sort (status, sort_order)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS platform_parser_configs (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  platform_key VARCHAR(64) NOT NULL,
  config_key VARCHAR(64) NOT NULL,
  config_json JSON NOT NULL,
  enabled TINYINT NOT NULL DEFAULT 1,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_platform_config (platform_key, config_key),
  KEY idx_platform_enabled (platform_key, enabled)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS user_quota_accounts (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  user_id BIGINT UNSIGNED NOT NULL,
  quota_type VARCHAR(32) NOT NULL DEFAULT 'parse',
  balance INT NOT NULL DEFAULT 0,
  daily_free_limit INT UNSIGNED NOT NULL DEFAULT 0,
  daily_free_used INT UNSIGNED NOT NULL DEFAULT 0,
  daily_reset_date DATE NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_user_quota (user_id, quota_type),
  KEY idx_balance (quota_type, balance)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS parse_quota_ledger (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  user_id BIGINT UNSIGNED NOT NULL,
  quota_type VARCHAR(32) NOT NULL DEFAULT 'parse',
  change_amount INT NOT NULL,
  balance_after INT NOT NULL,
  source_type VARCHAR(32) NOT NULL,
  source_id VARCHAR(128) NOT NULL DEFAULT '',
  idempotency_key VARCHAR(191) NOT NULL,
  remark VARCHAR(255) NOT NULL DEFAULT '',
  expires_at DATETIME NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_idempotency (idempotency_key),
  KEY idx_user_created (user_id, created_at),
  KEY idx_source (source_type, source_id),
  KEY idx_expires_at (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS ad_units (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  unit_key VARCHAR(64) NOT NULL,
  provider VARCHAR(32) NOT NULL,
  placement_id VARCHAR(128) NOT NULL,
  reward_quota INT UNSIGNED NOT NULL DEFAULT 1,
  daily_reward_limit INT UNSIGNED NOT NULL DEFAULT 0,
  cooldown_seconds INT UNSIGNED NOT NULL DEFAULT 0,
  enabled TINYINT NOT NULL DEFAULT 1,
  config_json JSON NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_unit_key (unit_key),
  KEY idx_provider_enabled (provider, enabled)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS ad_reward_events (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  user_id BIGINT UNSIGNED NOT NULL,
  ad_unit_key VARCHAR(64) NOT NULL,
  provider VARCHAR(32) NOT NULL,
  provider_event_id VARCHAR(191) NOT NULL,
  reward_quota INT UNSIGNED NOT NULL,
  reward_status VARCHAR(32) NOT NULL DEFAULT 'confirmed',
  raw_json JSON NULL,
  rewarded_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_provider_event (provider, provider_event_id),
  KEY idx_user_rewarded (user_id, rewarded_at),
  KEY idx_unit_rewarded (ad_unit_key, rewarded_at),
  KEY idx_status_created (reward_status, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS user_parse_records (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  request_id CHAR(32) NOT NULL,
  user_id BIGINT UNSIGNED NULL,
  parse_result_id BIGINT UNSIGNED NULL,
  share_id CHAR(24) NOT NULL DEFAULT '',
  url_hash CHAR(64) NOT NULL,
  platform VARCHAR(64) NOT NULL DEFAULT '',
  result_type VARCHAR(32) NOT NULL DEFAULT '',
  quota_cost INT UNSIGNED NOT NULL DEFAULT 0,
  cache_hit TINYINT NOT NULL DEFAULT 0,
  status VARCHAR(32) NOT NULL DEFAULT 'success',
  error_code VARCHAR(64) NOT NULL DEFAULT '',
  error_message VARCHAR(1024) NOT NULL DEFAULT '',
  duration_ms INT UNSIGNED NOT NULL DEFAULT 0,
  client_ip VARBINARY(16) NULL,
  user_agent VARCHAR(512) NOT NULL DEFAULT '',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_request_id (request_id),
  KEY idx_user_created (user_id, created_at),
  KEY idx_url_hash_created (url_hash, created_at),
  KEY idx_platform_created (platform, created_at),
  KEY idx_share_id (share_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS platform_test_runs (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  run_id CHAR(32) NOT NULL,
  admin_id BIGINT UNSIGNED NULL,
  total_count INT UNSIGNED NOT NULL DEFAULT 0,
  success_count INT UNSIGNED NOT NULL DEFAULT 0,
  failed_count INT UNSIGNED NOT NULL DEFAULT 0,
  duration_ms INT UNSIGNED NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_run_id (run_id),
  KEY idx_created_at (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS platform_test_items (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  run_id CHAR(32) NOT NULL,
  platform_key VARCHAR(64) NOT NULL,
  sample_url TEXT NOT NULL,
  success TINYINT NOT NULL DEFAULT 0,
  error_message VARCHAR(1024) NOT NULL DEFAULT '',
  duration_ms INT UNSIGNED NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_run_id (run_id),
  KEY idx_platform_created (platform_key, created_at),
  KEY idx_success_created (success, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
