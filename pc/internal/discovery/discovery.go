// Package discovery 实现电脑端的 UDP 广播发现服务。
//
// 手机端在断线或首次连接时，向局域网广播一个探测包，电脑端收到后
// 以 UDP 单播回一个应答，携带本机 HTTP 端口、设备名与版本号。
// 这样手机端无需假设网段（热点 192.168.137.x 或常见 192.168.1.x 都能覆盖），
// 电脑端 IP 变化后手机端也能自动重新发现。
//
// 轻量实现：仅用标准库 net，无第三方依赖。
package discovery

import (
	"encoding/json"
	"log"
	"net"
	"sync"
	"time"
)

// DefaultPort 是发现服务监听的 UDP 端口（手机端广播目标）。
const DefaultPort = 8788

// ProbeMagic 是手机端广播包与电脑端应答中约定的魔数前缀。
const ProbeMagic = "LIMELINK-DISCOVER"

// Reply 是电脑端对发现请求的应答。
type Reply struct {
	Magic   string `json:"magic"`   // 固定为 ProbeMagic
	Name    string `json:"name"`    // 电脑端设备名
	Port    int    `json:"port"`    // HTTP 服务端口
	Version string `json:"version"` // 电脑端版本号
}

// InfoProvider 提供应答所需的信息（由 main 注入，避免循环依赖）。
type InfoProvider interface {
	// Get 返回 (name, httpPort, version)。
	Get() (name string, port int, version string)
}

// Service 是 UDP 发现服务。
type Service struct {
	provider InfoProvider
	port     int

	mu     sync.Mutex
	conn   *net.UDPConn
	closed bool
	done   chan struct{}
}

// New 创建一个发现服务，监听指定 UDP 端口。
func New(provider InfoProvider, port int) *Service {
	return &Service{provider: provider, port: port, done: make(chan struct{})}
}

// Start 启动 UDP 监听（阻塞直到 Stop）。可反复调用（假死后由外部拉起）。
func (s *Service) Start() error {
	addr := &net.UDPAddr{IP: net.IPv4zero, Port: s.port}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.conn = conn
	s.closed = false
	s.done = make(chan struct{})
	s.mu.Unlock()

	log.Printf("[discovery] UDP 发现服务已启动，端口 %d\n", s.port)
	s.serveLoop(conn)
	return nil
}

// Stop 关闭发现服务。
func (s *Service) Stop() {
	s.mu.Lock()
	conn := s.conn
	done := s.done
	if !s.closed {
		s.closed = true
		if conn != nil {
			_ = conn.Close()
		}
		close(done)
	}
	s.mu.Unlock()
}

func (s *Service) serveLoop(conn *net.UDPConn) {
	buf := make([]byte, 512)
	for {
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, remote, err := conn.ReadFromUDP(buf)
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue // 读超时，继续等
			}
			log.Printf("[discovery] 读取错误: %v\n", err)
			continue
		}
		msg := string(buf[:n])
		if msg != ProbeMagic {
			continue
		}
		s.reply(conn, remote)
	}
}

// reply 向来源地址回一个应答。
func (s *Service) reply(conn *net.UDPConn, remote *net.UDPAddr) {
	name, port, version := s.provider.Get()
	rep := Reply{Magic: ProbeMagic, Name: name, Port: port, Version: version}
	b, err := json.Marshal(rep)
	if err != nil {
		return
	}
	if _, err := conn.WriteToUDP(b, remote); err != nil {
		log.Printf("[discovery] 应答失败: %v\n", err)
	}
}