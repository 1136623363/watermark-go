ALTER TABLE parse_attempts
  ADD COLUMN raw_input TEXT NULL AFTER id,
  ADD COLUMN source_url TEXT NULL AFTER raw_input,
  ADD COLUMN normalized_url VARCHAR(1024) NOT NULL DEFAULT '' AFTER source_url,
  ADD COLUMN host VARCHAR(255) NOT NULL DEFAULT '' AFTER normalized_url,
  ADD COLUMN classification VARCHAR(32) NOT NULL DEFAULT '' AFTER host,
  ADD COLUMN entrypoint VARCHAR(64) NOT NULL DEFAULT '' AFTER parser,
  ADD COLUMN user_agent VARCHAR(512) NOT NULL DEFAULT '' AFTER client_ip,
  ADD KEY idx_host_created (host, created_at),
  ADD KEY idx_classification_created (classification, created_at),
  ADD KEY idx_entrypoint_created (entrypoint, created_at);
