CREATE TABLE IF NOT EXISTS wechat_download_domains (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  origin VARCHAR(255) NOT NULL,
  host VARCHAR(255) NOT NULL,
  scheme VARCHAR(16) NOT NULL DEFAULT 'https',
  platform VARCHAR(64) NOT NULL DEFAULT '',
  media_types VARCHAR(255) NOT NULL DEFAULT '',
  hit_count BIGINT UNSIGNED NOT NULL DEFAULT 1,
  status VARCHAR(32) NOT NULL DEFAULT 'pending',
  last_sample_url TEXT NULL,
  last_example_path VARCHAR(512) NOT NULL DEFAULT '',
  note VARCHAR(255) NOT NULL DEFAULT '',
  first_seen_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_seen_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_origin (origin),
  KEY idx_status_updated (status, updated_at),
  KEY idx_platform (platform),
  KEY idx_host (host)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS wechat_download_domain_observations (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  domain_id BIGINT UNSIGNED NULL,
  origin VARCHAR(255) NOT NULL,
  host VARCHAR(255) NOT NULL,
  platform VARCHAR(64) NOT NULL DEFAULT '',
  source_url TEXT NULL,
  result_share_id VARCHAR(64) NOT NULL DEFAULT '',
  media_type VARCHAR(32) NOT NULL DEFAULT '',
  field_path VARCHAR(128) NOT NULL DEFAULT '',
  url_hash CHAR(64) NOT NULL,
  example_path VARCHAR(512) NOT NULL DEFAULT '',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_url_field (url_hash, field_path),
  KEY idx_domain_created (origin, created_at),
  KEY idx_platform_created (platform, created_at),
  KEY idx_domain_id (domain_id),
  CONSTRAINT fk_wechat_download_domain_observations_domain
    FOREIGN KEY (domain_id) REFERENCES wechat_download_domains(id)
    ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
