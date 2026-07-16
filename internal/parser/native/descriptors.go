package native

import (
	"context"
	"errors"
	"strings"
	"time"

	coreparser "github.com/1136623363/watermark-go/internal/parser"
)

type legacyParserBinding struct {
	share videoShareUrlParser
	id    videoIdParser
}

type nativeRegistration struct {
	key          string
	displayName  string
	aliases      []coreparser.PlatformKey
	hostRules    []coreparser.HostRule
	capabilities coreparser.Capability
	queryKeys    []string
	maxRequests  int
	maxRedirects int
	sessionHost  string
	bind         func(legacyHTTPClients) legacyParserBinding
}

func bindShare(parser videoShareUrlParser) legacyParserBinding {
	return legacyParserBinding{share: parser}
}

func bindShareAndID(parser interface {
	videoShareUrlParser
	videoIdParser
}) legacyParserBinding {
	return legacyParserBinding{share: parser, id: parser}
}

func (registration nativeRegistration) supportsID() bool {
	return registration.bind != nil && registration.bind(legacyHTTPClients{}).id != nil
}

// nativeRegistrations is the sole authority for native routing, metadata and
// parser bindings. Compatibility views are derived from this list below; no
// production request consults a second platform map.
var nativeRegistrations = []nativeRegistration{
	{
		key: SourceDouYin, displayName: "抖音",
		hostRules: []coreparser.HostRule{
			{Host: "v.douyin.com", IncludeSubdomains: true},
			{Host: "www.iesdouyin.com", IncludeSubdomains: true},
			{Host: "www.douyin.com", IncludeSubdomains: true},
		},
		capabilities: coreparser.CapabilityVideo | coreparser.CapabilityGallery | coreparser.CapabilityAudio | coreparser.CapabilityLivePhoto,
		queryKeys:    []string{"modal_id"}, maxRequests: 4, maxRedirects: 3,
		bind: func(clients legacyHTTPClients) legacyParserBinding {
			return bindShareAndID(douYin{legacyHTTPClients: clients})
		},
	},
	{
		key: SourceKuaiShou, displayName: "快手",
		hostRules: []coreparser.HostRule{
			{Host: "v.kuaishou.com", IncludeSubdomains: true},
			{Host: "www.kuaishou.com", IncludeSubdomains: true},
		},
		capabilities: coreparser.CapabilityVideo | coreparser.CapabilityGallery,
		queryKeys:    []string{"id", "photoid"}, maxRequests: 4, maxRedirects: 3,
		bind: func(clients legacyHTTPClients) legacyParserBinding {
			return bindShare(kuaiShou{legacyHTTPClients: clients})
		},
	},
	{
		key: SourceZuiYou, displayName: "最右",
		hostRules:    []coreparser.HostRule{{Host: "share.xiaochuankeji.cn", IncludeSubdomains: true}},
		capabilities: coreparser.CapabilityVideo,
		queryKeys:    []string{"pid"}, maxRequests: 4, maxRedirects: 3,
		bind: func(clients legacyHTTPClients) legacyParserBinding {
			return bindShare(zuiYou{legacyHTTPClients: clients})
		},
	},
	{
		key: SourceXiGua, displayName: "西瓜视频", aliases: []coreparser.PlatformKey{"ixigua"},
		hostRules: []coreparser.HostRule{
			{Host: "v.ixigua.com", IncludeSubdomains: true},
			{Host: "www.ixigua.com", IncludeSubdomains: true},
			{Host: "m.ixigua.com", IncludeSubdomains: true},
		},
		capabilities: coreparser.CapabilityVideo,
		queryKeys:    []string{"id"}, maxRequests: 4, maxRedirects: 3,
		bind: func(clients legacyHTTPClients) legacyParserBinding {
			return bindShareAndID(xiGua{legacyHTTPClients: clients})
		},
	},
	{
		key: SourcePiPiXia, displayName: "皮皮虾",
		hostRules:    []coreparser.HostRule{{Host: "h5.pipix.com", IncludeSubdomains: true}},
		capabilities: coreparser.CapabilityVideo | coreparser.CapabilityGallery,
		queryKeys:    []string{"id"}, maxRequests: 4, maxRedirects: 3,
		bind: func(clients legacyHTTPClients) legacyParserBinding {
			return bindShareAndID(piPiXia{legacyHTTPClients: clients})
		},
	},
	{
		key: SourceWeiShi, displayName: "微视",
		hostRules:    []coreparser.HostRule{{Host: "weishi.qq.com", IncludeSubdomains: true}},
		capabilities: coreparser.CapabilityVideo,
		queryKeys:    []string{"id"}, maxRequests: 4, maxRedirects: 3,
		bind: func(clients legacyHTTPClients) legacyParserBinding {
			return bindShareAndID(weiShi{legacyHTTPClients: clients})
		},
	},
	{
		key: SourceHuoShan, displayName: "火山",
		hostRules:    []coreparser.HostRule{{Host: "share.huoshan.com", IncludeSubdomains: true}},
		capabilities: coreparser.CapabilityVideo,
		queryKeys:    []string{"item_id"}, maxRequests: 4, maxRedirects: 3,
		bind: func(clients legacyHTTPClients) legacyParserBinding {
			return bindShareAndID(huoShan{legacyHTTPClients: clients})
		},
	},
	{
		key: SourceLiShiPin, displayName: "梨视频",
		hostRules:    []coreparser.HostRule{{Host: "www.pearvideo.com", IncludeSubdomains: true}},
		capabilities: coreparser.CapabilityVideo,
		maxRequests:  4, maxRedirects: 3,
		bind: func(clients legacyHTTPClients) legacyParserBinding {
			return bindShareAndID(liShiPin{legacyHTTPClients: clients})
		},
	},
	{
		key: SourcePiPiGaoXiao, displayName: "皮皮搞笑",
		hostRules:    []coreparser.HostRule{{Host: "h5.pipigx.com", IncludeSubdomains: true}},
		capabilities: coreparser.CapabilityVideo,
		queryKeys:    []string{"pid"}, maxRequests: 4, maxRedirects: 3,
		bind: func(clients legacyHTTPClients) legacyParserBinding {
			return bindShareAndID(piPiGaoXiao{legacyHTTPClients: clients})
		},
	},
	{
		key: SourceQuanMin, displayName: "度小视",
		hostRules: []coreparser.HostRule{
			{Host: "xspshare.baidu.com", IncludeSubdomains: true},
			{Host: "quanmin.baidu.com", IncludeSubdomains: true},
			{Host: "quanmin.hao222.com", IncludeSubdomains: true},
		},
		capabilities: coreparser.CapabilityVideo,
		queryKeys:    []string{"vid"}, maxRequests: 4, maxRedirects: 3,
		bind: func(clients legacyHTTPClients) legacyParserBinding {
			return bindShareAndID(quanMin{legacyHTTPClients: clients})
		},
	},
	{
		key: SourceHuYa, displayName: "虎牙",
		hostRules: []coreparser.HostRule{
			{Host: "v.huya.com", IncludeSubdomains: true},
			{Host: "www.huya.com", IncludeSubdomains: true},
		},
		capabilities: coreparser.CapabilityVideo,
		queryKeys:    []string{"vid"}, maxRequests: 4, maxRedirects: 3,
		bind: func(clients legacyHTTPClients) legacyParserBinding {
			return bindShareAndID(huYa{legacyHTTPClients: clients})
		},
	},
	{
		key: SourceAcFun, displayName: "AcFun",
		hostRules:    []coreparser.HostRule{{Host: "www.acfun.cn", IncludeSubdomains: true}},
		capabilities: coreparser.CapabilityVideo,
		queryKeys:    []string{"ac"}, maxRequests: 4, maxRedirects: 3,
		bind: func(clients legacyHTTPClients) legacyParserBinding {
			return bindShareAndID(acFun{legacyHTTPClients: clients})
		},
	},
	{
		key: SourceWeiBo, displayName: "微博",
		hostRules:    []coreparser.HostRule{{Host: "weibo.com", IncludeSubdomains: true}},
		capabilities: coreparser.CapabilityVideo | coreparser.CapabilityGallery,
		queryKeys:    []string{"fid"}, maxRequests: 4, maxRedirects: 3,
		bind: func(clients legacyHTTPClients) legacyParserBinding {
			return bindShareAndID(weiBo{legacyHTTPClients: clients})
		},
	},
	{
		key: SourceLvZhou, displayName: "绿洲",
		hostRules:    []coreparser.HostRule{{Host: "weibo.cn", IncludeSubdomains: true}},
		capabilities: coreparser.CapabilityVideo,
		queryKeys:    []string{"id"}, maxRequests: 4, maxRedirects: 3,
		bind: func(clients legacyHTTPClients) legacyParserBinding {
			return bindShareAndID(lvZhou{legacyHTTPClients: clients})
		},
	},
	{
		key: SourceMeiPai, displayName: "美拍",
		hostRules:    []coreparser.HostRule{{Host: "meipai.com", IncludeSubdomains: true}},
		capabilities: coreparser.CapabilityVideo,
		queryKeys:    []string{"id"}, maxRequests: 4, maxRedirects: 3,
		bind: func(clients legacyHTTPClients) legacyParserBinding {
			return bindShareAndID(meiPai{legacyHTTPClients: clients})
		},
	},
	{
		key: SourceDouPai, displayName: "逗拍",
		hostRules:    []coreparser.HostRule{{Host: "doupai.cc", IncludeSubdomains: true}},
		capabilities: coreparser.CapabilityVideo,
		queryKeys:    []string{"id"}, maxRequests: 4, maxRedirects: 3,
		bind: func(clients legacyHTTPClients) legacyParserBinding {
			return bindShareAndID(douPai{legacyHTTPClients: clients})
		},
	},
	{
		key: SourceQuanMinKGe, displayName: "全民K歌", aliases: []coreparser.PlatformKey{"kgqq"},
		hostRules:    []coreparser.HostRule{{Host: "kg.qq.com", IncludeSubdomains: true}},
		capabilities: coreparser.CapabilityVideo,
		queryKeys:    []string{"s"}, maxRequests: 4, maxRedirects: 3,
		bind: func(clients legacyHTTPClients) legacyParserBinding {
			return bindShareAndID(quanMinKGe{legacyHTTPClients: clients})
		},
	},
	{
		key: SourceSixRoom, displayName: "六间房",
		hostRules:    []coreparser.HostRule{{Host: "6.cn", IncludeSubdomains: true}},
		capabilities: coreparser.CapabilityVideo,
		queryKeys:    []string{"vid"}, maxRequests: 4, maxRedirects: 3,
		bind: func(clients legacyHTTPClients) legacyParserBinding {
			return bindShareAndID(sixRoom{legacyHTTPClients: clients})
		},
	},
	{
		key: SourceXinPianChang, displayName: "新片场",
		hostRules:    []coreparser.HostRule{{Host: "xinpianchang.com", IncludeSubdomains: true}},
		capabilities: coreparser.CapabilityVideo,
		queryKeys:    []string{"id"}, maxRequests: 4, maxRedirects: 3,
		bind: func(clients legacyHTTPClients) legacyParserBinding {
			return bindShare(xinPianChang{legacyHTTPClients: clients})
		},
	},
	{
		key: SourceHaoKan, displayName: "好看视频",
		hostRules: []coreparser.HostRule{
			{Host: "haokan.baidu.com", IncludeSubdomains: true},
			{Host: "haokan.hao123.com", IncludeSubdomains: true},
		},
		capabilities: coreparser.CapabilityVideo,
		queryKeys:    []string{"vid"}, maxRequests: 4, maxRedirects: 3,
		bind: func(clients legacyHTTPClients) legacyParserBinding {
			return bindShareAndID(haoKan{legacyHTTPClients: clients})
		},
	},
	{
		key: SourceRedBook, displayName: "小红书", aliases: []coreparser.PlatformKey{"xiaohongshu"},
		hostRules: []coreparser.HostRule{
			{Host: "www.xiaohongshu.com", IncludeSubdomains: true},
			{Host: "xhslink.com", IncludeSubdomains: true},
		},
		capabilities: coreparser.CapabilityVideo | coreparser.CapabilityGallery | coreparser.CapabilityLivePhoto,
		queryKeys:    []string{"xsec_token", "xsec_source"}, maxRequests: 4, maxRedirects: 3,
		bind: func(clients legacyHTTPClients) legacyParserBinding {
			return bindShare(redBook{legacyHTTPClients: clients})
		},
	},
	{
		key: SourceBiliBili, displayName: "哔哩哔哩",
		hostRules: []coreparser.HostRule{
			{Host: "bilibili.com", IncludeSubdomains: true},
			{Host: "b23.tv", IncludeSubdomains: true},
		},
		capabilities: coreparser.CapabilityVideo,
		queryKeys:    []string{"bvid", "p"}, maxRequests: 4, maxRedirects: 3,
		bind: func(clients legacyHTTPClients) legacyParserBinding {
			return bindShare(biliBili{legacyHTTPClients: clients})
		},
	},
	{
		key: SourceTwitter, displayName: "X/Twitter",
		hostRules: []coreparser.HostRule{
			{Host: "x.com", IncludeSubdomains: true},
			{Host: "twitter.com", IncludeSubdomains: true},
			{Host: "t.co", IncludeSubdomains: true},
		},
		capabilities: coreparser.CapabilityVideo | coreparser.CapabilityGallery,
		queryKeys:    []string{"s", "t"}, maxRequests: 4, maxRedirects: 3,
		bind: func(clients legacyHTTPClients) legacyParserBinding {
			return bindShareAndID(twitter{legacyHTTPClients: clients})
		},
	},
	{
		key: SourceQQVideo, displayName: "腾讯视频",
		hostRules:    []coreparser.HostRule{{Host: "v.qq.com", IncludeSubdomains: true}},
		capabilities: coreparser.CapabilityVideo,
		queryKeys:    []string{"v", "vid"}, maxRequests: 4, maxRedirects: 3,
		bind: func(clients legacyHTTPClients) legacyParserBinding {
			return bindShareAndID(qqVideo{legacyHTTPClients: clients})
		},
	},
	{
		key: SourceSohu, displayName: "搜狐视频",
		hostRules: []coreparser.HostRule{
			{Host: "tv.sohu.com", IncludeSubdomains: true},
			{Host: "my.tv.sohu.com", IncludeSubdomains: true},
		},
		capabilities: coreparser.CapabilityVideo,
		queryKeys:    []string{"id", "vid"}, maxRequests: 4, maxRedirects: 3,
		sessionHost: SohuSessionHost,
		bind: func(clients legacyHTTPClients) legacyParserBinding {
			return bindShareAndID(sohuVideo{legacyHTTPClients: clients})
		},
	},
	{
		key: SourceCCTV, displayName: "央视网",
		hostRules: []coreparser.HostRule{
			{Host: "tv.cctv.cn", IncludeSubdomains: true},
			{Host: "tv.cctv.com", IncludeSubdomains: true},
		},
		capabilities: coreparser.CapabilityVideo,
		queryKeys:    []string{"pid"}, maxRequests: 4, maxRedirects: 3,
		bind: func(clients legacyHTTPClients) legacyParserBinding {
			return bindShareAndID(cctvVideo{legacyHTTPClients: clients})
		},
	},
}

