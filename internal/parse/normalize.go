package parse

import "strings"

func Normalize(result Result) CompatData {
	result.Platform = strings.TrimSpace(result.Platform)
	result.Type = inferType(result)
	data := CompatData{
		Platform:  result.Platform,
		Type:      result.Type,
		Title:     strings.TrimSpace(result.Title),
		Desc:      strings.TrimSpace(firstNonEmpty(result.Description, result.Title)),
		Cover:     strings.TrimSpace(result.CoverURL),
		Author:    strings.TrimSpace(result.Author.Name),
		Avatar:    strings.TrimSpace(result.Author.Avatar),
		Music:     strings.TrimSpace(result.AudioURL),
		MP3:       strings.TrimSpace(result.AudioURL),
		Audio:     strings.TrimSpace(result.AudioURL),
		AudioURL:  strings.TrimSpace(result.AudioURL),
		Duration:  result.Duration,
		Downloads: []DownloadItem{},
		Images:    []string{},
		Pics:      []string{},
		M3U8:      strings.TrimSpace(result.M3U8URL),
		PreviewURL: strings.TrimSpace(firstNonEmpty(
			result.PreviewURL,
			result.VideoURL,
			result.M3U8URL,
		)),
		PlayAddr: strings.TrimSpace(firstNonEmpty(result.VideoURL, result.M3U8URL)),
	}
	if data.M3U8 != "" && data.PreviewURL == "" {
		data.PreviewURL = data.M3U8
	}
	if data.M3U8 != "" && data.PlayAddr == "" {
		data.PlayAddr = data.M3U8
	}
	if video := strings.TrimSpace(result.VideoURL); video != "" {
		data.Downloads = append(data.Downloads, DownloadItem{URL: video, Label: "video"})
	}
	if m3u8 := strings.TrimSpace(result.M3U8URL); m3u8 != "" {
		data.Downloads = append(data.Downloads, DownloadItem{URL: m3u8, Label: "m3u8"})
	}
	if audio := strings.TrimSpace(result.AudioURL); audio != "" && result.VideoURL == "" {
		data.Downloads = append(data.Downloads, DownloadItem{URL: audio, Label: "audio"})
	}
	hasLivePhoto := false
	for _, image := range result.Images {
		image.URL = strings.TrimSpace(image.URL)
		image.LivePhotoURL = strings.TrimSpace(image.LivePhotoURL)
		if image.URL == "" {
			continue
		}
		data.Images = append(data.Images, image.URL)
		data.Pics = append(data.Pics, image.URL)
		if image.LivePhotoURL != "" {
			hasLivePhoto = true
		}
		data.ImageAssets = append(data.ImageAssets, image)
	}
	if !hasLivePhoto {
		data.ImageAssets = nil
	}
	return data
}

func inferType(result Result) string {
	if result.Type = strings.TrimSpace(result.Type); result.Type != "" {
		return result.Type
	}
	switch {
	case strings.TrimSpace(result.M3U8URL) != "":
		return "m3u8"
	case len(result.Images) > 0:
		return "gallery"
	case strings.TrimSpace(result.VideoURL) != "":
		return "video"
	case strings.TrimSpace(result.AudioURL) != "":
		return "audio"
	default:
		return "unknown"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
