package server

import (
	"context"
	"net/url"
	"strings"

	universalparser "github.com/1136623363/watermark-go/internal/parsers/universal"
	"github.com/1136623363/watermark-go/internal/runtimecfg"
)

func tryParseWithUniversalParser(rawURL string) (*parseResult, error) {
	cfg := runtimecfg.UniversalParser()
	bridge := universalparser.NewPythonBridge(universalparser.Config{
		PythonBin:         cfg.PythonBin,
		BridgeScript:      cfg.BridgeScript,
		VideoDLPath:       cfg.VideoDLPath,
		MusicDLPath:       cfg.MusicDLPath,
		WorkDir:           cfg.WorkDir,
		BridgeTimeout:     cfg.TimeoutSeconds,
		MusicDLTimeout:    cfg.MusicDLTimeoutSeconds,
		MusicDLItemLimit:  cfg.MusicDLItemLimit,
		MusicDLConfigJSON: cfg.MusicDLConfigJSON,
	})

	logInfof("universal parser started target=%s", targetForLog(rawURL))
	req := universalparser.ParseRequest{
		URL:             rawURL,
		Limit:           5,
		MusicItemLimit:  cfg.MusicDLItemLimit,
		MusicConfigJSON: cfg.MusicDLConfigJSON,
	}
	var data universalparser.ParseData
	var err error
	if looksLikeMusicURL(rawURL) {
		if cfg.MusicDLItemLimit > 0 {
			req.Limit = cfg.MusicDLItemLimit
		}
		data, err = bridge.ParseMusicPlaylist(context.Background(), req)
	} else {
		data, err = bridge.ParseVideo(context.Background(), req)
	}
	if err != nil {
		logErrorf("universal parser failed target=%s error=%v", targetForLog(rawURL), err)
		return nil, err
	}

	serverData := parseDataFromUniversal(data)
	source := firstNonEmpty(serverData.Platform, detectSource(rawURL), "universal")
	serverData.Platform = source
	serverData.SourceURL = rawURL
	logInfof("universal parser succeeded target=%s platform=%s type=%s", targetForLog(rawURL), source, serverData.Type)
	return &parseResult{
		source:       source,
		sourceURL:    rawURL,
		parserEngine: runtimecfg.ParserEngineUniversal,
		info:         toVideoParseInfo(serverData),
		data:         serverData,
	}, nil
}

func universalParserDiagnostics() map[string]any {
	cfg := runtimecfg.UniversalParser()
	bridge := universalparser.NewPythonBridge(universalparser.Config{
		PythonBin:         cfg.PythonBin,
		BridgeScript:      cfg.BridgeScript,
		VideoDLPath:       cfg.VideoDLPath,
		MusicDLPath:       cfg.MusicDLPath,
		WorkDir:           cfg.WorkDir,
		BridgeTimeout:     cfg.TimeoutSeconds,
		MusicDLTimeout:    cfg.MusicDLTimeoutSeconds,
		MusicDLItemLimit:  cfg.MusicDLItemLimit,
		MusicDLConfigJSON: cfg.MusicDLConfigJSON,
	})
	return bridge.Health(context.Background())
}

func parseDataFromUniversal(data universalparser.ParseData) parseData {
	downloads := make([]downloadItem, 0, len(data.Downloads))
	for _, item := range data.Downloads {
		if strings.TrimSpace(item.URL) == "" {
			continue
		}
		downloads = append(downloads, downloadItem{
			URL:   strings.TrimSpace(item.URL),
			Label: strings.TrimSpace(item.Label),
		})
	}

	return normalizeParseDataMediaAliases(parseData{
		Platform:  strings.TrimSpace(data.Platform),
		Type:      firstNonEmpty(data.Type, "video"),
		Title:     strings.TrimSpace(data.Title),
		Desc:      strings.TrimSpace(data.Desc),
		Cover:     strings.TrimSpace(data.Cover),
		Author:    strings.TrimSpace(data.Author),
		Avatar:    strings.TrimSpace(data.Avatar),
		Music:     strings.TrimSpace(data.Music),
		Duration:  data.Duration,
		Downloads: downloads,
		Images:    cleanStringList(data.Images),
		Pics:      cleanStringList(data.Pics),
		M3U8:      strings.TrimSpace(data.M3U8),
		Preview:   firstNonEmpty(data.Preview, data.PlayAddr, data.M3U8),
		PlayAddr:  firstNonEmpty(data.PlayAddr, data.M3U8),
		ShareID:   strings.TrimSpace(data.ShareID),
		SourceURL: strings.TrimSpace(data.SourceURL),
	})
}

func looksLikeMusicURL(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	path := strings.ToLower(parsed.Path)
	switch host {
	case "5sing.kugou.com",
		"h5app.kuwo.cn",
		"music.apple.com",
		"music.migu.cn",
		"music.163.com",
		"music.91q.com",
		"open.qobuz.com",
		"open.spotify.com",
		"qishui.douyin.com",
		"soundcloud.com",
		"tidal.com",
		"www.deezer.com",
		"www.jamendo.com",
		"www.jiosaavn.com",
		"www.joox.com",
		"www.kugou.com",
		"www.kuwo.cn",
		"www.qobuz.com",
		"www.streetvoice.cn",
		"freemusicarchive.org":
		return true
	case "www.bilibili.com":
		return strings.HasPrefix(path, "/audio")
	default:
		return false
	}
}

func cleanStringList(input []string) []string {
	out := make([]string, 0, len(input))
	for _, item := range input {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}
