package media

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestM3U8RejectsMaliciousManifests(t *testing.T) {
	cases := []struct {
		name     string
		manifest string
		options  M3U8Options
		want     error
	}{
		{
			name:     "too deep child manifest",
			manifest: "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nchild.m3u8\n",
			options:  M3U8Options{CurrentDepth: 1, MaxDepth: 1, MaxSegments: 10, MaxManifestBytes: 1024},
			want:     ErrM3U8Depth,
		},
		{
			name:     "too many segments",
			manifest: "#EXTM3U\none.ts\ntwo.ts\nthree.ts\n",
			options:  M3U8Options{MaxDepth: 2, MaxSegments: 2, MaxManifestBytes: 1024},
			want:     ErrM3U8Limit,
		},
		{
			name:     "manifest bytes over limit",
			manifest: "#EXTM3U\n" + strings.Repeat("a", 64) + ".ts\n",
			options:  M3U8Options{MaxDepth: 2, MaxSegments: 10, MaxManifestBytes: 16},
			want:     ErrM3U8Limit,
		},
		{
			name:     "absolute path",
			manifest: "#EXTM3U\n/var/private.ts\n",
			options:  M3U8Options{MaxDepth: 2, MaxSegments: 10, MaxManifestBytes: 1024},
			want:     ErrM3U8UnsafeURI,
		},
		{
			name:     "path traversal",
			manifest: "#EXTM3U\n../private.ts\n",
			options:  M3U8Options{MaxDepth: 2, MaxSegments: 10, MaxManifestBytes: 1024},
			want:     ErrM3U8UnsafeURI,
		},
		{
			name:     "file scheme",
			manifest: "#EXTM3U\nfile:///etc/passwd\n",
			options:  M3U8Options{MaxDepth: 2, MaxSegments: 10, MaxManifestBytes: 1024},
			want:     ErrM3U8UnsafeURI,
		},
		{
			name:     "concat scheme",
			manifest: "#EXTM3U\nconcat:a.ts|b.ts\n",
			options:  M3U8Options{MaxDepth: 2, MaxSegments: 10, MaxManifestBytes: 1024},
			want:     ErrM3U8UnsafeURI,
		},
		{
			name:     "data scheme",
			manifest: "#EXTM3U\ndata:text/plain,AAAA\n",
			options:  M3U8Options{MaxDepth: 2, MaxSegments: 10, MaxManifestBytes: 1024},
			want:     ErrM3U8UnsafeURI,
		},
		{
			name:     "crypto scheme",
			manifest: "#EXTM3U\ncrypto:seg.ts\n",
			options:  M3U8Options{MaxDepth: 2, MaxSegments: 10, MaxManifestBytes: 1024},
			want:     ErrM3U8UnsafeURI,
		},
		{
			name:     "http scheme after rewrite",
			manifest: "#EXTM3U\nhttp://example.com/seg.ts\n",
			options:  M3U8Options{MaxDepth: 2, MaxSegments: 10, MaxManifestBytes: 1024},
			want:     ErrM3U8UnsafeURI,
		},
		{
			name:     "encrypted manifest",
			manifest: "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\"\nseg.ts\n",
			options:  M3U8Options{MaxDepth: 2, MaxSegments: 10, MaxManifestBytes: 1024},
			want:     ErrM3U8Encryption,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseM3U8Manifest(test.manifest, test.options)
			if !errors.Is(err, test.want) {
				t.Fatalf("ParseM3U8Manifest() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestM3U8RejectsCumulativeSegmentBytes(t *testing.T) {
	err := ValidateSegmentBudget([]SegmentInfo{
		{URI: "one.ts", Bytes: 70},
		{URI: "two.ts", Bytes: 40},
	}, 100)
	if !errors.Is(err, ErrM3U8Limit) {
		t.Fatalf("ValidateSegmentBudget() error = %v, want ErrM3U8Limit", err)
	}
}

func TestM3U8LocalizesSegmentsAndFFmpegArgsStayFileOnly(t *testing.T) {
	root := t.TempDir()
	manifest := "#EXTM3U\n#EXTINF:2.0,\nseg-a.ts\n#EXTINF:2.0,\nsubdir/seg-b.ts\n"
	parsed, err := ParseM3U8Manifest(manifest, M3U8Options{MaxDepth: 2, MaxSegments: 10, MaxManifestBytes: 1024})
	if err != nil {
		t.Fatalf("ParseM3U8Manifest() error = %v", err)
	}
	localized, err := LocalizeM3U8(parsed, LocalizeOptions{TempRoot: root})
	if err != nil {
		t.Fatalf("LocalizeM3U8() error = %v", err)
	}
	if strings.Contains(localized.Manifest, "seg-a.ts") || strings.Contains(localized.Manifest, "subdir") ||
		strings.Contains(localized.Manifest, "http://") || strings.Contains(localized.Manifest, "..") {
		t.Fatalf("localized manifest still exposes remote/user-controlled paths:\n%s", localized.Manifest)
	}
	for _, segment := range localized.Segments {
		if !strings.HasPrefix(segment.LocalPath, root+string(filepath.Separator)) {
			t.Fatalf("segment escaped temp root: %s", segment.LocalPath)
		}
		if strings.Contains(segment.LocalURI, "/") || strings.Contains(segment.LocalURI, "..") {
			t.Fatalf("local URI is not generated basename: %q", segment.LocalURI)
		}
	}

	command, err := BuildFFmpegCommand(FFmpegCommandRequest{
		Executable:   "ffmpeg",
		TempRoot:     root,
		ManifestPath: filepath.Join(root, "playlist.local.m3u8"),
		OutputPath:   filepath.Join(root, "output.mp4"),
	})
	if err != nil {
		t.Fatalf("BuildFFmpegCommand() error = %v", err)
	}
	joined := strings.Join(append([]string{command.Executable}, command.Args...), " ")
	if strings.Contains(joined, "http://") || strings.Contains(joined, "https://") ||
		strings.Contains(joined, "concat:") || strings.Contains(joined, "crypto:") {
		t.Fatalf("ffmpeg command contains remote protocol material: %s", joined)
	}
	if !containsAdjacent(command.Args, "-protocol_whitelist", "file") {
		t.Fatalf("ffmpeg args missing exact file-only protocol whitelist: %#v", command.Args)
	}
	if _, err := BuildFFmpegCommand(FFmpegCommandRequest{
		Executable:   "/usr/bin/ffmpeg",
		TempRoot:     root,
		ManifestPath: filepath.Join(root, "playlist.local.m3u8"),
		OutputPath:   filepath.Join(root, "output.mp4"),
	}); !errors.Is(err, ErrFFmpegExecutable) {
		t.Fatalf("BuildFFmpegCommand(dynamic executable) error = %v, want ErrFFmpegExecutable", err)
	}
	if _, err := BuildFFmpegCommand(FFmpegCommandRequest{
		Executable:   "ffmpeg",
		TempRoot:     root,
		ManifestPath: "https://example.com/playlist.m3u8",
		OutputPath:   filepath.Join(root, "output.mp4"),
	}); !errors.Is(err, ErrUnsafeLocalPath) {
		t.Fatalf("BuildFFmpegCommand(remote manifest) error = %v, want ErrUnsafeLocalPath", err)
	}
}

func containsAdjacent(values []string, left, right string) bool {
	for index := 0; index+1 < len(values); index++ {
		if values[index] == left && values[index+1] == right {
			return true
		}
	}
	return false
}
