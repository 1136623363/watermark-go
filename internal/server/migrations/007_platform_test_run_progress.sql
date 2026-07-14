ALTER TABLE platform_test_runs
  ADD COLUMN status VARCHAR(32) NOT NULL DEFAULT 'completed' AFTER run_id,
  ADD COLUMN message VARCHAR(512) NOT NULL DEFAULT '' AFTER status,
  ADD COLUMN started_at DATETIME NULL AFTER duration_ms,
  ADD COLUMN completed_at DATETIME NULL AFTER started_at,
  ADD COLUMN updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP AFTER completed_at;

ALTER TABLE platform_test_items
  ADD COLUMN display_name VARCHAR(128) NOT NULL DEFAULT '' AFTER platform_key,
  ADD COLUMN status VARCHAR(32) NOT NULL DEFAULT 'completed' AFTER sample_url,
  ADD COLUMN result_platform VARCHAR(64) NOT NULL DEFAULT '' AFTER success,
  ADD COLUMN result_type VARCHAR(32) NOT NULL DEFAULT '' AFTER result_platform,
  ADD COLUMN parser_engine VARCHAR(64) NOT NULL DEFAULT '' AFTER result_type,
  ADD COLUMN result_title VARCHAR(512) NOT NULL DEFAULT '' AFTER parser_engine,
  ADD COLUMN share_id VARCHAR(128) NOT NULL DEFAULT '' AFTER result_title,
  ADD COLUMN sort_order INT UNSIGNED NOT NULL DEFAULT 0 AFTER duration_ms,
  ADD COLUMN updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP AFTER created_at;

ALTER TABLE platform_test_items
  ADD KEY idx_run_sort (run_id, sort_order);

UPDATE platform_test_runs
SET started_at = COALESCE(started_at, created_at),
    completed_at = COALESCE(completed_at, created_at),
    updated_at = COALESCE(updated_at, created_at)
WHERE started_at IS NULL OR completed_at IS NULL;
