package parser

import (
	"bytes"
	"errors"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/go-resty/resty/v2"
	"github.com/tidwall/gjson"
)

type xiGua struct {
}

func (x xiGua) parseShareUrl(shareUrl string) (*VideoParseInfo, error) {
	client := newRestyClient()
	// disable redirects in the HTTP client, get params before redirects
	client.SetRedirectPolicy(resty.NoRedirectPolicy())
	res, err := client.R().
		SetHeader(HttpHeaderUserAgent, DefaultUserAgent).
		Get(shareUrl)
	if err == nil {
		return x.parseDirectURL(shareUrl)
	}
	// 非 resty.ErrAutoRedirectDisabled 错误时，返回错误
	if !errors.Is(err, resty.ErrAutoRedirectDisabled) {
		return nil, err
	}

	locationRes, err := res.RawResponse.Location()
	if err != nil {
		return nil, err
	}

	videoId := strings.ReplaceAll(strings.Trim(locationRes.Path, "/"), "video/", "")
	if len(videoId) <= 0 {
		return nil, errors.New("parse video id from share url fail")
	}

	return x.parseVideoID(videoId)
}

func (x xiGua) parseDirectURL(shareUrl string) (*VideoParseInfo, error) {
	urlRes, err := url.Parse(shareUrl)
	if err != nil {
		return nil, err
	}
	videoId := strings.Trim(urlRes.Path, "/")
	videoId = strings.TrimPrefix(videoId, "video/")
	if len(videoId) <= 0 {
		return nil, errors.New("parse video id from share url fail")
	}
	return x.parseVideoID(videoId)
}

func (x xiGua) parseVideoID(videoId string) (*VideoParseInfo, error) {
	reqUrl := "https://m.ixigua.com/douyin/share/video/" + videoId + "?aweme_type=107&schema_type=1&utm_source=copy&utm_campaign=client_share&utm_medium=android&app=aweme"
	headers := xiGuaRequestHeaders()

	client := newRestyClient()
	res, err := client.R().
		SetHeaders(headers).
		Get(reqUrl)
	if err != nil {
		return nil, err
	}

	re := regexp.MustCompile(`window._ROUTER_DATA\s*=\s*(.*?)</script>`)
	findRes := re.FindSubmatch(res.Body())
	if len(findRes) < 2 {
		return nil, errors.New("parse video json info from html fail")
	}

	jsonBytes := bytes.TrimSpace(findRes[1])
	videoData := gjson.GetBytes(jsonBytes, "loaderData.video_(id)/page.videoInfoRes.item_list.0")

	userId := videoData.Get("author.user_id").String()
	userName := videoData.Get("author.nickname").String()
	userAvatar := videoData.Get("author.avatar_thumb.url_list.0").String()
	videoDesc := videoData.Get("desc").String()
	videoAddr := videoData.Get("video.play_addr.url_list.0").String()
	coverUrl := videoData.Get("video.cover.url_list.0").String()

	parseRes := &VideoParseInfo{
		Title:    videoDesc,
		VideoUrl: videoAddr,
		CoverUrl: coverUrl,
	}
	parseRes.Author.Uid = userId
	parseRes.Author.Name = userName
	parseRes.Author.Avatar = userAvatar

	return parseRes, nil
}

func xiGuaRequestHeaders() map[string]string {
	headers := map[string]string{
		HttpHeaderUserAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/79.0.3945.88 Safari/537.36",
	}
	if cookie := strings.TrimSpace(os.Getenv("XIGUA_COOKIE")); cookie != "" {
		headers[HttpHeaderCookie] = cookie
	}
	return headers
}
