package server

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

func normalizeDisplayText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || !looksLikeMojibake(value) {
		return value
	}
	recovered, ok := recoverUTF8DecodedAsGB18030(value)
	if !ok || recovered == "" || looksLikeMojibake(recovered) {
		return value
	}
	return recovered
}

func looksLikeMojibake(value string) bool {
	if strings.Count(value, "?") >= 3 || strings.ContainsRune(value, utf8.RuneError) {
		return true
	}
	suspicious := 0
	for _, r := range value {
		if strings.ContainsRune("锟斤拷鐟鐧鍚閮鎴缂顖婵鏉閺銉瑙瀵绾撶緱娆鍦皬搴瑗寰澶", r) {
			suspicious++
		}
	}
	return suspicious >= 2
}

func recoverUTF8DecodedAsGB18030(value string) (string, bool) {
	bytes, _, err := transform.Bytes(simplifiedchinese.GB18030.NewEncoder(), []byte(value))
	if err != nil || !utf8.Valid(bytes) {
		return "", false
	}
	recovered := strings.TrimSpace(string(bytes))
	if recovered == "" || hanRuneCount(recovered) == 0 {
		return "", false
	}
	return recovered, true
}

func hanRuneCount(value string) int {
	count := 0
	for _, r := range value {
		if unicode.Is(unicode.Han, r) {
			count++
		}
	}
	return count
}
