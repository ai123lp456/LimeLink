// Package config 处理 LimeLink 电脑端配置（端口等）。
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// Config 是持久化配置。
type Config struct {
	Port    int    `json:"port"`
	DevName string `json:"device_name,omitempty"` // 电脑端设备名（v1.2.0）
}

// Name 返回设备名；空则回默认名。
func (c Config) Name() string {
	if c.DevName == "" {
		return DefaultName
	}
	return c.DevName
}

// DefaultPort 默认端口。
const DefaultPort = 8787

// DefaultName 默认电脑端设备名（v1.2.0，首次运行自动生成后缀）。
const DefaultName = "LimeLink-PC"

// ConfigDir 返回配置目录（用户级）。
func ConfigDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "."
	}
	return filepath.Join(dir, "LimeLink")
}

func configPath() string {
	return filepath.Join(ConfigDir(), "config.json")
}

// Load 读取配置，不存在则返回默认值。
func Load() Config {
	c := Config{Port: DefaultPort}
	b, err := os.ReadFile(configPath())
	if err != nil {
		c.DevName = defaultGeneratedName()
		return c
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{Port: DefaultPort, DevName: defaultGeneratedName()}
	}
	if c.Port <= 0 || c.Port > 65535 {
		c.Port = DefaultPort
	}
	if c.DevName == "" {
		c.DevName = defaultGeneratedName()
	}
	return c
}

// defaultGeneratedName 生成默认设备名（MAC 后 4 位大写，无网卡时随机）。
func defaultGeneratedName() string {
	suffix := hostIDSuffix()
	return fmt.Sprintf("%s-%s", DefaultName, suffix)
}

// hostIDSuffix 取第一块非回环、非全 0 网卡 MAC 的后 4 位（大写）；
// 取不到则回退为 4 位随机十六进制。
func hostIDSuffix() string {
	ifaces, err := net.Interfaces()
	if err == nil {
		for _, ifc := range ifaces {
			if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
				continue
			}
			mac := ifc.HardwareAddr
			if len(mac) < 4 {
				continue
			}
			any := false
			for _, b := range mac {
				if b != 0 {
					any = true
					break
				}
			}
			if any {
				return strings.ToUpper(hex.EncodeToString(mac[len(mac)-2:]))
			}
		}
	}
	// 回退：4 位随机十六进制
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return strings.ToUpper(hex.EncodeToString(b))
}

// Save 保存配置。
func (c Config) Save() error {
	dir := ConfigDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(), b, 0o644)
}

// Addr 返回监听地址。
func (c Config) Addr() string {
	return fmt.Sprintf(":%d", c.Port)
}