func Descriptors() []coreparser.Descriptor {
	return descriptorsFromRegistrations(nativeRegistrations)
}

func descriptorsFromRegistrations(registrations []nativeRegistration) []coreparser.Descriptor {
	descriptors := make([]coreparser.Descriptor, 0, len(registrations))
	for priority, registration := range registrations {
		descriptor := coreparser.Descriptor{
			Key: coreparser.PlatformKey(registration.key), DisplayName: registration.displayName,
			Aliases:      append([]coreparser.PlatformKey(nil), registration.aliases...),
			HostRules:    append([]coreparser.HostRule(nil), registration.hostRules...),
			Capabilities: registration.capabilities, Priority: priority,
			QueryKeys:  append([]string(nil), registration.queryKeys...),
			SupportsID: registration.supportsID(), MaxRequests: registration.maxRequests,
			MaxRedirects: registration.maxRedirects, SessionHost: registration.sessionHost,
		}
		key := descriptor.Key
		capabilities := descriptor.Capabilities
		maxRequests := descriptor.MaxRequests
		maxRedirects := descriptor.MaxRedirects
		sessionHost := descriptor.SessionHost
		bind := registration.bind
		descriptor.New = func(dependencies coreparser.Dependencies) (coreparser.Parser, error) {
			if dependencies.Fetcher == nil {
				return nil, errors.New("native parser requires a guarded fetcher")
			}
			if bind == nil {
				return nil, errors.New("native parser binding is absent")
			}
			return &legacyAdapter{
				key: key, capabilities: capabilities, dependencies: dependencies,
				maxRequests: maxRequests, maxRedirects: maxRedirects,
				sessionHost: sessionHost, bind: bind,
			}, nil
		}
		descriptors = append(descriptors, descriptor)
	}
	return descriptors
}

