package native

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-resty/resty/v2"
	"github.com/tidwall/gjson"

	coreparser "github.com/1136623363/watermark-go/internal/parser"
)

type kuaiShou struct{ legacyHTTPClients }

func (k kuaiShou) parseShareUrl(shareUrl string) (*VideoParseInfo, error) {
	client := k.newRestyClientWithCheckRedirect(func(req *http.Request, via []*http.Request) error {
		// 检查是否 /short-video/3xb58q4e3egqttc 这样的路径，如果是就继续跟随 Location 重定向，否则就停止
		if matched, _ := regexp.MatchString(`^/short-video/[^/]+/?$`, req.URL.Path); !matched {
			return resty.ErrAutoRedirectDisabled
		}
		return nil
	})
	shareRes, err := client.R().
		SetHeader(HttpHeaderUserAgent, DefaultUserAgent).
		SetHeader("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7").
		Get(shareUrl)
	// 非 resty.ErrAutoRedirectDisabled 错误时，返回错误
	if !errors.Is(err, resty.ErrAutoRedirectDisabled) {
		return nil, err
	}

	locationRes, err := shareRes.RawResponse.Location()
	if err != nil {
		return nil, err
	}

	// /fw/long-video/ 返回结果不一样, 统一替换为 /fw/photo/ 请求
	locationUrl := locationRes.String()
	locationUrl = strings.ReplaceAll(locationUrl, "/fw/long-video/", "/fw/photo/")

	res, err := client.R().
		SetHeader(HttpHeaderUserAgent, DefaultUserAgent).
		SetHeader("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7").
		Get(locationUrl)
	if err != nil {
		return nil, err
	}

	structured, _, err := extractStructuredJSON(res.Body(), "window.INIT_STATE", "window.__APOLLO_STATE__")
	if err != nil {
		return nil, err
	}
	snapshot, err := findKuaishouSnapshot(structured)
	if err != nil {
		return nil, err
	}
	videoRes := gjson.ParseBytes(snapshot)

	if resultCode := videoRes.Get("result").Int(); resultCode != 1 {
		return nil, fmt.Errorf("获取作品信息失败:result=%d", resultCode)
	}

	data := videoRes.Get("photo")
	avatar := data.Get("headUrl").String()
	author := data.Get("userName").String()
	title := data.Get("caption").String()
	cover := data.Get("coverUrls.0.url").String()
	candidates := make([]coreparser.MediaCandidate, 0)
	for _, source := range data.Get("mainMvUrls").Array() {
		metadata := candidateMetadata{
			Quality: positiveGJSONInt(source, "quality"),
			Bitrate: positiveGJSONInt(source, "bitrate", "bandwidth"),
			Width:   positiveGJSONInt(source, "width"),
			Height:  positiveGJSONInt(source, "height"),
		}
		candidates = appendUsableMediaCandidate(candidates, source.Get("url").String(), coreparser.MediaKindVideo, metadata)
	}

	// 获取图集
	imageCdnHost := data.Get("ext_params.atlas.cdn.0").String()
	imagesObjArr := data.Get("ext_params.atlas.list").Array()
	images := make([]ImgInfo, 0, len(imagesObjArr))
	if len(imageCdnHost) > 0 && len(imagesObjArr) > 0 {
		for _, imageItem := range imagesObjArr {
			imageUrl := fmt.Sprintf("https://%s/%s", imageCdnHost, imageItem.String())
			images = append(images, ImgInfo{
				Url: imageUrl,
			})
		}
	}

	parseRes := &VideoParseInfo{
		Title:    title,
		CoverUrl: normalizedExternalURLOrEmpty(cover),
		Images:   images,
	}
	parseRes.Author.Name = author
	parseRes.Author.Avatar = normalizedExternalURLOrEmpty(avatar)
	applyMediaCandidates(parseRes, candidates)

	return parseRes, nil
}

func positiveGJSONInt(value gjson.Result, paths ...string) int {
	for _, path := range paths {
		field := value.Get(path)
		if !field.Exists() {
			continue
		}
		parsed := field.Int()
		if parsed > 0 && parsed <= int64(^uint(0)>>1) {
			return int(parsed)
		}
	}
	return 0
}
