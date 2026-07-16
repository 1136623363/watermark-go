package native

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-resty/resty/v2"
	"github.com/tidwall/gjson"

	coreparser "github.com/1136623363/watermark-go/internal/parser"
)

type weiBo struct {
	legacyHTTPClients
}

func setWeiboCookie(request *resty.Request, configured string) {
	cookie := strings.TrimSpace(configured)
	if cookie == "" {
		return
	}
	request.SetHeader(HttpHeaderCookie, cookie)
}

func (w weiBo) parseShareUrl(shareUrl string) (*VideoParseInfo, error) {
	urlInfo, err := url.Parse(shareUrl)
	if err != nil {
		return nil, errors.New("parse share url fail")
	}

	// Handle video URLs
	if strings.Contains(shareUrl, "show?fid=") {
		if len(urlInfo.Query()["fid"]) <= 0 {
			return nil, errors.New("can not parse video id from share url")
		}
		videoId := urlInfo.Query()["fid"][0]
		return w.parseVideoID(videoId)
	} else if strings.Contains(shareUrl, "/tv/show/") {
		videoId := strings.ReplaceAll(urlInfo.Path, "/tv/show/", "")
		return w.parseVideoID(videoId)
	} else {
		// Handle regular post URLs (potential image albums)
		// Extract post ID from URLs like https://weibo.com/2543858012/Q9pcJ4S21
		pathParts := strings.Split(strings.Trim(urlInfo.Path, "/"), "/")
		if len(pathParts) >= 2 {
			postId := pathParts[len(pathParts)-1]
			return w.parsePostUrl(postId, shareUrl)
		}
	}

	return nil, errors.New("unsupported weibo url format")
}

func (w weiBo) parseVideoID(videoId string) (*VideoParseInfo, error) {
	reqUrl := fmt.Sprintf("https://h5.video.weibo.com/api/component?page=/show/%s", videoId)
	client := w.newRestyClient()
	request := client.R()
	setWeiboCookie(request, w.weiboCookie)
	videoRes, err := request.
		SetHeader(HttpHeaderReferer, "https://h5.video.weibo.com/show/"+videoId).
		SetHeader(HttpHeaderContentType, "application/x-www-form-urlencoded").
		SetHeader(HttpHeaderUserAgent, DefaultUserAgent).
		SetBody([]byte(`data={"Component_Play_Playinfo":{"oid":"` + videoId + `"}}`)).
		Post(reqUrl)
	if err != nil {
		return nil, err
	}
	data := gjson.GetBytes(videoRes.Body(), "data.Component_Play_Playinfo")
	candidates := make([]coreparser.MediaCandidate, 0)
	data.Get("urls").ForEach(func(key, value gjson.Result) bool {
		candidates = appendUsableMediaCandidate(candidates, value.String(), coreparser.MediaKindVideo, weiboCandidateMetadata(key.String()))
		return true
	})
	parseInfo := &VideoParseInfo{
		Title:    data.Get("title").String(),
		CoverUrl: normalizedExternalURLOrEmpty(data.Get("cover_image").String()),
	}
	parseInfo.Author.Name = data.Get("author").String()
	parseInfo.Author.Avatar = normalizedExternalURLOrEmpty(data.Get("avatar").String())
	applyMediaCandidates(parseInfo, candidates)

	return parseInfo, nil
}

