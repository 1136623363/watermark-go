package native

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"

	coreparser "github.com/1136623363/watermark-go/internal/parser"
)

var sohuBase64VidRe = regexp.MustCompile(`/v/([A-Za-z0-9+/=]+)\.html`)
var sohuUserVidRe = regexp.MustCompile(`/?us/\d+/(\d+)\.shtml`)

// SohuSessionHost is the exact upstream authority that receives the opaque
// API credential. Session cache scope must use this host, never a share URL.
const SohuSessionHost = "api.tv.sohu.com"

type sohuVideo struct{ legacyHTTPClients }

func (s sohuVideo) parseShareUrl(shareUrl string) (*VideoParseInfo, error) {
	vid, err := s.extractVid(shareUrl)
	if err != nil {
		return nil, fmt.Errorf("提取视频ID失败: %w", err)
	}

	return s.parseVideoID(vid)
}

func (s sohuVideo) parseVideoID(videoId string) (*VideoParseInfo, error) {
	if len(videoId) == 0 {
		return nil, errors.New("视频ID不能为空")
	}

	if s.sohuToken == nil || !s.sohuToken.Configured() {
		return nil, coreparser.NewParseError(coreparser.ErrorCredentialRequired, errors.New("sohu API credential is not configured"))
	}
	apiURL := ""
	if err := s.sohuToken.Use(func(token string) error {
		values := url.Values{}
		values.Set("site", "2")
		values.Set("api_key", token)
		values.Set("sver", "6.2.0")
		apiURL = "https://" + SohuSessionHost + "/v4/video/info/" + url.PathEscape(videoId) + ".json?" + values.Encode()
		return nil
	}); err != nil {
		return nil, coreparser.NewParseError(coreparser.ErrorCredentialRequired, errors.New("sohu API credential is unavailable"))
	}

	client := s.newRestyClient()
	res, err := client.R().
		SetHeader(HttpHeaderUserAgent, DefaultUserAgent).
		Get(apiURL)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, context.DeadlineExceeded
		}
		return nil, coreparser.NewParseError(coreparser.ErrorSecurityRejected, errors.New("sohu API request failed"))
	}
	if res == nil {
		return nil, coreparser.NewParseError(coreparser.ErrorUpstreamFailed, errors.New("sohu API response is absent"))
	}
	if res.StatusCode() == 401 || res.StatusCode() == 403 {
		return nil, coreparser.NewParseError(coreparser.ErrorSessionExpired, errors.New("sohu API credential expired"))
	}
	if res.StatusCode() < 200 || res.StatusCode() >= 300 {
		return nil, coreparser.NewParseError(coreparser.ErrorUpstreamFailed, errors.New("sohu API rejected the request"))
	}

	jsonStr := string(res.Body())

	status := gjson.Get(jsonStr, "status").Int()
	if status == 401 || status == 403 {
		return nil, coreparser.NewParseError(coreparser.ErrorSessionExpired, errors.New("sohu API credential expired"))
	}
	if status != 200 {
		return nil, coreparser.NewParseError(coreparser.ErrorUpstreamFailed, errors.New("sohu API returned an unsuccessful status"))
	}

	data := gjson.Get(jsonStr, "data")
	if !data.Exists() {
		return nil, coreparser.NewParseError(coreparser.ErrorSchemaChanged, errors.New("sohu API video data is absent"))
	}

	videoUrl := data.Get("url_high_mp4").String()
	if len(videoUrl) == 0 {
		videoUrl = data.Get("download_url").String()
	}
	if len(videoUrl) == 0 {
		return nil, coreparser.NewParseError(coreparser.ErrorSchemaChanged, errors.New("sohu API media URL is absent"))
	}

	title := data.Get("video_name").String()
	coverUrl := data.Get("originalCutCover").String()
	authorUid := data.Get("user.user_id").String()
	authorName := data.Get("user.nickname").String()
	authorAvatar := data.Get("user.small_pic").String()

	parseRes := &VideoParseInfo{
		Title:    title,
		VideoUrl: videoUrl,
		CoverUrl: coverUrl,
		Images:   make([]ImgInfo, 0),
	}
	parseRes.Author.Uid = authorUid
	parseRes.Author.Name = authorName
	parseRes.Author.Avatar = authorAvatar

	return parseRes, nil
}

func (s sohuVideo) extractVid(rawUrl string) (string, error) {
	if matches := sohuBase64VidRe.FindStringSubmatch(rawUrl); len(matches) >= 2 {
		decoded, err := base64.StdEncoding.DecodeString(matches[1])
		if err != nil {
			return "", fmt.Errorf("base64解码失败: %w", err)
		}
		return s.extractVidFromPath(string(decoded))
	}

	if strings.Contains(rawUrl, "my.tv.sohu.com") || strings.Contains(rawUrl, "tv.sohu.com/us/") {
		return s.extractVidFromPath(rawUrl)
	}

	return "", errors.New("不是有效的搜狐视频链接")
}

func (s sohuVideo) extractVidFromPath(path string) (string, error) {
	matches := sohuUserVidRe.FindStringSubmatch(path)
	if len(matches) >= 2 && len(matches[1]) > 0 {
		return matches[1], nil
	}
	return "", errors.New("无法从路径中提取视频ID")
}
