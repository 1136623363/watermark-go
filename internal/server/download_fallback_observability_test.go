package server

import "testing"

func TestBuildDownloadFallbackProxyTaskIDIsStable(t *testing.T) {
	req := downloadFallbackRequest{
		SourceURL: "https://v.douyin.com/demo/",
		MediaURL:  "https://example.com/video.mp4",
		MediaType: "video",
		ShareID:   "share-1",
		PublicID:  "30000031",
	}
	first := buildDownloadFallbackProxyTaskID(req)
	second := buildDownloadFallbackProxyTaskID(req)
	if first == "" || first != second {
		t.Fatalf("expected stable proxy task id, got %q and %q", first, second)
	}

	req.ShareID = "share-2"
	third := buildDownloadFallbackProxyTaskID(req)
	if third == first {
		t.Fatalf("expected a different proxy task id for a different share id: %q", third)
	}
}

func TestCompactDownloadFallbackRecentEventsMergesSameTask(t *testing.T) {
	items := []downloadFallbackEventRecord{
		{
			RequestID:        "finished-request",
			Mode:             "proxy",
			Status:           downloadFallbackEventStatusFailed,
			TaskID:           "proxy_same",
			SourceURL:        "https://v.douyin.com/demo/",
			BytesTransferred: 1024,
			ErrorMessage:     "unexpected EOF",
		},
		{
			RequestID: "issued-request",
			Mode:      "proxy",
			Status:    downloadFallbackEventStatusIssued,
			TaskID:    "proxy_same",
			SourceURL: "https://v.douyin.com/demo/",
		},
	}

	merged := compactDownloadFallbackRecentEvents(items, 50)
	if len(merged) != 1 {
		t.Fatalf("expected one compacted event, got %d: %#v", len(merged), merged)
	}
	if merged[0].Status != downloadFallbackEventStatusFailed {
		t.Fatalf("expected latest terminal status to remain, got %q", merged[0].Status)
	}
	if merged[0].BytesTransferred != 1024 {
		t.Fatalf("expected transfer bytes to be preserved, got %d", merged[0].BytesTransferred)
	}
}

func TestCompactDownloadFallbackRecentEventsMergesLegacyProxyEventsWithoutTaskID(t *testing.T) {
	items := []downloadFallbackEventRecord{
		{
			RequestID: "legacy-failed",
			Mode:      "proxy",
			Status:    downloadFallbackEventStatusFailed,
			SourceURL: "https://v.douyin.com/demo/",
			MediaURL:  "https://example.com/video.mp4",
			ClientIP:  "203.0.113.20",
		},
		{
			RequestID: "legacy-issued",
			Mode:      "proxy",
			Status:    downloadFallbackEventStatusIssued,
			SourceURL: "https://v.douyin.com/demo/",
			MediaURL:  "https://example.com/video.mp4",
			ClientIP:  "203.0.113.20",
		},
	}

	merged := compactDownloadFallbackRecentEvents(items, 50)
	if len(merged) != 1 {
		t.Fatalf("expected one compacted legacy event, got %d: %#v", len(merged), merged)
	}
}
