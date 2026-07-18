ALTER TABLE parse_results
  ADD KEY idx_status_updated (status, updated_at);
