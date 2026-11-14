// Package discovery 实现电脑端的 UDP 局域网发现服务（v1.3.0，参考 LocalSend 模式）。
//
// 与 v1.2.x 的「被动应答」不同，v1.3.0 的电脑端会**主动定时广播自己的存在**：
//
//   - 每 4s 向「子网定向广播 + 255.255.255.255 + 组播组 239.255.42.99」发一条 announce，
//     手机一打开 App 就能在「附近的电脑」里看到它，不用先知道 IP。
//   - 同时保留 v1.2.x 的探测包（"LIMELINK-DISCOVER"）应答，旧版手机仍可用。
//   - 收到手机的 announce 会回调 OnPeer，电脑端界面因此能显示「手机在线」。
//
// 广播 + 组播双发是因为：部分路由器/热点会拦掉其中一种，双保险比单选更稳。
// 仅用标准库，无第三方依赖。
package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"syscall"
	"time"
)

// DefaultPort 是发现服务监听的 UDP 端口（手机端广播/组播目标）。
const DefaultPort = 8788

// ProbeMagic 是 v1.2.x 手机端的探测包内容（兼容保留）。
const ProbeMagic = "LIMELINK-DISCOVER"

// Magic 是 v1.3.0 announce / probe 报文的 magic 字段。
const Magic = "LIMELINK3"

// MulticastGroup 是 v1.3.0 使用的组播地址（LocalSend 用的是 224.0.0.167，这里自带一份）。
const MulticastGroup = "239.255.42.99"

// 报文类型。
const (
	TypeAnnounce = "announce" // 我在线，这是我的信息
	TypeProbe    = "probe"    // 有人吗，报一下你的信息
)

// Packet 是 v1.3.0 的发现报文。
type Packet struct {
	Magic   string `json:"m"`   // 固定 Magic
	Type    string `json:"t"`   // announce / probe
	ID      string `json:"id"`  // 发送方设备 id
	Name    string `json:"name"`// 发送方设备名
	Role    string `json:"role"`// pc / phone
	Port    int    `json:"port"`// 发送方 HTTP 端口
	Version string `json:"ver"` // 版本号
}

// LegacyReply 是 v1.2.x 手机端期待的应答格式。
type LegacyReply struct {
	Magic   string `json:"magic"`
	Name    string `json:"name"`
	Port    int    `json:"port"`
	Version string `json:"version"`
}

// InfoProvider 提供应答所需的信息（由 main 注入，避免循环依赖）。
type InfoProvider interface {
	// Get 返回 (本机设备名, HTTP 端口, 版本号)。
	Get() (name string, port int, version string)
	// OnPeer 在收到手机 announce 时调用，让服务端记录「该手机在线」。
	OnPeer(id, name, ip string)
}

// Service 是 UDP 发现服务。
type Service struct {
	provider InfoProvider
	port     int

	mu     sync.Mutex
	conn   *net.UDPConn // 主 socket：收广播/单播，发广播/组播
	mconn  *net.UDPConn // 组播监听 socket
	closed bool
	done   chan struct{}
}

// New 创建一个发现服务，监听指定 UDP 端口。
func New(provider InfoProvider, port int) *Service {
	return &Service{provider: provider, port: port, done: make(chan struct{})}
}

// Start 启动监听与定时广播（阻塞直到 Stop 被调用后返回，便于外部用 goroutine 包）。
func (s *Service) Start() error {
	lc := net.ListenConfig{Control: func(network, address string, c syscall.RawConn) error {
		return c.Control(func(fd uintptr) {
			// SO_REUSEADDR：允许随后的组播 socket 绑定同一个端口（Windows 需要）
			_ = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
		})
	}}
	pc, err := lc.ListenPacket(context.Background(), "udp4", fmt.Sprintf("0.0.0.0:%d", s.port))
	if err != nil {
		return err
	}
	conn, ok := pc.(*net.UDPConn)
	if !ok {
		_ = pc.Close()
		return fmt.Errorf("UDP 监听类型异常")
	}
	_ = conn.SetReadBuffer(256 * 1024)
	enableBroadcast(conn)

	done := make(chan struct{})
	s.mu.Lock()
	s.conn = conn
	s.closed = false
	s.done = done
	s.mu.Unlock()

	// 组播监听（失败不影响广播模式）
	if mc, err := net.ListenMulticastUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP(MulticastGroup), Port: s.port}); err == nil {
		_ = mc.SetReadBuffer(256 * 1024)
		s.mu.Lock()
		s.mconn = mc
		s.mu.Unlock()
		go s.readLoop(mc, conn, done)
	} else {
		log.Printf("[discovery] 组播监听失败（降级为纯广播）: %v\n", err)
	}

	go s.readLoop(conn, conn, done)
	go s.announceLoop(conn, done)

	log.Printf("[discovery] UDP 发现服务已启动，端口 %d（组播 %s + 广播）\n", s.port, MulticastGroup)
	<-done
	return nil
}