type legacyAdapter struct {
	key          coreparser.PlatformKey
	capabilities coreparser.Capability
	dependencies coreparser.Dependencies
	maxRequests  int
	maxRedirects int
	sessionHost  string
	bind         func(legacyHTTPClients) legacyParserBinding
}

func (adapter *legacyAdapter) Parse(ctx context.Context, request coreparser.Request) (coreparser.Result, error) {
	if adapter == nil || ctx == nil {
		return coreparser.Result{}, errors.New("invalid native parser request")
	}
	if err := ctx.Err(); err != nil {
		return coreparser.Result{}, err
	}
	budget, err := coreparser.NewRequestBudget(coreparser.BudgetOptions{
		MaxRequests: adapter.maxRequests, MaxRedirects: adapter.maxRedirects,
		Duration: 20 * time.Second, Clock: adapter.dependencies.Clock,
	})
	if err != nil {
		return coreparser.Result{}, err
	}
	budgetContext, cancelBudget, err := budget.BindContext(ctx)
	if err != nil {
		return coreparser.Result{}, err
	}
	defer cancelBudget()
	sessionSecret, sessionMaterial, sessionKey, providerBacked, err := adapter.sessionSecret(budgetContext, request, budget)
	if err != nil {
		return coreparser.Result{}, sanitizeLegacyError(err)
	}
	clients := legacyHTTPClients{
		ctx: budgetContext, fetcher: adapter.dependencies.Fetcher, maxRedirects: adapter.maxRedirects,
		weiboCookie: strings.TrimSpace(adapter.dependencies.WeiboCookie),
		xiguaCookie: strings.TrimSpace(adapter.dependencies.XiguaCookie),
		sohuToken:   sessionSecret, budget: budget,
	}
	parsed, err := adapter.parseLegacy(clients, request)
	if providerBacked && err != nil {
		guard := &coreparser.SessionRefreshGuard{}
		material, refreshed, refreshErr := adapter.dependencies.Sessions.RefreshOnce(
			budgetContext, sessionKey, sessionMaterial, err, guard,
			func(loadContext context.Context) (coreparser.SensitiveMaterial, error) {
				return adapter.dependencies.SessionLoader(loadContext, sessionKey, budget)
			},
		)
		if refreshed {
			if refreshErr != nil {
				return coreparser.Result{}, sanitizeLegacyError(refreshErr)
			}
			clients.sohuToken = material
			parsed, err = adapter.parseLegacy(clients, request)
		}
	}
	if err != nil {
		return coreparser.Result{}, sanitizeLegacyError(err)
	}
	if parsed == nil {
		return coreparser.Result{}, coreparser.NewParseError(coreparser.ErrorSchemaChanged, errors.New("native parser returned an empty snapshot"))
	}
	result := legacyToResult(adapter.key, parsed)
	if err := result.ValidateAgainst(coreparser.Descriptor{Capabilities: adapter.capabilities}); err != nil {
		return coreparser.Result{}, coreparser.NewParseError(coreparser.ErrorSchemaChanged, err)
	}
	return result, nil
}

