ALTER TABLE platform_test_items
  ADD COLUMN started_at DATETIME NULL AFTER duration_ms,
  ADD COLUMN responded_at DATETIME NULL AFTER started_at,
  ADD COLUMN node_id VARCHAR(128) NOT NULL DEFAULT '' AFTER responded_at,
  ADD COLUMN node_name VARCHAR(128) NOT NULL DEFAULT '' AFTER node_id,
  ADD COLUMN node_role VARCHAR(32) NOT NULL DEFAULT '' AFTER node_name;
