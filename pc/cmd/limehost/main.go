// Command limehost 是 LimeLink 电脑端常驻程序。
// 功能：
//   - 启动 HTTP 服务（默认 8787 端口，读写超时 + 优雅关闭），接收手机推送的通知
//   - 启动 UDP 发现服务（8788），手机端广播即可自动找到电脑
//   - 用 Windows 原生 Toast 弹窗（同来源新 Toast 立即覆盖旧 Toast），失败降级托盘气泡
//   - 系统托盘图标 + 右键菜单（本机地址 / 配对码 / 设备改名 / 最近记录 / 重置配对 / 关于 / 退出）
package main

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/getlantern/systray"
	"limelink/pc/internal/config"
	"limelink/pc/internal/discovery"
	"limelink/pc/internal/netinfo"
	"limelink/pc/internal/notifier"
	"limelink/pc/internal/server"
)

const appID = notifier.DefaultAppID

// appVersion 是电脑端版本号（同时体现在 README 与托盘 tooltip/关于）。
const appVersion = "1.2.2"

func main() {
	cfg := config.Load()

	// 启动 HTTP 服务（带读写超时的 http.Server）
	srv := server.NewWithName(newPairCode6(), cfg.Name())
	ntf := notifier.New(appID)
	httpSrv := srv.NewHTTPServer(ntf)

	ln, err := listen(cfg.Addr())
	if err != nil {
		// 端口占用：给出具体提示
		log.Printf("[fatal] 端口 %d 被占用或无法监听: %v\n", cfg.Port, err)
		fmt.Printf("电脑端端口 %d 被占用，请换一个端口或关闭占用程序。\n%s\n", cfg.Port, err)
		return
	}
	go func() {
		if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[http] 服务退出: %v\n", err)
		}
	}()

	// 启动 UDP 发现服务（供手机端 IP 自动重发现）
	disc := discovery.New(discoveryProvider{srv: srv, port: cfg.Port}, discovery.DefaultPort)
	discErr := make(chan error, 1)
	go func() { discErr <- disc.Start() }()

	log.Printf("[http] LimeLink 电脑端已启动，监听端口 %d（发现 %d），本机 IP %s，设备名 %s\n",
		cfg.Port, discovery.DefaultPort, netinfo.LocalIPv4(), cfg.Name())

	// 托盘
	systray.Run(onReady(srv, ntf, cfg, httpSrv, disc), onExit)
}

func listen(addr string) (netListener, error) {
	return netListen("tcp", addr)
}

// discoveryProvider 实现 discovery.InfoProvider，提供电脑端信息给 UDP 应答。
type discoveryProvider struct {
	srv  *server.Server
	port int
}

func (p discoveryProvider) Get() (name string, port int, version string) {
	return p.srv.Name(), p.port, appVersion
}

func onReady(srv *server.Server, ntf *notifier.Notifier, cfg config.Config, httpSrv *http.Server, disc *discovery.Service) func() {
	return func() {
		systray.SetIcon(loadIcon())
		systray.SetTitle("小青柠")
		systray.SetTooltip(fmt.Sprintf("小青柠 LimeLink v%s", appVersion))

		mIP := systray.AddMenuItem(ipMenuTitle(cfg), "点击列出本机所有可用局域网地址")
		mPair := systray.AddMenuItem("配对码", "当前配对码")
		mRename := systray.AddMenuItem("设备改名", "修改电脑端设备名（手机端可见）")
		mRecords := systray.AddMenuItem("最近同步记录", "查看最近通知")
		mReset := systray.AddMenuItem("重置配对", "重新生成配对码")
		mAbout := systray.AddMenuItem("关于", "版本信息")
		systray.AddSeparator()
		mQuit := systray.AddMenuItem("退出", "退出小青柠")

		go func() {
			for {
				select {
				case <-mIP.ClickedCh:
					mIP.SetTitle(ipMenuTitle(cfg))
					showAddresses(cfg)
				case <-mPair.ClickedCh:
					mPair.SetTitle(fmt.Sprintf("配对码：%s", srv.PairCode()))
				case <-mRename.ClickedCh:
					doRename(cfg, srv, ntf)
				case <-mRecords.ClickedCh:
					showRecords(srv)
				case <-mReset.ClickedCh:
					code := srv.ResetPairCode()
					mPair.SetTitle(fmt.Sprintf("配对码：%s", code))
					_ = ntf.Show(server.Notification{
						Title:    "配对已重置",
						Text:     "新配对码 " + code,
						AppName:  "LimeLink",
						IconType: "system",
					})
				case <-mAbout.ClickedCh:
					_ = notifier.ShowMsg(fmt.Sprintf("小青柠 LimeLink 电脑端 v%s\n手机通知 → Windows Toast 同步工具", appVersion))
				case <-mQuit.ClickedCh:
					shutdown(httpSrv, disc)
					systray.Quit()
					return
				}
			}
		}()
	}
}