func (adapter *legacyAdapter) sessionSecret(
	ctx context.Context,
	request coreparser.Request,
	budget *coreparser.RequestBudget,
) (coreparser.Secret, coreparser.SensitiveMaterial, coreparser.SessionMaterialKey, bool, error) {
	if adapter.sessionHost == "" {
		return adapter.dependencies.SohuToken, coreparser.SensitiveMaterial{}, coreparser.SessionMaterialKey{}, false, nil
	}
	providerConfigured := adapter.dependencies.Sessions != nil
	loaderConfigured := adapter.dependencies.SessionLoader != nil
	if !providerConfigured && !loaderConfigured {
		// Direct Secret injection remains available only for focused hermetic
		// adapter tests. Production supplies both provider and typed loader.
		return adapter.dependencies.SohuToken, coreparser.SensitiveMaterial{}, coreparser.SessionMaterialKey{}, false, nil
	}
	if !providerConfigured || !loaderConfigured {
		return nil, coreparser.SensitiveMaterial{}, coreparser.SessionMaterialKey{}, false, coreparser.NewParseError(
			coreparser.ErrorCredentialRequired, errors.New("session material dependency is incomplete"),
		)
	}
	key, err := adapter.sessionKey()
	if err != nil {
		return nil, coreparser.SensitiveMaterial{}, coreparser.SessionMaterialKey{}, false, err
	}
	material, err := adapter.dependencies.Sessions.Get(ctx, key, func(loadContext context.Context) (coreparser.SensitiveMaterial, error) {
		return adapter.dependencies.SessionLoader(loadContext, key, budget)
	})
	if err != nil {
		return nil, coreparser.SensitiveMaterial{}, coreparser.SessionMaterialKey{}, false, err
	}
	return material, material, key, true, nil
}

