UPDATE platform_test_samples
SET enabled = 1,
    updated_at = NOW()
WHERE enabled = 0
  AND TRIM(sample_url) <> ''
  AND (
    note LIKE 'videodl 每日状态页样本%'
    OR note LIKE 'musicdl 播放列表样本%'
  );
