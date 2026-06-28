// Package data skill_base64.go — base64 helpers isolated so skill.go
// stays focused on DB logic.
package data

import "encoding/base64"

func base64StdEncodingEncode(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

func base64StdEncodingDecode(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil
	}
	return b
}