func (adapter *legacyAdapter) sessionKey() (coreparser.SessionMaterialKey, error) {
	host := adapter.sessionHost
	if host == "" {
		return coreparser.SessionMaterialKey{}, coreparser.NewParseError(
			coreparser.ErrorCredentialRequired, errors.New("session material default scope is absent"),
		)
	}
	return coreparser.SessionMaterialKey{Platform: adapter.key, Host: host}, nil
}

func (adapter *legacyAdapter) parseLegacy(clients legacyHTTPClients, request coreparser.Request) (*VideoParseInfo, error) {
	if adapter.bind == nil {
		return nil, errors.New("native parser adapter is absent")
	}
	binding := adapter.bind(clients)
	if strings.TrimSpace(request.ID) != "" {
		if binding.id == nil {
			return nil, errors.New("native parser does not support IDs")
		}
		return binding.id.parseVideoID(request.ID)
	}
	if !request.URL.Valid() || binding.share == nil {
		return nil, errors.New("native parser URL is required")
	}
	rawURL := ""
	if err := request.URL.Use(func(value string) error {
		rawURL = value
		return nil
	}); err != nil {
		return nil, err
	}
	return binding.share.parseShareUrl(rawURL)
}

func sanitizeLegacyError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var typed *coreparser.ParseError
	if errors.As(err, &typed) {
		return coreparser.NewParseError(typed.Code, errors.New("native parser rejected the upstream snapshot"))
	}
	return coreparser.NewParseError(coreparser.ErrorUpstreamFailed, errors.New("native parser failed"))
}

