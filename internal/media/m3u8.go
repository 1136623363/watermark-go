package media

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

var (
	ErrM3U8Depth        = errors.New("m3u8 manifest depth exceeded")
	ErrM3U8Limit        = errors.New("m3u8 manifest limit exceeded")
	ErrM3U8UnsafeURI    = errors.New("m3u8 unsafe uri")
	ErrM3U8Encryption   = errors.New("m3u8 encryption is not supported")
	ErrUnsafeLocalPath  = errors.New("unsafe local media path")
	ErrFFmpegExecutable = errors.New("ffmpeg executable must be fixed")
	ErrFFmpegFailed     = errors.New("ffmpeg failed")
)

type M3U8Options struct {
	CurrentDepth     int
	MaxDepth         int
	MaxSegments      int
	MaxManifestBytes int
}

type Manifest struct {
	Original       string
	Segments       []SegmentInfo
	ChildManifests []string
}

type SegmentInfo struct {
	URI   string
	Bytes int64
}

type LocalizeOptions struct {
	TempRoot string
}

type LocalizedM3U8 struct {
	Manifest string
	Segments []LocalizedSegment
}

type LocalizedSegment struct {
	SourceURI string
	LocalURI  string
	LocalPath string
}

type FFmpegCommandRequest struct {
	Executable   string
	TempRoot     string
	ManifestPath string
	OutputPath   string
}

type FFmpegCommand struct {
	Executable string
	Args       []string
}

func ParseM3U8Manifest(manifest string, options M3U8Options) (Manifest, error) {
	options = normalizeM3U8Options(options)
	if len([]byte(manifest)) > options.MaxManifestBytes {
		return Manifest{}, ErrM3U8Limit
	}
	parsed := Manifest{Original: manifest}
	expectChild := false
	for _, rawLine := range strings.Split(manifest, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		upper := strings.ToUpper(line)
		if strings.HasPrefix(line, "#") {
			if strings.HasPrefix(upper, "#EXT-X-KEY") || strings.Contains(upper, "METHOD=AES-128") {
				return Manifest{}, ErrM3U8Encryption
			}
			expectChild = strings.HasPrefix(upper, "#EXT-X-STREAM-INF")
			continue
		}
		if err := validateManifestURI(line); err != nil {
			return Manifest{}, err
		}
		if expectChild {
			if options.CurrentDepth >= options.MaxDepth {
				return Manifest{}, ErrM3U8Depth
			}
			parsed.ChildManifests = append(parsed.ChildManifests, line)
			expectChild = false
			continue
		}
		parsed.Segments = append(parsed.Segments, SegmentInfo{URI: line})
		if len(parsed.Segments) > options.MaxSegments {
			return Manifest{}, ErrM3U8Limit
		}
	}
	return parsed, nil
}

func ValidateSegmentBudget(segments []SegmentInfo, maxBytes int64) error {
	if maxBytes <= 0 {
		return nil
	}
	var total int64
	for _, segment := range segments {
		if segment.Bytes < 0 {
			return ErrM3U8Limit
		}
		total += segment.Bytes
		if total > maxBytes {
			return ErrM3U8Limit
		}
	}
	return nil
}

func LocalizeM3U8(manifest Manifest, options LocalizeOptions) (LocalizedM3U8, error) {
	root, err := absoluteRoot(options.TempRoot)
	if err != nil {
		return LocalizedM3U8{}, err
	}
	segmentNames := make(map[string]LocalizedSegment, len(manifest.Segments))
	for index, segment := range manifest.Segments {
		localURI := fmt.Sprintf("segment_%06d.ts", index+1)
		localPath := filepath.Join(root, localURI)
		segmentNames[segment.URI] = LocalizedSegment{
			SourceURI: segment.URI,
			LocalURI:  localURI,
			LocalPath: localPath,
		}
	}
	var rewritten []string
	for _, rawLine := range strings.Split(manifest.Original, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			rewritten = append(rewritten, line)
			continue
		}
		if segment, ok := segmentNames[line]; ok {
			rewritten = append(rewritten, segment.LocalURI)
		}
	}
	localized := LocalizedM3U8{
		Manifest: strings.Join(rewritten, "\n") + "\n",
		Segments: make([]LocalizedSegment, 0, len(manifest.Segments)),
	}
	for _, segment := range manifest.Segments {
		localized.Segments = append(localized.Segments, segmentNames[segment.URI])
	}
	return localized, nil
}

func BuildFFmpegCommand(request FFmpegCommandRequest) (FFmpegCommand, error) {
	executable := strings.TrimSpace(request.Executable)
	if executable != "ffmpeg" {
		return FFmpegCommand{}, ErrFFmpegExecutable
	}
	manifestPath, err := requireLocalPath(request.TempRoot, request.ManifestPath)
	if err != nil {
		return FFmpegCommand{}, err
	}
	outputPath, err := requireLocalPath(request.TempRoot, request.OutputPath)
	if err != nil {
		return FFmpegCommand{}, err
	}
	return FFmpegCommand{
		Executable: executable,
		Args: []string{
			"-hide_banner",
			"-nostdin",
			"-protocol_whitelist", "file",
			"-allowed_extensions", "ALL",
			"-i", manifestPath,
			"-c", "copy",
			"-y", outputPath,
		},
	}, nil
}

func normalizeM3U8Options(options M3U8Options) M3U8Options {
	if options.MaxDepth <= 0 {
		options.MaxDepth = 3
	}
	if options.MaxSegments <= 0 {
		options.MaxSegments = 256
	}
	if options.MaxManifestBytes <= 0 {
		options.MaxManifestBytes = 1 << 20
	}
	return options
}

func validateManifestURI(raw string) error {
	if raw == "" || strings.ContainsAny(raw, "\x00\r\n\\") {
		return ErrM3U8UnsafeURI
	}
	decoded, err := url.PathUnescape(raw)
	if err == nil && decoded != raw {
		if err := validateManifestURI(decoded); err != nil {
			return err
		}
	}
	if strings.HasPrefix(raw, "/") {
		return ErrM3U8UnsafeURI
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ErrM3U8UnsafeURI
	}
	if parsed.Scheme != "" || parsed.Host != "" || parsed.Opaque != "" {
		return ErrM3U8UnsafeURI
	}
	cleaned := path.Clean(parsed.Path)
	if cleaned == "." || strings.HasPrefix(cleaned, "../") || cleaned == ".." || strings.Contains(cleaned, "/../") {
		return ErrM3U8UnsafeURI
	}
	if strings.Contains(raw, ":") && (strings.Index(raw, ":") < strings.Index(raw+"/", "/")) {
		return ErrM3U8UnsafeURI
	}
	return nil
}

func requireLocalPath(root string, candidate string) (string, error) {
	if strings.TrimSpace(candidate) == "" || strings.Contains(candidate, "://") {
		return "", ErrUnsafeLocalPath
	}
	absRoot, err := absoluteRoot(root)
	if err != nil {
		return "", err
	}
	absCandidate, err := filepath.Abs(candidate)
	if err != nil {
		return "", ErrUnsafeLocalPath
	}
	absCandidate = filepath.Clean(absCandidate)
	relative, err := filepath.Rel(absRoot, absCandidate)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == ".." || filepath.IsAbs(relative) {
		return "", ErrUnsafeLocalPath
	}
	return absCandidate, nil
}

func absoluteRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" || strings.Contains(root, "://") {
		return "", ErrUnsafeLocalPath
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", ErrUnsafeLocalPath
	}
	if err := os.MkdirAll(absRoot, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(absRoot, 0o700); err != nil {
		return "", err
	}
	return filepath.Clean(absRoot), nil
}
