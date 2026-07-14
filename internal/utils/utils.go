package utils

import (
	"fmt"
	"regexp"
	"strings"
)

var urlPattern = regexp.MustCompile(`https?://[^\s<>"']+`)

func RegexpMatchUrlFromString(str string) (string, error) {
	findStr := strings.TrimSpace(urlPattern.FindString(str))
	if len(findStr) <= 0 {
		return "", fmt.Errorf("str not have url")
	}

	return strings.TrimRight(findStr, "\"'，。！？!?）)]}>》」"), nil
}
