package parser

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
)

var qqVidPathRe = regexp.MustCompile(`/x/(?:page|cover)/(?:[^/]+/)?(\w+)\.html`)

type qqVideo struct{}

func (q qqVideo) parseShareUrl(shareUrl string) (*VideoParseInfo, error) {
	vid, err := q.extractVid(shareUrl)
	if err != nil {
		return nil, fmt.Errorf("提取视频ID失败: %w", err)
	}

	return q.parseVideoID(vid)
}

func (q qqVideo) parseVideoID(videoId string) (*VideoParseInfo, error) {
	if len(videoId) == 0 {
		return nil, errors.New("视频ID不能为空")
	}

	apiUrl := fmt.Sprintf(
		"https://vv.video.qq.com/getinfo?vids=%s&platform=101001&otype=json&defn=shd",
		videoId,
	)

	client := newRestyClient()
	res, err := client.R().
		SetHeader(HttpHeaderUserAgent, DefaultUserAgent).
		Get(apiUrl)
	if err != nil {
		return nil, fmt.Errorf("请求腾讯视频API失败: %w", err)
	}

	body := string(res.Body())

	jsonStr := strings.TrimPrefix(body, "QZOutputJson=")
	jsonStr = strings.TrimSuffix(jsonStr, ";")

	em := gjson.Get(jsonStr, "em").Int()
	if em != 0 {
		msg := gjson.Get(jsonStr, "msg").String()
		return nil, fmt.Errorf("腾讯视频API返回错误: %s (em: %d)", msg, em)
	}

	viResult := gjson.Get(jsonStr, "vl.vi.0")
	if !viResult.Exists() {
		return nil, errors.New("未找到视频信息，视频可能已被删除或设为私密")
	}

	uiResult := viResult.Get("ul.ui.0")
	if !uiResult.Exists() {
		return nil, errors.New("未找到视频CDN地址")
	}

	baseUrl := uiResult.Get("url").String()
	fn := viResult.Get("fn").String()
	fvkey := viResult.Get("fvkey").String()
	if len(baseUrl) == 0 || len(fn) == 0 || len(fvkey) == 0 {
		return nil, errors.New("视频地址信息不完整")
	}

	videoUrl := fmt.Sprintf("%s%s?vkey=%s", baseUrl, fn, fvkey)

	vid := viResult.Get("vid").String()
	title := viResult.Get("ti").String()
	coverUrl := fmt.Sprintf("https://puui.qpic.cn/vpic_cover/%s/%s_hz.jpg/496", vid, vid)

	parseRes := &VideoParseInfo{
		Title:    title,
		VideoUrl: videoUrl,
		CoverUrl: coverUrl,
		Images:   make([]ImgInfo, 0),
	}

	return parseRes, nil
}

func (q qqVideo) extractVid(rawUrl string) (string, error) {
	parsedUrl, err := url.Parse(rawUrl)
	if err != nil {
		return "", fmt.Errorf("URL格式无效: %w", err)
	}

	host := parsedUrl.Host

	if strings.Contains(host, "m.v.qq.com") {
		vid := parsedUrl.Query().Get("vid")
		if len(vid) > 0 {
			return vid, nil
		}
		return "", errors.New("移动端链接中未找到vid参数")
	}

	if strings.Contains(host, "v.qq.com") {
		return q.extractVidFromPath(parsedUrl.Path)
	}

	return "", fmt.Errorf("不支持的腾讯视频域名: %s", host)
}

func (q qqVideo) extractVidFromPath(path string) (string, error) {
	matches := qqVidPathRe.FindStringSubmatch(path)
	if len(matches) >= 2 && len(matches[1]) > 0 {
		return matches[1], nil
	}

	return "", fmt.Errorf("无法从路径 %s 中提取视频ID", path)
}