func legacyToResult(platform coreparser.PlatformKey, info *VideoParseInfo) coreparser.Result {
	candidates := normalizeMediaCandidates(info.Candidates)
	candidates = appendUsableMediaCandidate(candidates, info.VideoUrl, coreparser.MediaKindVideo, candidateMetadata{})
	candidates = appendUsableMediaCandidate(candidates, info.MusicUrl, coreparser.MediaKindAudio, candidateMetadata{})
	coreparser.SortMediaCandidates(candidates)
	videoURL := normalizedExternalURLOrEmpty(info.VideoUrl)
	if videoURL == "" {
		videoURL = firstCandidateURL(candidates, coreparser.MediaKindVideo)
	}
	audioURL := normalizedExternalURLOrEmpty(info.MusicUrl)
	if audioURL == "" {
		audioURL = firstCandidateURL(candidates, coreparser.MediaKindAudio)
	}
	result := coreparser.Result{
		Platform: platform, Title: info.Title, VideoURL: videoURL,
		PreviewURL: info.PreviewUrl, AudioURL: audioURL, CoverURL: info.CoverUrl,
		Author: coreparser.Author{UID: info.Author.Uid, Name: info.Author.Name, Avatar: info.Author.Avatar},
		Images: make([]coreparser.ImageAsset, 0, len(info.Images)), Candidates: candidates,
	}
	for _, image := range info.Images {
		result.Images = append(result.Images, coreparser.ImageAsset{URL: image.Url, LivePhotoURL: image.LivePhotoUrl})
	}
	return result
}

func resultToLegacy(result coreparser.Result) *VideoParseInfo {
	candidates := normalizeMediaCandidates(result.Candidates)
	candidates = appendUsableMediaCandidate(candidates, result.VideoURL, coreparser.MediaKindVideo, candidateMetadata{})
	candidates = appendUsableMediaCandidate(candidates, result.AudioURL, coreparser.MediaKindAudio, candidateMetadata{})
	coreparser.SortMediaCandidates(candidates)
	videoURL := normalizedExternalURLOrEmpty(result.VideoURL)
	if videoURL == "" {
		videoURL = firstCandidateURL(candidates, coreparser.MediaKindVideo)
	}
	audioURL := normalizedExternalURLOrEmpty(result.AudioURL)
	if audioURL == "" {
		audioURL = firstCandidateURL(candidates, coreparser.MediaKindAudio)
	}
	info := &VideoParseInfo{
		Title: result.Title, VideoUrl: videoURL, PreviewUrl: result.PreviewURL,
		MusicUrl: audioURL, CoverUrl: result.CoverURL,
		Images: make([]ImgInfo, 0, len(result.Images)), Candidates: candidates,
	}
	info.Author.Uid = result.Author.UID
	info.Author.Name = result.Author.Name
	info.Author.Avatar = result.Author.Avatar
	for _, image := range result.Images {
		info.Images = append(info.Images, ImgInfo{Url: image.URL, LivePhotoUrl: image.LivePhotoURL})
	}
	return info
}
