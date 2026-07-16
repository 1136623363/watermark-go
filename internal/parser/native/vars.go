package native

import coreparser "github.com/1136623363/watermark-go/internal/parser"

// 视频渠道来源
const (
	SourceDouYin       = "douyin"       // 抖音
	SourceKuaiShou     = "kuaishou"     // 快手
	SourcePiPiXia      = "pipixia"      // 皮皮虾
	SourceHuoShan      = "huoshan"      // 火山
	SourceWeiBo        = "weibo"        // 微博
	SourceWeiShi       = "weishi"       // 微视
	SourceLvZhou       = "lvzhou"       // 绿洲
	SourceZuiYou       = "zuiyou"       // 最右
	SourceQuanMin      = "quanmin"      // 度小视(原 全民小视频)
	SourceXiGua        = "xigua"        // 西瓜
	SourceLiShiPin     = "lishipin"     // 梨视频
	SourcePiPiGaoXiao  = "pipigaoxiao"  // 皮皮搞笑
	SourceHuYa         = "huya"         // 虎牙
	SourceAcFun        = "acfun"        // A站
	SourceDouPai       = "doupai"       // 逗拍
	SourceMeiPai       = "meipai"       // 美拍
	SourceQuanMinKGe   = "quanminkge"   // 全民K歌
	SourceSixRoom      = "sixroom"      // 六间房
	SourceXinPianChang = "xinpianchang" // 新片场
	SourceHaoKan       = "haokan"       // 好看视频
	SourceRedBook      = "redbook"      // 小红书
	SourceBiliBili     = "bilibili"     // 哔哩哔哩
	SourceTwitter      = "twitter"      // X/Twitter
	SourceQQVideo      = "qqvideo"      // 腾讯视频
	SourceCCTV         = "cctv"         // 央视网
	SourceSohu         = "sohu"         // 搜狐视频
)

// http 相关
const (
	HttpHeaderUserAgent   = "User-Agent" //http header
	HttpHeaderReferer     = "Referer"
	HttpHeaderContentType = "Content-Type"
	HttpHeaderCookie      = "Cookie"

	// DefaultUserAgent 默认UserAgent
	DefaultUserAgent = "Mozilla/5.0 (iPhone; CPU iPhone OS 26_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.0 Mobile/15E148 Safari/604.1"
)

// videoShareUrlParser 根据视频分享地址解析
type videoShareUrlParser interface {
	parseShareUrl(shareUrl string) (*VideoParseInfo, error)
}

// videoIdParser 根据视频ID解析
type videoIdParser interface {
	parseVideoID(videoId string) (*VideoParseInfo, error)
}

// VideoParseInfo 视频解析信息
type VideoParseInfo struct {
	Author struct {
		Uid    string `json:"uid"`    // 作者id
		Name   string `json:"name"`   // 作者名称
		Avatar string `json:"avatar"` // 作者头像
	} `json:"author"`
	Title      string    `json:"title"`       // 描述
	VideoUrl   string    `json:"video_url"`   // 视频播放地址
	PreviewUrl string    `json:"preview_url"` // Web 端优先用于预览的地址
	MusicUrl   string    `json:"music_url"`   // 音乐播放地址
	CoverUrl   string    `json:"cover_url"`   // 视频封面地址
	Images     []ImgInfo `json:"images"`      // 图集图片地址列表
	// Candidates is an internal-only lossless media projection. Compatibility
	// JSON keeps the historical single VideoUrl/MusicUrl fields.
	Candidates []coreparser.MediaCandidate `json:"-"`
}

type ImgInfo struct {
	Url          string `json:"url"`            // 图片url
	LivePhotoUrl string `json:"live_photo_url"` // livePhoto 视频地址
}

// BatchParseItem 批量解析时, 单条解析格式
type BatchParseItem struct {
	ParseInfo *VideoParseInfo // 视频解析信息
	Error     error           // 错误, 如果单条解析失败时, 记录error信息
}

// 视频渠道信息
type videoSourceInfo struct {
	VideoShareUrlDomain []string            // 视频分享地址域名
	VideoShareUrlParser videoShareUrlParser // 视频分享地址解析方法
	VideoIdParser       videoIdParser       // 视频id解析方法, 有些渠道可能没有id解析方法
}

// 视频渠道映射信息
func newVideoSourceInfoMapping(clients legacyHTTPClients) map[string]videoSourceInfo {
	mapping := make(map[string]videoSourceInfo, len(nativeRegistrations))
	for _, registration := range nativeRegistrations {
		if registration.bind == nil {
			continue
		}
		binding := registration.bind(clients)
		domains := make([]string, 0, len(registration.hostRules))
		for _, rule := range registration.hostRules {
			domains = append(domains, rule.Host)
		}
		mapping[registration.key] = videoSourceInfo{
			VideoShareUrlDomain: domains,
			VideoShareUrlParser: binding.share,
			VideoIdParser:       binding.id,
		}
	}
	return mapping
}

// The legacy map is retained only for package-local extraction regression
// tests. It is a compatibility view derived from nativeRegistrations;
// production routing and parsing never consult it.
var videoSourceInfoMapping = newVideoSourceInfoMapping(legacyHTTPClients{})
