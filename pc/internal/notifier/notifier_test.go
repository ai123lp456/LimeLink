package notifier

import (
	"testing"

	"limelink/pc/internal/server"
	"limelink/pc/internal/toast"
)

// v1.3.2 验收 1：Silent 的通知必须把 Toast 音频设成 silent，
// 否则同一个 Tag 只能顶掉画面，声音照播（1.3.1 的老坑）。
func TestSilentNotificationMuted(t *testing.T) {
	muted := buildToast("app", server.Notification{
		Title: "QQ 计时中", Text: "已使用10:47", AppName: "时间侦探", Silent: true,
	})
	if muted.Audio != toast.Silent {
		t.Fatalf("Silent 通知的 Audio 应为 silent，实际 %q", muted.Audio)
	}

	loud := buildToast("app", server.Notification{
		Title: "张三", Text: "在吗", AppName: "微信", IconType: "wechat",
	})
	if loud.Audio == toast.Silent {
		t.Fatal("非静音通知不应被设成 silent")
	}
}

// 同一来源共用一个 Tag：新 Toast 才能顶掉旧画面。
func TestToastTagPerSource(t *testing.T) {
	if got := buildToast("app", server.Notification{IconType: "wechat"}).Tag; got != "wechat" {
		t.Fatalf("微信应共用 wechat Tag，实际 %q", got)
	}
	if got := buildToast("app", server.Notification{IconType: "app", Pkg: "com.thx.apptimerchan"}).Tag; got != "com.thx.apptimerchan" {
		t.Fatalf("其他 App 应用包名作 Tag，实际 %q", got)
	}
}
