// Package toast 是 Windows Toast 通知的轻量封装（经 PowerShell 调用 WinRT）。
//
// 来源：github.com/go-toast/toast v0.0.0-20190211030409-01e6764cf0a4
// （MIT License, Copyright (c) 2016 Jacob Marshall，见同目录 LICENSE）。
// 内嵌进本项目的原因：该版本（含上游 master）不支持 Toast 的 Tag/Group，
// 无法实现「同一来源的新通知立即替换正在显示的旧通知」。
//
// 相对上游的改动（LimeLink v1.1.0）：
//  1. Notification 新增 Tag / Group 字段，并在 PowerShell 脚本中设置到
//     ToastNotification 上：同 AppID + 同 Tag + 同 Group 的 Toast 会互相替换；
//  2. Tag / Group / AppID 中的单引号做 PowerShell 转义，防注入；
//  3. 去掉 github.com/nu7hatch/gouuid 依赖，临时脚本名改用 crypto/rand 生成；
//  4. ioutil 换成 os.WriteFile。
package toast

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"syscall"
	"time"
)

var toastTemplate *template.Template

var (
	ErrorInvalidAudio    error = errors.New("toast: invalid audio")
	ErrorInvalidDuration       = errors.New("toast: invalid duration")
)

type toastAudio string

const (
	Default        toastAudio = "ms-winsoundevent:Notification.Default"
	IM                        = "ms-winsoundevent:Notification.IM"
	Mail                      = "ms-winsoundevent:Notification.Mail"
	Reminder                  = "ms-winsoundevent:Notification.Reminder"
	SMS                       = "ms-winsoundevent:Notification.SMS"
	LoopingAlarm              = "ms-winsoundevent:Notification.Looping.Alarm"
	LoopingAlarm2             = "ms-winsoundevent:Notification.Looping.Alarm2"
	LoopingAlarm3             = "ms-winsoundevent:Notification.Looping.Alarm3"
	LoopingAlarm4             = "ms-winsoundevent:Notification.Looping.Alarm4"
	LoopingAlarm5             = "ms-winsoundevent:Notification.Looping.Alarm5"
	LoopingAlarm6             = "ms-winsoundevent:Notification.Looping.Alarm6"
	LoopingAlarm7             = "ms-winsoundevent:Notification.Looping.Alarm7"
	LoopingAlarm8             = "ms-winsoundevent:Notification.Looping.Alarm8"
	LoopingAlarm9             = "ms-winsoundevent:Notification.Looping.Alarm9"
	LoopingAlarm10            = "ms-winsoundevent:Notification.Looping.Alarm10"
	LoopingCall               = "ms-winsoundevent:Notification.Looping.Call"
	LoopingCall2              = "ms-winsoundevent:Notification.Looping.Call2"
	LoopingCall3              = "ms-winsoundevent:Notification.Looping.Call3"
	LoopingCall4              = "ms-winsoundevent:Notification.Looping.Call4"
	LoopingCall5              = "ms-winsoundevent:Notification.Looping.Call5"
	LoopingCall6              = "ms-winsoundevent:Notification.Looping.Call6"
	LoopingCall7              = "ms-winsoundevent:Notification.Looping.Call7"
	LoopingCall8              = "ms-winsoundevent:Notification.Looping.Call8"
	LoopingCall9              = "ms-winsoundevent:Notification.Looping.Call9"
	LoopingCall10             = "ms-winsoundevent:Notification.Looping.Call10"
	Silent                    = "silent"
)

type toastDuration string

const (
	Short toastDuration = "short"
	Long                = "long"
)

func init() {
	toastTemplate = template.New("toast")
	toastTemplate.Parse(`
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null
[Windows.UI.Notifications.ToastNotification, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] | Out-Null

$APP_ID = '{{if .AppID}}{{.AppID}}{{else}}Windows App{{end}}'

$template = @"
<toast activationType="{{.ActivationType}}" launch="{{.ActivationArguments}}" duration="{{.Duration}}">
    <visual>
        <binding template="ToastGeneric">
            {{if .Icon}}
            <image placement="appLogoOverride" src="{{.Icon}}" />
            {{end}}
            {{if .Title}}
            <text><![CDATA[{{.Title}}]]></text>
            {{end}}
            {{if .Message}}
            <text><![CDATA[{{.Message}}]]></text>
            {{end}}
        </binding>
    </visual>
    {{if ne .Audio "silent"}}
	<audio src="{{.Audio}}" loop="{{.Loop}}" />
	{{else}}
	<audio silent="true" />
	{{end}}
    {{if .Actions}}
    <actions>
        {{range .Actions}}
        <action activationType="{{.Type}}" content="{{.Label}}" arguments="{{.Arguments}}" />
        {{end}}
    </actions>
    {{end}}
</toast>
"@

$xml = New-Object Windows.Data.Xml.Dom.XmlDocument
$xml.LoadXml($template)
$toast = New-Object Windows.UI.Notifications.ToastNotification $xml
{{if .Tag}}
$toast.Tag = '{{.Tag}}'
{{end}}
{{if .Group}}
$toast.Group = '{{.Group}}'
{{end}}
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($APP_ID).Show($toast)
    `)
}

