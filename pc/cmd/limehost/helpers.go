package main

import (
	"crypto/rand"
	"math/big"
	"net"
	"strings"

	"github.com/gen2brain/dlgs"
	"limelink/pc/internal/server"
)

// netListener / netListen 抽象一次，便于未来替换（如优雅关闭）。
type netListener interface {
	Accept() (net.Conn, error)
	Close() error
	Addr() net.Addr
}

func netListen(network, addr string) (netListener, error) {
	ln, err := net.Listen(network, addr)
	if err != nil {
		return nil, err
	}
	return ln, nil
}

// newPairCode6 生成 6 位数字配对码。
func newPairCode6() string {
	const chars = "0123456789"
	b := make([]byte, 6)
	for i := range b {
		v, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		b[i] = chars[v.Int64()]
	}
	return string(b)
}

// promptForName 弹 Windows 原生输入框（dlgs），返回输入值与是否确认。
func promptForName(prompt, title, def string) (string, bool, error) {
	v, ok, err := dlgs.Entry(title, prompt, def)
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(v), ok, nil
}

// 关联 server 包里的类型，避免未使用 import（编译期检查用）。
var _ = server.New