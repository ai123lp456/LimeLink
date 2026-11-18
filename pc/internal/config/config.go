// Package config 处理 LimeLink 电脑端配置（端口、设备名、界面开关）。
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
	Port     int    `json:"port"`
	DevName  string `json:"device_name,omitempty"` // 电脑端设备名
	NoWindow bool   `json:"no_window,omitempty"`   // 启动时不自动打开界面（v1.3.0）
	Theme    string `json:"theme,omitempty"`       // 界面主题：light / dark（v1.3.1）

	// v1.3.2：重复通知的两个开关。用指针区分「配置里没写过」和「显式关掉」，
	// 缺省（nil）一律按开处理，老配置文件升级后自动开启。
	MergeRepeat *bool `json:"merge_repeat,omitempty"` // 合并重复通知（同 key 的实时通知原地刷新）
	MuteRepeat  *bool `json:"mute_repeat,omitempty"`  // 重复通知静音

	// CloseHinted 是否已经解释过「关窗不退出」
	CloseHinted bool `json:"close_hinted,omitempty"`
}

// MergeRepeatEnabled 返回「合并重复通知」是否开启（缺省开）。
func (c Config) MergeRepeatEnabled() bool { return c.MergeRepeat == nil || *c.MergeRepeat }

// MuteRepeatEnabled 返回「重复通知静音」是否开启（缺省开）。
func (c Config) MuteRepeatEnabled() bool { return c.MuteRepeat == nil || *c.MuteRepeat }

// SetMergeRepeat 写入「合并重复通知」开关。
func (c *Config) SetMergeRepeat(v bool) { c.MergeRepeat = &v }

// SetMuteRepeat 写入「重复通知静音」开关。
func (c *Config) SetMuteRepeat(v bool) { c.MuteRepeat = &v }

// ThemeName 返回界面主题（light / dark），空则浅色。
func (c Config) ThemeName() string {
	if c.Theme == "dark" {
		return "dark"
	}
	return "light"
}

// Name 返回设备名；空则回默认名。
func (c Config) Name() string {
	if c.DevName == "" {
		return DefaultName
	}
	return c.DevName
}

// DefaultPort 默认 HTTP 端口。
const DefaultPort = 8787

// DefaultName 默认电脑端设备名前缀（首次运行自动生成后缀）。
const DefaultName = "LimeLink-PC"

// ConfigDir 返回配置与数据目录（用户级，%APPDATA%\LimeLink）。
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
	return fmt.Sprintf("%s-%s", DefaultName, hostIDSuffix())
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
