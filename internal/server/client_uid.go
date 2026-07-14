package server

import (
	"strconv"
	"strings"
)

const clientVisibleUIDOffset int64 = 30000000

func clientVisibleUID(userID int64) string {
	if userID <= 0 {
		return ""
	}
	uid := clientVisibleUIDOffset + userID
	if uid > 99999999 {
		return strconv.FormatInt(userID, 10)
	}
	return strconv.FormatInt(uid, 10)
}

func clientVisibleUIDOrPublicID(userID int64, publicID string) string {
	if uid := clientVisibleUID(userID); uid != "" {
		return uid
	}
	if publicID = strings.TrimSpace(publicID); publicID != "" {
		return publicID
	}
	return ""
}
