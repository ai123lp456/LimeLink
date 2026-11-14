package main

import (
	"crypto/rand"
	"math/big"
	"strings"
)

// newPairCode6 生成 6 位数字配对码。
func newPairCode6() string {
	const chars = "0123456789"
	b := make([]byte, 6)
	for i := range b {
		v, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		if err != nil {
			b[i] = '0'
			continue
		}
		b[i] = chars[v.Int64()]
	}
	return string(b)
}

// trimSpace 去掉首尾空白（包一层，避免各处重复 import strings）。
func trimSpace(s string) string {
	return strings.TrimSpace(s)
}
