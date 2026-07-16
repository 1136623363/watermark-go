package universal

import ytdlprunner "github.com/1136623363/watermark-go/internal/parser/ytdlp"

type Config struct {
	PythonBin        string
	BridgeScript     string
	VideoDLPath      string
	MusicDLPath      string
	WorkDir          string
	BridgeTimeout    int
	MusicDLTimeout   int
	MusicDLItemLimit int
	Runner           ytdlprunner.Executor
	GuardProxy       ytdlprunner.GuardProxy
	Paths            ytdlprunner.ImagePathPolicy
}

type ParseRequest struct {
	URL            string   `json:"url"`
	Keyword        string   `json:"keyword"`
	Kind           string   `json:"kind"`
	Limit          int      `json:"limit"`
	MusicItemLimit int      `json:"musicItemLimit,omitempty"`
	Sources        []string `json:"sources"`
	CommonOnly     bool     `json:"commonOnly"`
}

type DownloadItem struct {
	URL   string `json:"url"`
	Label string `json:"label,omitempty"`
}

type MediaItem struct {
	Platform string `json:"platform,omitempty"`
	Type     string `json:"type,omitempty"`
	Title    string `json:"title,omitempty"`
	Cover    string `json:"cover,omitempty"`
	Author   string `json:"author,omitempty"`
	Duration int    `json:"duration,omitempty"`
	URL      string `json:"url,omitempty"`
	Music    string `json:"music,omitempty"`
}

type ParseData struct {
	Platform  string         `json:"platform"`
	Type      string         `json:"type"`
	Title     string         `json:"title"`
	Desc      string         `json:"desc"`
	Cover     string         `json:"cover"`
	Author    string         `json:"author"`
	Avatar    string         `json:"avatar"`
	Music     string         `json:"music"`
	Duration  int            `json:"duration"`
	Downloads []DownloadItem `json:"downloads"`
	Images    []string       `json:"images"`
	Pics      []string       `json:"pics"`
	M3U8      string         `json:"m3u8"`
	Preview   string         `json:"previewUrl"`
	PlayAddr  string         `json:"playAddr"`
	ShareID   string         `json:"shareId,omitempty"`
	SourceURL string         `json:"sourceUrl,omitempty"`
	Items     []MediaItem    `json:"items,omitempty"`
}
