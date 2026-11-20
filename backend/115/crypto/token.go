package crypto

import (
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"strconv"
	"strings"
)

const (
	md5Salt = "Qclm8MGWUv59TnrR0XPg"
)

// UploadSignature builds the initupload `sig` field.
func UploadSignature(userID int64, userKey, targetID, fileID string) string {
	sum := sha1.Sum([]byte(strconv.FormatInt(userID, 10) + fileID + targetID + "0"))
	h := hex.EncodeToString(sum[:])
	sum = sha1.Sum([]byte(userKey + h + "000000"))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// UploadToken builds the initupload v4 `token` field.
func UploadToken(userID int64, fileID, preID, timeStamp, fileSize, signKey, signVal, appVer string) string {
	userIDStr := strconv.FormatInt(userID, 10)
	userIDMd5 := md5.Sum([]byte(userIDStr))
	tokenMd5 := md5.Sum([]byte(md5Salt + fileID + fileSize + signKey + signVal + userIDStr + timeStamp + hex.EncodeToString(userIDMd5[:]) + appVer))
	return hex.EncodeToString(tokenMd5[:])
}
