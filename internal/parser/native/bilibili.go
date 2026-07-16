package native

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/1136623363/watermark-go/internal/netguard"
	coreparser "github.com/1136623363/watermark-go/internal/parser"
)

const UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/108.0.0.0 Safari/537.36"

type biliBili struct{ legacyHTTPClients }

func (b biliBili) parseShareUrl(shareUrl string) (*VideoParseInfo, error) {
	bvid, err := b.getBvidFromURL(shareUrl)
	if err != nil {
		return nil, fmt.Errorf("无法提取BVID: %w", err)
	}
	viewAPIURL := fmt.Sprintf("https://api.bilibili.com/x/web-interface/view?bvid=%s", bvid)
	viewRespBytes, err := b.sendBiliRequest(viewAPIURL)
	if err != nil {
		return nil, fmt.Errorf("请求视频信息API失败: %w", err)
	}
	viewResp, err := decodeBiliViewSnapshot(viewRespBytes)
	if err != nil {
		return nil, err
	}
	firstPageCID := viewResp.Data.Pages[0].Cid

	playAPIURL := fmt.Sprintf(
		"https://api.bilibili.com/x/player/playurl?otype=json&fnver=0&fnval=0&qn=80&bvid=%s&cid=%d&platform=html5",
		bvid, firstPageCID,
	)

	playRespBytes, err := b.sendBiliRequest(playAPIURL)
	if err != nil {
		return nil, fmt.Errorf("请求播放链接API失败: %w", err)
	}

	playResp, err := decodeBiliPlaySnapshot(playRespBytes)
	if err != nil {
		return nil, err
	}

	videoInfo := videoInfoFromBiliSnapshots(viewResp, playResp)
	if videoInfo.VideoUrl != "" {
		return videoInfo, nil
	}

	return nil, fmt.Errorf("无法获取该视频")
}

func videoInfoFromBiliSnapshots(viewResp biliViewResponse, playResp biliPlayURLResponse) *VideoParseInfo {
	videoInfo := &VideoParseInfo{
		Title: viewResp.Data.Title, CoverUrl: viewResp.Data.Pic,
		Images: make([]ImgInfo, 0),
	}
	videoInfo.Author.Uid = fmt.Sprintf("%d", viewResp.Data.Owner.Mid)
	videoInfo.Author.Name = viewResp.Data.Owner.Name
	videoInfo.Author.Avatar = viewResp.Data.Owner.Face

	// Durl entries are ordered media segments, not quality alternatives. The
	// compatibility API historically returned the first segment, so preserve
	// that projection without presenting later segments as fallback copies.
	// Only a single durl entry and its mirrors form independently usable
	// candidates under the current MediaCandidate model.
	if len(playResp.Data.Durl) > 0 {
		videoInfo.VideoUrl = normalizedExternalURLOrEmpty(playResp.Data.Durl[0].URL)
	}
	if len(playResp.Data.Durl) == 1 {
		segment := playResp.Data.Durl[0]
		candidates := make([]coreparser.MediaCandidate, 0, 1+len(segment.BackupURL))
		candidates = appendUsableMediaCandidate(candidates, segment.URL, coreparser.MediaKindVideo, candidateMetadata{})
		for _, backup := range segment.BackupURL {
			candidates = appendUsableMediaCandidate(candidates, backup, coreparser.MediaKindVideo, candidateMetadata{})
		}
		applyMediaCandidates(videoInfo, candidates)
	}
	// DASH video and audio are separate tracks. Until the media-task layer can
	// model and mux an explicit pair, exposing a video-only DASH URL as the
	// legacy VideoUrl (or as a HEAD fallback) would be a functional regression.
	return videoInfo
}

func decodeBiliViewSnapshot(document []byte) (biliViewResponse, error) {
	var response biliViewResponse
	if err := decodeBiliJSON(document, &response); err != nil {
		return biliViewResponse{}, err
	}
	if err := classifyBiliResponseCode(response.Code); err != nil {
		return biliViewResponse{}, err
	}
	if strings.TrimSpace(response.Data.Bvid) == "" || len(response.Data.Pages) == 0 || response.Data.Pages[0].Cid <= 0 {
		return biliViewResponse{}, coreparser.NewParseError(coreparser.ErrorSchemaChanged, errors.New("Bilibili view snapshot has no core fields"))
	}
	return response, nil
}

