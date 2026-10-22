package server

import (
	"crypto/rand"
	"math/big"
)

const codeChars = "0123456789"

// newPairCode 生成 n 位数字配对码。
func newPairCode(n int) string {
	b := make([]byte, n)
	for i := range b {
		v, err := rand.Int(rand.Reader, big.NewInt(int64(len(codeChars))))
		if err != nil {
			b[i] = '0'
			continue
		}
		b[i] = codeChars[v.Int64()]
	}
	return string(b)
}

const tokenChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// newToken 生成长度 n 的随机 token。
func newToken(n int) string {
	b := make([]byte, n)
	for i := range b {
		v, err := rand.Int(rand.Reader, big.NewInt(int64(len(tokenChars))))
		if err != nil {
			b[i] = '0'
			continue
		}
		b[i] = tokenChars[v.Int64()]
	}
	return string(b)
}