package server

import (
	"reflect"
	"sort"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestExtractWechatDomainCandidates(t *testing.T) {
	data := parseData{
		Platform: "douyin",
		Type:     "video",
		Cover:    "https://p3-sign.douyinpic.com/tos-cn-i-0813/example.jpeg?x=1",
		Avatar:   "http://insecure.example.com/avatar.jpeg",
		Music:    "https://music.example.com/audio.m4a",
		Downloads: []downloadItem{
			{URL: "https://v26-web.douyinvod.com/abc/video.mp4?token=secret", Label: "video"},
			{URL: "https://v26-web.douyinvod.com/abc/video.mp4?token=secret", Label: "video"},
			{URL: "https://127.0.0.1/video.mp4", Label: "video"},
		},
		Images: []string{
			"https://sns-img.example.cn/a/b.png",
			"file:///tmp/a.png",
		},
		M3U8: "https://stream.example.net/live/index.m3u8",
	}

	items := extractWechatDomainCandidates(data)
	got := make([]string, 0, len(items))
	for _, item := range items {
		got = append(got, item.Origin+"|"+item.MediaType)
	}
	sort.Strings(got)

	want := []string{
		"https://music.example.com|audio",
		"https://p3-sign.douyinpic.com|cover",
		"https://sns-img.example.cn|image",
		"https://stream.example.net|m3u8",
		"https://v26-web.douyinvod.com|video",
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("domains = %#v, want %#v", got, want)
	}
}

func TestMergeCSV(t *testing.T) {
	if got := mergeCSV("video,image", "audio", "video", ""); got != "audio,image,video" {
		t.Fatalf("mergeCSV = %q, want audio,image,video", got)
	}
}

func TestUpsertWechatDownloadDomainsNeverPersistsSourceURLSecrets(t *testing.T) {
	t.Parallel()
	candidate := wechatDomainCandidate{
		Origin:      "https://cdn.example.com",
		Host:        "cdn.example.com",
		Scheme:      "https",
		MediaType:   "video",
		FieldPath:   "downloads.0.url",
		URL:         "https://cdn.example.com/media.mp4?upstream=opaque",
		ExamplePath: "/media.mp4",
	}
	for _, test := range []struct {
		name       string
		sourceURL  string
		safeSource string
	}{
		{
			name:       "query and fragment",
			sourceURL:  "https://www.xiaohongshu.com/explore/synthetic?xsec_token=query-secret&session=session-secret#fragment-secret",
			safeSource: "https://www.xiaohongshu.com/explore/synthetic",
		},
		{
			name:       "userinfo",
			sourceURL:  "https://credential-user:credential-password@www.xiaohongshu.com/explore/synthetic",
			safeSource: "",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()

			mock.ExpectBegin()
			mock.ExpectQuery(`(?s)SELECT id, media_types.*FROM wechat_download_domains`).
				WithArgs(candidate.Origin).
				WillReturnRows(sqlmock.NewRows([]string{"id", "media_types"}))
			mock.ExpectExec(`(?s)INSERT INTO wechat_download_domains.*VALUES`).
				WithArgs(
					candidate.Origin,
					candidate.Host,
					candidate.Scheme,
					"redbook",
					"video",
					test.safeSource,
					candidate.ExamplePath,
				).
				WillReturnResult(sqlmock.NewResult(42, 1))
			mock.ExpectExec(`(?s)INSERT INTO wechat_download_domain_observations.*VALUES`).
				WithArgs(
					int64(42),
					candidate.Origin,
					candidate.Host,
					"redbook",
					test.safeSource,
					"share-id",
					candidate.MediaType,
					candidate.FieldPath,
					sqlmock.AnyArg(),
					candidate.ExamplePath,
				).
				WillReturnResult(sqlmock.NewResult(1, 1))
			mock.ExpectCommit()

			err = upsertWechatDownloadDomains(t.Context(), db, test.sourceURL, parseData{
				Platform: "redbook",
				ShareID:  "share-id",
			}, []wechatDomainCandidate{candidate})
			if err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("database arguments retained source URL query, fragment, or credential material: %v", err)
			}
		})
	}
}
