ALTER TABLE parse_tasks
  ADD COLUMN locked_by VARCHAR(128) NOT NULL DEFAULT '' AFTER retry_count,
  ADD COLUMN locked_until DATETIME NULL AFTER locked_by,
  ADD COLUMN next_attempt_at DATETIME NULL AFTER locked_until,
  ADD KEY idx_status_next_attempt (status, next_attempt_at),
  ADD KEY idx_locked_until (locked_until);
