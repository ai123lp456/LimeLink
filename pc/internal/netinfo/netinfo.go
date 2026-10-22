// Package netinfo 提供本机局域网 IP 探测，用于手机端配置提示。
package netinfo

import (
	"net"
)

// IPEntry 是一块网卡上的一个可用 IPv4 地址。
type IPEntry struct {
	Iface string // 网卡名
	IP    string // IPv4 地址
}

// ListLocalIPv4 返回所有非回环、非链路本地（169.254.*）的 IPv4，附带网卡名。
func ListLocalIPv4() []IPEntry {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []IPEntry
	for _, ifc := range ifaces {
		// 跳过未启用的网卡
		if ifc.Flags&net.FlagUp == 0 {
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
			out = append(out, IPEntry{Iface: ifc.Name, IP: ip4.String()})
		}
	}
	return out
}

// LocalIPv4 返回首选 IP：优先私网段（192.168/10./172.16-31），否则取第一个可用项。
func LocalIPv4() string {
	list := ListLocalIPv4()
	if len(list) == 0 {
		return ""
	}
	for _, e := range list {
		if ip := net.ParseIP(e.IP); ip != nil && ip.IsPrivate() {
			return e.IP
		}
	}
	return list[0].IP
}
