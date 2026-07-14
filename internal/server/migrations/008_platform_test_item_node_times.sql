ALTER TABLE platform_test_items
  ADD COLUMN started_at DATETIME NULL AFTER duration_ms,
  ADD COLUMN responded_at DATETIME NULL AFTER started_at,
  ADD COLUMN node_id VARCHAR(128) NOT NULL DEFAULT '' AFTER responded_at,
  ADD COLUMN node_name VARCHAR(128) NOT NULL DEFAULT '' AFTER node_id,
  ADD COLUMN node_role VARCHAR(32) NOT NULL DEFAULT '' AFTER node_name;

UPDATE platform_test_items
SET responded_at = COALESCE(responded_at, updated_at, created_at),
    node_role = COALESCE(NULLIF(node_role, ''), 'unknown')
WHERE status = 'completed'
  AND responded_at IS NULL;
