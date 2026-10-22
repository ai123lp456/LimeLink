package netinfo

import (
	"strings"
	"testing"
)

// 本机可能存在 169.254.* 链路本地地址（APIPA），必须被排除。
func TestListLocalIPv4ExcludesJunk(t *testing.T) {
	list := ListLocalIPv4()
	for _, e := range list {
		if strings.HasPrefix(e.IP, "169.254.") {
			t.Errorf("链路本地地址未被排除: %s（%s）", e.IP, e.Iface)
		}
		if strings.HasPrefix(e.IP, "127.") {
			t.Errorf("回环地址未被排除: %s（%s）", e.IP, e.Iface)
		}
		if e.Iface == "" {
			t.Errorf("IPEntry 缺少网卡名: %s", e.IP)
		}
	}
	if len(list) == 0 {
		t.Log("本机无可用 IPv4（测试环境可能离线）")
	}
}

// LocalIPv4 不得返回链路本地或回环地址。
func TestLocalIPv4NotLinkLocal(t *testing.T) {
	ip := LocalIPv4()
	if ip == "" {
		t.Skip("本机无可用 IPv4")
	}
	if strings.HasPrefix(ip, "169.254.") || strings.HasPrefix(ip, "127.") {
		t.Errorf("LocalIPv4 返回了无效地址: %s", ip)
	}
}