var (
	weiboDimensionsPattern = regexp.MustCompile(`(?i)(\d{2,5})\s*[x×]\s*(\d{2,5})`)
	weiboHeightPattern     = regexp.MustCompile(`(?i)(\d{3,4})\s*p(?:\b|$)`)
	weiboBitratePattern    = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*([km])?bps`)
)

func weiboCandidateMetadata(label string) candidateMetadata {
	metadata := candidateMetadata{}
	if dimensions := weiboDimensionsPattern.FindStringSubmatch(label); len(dimensions) == 3 {
		metadata.Width, _ = strconv.Atoi(dimensions[1])
		metadata.Height, _ = strconv.Atoi(dimensions[2])
		metadata.Quality = metadata.Height
	}
	if height := weiboHeightPattern.FindStringSubmatch(label); len(height) == 2 {
		metadata.Quality, _ = strconv.Atoi(height[1])
		if metadata.Height == 0 {
			metadata.Height = metadata.Quality
		}
	}
	if bitrate := weiboBitratePattern.FindStringSubmatch(label); len(bitrate) == 3 {
		value, parseErr := strconv.ParseFloat(bitrate[1], 64)
		if parseErr == nil {
			switch strings.ToLower(bitrate[2]) {
			case "m":
				value *= 1_000_000
			case "k":
				value *= 1_000
			}
			if value > 0 && value <= float64(^uint(0)>>1) {
				metadata.Bitrate = int(value)
			}
		}
	}
	return metadata
}

func normalizedExternalURLOrEmpty(raw string) string {
	normalized, _ := normalizeExternalMediaURL(raw)
	return normalized
}

func (w weiBo) parsePostUrl(postId string, originalUrl string) (*VideoParseInfo, error) {
	// Try mobile API first
	reqUrl := fmt.Sprintf("https://m.weibo.cn/statuses/show?id=%s", postId)
	client := w.newRestyClient()

	res, err := client.R().
		SetHeader(HttpHeaderUserAgent, "Mozilla/5.0 (iPhone; CPU iPhone OS 14_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/14.0 Mobile/15E148 Safari/604.1").
		SetHeader(HttpHeaderReferer, "https://m.weibo.cn/").
		SetHeader(HttpHeaderContentType, "application/json;charset=UTF-8").
		SetHeader("X-Requested-With", "XMLHttpRequest").
		Get(reqUrl)
	if err == nil {
		data := gjson.GetBytes(res.Body(), "data")
		if data.Exists() {
			return w.parseMobileApiData(data)
		}
	}

	// Fallback to desktop page parsing using the original URL
	res, err = client.R().
		SetHeader(HttpHeaderUserAgent, "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36").
		Get(originalUrl)
	if err != nil {
		return nil, err
	}

	return w.parseHtmlPage(res.Body())
}

func (w weiBo) parseMobileApiData(data gjson.Result) (*VideoParseInfo, error) {
	// Extract basic info
	title := data.Get("text").String()
	authorName := data.Get("user.screen_name").String()
	authorAvatar := data.Get("user.avatar_large").String()

	// Get images
	images := make([]ImgInfo, 0)
	picsData := data.Get("pics")
	if picsData.Exists() {
		picsArray := picsData.Array()
		for _, pic := range picsArray {
			// Get the largest image URL available
			largePicUrl := pic.Get("large.url").String()
			if largePicUrl == "" {
				largePicUrl = pic.Get("original.url").String()
			}
			if largePicUrl == "" {
				largePicUrl = pic.Get("bmiddle.url").String()
			}
			if largePicUrl == "" {
				largePicUrl = pic.Get("url").String()
			}

			if largePicUrl != "" {
				images = append(images, ImgInfo{
					Url: largePicUrl,
				})
			}
		}
	}

	parseInfo := &VideoParseInfo{
		Title:    w.cleanText(title),
		VideoUrl: "", // Regular posts don't have videos
		CoverUrl: "",
		Images:   images,
	}
	parseInfo.Author.Name = authorName
	parseInfo.Author.Avatar = authorAvatar

	return parseInfo, nil
}

func (w weiBo) parseHtmlPage(htmlBody []byte) (*VideoParseInfo, error) {
	// Try to extract data from $render_data script
	re := regexp.MustCompile(`\$render_data\s*=\s*(.*?)\[0\]`)
	findRes := re.FindSubmatch(htmlBody)
	if len(findRes) < 2 {
		return nil, errors.New("parse weibo html page fail")
	}

	jsonStr := string(findRes[1]) + "[0]"
	data := gjson.Parse(jsonStr)

	// Extract basic info
	title := data.Get("status.text").String()
	authorName := data.Get("status.user.screen_name").String()
	authorAvatar := data.Get("status.user.avatar_large").String()

	// Get images
	images := make([]ImgInfo, 0)
	picsData := data.Get("status.pics")
	if picsData.Exists() {
		picsArray := picsData.Array()
		for _, pic := range picsArray {
			// Get the largest image URL available
			largePicUrl := pic.Get("large.url").String()
			if largePicUrl == "" {
				largePicUrl = pic.Get("original.url").String()
			}
			if largePicUrl == "" {
				largePicUrl = pic.Get("bmiddle.url").String()
			}
			if largePicUrl == "" {
				largePicUrl = pic.Get("url").String()
			}

			if largePicUrl != "" {
				images = append(images, ImgInfo{
					Url: largePicUrl,
				})
			}
		}
	}

	parseInfo := &VideoParseInfo{
		Title:    w.cleanText(title),
		VideoUrl: "", // Regular posts don't have videos
		CoverUrl: "",
		Images:   images,
	}
	parseInfo.Author.Name = authorName
	parseInfo.Author.Avatar = authorAvatar

	return parseInfo, nil
}

// cleanText removes HTML tags from text
func (w weiBo) cleanText(text string) string {
	// Remove HTML tags
	re := regexp.MustCompile(`<[^>]*>`)
	cleaned := re.ReplaceAllString(text, "")
	return strings.TrimSpace(cleaned)
}