func decodeBiliPlaySnapshot(document []byte) (biliPlayURLResponse, error) {
	var response biliPlayURLResponse
	if err := decodeBiliJSON(document, &response); err != nil {
		return biliPlayURLResponse{}, err
	}
	if err := classifyBiliResponseCode(response.Code); err != nil {
		return biliPlayURLResponse{}, err
	}
	if len(response.Data.Durl) == 0 && len(response.Data.Dash.Video) == 0 && len(response.Data.Dash.Audio) == 0 {
		return biliPlayURLResponse{}, coreparser.NewParseError(
			coreparser.ErrorSchemaChanged,
			errors.New("Bilibili play snapshot has no core media fields"),
		)
	}
	return response, nil
}

func decodeBiliJSON(document []byte, destination any) error {
	payload := document
	var quoted string
	if err := json.Unmarshal(document, &quoted); err == nil {
		payload = []byte(quoted)
	}
	if err := json.Unmarshal(payload, destination); err != nil {
		return coreparser.NewParseError(coreparser.ErrorSchemaChanged, errors.New("invalid Bilibili structured snapshot"))
	}
	return nil
}

func classifyBiliResponseCode(code int) error {
	switch code {
	case 0:
		return nil
	case -101, -111:
		return coreparser.NewParseError(coreparser.ErrorCredentialRequired, errors.New("Bilibili credential is required"))
	case -352, -412:
		return coreparser.NewParseError(coreparser.ErrorSecurityRejected, errors.New("Bilibili risk control rejected the request"))
	default:
		return coreparser.NewParseError(coreparser.ErrorUpstreamFailed, errors.New("Bilibili upstream rejected the request"))
	}
}

type biliViewResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Bvid  string `json:"bvid"`
		Title string `json:"title"`
		Pic   string `json:"pic"` // 封面图
		Owner struct {
			Mid  int64  `json:"mid"` // 作者UID
			Name string `json:"name"`
			Face string `json:"face"` // 作者头像
		} `json:"owner"`
		Pages []struct {
			Cid int `json:"cid"`
		} `json:"pages"`
	} `json:"data"`
}

type biliPlayURLResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Durl []struct {
			URL       string   `json:"url"`
			BackupURL []string `json:"backup_url"`
		} `json:"durl"`

		Dash struct {
			Video []struct {
				ID             int      `json:"id"`
				BaseURL        string   `json:"baseUrl"`
				BaseURLSnake   string   `json:"base_url"`
				BackupURL      []string `json:"backupUrl"`
				BackupURLSnake []string `json:"backup_url"`
				Bandwidth      int      `json:"bandwidth"`
				Width          int      `json:"width"`
				Height         int      `json:"height"`
			} `json:"video"`
			Audio []struct {
				BaseURL   string `json:"baseUrl"`
				Bandwidth int    `json:"bandwidth"`
			} `json:"audio"`
		} `json:"dash"`
	} `json:"data"`
}

func (b biliBili) getBvidFromURL(rawURL string) (string, error) {
	target, err := netguard.NewFetchURL(rawURL)
	if err != nil {
		return "", fmt.Errorf("URL格式无效")
	}
	canonicalURL := ""
	if err := target.Use(func(value string) error {
		canonicalURL = value
		return nil
	}); err != nil {
		return "", fmt.Errorf("URL格式无效")
	}
	parsedURL, err := url.Parse(canonicalURL)
	if err != nil {
		return "", fmt.Errorf("URL格式无效")
	}
	host := strings.ToLower(strings.TrimSuffix(parsedURL.Hostname(), "."))

	if controlledHostname(host, "b23.tv") {
		client := b.newHTTPClientWithCheckRedirect(func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		})
		resp, err := client.Get(canonicalURL)
		if err != nil {
			return "", fmt.Errorf("请求b23.tv短链失败: %v", err)
		}
		defer func(Body io.ReadCloser) {
			_ = Body.Close()
		}(resp.Body)

		location, err := resp.Location()
		if err != nil || location == nil {
			return "", fmt.Errorf("无法从b23.tv获取重定向链接")
		}
		return b.getBvidFromURL(location.String())
	}

	if controlledHostname(host, "bilibili.com") {
		path := strings.Trim(parsedURL.Path, "/")
		parts := strings.Split(path, "/")
		if len(parts) >= 2 && parts[0] == "video" {
			if strings.HasPrefix(parts[1], "BV") {
				return parts[1], nil
			}
		}
	}

	return "", fmt.Errorf("不是有效的B站视频链接")
}

func controlledHostname(host, root string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	root = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(root), "."))
	return host == root || strings.HasSuffix(host, "."+root)
}

func (b biliBili) sendBiliRequest(apiURL string) ([]byte, error) {
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Referer", "https://www.bilibili.com/")

	client := b.newHTTPClient()
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP请求失败, 状态码: %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}
