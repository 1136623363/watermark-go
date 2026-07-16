package native

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/tidwall/gjson"
)

var cctvGuidRe = regexp.MustCompile(`var\s+guid\s*=\s*"([^"]+)"`)

type cctvVideo struct{ legacyHTTPClients }

func (c cctvVideo) parseShareUrl(shareUrl string) (*VideoParseInfo, error) {
	guid, err := c.extractGuid(shareUrl)
	if err != nil {
		return nil, fmt.Errorf("提取视频GUID失败: %w", err)
	}

	return c.parseVideoID(guid)
}

func (c cctvVideo) parseVideoID(videoId string) (*VideoParseInfo, error) {
	if len(videoId) == 0 {
		return nil, errors.New("视频GUID不能为空")
	}

	apiUrl := fmt.Sprintf(
		"https://vdn.apps.cntv.cn/api/getHttpVideoInfo.do?pid=%s",
		videoId,
	)

	client := c.newRestyClient()
	res, err := client.R().
		SetHeader(HttpHeaderUserAgent, DefaultUserAgent).
		Get(apiUrl)
	if err != nil {
		return nil, fmt.Errorf("请求央视网视频API失败: %w", err)
	}

	jsonStr := string(res.Body())

	status := gjson.Get(jsonStr, "status").String()
	if status != "001" {
		msg := gjson.Get(jsonStr, "title").String()
		return nil, fmt.Errorf("央视网视频API返回错误 (status: %s, title: %s)", status, msg)
	}

	// manifest 中的 h5e/enc/enc2 高码率流存在帧级加扰，hls_url 可以正常播放。
	videoUrl := gjson.Get(jsonStr, "hls_url").String()
	if len(videoUrl) == 0 {
		return nil, errors.New("未找到视频播放地址")
	}

	title := gjson.Get(jsonStr, "title").String()
	coverUrl := gjson.Get(jsonStr, "image").String()
	playChannel := gjson.Get(jsonStr, "play_channel").String()

	parseRes := &VideoParseInfo{
		Title:    title,
		VideoUrl: videoUrl,
		CoverUrl: coverUrl,
		Images:   make([]ImgInfo, 0),
	}
	parseRes.Author.Name = playChannel

	return parseRes, nil
}

func (c cctvVideo) extractGuid(pageUrl string) (string, error) {
	client := c.newRestyClient()
	res, err := client.R().
		SetHeader(HttpHeaderUserAgent, DefaultUserAgent).
		Get(pageUrl)
	if err != nil {
		return "", fmt.Errorf("请求页面失败: %w", err)
	}

	return c.extractGuidFromHTML(string(res.Body()))
}

func (c cctvVideo) extractGuidFromHTML(html string) (string, error) {
	matches := cctvGuidRe.FindStringSubmatch(html)
	if len(matches) >= 2 && len(matches[1]) > 0 {
		return matches[1], nil
	}

	return "", errors.New("页面中未找到视频GUID")
}
