package worker

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

func SignPayload(payload []byte, secret []byte) string {

	mac := hmac.New(sha256.New, secret)

	mac.Write(payload)

	signature := mac.Sum(nil)

	return hex.EncodeToString(signature)
}