func onExit() {
	log.Println("[exit] LimeLink 已退出")
}

// shutdown 优雅关闭 HTTP 与 UDP 发现服务，释放资源。
func shutdown(httpSrv *http.Server, disc *discovery.Service) {
	disc.Stop()
	ctxDone := make(chan struct{})
	go func() {
		_ = httpSrv.Close()
		close(ctxDone)
	}()
	select {
	case <-ctxDone:
	case <-time.After(2 * time.Second):
		log.Println("[exit] HTTP 服务关闭超时")
	}
}

// ipMenuTitle 计算托盘「本机地址」菜单项标题（启动/点击时实时计算）。
func ipMenuTitle(cfg config.Config) string {
	ip := netinfo.LocalIPv4()
	if ip == "" {
		return "本机地址：未找到可用网络"
	}
	return fmt.Sprintf("本机地址：%s:%d", ip, cfg.Port)
}

// showAddresses 点击「本机地址」后，实时枚举所有网卡地址并弹 Toast 列出。
func showAddresses(cfg config.Config) {
	list := netinfo.ListLocalIPv4()
	if len(list) == 0 {
		_ = notifier.ShowMsg("未找到可用局域网地址。请检查网络连接，或开启移动热点后再试。")
		return
	}
	buf := "手机端填以下地址之一（端口 " + fmt.Sprintf("%d", cfg.Port) + "）：\n"
	for _, e := range list {
		buf += fmt.Sprintf("%s（%s）\n", e.IP, e.Iface)
	}
	_ = notifier.Show(server.Notification{
		Title:    "本机局域网地址",
		Text:     buf,
		AppName:  "LimeLink",
		IconType: "system",
	})
}

func showRecords(srv *server.Server) {
	recs := srv.Records()
	peer := srv.PeerName()
	if len(recs) == 0 {
		txt := "暂无记录"
		if peer != "" {
			txt = "已配对设备：" + peer + "\n暂无同步记录"
		}
		_ = notifier.Show(server.Notification{
			Title:    "最近同步记录",
			Text:     txt,
			AppName:  "LimeLink",
			IconType: "system",
		})
		return
	}
	buf := ""
	if peer != "" {
		buf = "已配对设备：" + peer + "\n"
	}
	for i, r := range recs {
		if i >= 10 {
			break
		}
		buf += fmt.Sprintf("%s [%s] %s\n", r.ReceivedAt.Format("15:04:05"), r.AppName, r.Title)
	}
	_ = notifier.ShowMsg(buf)
}

// doRename 弹输入框修改电脑端设备名，写回配置并同步到内存。
func doRename(cfg config.Config, srv *server.Server, ntf *notifier.Notifier) {
	newName, ok, err := promptForName("请输入电脑端设备名（手机端可见）：", "设备改名", cfg.Name())
	if err != nil || !ok {
		return // 用户取消或出错
	}
	if newName == "" {
		_ = notifier.ShowMsg("设备名不能为空，未修改。")
		return
	}
	cfg.DevName = newName
	if err := cfg.Save(); err != nil {
		_ = notifier.ShowMsg("保存设备名失败：" + err.Error())
		return
	}
	srv.SetName(newName)
	_ = ntf.Show(server.Notification{
		Title:    "设备已改名",
		Text:     "新名称：" + newName,
		AppName:  "LimeLink",
		IconType: "system",
	})
}