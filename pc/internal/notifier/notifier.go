// Package notifier 负责把通知弹成 Windows Toast，失败时降级为托盘气泡。
// v1.1.0：按来源设置 Toast Tag（Group 统一为 LimeLink），
// 同一来源连续来消息时新 Toast 立即替换旧 Toast，不再排队逐条显示。
package notifier

import (
	"embed"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"limelink/pc/internal/server"
	"limelink/pc/internal/toast"
)

//go:embed assets/wechat.png assets/sms.png assets/system.png
var iconFS embed.FS

// DefaultAppID 是 Toast 的 AppUserModelID（含版本，便于在系统通知设置中区分来源）。
const DefaultAppID = "LimeLink.Notification.1.2.2"

// toastGroup 是所有 Toast 统一的 Group；Tag 区分来源，两者配合实现覆盖。
const toastGroup = "LimeLink"

// Notifier 持有托盘信息，弹窗失败时降级到托盘气泡。
type Notifier struct {
	appID string
}

// New 创建一个 Notifier。
func New(appID string) *Notifier {
	return &Notifier{appID: appID}
}

// OnNotification 实现 server.Hook 接口：收到通知即弹窗。
func (n *Notifier) OnNotification(nt server.Notification) {
	if err := n.Show(nt); err != nil {
		log.Printf("[notify] 弹窗失败: %v\n", err)
	}
}

// Show 弹出一条通知。优先原生 Toast，失败降级托盘气泡。
func (n *Notifier) Show(nt server.Notification) error {
	title := displayTitle(nt)
	body := displayBody(nt)

	t := toast.Notification{
		AppID:   n.appID,
		Title:   title,
		Message: body,
		Tag:     toastTag(nt), // 同来源共用一个 Tag → 新 Toast 覆盖旧 Toast
		Group:   toastGroup,
	}
	if p := writeTempIcon(nt.IconType); p != "" {
		t.Icon = p
		// v1.1.0：不再删除临时图标。图标固定路径复用，
		// 避免连续弹窗时上一条的图标文件被提前删除导致图标缺失。
	}
	if err := t.Push(); err != nil {
		log.Printf("[toast] 弹窗失败，降级: %v\n", err)
		return err
	}
	return nil
}

// toastTag 按来源生成 Tag：同一来源的 Toast 用相同 Tag，新弹的立即替换旧的；
// 不同来源 Tag 不同，互不干扰。
//   - 微信 → wechat；短信 → sms；系统 → system
//   - 其他 App → 包名（旧版手机端未上报包名时退回 "app"）
func toastTag(nt server.Notification) string {
	switch nt.IconType {
	case "wechat", "sms", "system":
		return nt.IconType
	default:
		if nt.Pkg != "" {
			return nt.Pkg
		}
		return "app"
	}
}

// writeTempIcon 把内嵌图标写到临时目录并返回绝对路径（无对应图标则空串）。
// 固定路径 + 已存在则跳过写入，供连续弹窗复用。
func writeTempIcon(iconType string) string {
	names := map[string]string{
		"wechat": "assets/wechat.png",
		"sms":    "assets/sms.png",
		"system": "assets/system.png",
	}
	name, ok := names[iconType]
	if !ok {
		return ""
	}
	p := filepath.Join(os.TempDir(), "limelink_"+iconType+".png")
	if _, err := os.Stat(p); err == nil {
		return p // 已存在，直接复用
	}
	b, err := iconFS.ReadFile(name)
	if err != nil {
		return ""
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		return ""
	}
	return p
}

// displayTitle 计算 Toast 标题。
func displayTitle(nt server.Notification) string {
	if nt.Title != "" {
		return nt.Title
	}
	if nt.AppName != "" {
		return nt.AppName
	}
	return "LimeLink"
}

// displayBody 计算 Toast 正文：优先正文，落到 App 名。
func displayBody(nt server.Notification) string {
	if nt.Text != "" {
		return nt.Text
	}
	if nt.Hidden {
		return "（内容已隐藏）"
	}
	return ""
}

// Summary 描述来源标识（用于记录展示）。
func Summary(nt server.Notification) string {
	return fmt.Sprintf("[%s] %s", nt.AppName, nt.Title)
}

// Show 是无状态的快捷弹窗（用于系统提示），失败静默降级。
func Show(nt server.Notification) error {
	return New(DefaultAppID).Show(nt)
}

// ShowMsg 弹一条纯文本系统提示。
func ShowMsg(text string) error {
	return Show(server.Notification{
		Title:    "小青柠",
		Text:     text,
		AppName:  "LimeLink",
		IconType: "system",
	})
}