// Notification 是一条 Toast 通知的数据。
//
// Tag / Group（LimeLink v1.1.0 新增）：
// 同一 AppID + 同 Tag + 同 Group 的 Toast 会互相替换（含正在显示的横幅），
// 不同 Tag 之间互不干扰。用于「同一来源连续来消息时立即覆盖」。
type Notification struct {
	// App 名，显示在 Action Center，也作为 Toast 的 AppUserModelID。
	AppID string

	// 标题。
	Title string

	// 正文。
	Message string

	// 可选的左侧小图标（绝对路径）。
	Icon string

	// 点击行为类型。
	ActivationType string

	// 点击触发的参数。
	ActivationArguments string

	// 可选按钮。
	Actions []Action

	// 提示音。
	Audio toastAudio

	// 是否循环提示音。
	Loop bool

	// 显示时长（short/long）。
	Duration toastDuration

	// 替换标记：同 Tag 的 Toast 互相替换。
	Tag string

	// 替换分组：Group 统一为 "LimeLink"，与 Tag 配合生效。
	Group string
}

// Action 定义一个按钮。
type Action struct {
	Type      string
	Label     string
	Arguments string
}

func (n *Notification) applyDefaults() {
	if n.ActivationType == "" {
		n.ActivationType = "protocol"
	}
	if n.Duration == "" {
		n.Duration = Short
	}
	if n.Audio == "" {
		n.Audio = Default
	}
	// PowerShell 单引号字符串转义（v1.1.0 新增）
	n.AppID = psEscape(n.AppID)
	n.Tag = psEscape(n.Tag)
	n.Group = psEscape(n.Group)
}

// psEscape 转义 PowerShell 单引号字符串中的单引号。
func psEscape(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

func (n *Notification) buildXML() (string, error) {
	var out bytes.Buffer
	err := toastTemplate.Execute(&out, n)
	if err != nil {
		return "", err
	}
	return out.String(), nil
}

// Push 弹出这条 Toast（内部调用一次 PowerShell，耗时约 1~2 秒属正常）。
func (n *Notification) Push() error {
	n.applyDefaults()
	xml, err := n.buildXML()
	if err != nil {
		return err
	}
	return invokeTemporaryScript(xml)
}

// Audio 按名称返回提示音（未匹配则返回 Default 和错误）。
func Audio(name string) (toastAudio, error) {
	switch strings.ToLower(name) {
	case "default":
		return Default, nil
	case "im":
		return IM, nil
	case "mail":
		return Mail, nil
	case "reminder":
		return Reminder, nil
	case "sms":
		return SMS, nil
	case "silent":
		return Silent, nil
	default:
		return Default, ErrorInvalidAudio
	}
}

// Duration 按名称返回显示时长（未匹配则返回 Short 和错误）。
func Duration(name string) (toastDuration, error) {
	switch strings.ToLower(name) {
	case "short":
		return Short, nil
	case "long":
		return Long, nil
	default:
		return Short, ErrorInvalidDuration
	}
}

// newScriptID 生成临时脚本文件名（替代上游的 gouuid 依赖）。
func newScriptID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("limelink_%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func invokeTemporaryScript(content string) error {
	file := filepath.Join(os.TempDir(), newScriptID()+".ps1")
	defer os.Remove(file)
	bomUtf8 := []byte{0xEF, 0xBB, 0xBF}
	out := append(bomUtf8, []byte(content)...)
	if err := os.WriteFile(file, out, 0o600); err != nil {
		return err
	}
	cmd := exec.Command("PowerShell", "-ExecutionPolicy", "Bypass", "-File", file)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}
