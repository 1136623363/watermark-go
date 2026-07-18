package parse

import "time"

type HostRule struct {
	Host              string `json:"host"`
	IncludeSubdomains bool   `json:"includeSubdomains"`
}

type Descriptor struct {
	Platform  string
	QueryKeys []string
	HostRules []HostRule
}

type Request struct {
	URL          string
	ForceRefresh bool
	Source       int
	Timestamp    int64
	Signature    string
	Version      int
}

type ParserRequest struct {
	RawURL     string
	Canonical  CanonicalResource
	Descriptor Descriptor
}

type IDRequest struct {
	Source  string
	VideoID string
}

type IDParserRequest struct {
	Source     string
	VideoID    string
	Descriptor Descriptor
}

type Result struct {
	Platform    string
	Type        string
	Title       string
	Description string
	VideoURL    string
	AudioURL    string
	CoverURL    string
	PreviewURL  string
	M3U8URL     string
	Author      Author
	Images      []ImageAsset
	Duration    int
}

type Author struct {
	UID    string `json:"uid,omitempty"`
	Name   string `json:"name,omitempty"`
	Avatar string `json:"avatar,omitempty"`
}

type ImageAsset struct {
	URL          string `json:"url"`
	LivePhotoURL string `json:"livePhotoUrl,omitempty"`
}

type DownloadItem struct {
	URL   string `json:"url"`
	Label string `json:"label,omitempty"`
}

type CompatData struct {
	Platform    string         `json:"platform"`
	Type        string         `json:"type"`
	Title       string         `json:"title"`
	Desc        string         `json:"desc"`
	Cover       string         `json:"cover"`
	Author      string         `json:"author"`
	Avatar      string         `json:"avatar"`
	Music       string         `json:"music"`
	MP3         string         `json:"mp3"`
	Audio       string         `json:"audio"`
	AudioURL    string         `json:"audioUrl"`
	Duration    int            `json:"duration"`
	Downloads   []DownloadItem `json:"downloads"`
	Images      []string       `json:"images"`
	Pics        []string       `json:"pics"`
	M3U8        string         `json:"m3u8"`
	PreviewURL  string         `json:"previewUrl"`
	PlayAddr    string         `json:"playAddr"`
	ShareID     string         `json:"shareId,omitempty"`
	SourceURL   string         `json:"sourceUrl,omitempty"`
	ImageAssets []ImageAsset   `json:"imageAssets,omitempty"`
}

type ParseOutput struct {
	Result Result
	Data   CompatData
	Cache  CacheIdentity
}

type StoredResult struct {
	ShareID   string
	Cache     CacheIdentity
	Result    Result
	Data      CompatData
	CreatedAt time.Time
}