// Stop 关闭发现服务。
func (s *Service) Stop() {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		if s.conn != nil {
			_ = s.conn.Close()
		}
		if s.mconn != nil {
			_ = s.mconn.Close()
		}
		close(s.done)
	}
	s.mu.Unlock()
}

// announceLoop 每 4s 广播一次「我在线」，并在启动时立刻广播一次。
func (s *Service) announceLoop(conn *net.UDPConn, done <-chan struct{}) {
	s.sendAnnounce(conn)
	ticker := time.NewTicker(4 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			s.sendAnnounce(conn)
		}
	}
}

func (s *Service) packet(t string) []byte {
	name, port, version := s.provider.Get()
	p := Packet{Magic: Magic, Type: t, ID: localID(), Name: name, Role: "pc", Port: port, Version: version}
	b, err := json.Marshal(p)
	if err != nil {
		return nil
	}
	return b
}

// localID 是电脑端稳定的设备 id（按主机名+MAC 派生，仅用于发现阶段去重）。
var idOnce sync.Once
var idValue string

func localID() string {
	idOnce.Do(func() {
		idValue = "pc-" + shortHash(machineSeed())
	})
	return idValue
}

func machineSeed() string {
	host, _ := os.Hostname()
	seed := host
	if ifaces, err := net.Interfaces(); err == nil {
		for _, ifc := range ifaces {
			if ifc.Flags&net.FlagLoopback != 0 || len(ifc.HardwareAddr) < 4 {
				continue
			}
			seed += ifc.HardwareAddr.String()
			break
		}
	}
	if seed == "" {
		seed = "limelink"
	}
	return seed
}

func shortHash(s string) string {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	const hexDigits = "0123456789abcdef"
	out := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		out[i] = hexDigits[h&0xf]
		h >>= 4
	}
	return string(out)
}

func (s *Service) sendAnnounce(conn *net.UDPConn) {
	b := s.packet(TypeAnnounce)
	if b == nil {
		return
	}
	for _, addr := range broadcastTargets(s.port) {
		// 广播失败很常见（网卡状态/权限），不刷屏
		_, _ = conn.WriteToUDP(b, addr)
	}
}

// broadcastTargets 返回所有广播目标：255.255.255.255 + 各组播 + 各网卡的子网定向广播地址。
func broadcastTargets(port int) []*net.UDPAddr {
	out := []*net.UDPAddr{{IP: net.IPv4bcast, Port: port}}
	if g := net.ParseIP(MulticastGroup); g != nil {
		out = append(out, &net.UDPAddr{IP: g, Port: port})
	}
	seen := map[string]bool{"255.255.255.255": true, MulticastGroup: true}
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
				continue
			}
			mask := ipnet.Mask
			if len(mask) == 16 {
				mask = mask[12:]
			}
			if len(mask) != 4 {
				continue
			}
			bc := make(net.IP, 4)
			for i := 0; i < 4; i++ {
				bc[i] = ip4[i] | ^mask[i]
			}
			key := bc.String()
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, &net.UDPAddr{IP: bc, Port: port})
		}
	}
	return out
}

func (s *Service) readLoop(conn *net.UDPConn, reply *net.UDPConn, done <-chan struct{}) {
	buf := make([]byte, 2048)
	for {
		select {
		case <-done:
			return
		default:
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, remote, err := conn.ReadFromUDP(buf)
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return
			}
			continue
		}
		s.handle(buf[:n], remote, reply)
	}
}

func (s *Service) handle(msg []byte, remote *net.UDPAddr, replyConn *net.UDPConn) {
	text := string(msg)

	// v1.2.x 兼容：裸探测包字符串
	if text == ProbeMagic {
		name, port, version := s.provider.Get()
		b, _ := json.Marshal(LegacyReply{Magic: ProbeMagic, Name: name, Port: port, Version: version})
		_, _ = replyConn.WriteToUDP(b, remote)
		return
	}

	var p Packet
	if err := json.Unmarshal(msg, &p); err != nil || p.Magic != Magic {
		return
	}
	switch p.Type {
	case TypeProbe:
		// 有人找电脑端：单播应答自己的信息
		name, port, version := s.provider.Get()
		rep, _ := json.Marshal(Packet{
			Magic: Magic, Type: TypeAnnounce, ID: localID(),
			Name: name, Role: "pc", Port: port, Version: version,
		})
		_, _ = replyConn.WriteToUDP(rep, remote)
	case TypeAnnounce:
		// 手机在广播「我在线」
		if p.Role == "phone" && p.ID != "" {
			s.provider.OnPeer(p.ID, p.Name, remote.IP.String())
		}
	}
}

// enableBroadcast 打开 SO_BROADCAST（Go 默认不设，发 255.255.255.255 会被拒）。
func enableBroadcast(conn *net.UDPConn) {
	rc, err := conn.SyscallConn()
	if err != nil {
		return
	}
	_ = rc.Control(func(fd uintptr) {
		_ = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
	})
}
