UPDATE platform_test_samples
SET enabled = 1,
    updated_at = NOW()
WHERE enabled = 0
  AND TRIM(sample_url) <> '';
