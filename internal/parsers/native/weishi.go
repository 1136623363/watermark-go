package parser

import (
	"errors"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"
)

type weiShi struct {
}

func (w weiShi) parseVideoID(videoId string) (*VideoParseInfo, error) {
	reqUrl := "https://h5.weishi.qq.com/webapp/json/weishi/WSH5GetPlayPage?feedid=" + videoId
	client := newRestyClient()
	res, err := client.R().
		SetHeader(HttpHeaderUserAgent, DefaultUserAgent).
		Get(reqUrl)
	if err != nil {
		return nil, err
	}

	// 接口返回错误
	if gjson.GetBytes(res.Body(), "ret").Int() != 0 {
		return nil, errors.New(gjson.GetBytes(res.Body(), "msg").String())
	}
	// 视频状态错误
	errMsg := gjson.GetBytes(res.Body(), "data.errmsg").String()
	if len(errMsg) > 0 {
		return nil, errors.New(errMsg)
	}

	data := gjson.GetBytes(res.Body(), "data.feeds.0")
	author := data.Get("poster.nick").String()
	avatar := data.Get("poster.avatar").String()
	title := data.Get("feed_desc_withat").String()
	videoUrl := data.Get("video_url").String()
	cover := data.Get("images.0.url").String()

	parseRes := &VideoParseInfo{
		Title:    title,
		VideoUrl: videoUrl,
		CoverUrl: cover,
	}
	parseRes.Author.Name = author
	parseRes.Author.Avatar = avatar

	return parseRes, nil
}

func (w weiShi) parseShareUrl(shareUrl string) (*VideoParseInfo, error) {
	videoId, err := w.extractVideoID(shareUrl)
	if err != nil {
		return nil, err
	}

	return w.parseVideoID(videoId)
}

func (w weiShi) extractVideoID(shareUrl string) (string, error) {
	urlRes, err := url.Parse(shareUrl)
	if err != nil {
		return "", err
	}

	videoId := urlRes.Query().Get("id")
	if strings.TrimSpace(videoId) != "" {
		return strings.TrimSpace(videoId), nil
	}

	path := strings.Trim(urlRes.Path, "/")
	if path == "" {
		return "", errors.New("parse video_id from share url fail")
	}
	parts := strings.Split(path, "/")
	videoId = strings.TrimSpace(parts[len(parts)-1])
	if videoId == "" || videoId == "index.html" || videoId == "sorry" {
		return "", errors.New("parse video_id from share url fail")
	}

	return videoId, nil
}
